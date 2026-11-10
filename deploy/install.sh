#!/usr/bin/env bash
set -Eeuo pipefail

REPOSITORY="${HL_PANEL_REPOSITORY:-Aurelian-HL/HL-Panel}"
VERSION="latest"
DOMAIN="hl-panel.invalid"
DOMAIN_EXPLICIT=false
ADMIN_USERNAME="admin"
ADMIN_USERNAME_EXPLICIT=false
ADMIN_DEFAULT_PASSWORD=false
EMAIL=""
PUBLIC_IP=""
API_PORT="8080"
IP_HTTPS_PORT="8443"
INSTALL_ROOT="/opt/hl-panel"
CONFIG_DIR="/etc/hl-panel"
STATE_DIR="/var/lib/hl-panel"
NGINX_AVAILABLE="/etc/nginx/sites-available/hl-panel.conf"
NGINX_ENABLED="/etc/nginx/sites-enabled/hl-panel.conf"
ACME_WEBROOT="/var/lib/hl-panel-acme"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
WORK_DIR=""

log() { printf '[HL-panel] %s\n' "$*"; }
fail() { printf '[HL-panel] 错误：%s\n' "$*" >&2; exit 1; }

check_nginx_capabilities() {
  local probe_dir probe_output module_include=""
  probe_dir="$(mktemp -d /tmp/hl-panel-nginx-check.XXXXXX)" || fail "无法创建 Nginx 检查目录"
  if [[ -d /etc/nginx/modules-enabled ]]; then
    module_include='include /etc/nginx/modules-enabled/*.conf;'
  fi
  # Parse an isolated configuration; never bind ports or reload existing sites.
  cat > "$probe_dir/nginx.conf" <<EOF
$module_include
error_log stderr;
pid $probe_dir/nginx.pid;
events {}
http {
  access_log off;
  client_body_temp_path $probe_dir/client;
  proxy_temp_path $probe_dir/proxy;
  limit_req_zone \$binary_remote_addr zone=hl_panel_probe:1m rate=5r/m;
  ssl_protocols TLSv1.2 TLSv1.3;
  server {
    listen 127.0.0.1:19993 http2;
    location / {
      limit_req zone=hl_panel_probe burst=4 nodelay;
      limit_req_status 429;
      proxy_pass http://127.0.0.1:19994;
    }
  }
}
EOF
  if ! probe_output="$(nginx -t -p "$probe_dir/" -c "$probe_dir/nginx.conf" 2>&1)"; then
    rm -rf -- "$probe_dir"
    printf '%s\n' "$probe_output" >&2
    fail "Nginx 缺少面板需要的模块或模块加载失败（登录限流、SSL、HTTP/2、反向代理）。尚未创建面板账号、文件或数据库；不会自动替换已有 Nginx。专用于面板的 VPS 可执行 apt-get update && apt-get install -y nginx-core 后重试；已有其他站点请先由管理员确认 Nginx 升级方案。"
  fi
  rm -rf -- "$probe_dir"
  log "Nginx 模块检查通过（登录限流、SSL、HTTP/2、反向代理）"
}

resolve_domain_records() {
  python3 - "$DOMAIN" "$1" <<'PY'
import socket
import sys

domain, record_type = sys.argv[1:]
family = socket.AF_INET if record_type == "A" else socket.AF_INET6
try:
    # Explicit family and flags=0 prevent synthesizing IPv4-mapped AAAA records.
    answers = socket.getaddrinfo(domain, None, family, socket.SOCK_STREAM, 0, 0)
except socket.gaierror as error:
    no_records = {socket.EAI_NONAME, getattr(socket, "EAI_NODATA", socket.EAI_NONAME)}
    if error.errno not in no_records:
        print(f"[HL-panel DNS] {domain} {record_type} 查询失败：{error}", file=sys.stderr)
        sys.exit(1)
    answers = []
print("\n".join(sorted({answer[4][0] for answer in answers})))
PY
}

usage() {
  cat <<'EOF'
用法：install.sh [--repo OWNER/REPOSITORY] [--version TAG|latest]
                 [--domain DOMAIN] [--public-ip IPv4] [--api-port PORT]
                 [--ip-https-port PORT]
                 [--admin-username NAME] [--email EMAIL]

选项：
  --repo              GitHub 仓库；默认 Aurelian-HL/HL-Panel
  --version           发布标签；默认 latest
  --domain            面板域名；未指定时询问，回车使用 IP 入口
  --public-ip         指定本机公网 IPv4；自动检测失败时必须提供
  --api-port          API 本机监听端口；默认 8080，已有服务占用时可指定 8081
  --ip-https-port     IP HTTPS 独立监听端口；默认 8443，不得使用 80、443 或 API 端口
  --admin-username    首次管理员账号；默认 admin
  --email             Let's Encrypt 证书通知邮箱，可选
  -h, --help          显示帮助

安装只创建 HL-panel 独立实例，不导入或删除其他面板数据。
安装会询问管理员账号、密码；回车采用 admin / 123456。
使用默认密码时首次登录必须在个人中心修改；自定义密码至少 8 个字符。
EOF
}

while (($#)); do
  case "$1" in
    --repo) (($# >= 2)) || fail "--repo 缺少参数"; REPOSITORY="$2"; shift 2 ;;
    --version) (($# >= 2)) || fail "--version 缺少参数"; VERSION="$2"; shift 2 ;;
    --domain) (($# >= 2)) || fail "--domain 缺少参数"; DOMAIN="$2"; DOMAIN_EXPLICIT=true; shift 2 ;;
    --public-ip) (($# >= 2)) || fail "--public-ip 缺少参数"; PUBLIC_IP="$2"; shift 2 ;;
    --api-port) (($# >= 2)) || fail "--api-port 缺少参数"; API_PORT="$2"; shift 2 ;;
    --ip-https-port) (($# >= 2)) || fail "--ip-https-port 缺少参数"; IP_HTTPS_PORT="$2"; shift 2 ;;
    --admin-username) (($# >= 2)) || fail "--admin-username 缺少参数"; ADMIN_USERNAME="$2"; ADMIN_USERNAME_EXPLICIT=true; shift 2 ;;
    --email) (($# >= 2)) || fail "--email 缺少参数"; EMAIL="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "未知参数：$1" ;;
  esac
done

normalize_port() {
  local option="$1" value="$2" port
  [[ "$value" =~ ^[0-9]{1,5}$ ]] || fail "$option 必须是 1 到 65535 的十进制 TCP 端口"
  port="$((10#$value))"
  ((port >= 1 && port <= 65535)) || fail "$option 必须是 1 到 65535 的十进制 TCP 端口"
  printf '%d\n' "$port"
}
API_PORT="$(normalize_port --api-port "$API_PORT")"
IP_HTTPS_PORT="$(normalize_port --ip-https-port "$IP_HTTPS_PORT")"
((IP_HTTPS_PORT != 80 && IP_HTTPS_PORT != 443)) || fail "--ip-https-port 不得使用 80 或 443；请使用独立端口（默认 8443）"
((IP_HTTPS_PORT != API_PORT)) || fail "--ip-https-port 不得与 --api-port 相同"
check_tcp_port_available() {
  local port="$1" option="$2" example="$3" port_hex occupied
  local socket_files=(/proc/net/tcp)
  [[ -r /proc/net/tcp ]] || fail "无法读取本机 TCP 监听状态；未执行安装"
  if [[ -r /proc/net/tcp6 ]]; then socket_files+=(/proc/net/tcp6); fi
  printf -v port_hex '%04X' "$port"
  occupied="$(awk -v port_hex="$port_hex" '
    $4 == "0A" {
      split($2, address, ":")
      if (toupper(address[2]) == port_hex) { print $2; exit }
    }
  ' "${socket_files[@]}")" || fail "读取本机 TCP 监听状态失败；未执行安装"
  [[ -z "$occupied" ]] || fail "TCP 端口 $port 已被其他服务监听；不会停止现有服务，请用 $option 指定空闲端口（例如 $example）"
}
check_api_port_available() { check_tcp_port_available "$API_PORT" --api-port 8081; }
check_ip_https_port_available() { check_tcp_port_available "$IP_HTTPS_PORT" --ip-https-port 9443; }
[[ $EUID -eq 0 ]] || fail "请使用 root 运行，例如 curl ... | sudo bash"
check_api_port_available
check_ip_https_port_available
command -v flock >/dev/null 2>&1 || fail "缺少 flock；请先安装 util-linux"
exec {INSTALL_LOCK_FD}>/run/lock/hl-panel-install.lock
flock -n "$INSTALL_LOCK_FD" || fail "检测到另一份 HL-panel 安装正在运行"
[[ -n "$REPOSITORY" ]] || fail "未配置 GitHub 仓库；请通过 --repo OWNER/REPOSITORY 指定"
[[ "$REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "GitHub 仓库格式应为 OWNER/REPOSITORY"
[[ "$VERSION" == "latest" || "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || fail "版本标签格式无效"
[[ "$DOMAIN" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$ ]] || fail "域名格式无效"
[[ "$ADMIN_USERNAME" =~ ^[A-Za-z0-9._@-]{1,128}$ ]] || fail "管理员账号仅支持字母、数字、点、下划线、@ 和连字符"
[[ -z "$EMAIL" || "$EMAIL" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] || fail "邮箱格式无效"
valid_public_ipv4() {
  local address="$1" a b c d octet
  [[ "$address" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
  IFS=. read -r a b c d <<<"$address"
  for octet in "$a" "$b" "$c" "$d"; do
    ((10#$octet <= 255)) || return 1
  done
  ((10#$a > 0 && 10#$a < 224)) || return 1
  ((10#$a != 10 && 10#$a != 127)) || return 1
  ((10#$a != 169 || 10#$b != 254)) || return 1
  ((10#$a != 172 || 10#$b < 16 || 10#$b > 31)) || return 1
  ((10#$a != 192 || 10#$b != 168)) || return 1
  ((10#$a != 100 || 10#$b < 64 || 10#$b > 127)) || return 1
  ((10#$a != 192 || 10#$b != 0 || (10#$c != 0 && 10#$c != 2))) || return 1
  ((10#$a != 198 || (10#$b != 18 && 10#$b != 19))) || return 1
  ((10#$a != 198 || 10#$b != 51 || 10#$c != 100)) || return 1
  ((10#$a != 203 || 10#$b != 0 || 10#$c != 113)) || return 1
  return 0
}
if [[ -n "$PUBLIC_IP" ]]; then
  valid_public_ipv4 "$PUBLIC_IP" || fail "--public-ip 必须是公网 IPv4 地址"
fi
if ! command -v apt-get >/dev/null 2>&1; then fail "当前仅支持 Debian/Ubuntu 的 apt 安装流程"; fi
if [[ ! -r /etc/os-release ]] || ! grep -Eq '^(ID=debian|ID=ubuntu|ID_LIKE=.*debian)' /etc/os-release; then
  fail "当前系统不是受支持的 Debian/Ubuntu"
fi
ARCH="$(uname -m)"
[[ "$ARCH" == "x86_64" ]] || fail "当前版本只提供 Linux amd64，检测到 $ARCH"
id hlpanel >/dev/null 2>&1 && fail "系统账号 hlpanel 已存在；安装器不会接管它"
if command -v nginx >/dev/null 2>&1; then check_nginx_capabilities; fi

[[ -r /dev/tty && -w /dev/tty ]] || fail "安装需要交互式终端，以安全接收管理员密码"
[[ ! -e "$INSTALL_ROOT" && ! -L "$INSTALL_ROOT" ]] || fail "$INSTALL_ROOT 已存在；为保护现有数据，安装器不会覆盖它"
[[ ! -e "$CONFIG_DIR" && ! -L "$CONFIG_DIR" ]] || fail "$CONFIG_DIR 已存在；为保护现有配置，安装器不会覆盖它"
[[ ! -e "$STATE_DIR" && ! -L "$STATE_DIR" ]] || fail "$STATE_DIR 已存在；为保护现有数据，安装器不会覆盖它"
[[ ! -e "$ACME_WEBROOT" && ! -L "$ACME_WEBROOT" ]] || fail "$ACME_WEBROOT 已存在；为保护现有文件，安装器不会覆盖它"
[[ ! -e "$NGINX_AVAILABLE" && ! -L "$NGINX_AVAILABLE" && ! -e "$NGINX_ENABLED" && ! -L "$NGINX_ENABLED" ]] || fail "检测到已有 HL-panel Nginx 配置，未做任何覆盖"
[[ ! -e /etc/systemd/system/hl-panel-control-api.service && ! -L /etc/systemd/system/hl-panel-control-api.service ]] || fail "检测到已有 HL-panel 服务，未做任何覆盖"
for existing_path in \
  /etc/nginx/conf.d/hl-panel-rate-limit.conf \
  /etc/nginx/snippets/hl-panel-security-headers.conf \
  /etc/nginx/snippets/hl-panel-api-proxy.conf \
  /etc/nginx/snippets/hl-panel-app-locations.conf \
  /usr/local/sbin/hl-panel-enable-domain-tls \
  /usr/local/sbin/hl-panel-update \
  /usr/local/sbin/hl-panel-configure-reality \
  /etc/letsencrypt/renewal-hooks/deploy/hl-panel-copy-certificate; do
  [[ ! -e "$existing_path" && ! -L "$existing_path" ]] || fail "检测到已有 HL-panel 文件 $existing_path，未做任何覆盖"
done

check_database_names() {
  id postgres >/dev/null 2>&1 || return 0
  command -v psql >/dev/null 2>&1 || return 0
  if runuser -u postgres -- psql -XAtqc "SELECT 1 FROM pg_roles WHERE rolname = 'hl_panel_app'" 2>/dev/null | grep -qx 1; then
    fail "PostgreSQL 已有 hl_panel_app 角色；安装器不会复用或重置既有数据库"
  fi
  if runuser -u postgres -- psql -XAtqc "SELECT 1 FROM pg_database WHERE datname = 'hl_panel_control'" 2>/dev/null | grep -qx 1; then
    fail "PostgreSQL 已有 hl_panel_control 数据库；安装器不会复用或重置既有数据库"
  fi
}
check_database_names
command -v systemctl >/dev/null 2>&1 || fail "缺少 systemctl；当前系统不支持 systemd 服务管理"
POSTGRESQL_ACTIVE_BEFORE=false
NGINX_ACTIVE_BEFORE=false
POSTGRESQL_ENABLED_BEFORE="$(systemctl is-enabled postgresql 2>/dev/null || true)"
NGINX_ENABLED_BEFORE="$(systemctl is-enabled nginx 2>/dev/null || true)"
systemctl is-active --quiet postgresql && POSTGRESQL_ACTIVE_BEFORE=true || true
systemctl is-active --quiet nginx && NGINX_ACTIVE_BEFORE=true || true

declare -A OWNED_FILES=()
declare -A OWNED_SYMLINKS=()
install_new_file() {
  local source_file="$1" destination="$2" owner="$3" group="$4" mode="$5"
  local temp_file digest
  temp_file="$(mktemp "$(dirname "$destination")/.hl-panel-install.XXXXXX")"
  if ! install -o "$owner" -g "$group" -m "$mode" "$source_file" "$temp_file"; then
    rm -f -- "$temp_file"
    return 1
  fi
  if ! ln -- "$temp_file" "$destination"; then
    rm -f -- "$temp_file"
    fail "文件路径在安装期间被占用：$destination"
  fi
  digest="$(sha256sum "$temp_file" | awk '{print $1}')"
  OWNED_FILES["$destination"]="$digest"
  rm -f -- "$temp_file"
}
remove_owned_files() {
  local path digest current_digest
  for path in "${!OWNED_SYMLINKS[@]}"; do
    [[ -L "$path" && "$(readlink -- "$path")" == "${OWNED_SYMLINKS[$path]}" ]] && rm -f -- "$path"
  done
  for path in "${!OWNED_FILES[@]}"; do
    [[ -f "$path" && ! -L "$path" ]] || continue
    digest="${OWNED_FILES[$path]}"
    current_digest="$(sha256sum -- "$path" 2>/dev/null | awk '{print $1}')"
    [[ "$current_digest" == "$digest" ]] && rm -f -- "$path"
  done
}

restore_unit_state() {
  local unit="$1" active_before="$2" enabled_before="$3"
  case "$enabled_before" in
    enabled) systemctl enable "$unit" >/dev/null 2>&1 || log "警告：无法恢复 $unit 的开机启用状态" ;;
    enabled-runtime) systemctl enable --runtime "$unit" >/dev/null 2>&1 || log "警告：无法恢复 $unit 的临时启用状态" ;;
    masked) systemctl mask "$unit" >/dev/null 2>&1 || log "警告：无法恢复 $unit 的屏蔽状态" ;;
    masked-runtime) systemctl mask --runtime "$unit" >/dev/null 2>&1 || log "警告：无法恢复 $unit 的临时屏蔽状态" ;;
    disabled|not-found|'') systemctl disable "$unit" >/dev/null 2>&1 || true ;;
  esac
  if [[ "$active_before" == true ]]; then
    systemctl start "$unit" >/dev/null 2>&1 || log "警告：无法恢复 $unit 的运行状态"
  else
    systemctl stop "$unit" >/dev/null 2>&1 || true
  fi
}

nginx_server_name_matches() {
  local host="${1,,}" name="$2" suffix prefix remainder regex regex_status nocasematch_was_set=false matched=false
  if [[ "$name" == '~*'* ]]; then
    regex="${name:2}"
    if ! shopt -q nocasematch; then
      shopt -s nocasematch
      nocasematch_was_set=true
    fi
  elif [[ "$name" == '~'* ]]; then
    regex="${name:1}"
  else
    regex=""
  fi
  if [[ "$name" == '~'* ]]; then
    if [[ "$host" =~ $regex ]]; then matched=true; else regex_status=$?; fi
    if [[ "$nocasematch_was_set" == true ]]; then shopt -u nocasematch; fi
    [[ "$matched" == true ]] && return 0
    ((regex_status == 2)) && return 0
    return 1
  fi
  name="${name,,}"
  if [[ "$name" == .* ]]; then
    suffix="${name#.}"
    [[ "$host" == "$suffix" || "$host" == *."$suffix" ]]
    return
  fi
  if [[ "$name" == \*.* ]]; then
    suffix="${name#*.}"
    [[ "$host" == *."$suffix" && "$host" != "$suffix" ]]
    return
  fi
  if [[ "$name" == *.\* ]]; then
    prefix="${name%.*}"
    [[ "$host" == "$prefix".* ]] || return 1
    remainder="${host#"$prefix".}"
    [[ -n "$remainder" ]]
    return
  fi
  if [[ "$name" == *\** ]]; then return 0; fi
  [[ "$host" == "$name" ]]
}

find_nginx_name_conflicts() {
  local line directive name target collecting=false
  directive=""
  NGINX_CONFLICTS=()
  while IFS= read -r line; do
    line="${line%%#*}"
    if [[ "$collecting" == false ]]; then
      [[ "$line" =~ ^[[:space:]]*server_name([[:space:]]|$)(.*)$ ]] || continue
      directive="${BASH_REMATCH[2]}"
      collecting=true
    else
      directive+=" $line"
    fi
    [[ "$directive" == *';'* ]] || continue
    directive="${directive%%;*}"
    for name in $directive; do
      name="${name#\"}"; name="${name%\"}"
      name="${name#\'}"; name="${name%\'}"
      for target in "$DOMAIN" "$PUBLIC_IP"; do
        if nginx_server_name_matches "$target" "$name"; then
          NGINX_CONFLICTS+=("$name matches $target")
        fi
      done
    done
    collecting=false
    directive=""
  done <<< "$1"
}

if [[ "$DOMAIN_EXPLICIT" != true ]]; then
  read -r -p "设置面板域名 [回车使用 IP 访问]：" DOMAIN_INPUT </dev/tty
  DOMAIN="${DOMAIN_INPUT:-hl-panel.invalid}"
  unset DOMAIN_INPUT
fi
[[ "$DOMAIN" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$ ]] || fail "域名格式无效"
if [[ "$ADMIN_USERNAME_EXPLICIT" != true ]]; then
  read -r -p "设置 HL-panel 管理员账号 [admin]：" ADMIN_USERNAME_INPUT </dev/tty
  ADMIN_USERNAME="${ADMIN_USERNAME_INPUT:-admin}"
  unset ADMIN_USERNAME_INPUT
fi
[[ "$ADMIN_USERNAME" =~ ^[A-Za-z0-9._@-]{1,128}$ ]] || fail "管理员账号仅支持字母、数字、点、下划线、@ 和连字符"
read -r -s -p "设置 HL-panel 管理员密码 [回车使用 123456]：" ADMIN_PASSWORD </dev/tty
printf '\n' >/dev/tty
if [[ -z "$ADMIN_PASSWORD" || "$ADMIN_PASSWORD" == "123456" ]]; then
  ADMIN_PASSWORD="123456"
  ADMIN_DEFAULT_PASSWORD=true
  log "初始密码：123456；首次登录后必须在个人中心修改密码"
else
  ((${#ADMIN_PASSWORD} >= 8 && ${#ADMIN_PASSWORD} <= 256)) || fail "自定义管理员密码需为 8 到 256 个字符"
  read -r -s -p "再次输入管理员密码：" ADMIN_PASSWORD_CONFIRM </dev/tty
  printf '\n' >/dev/tty
  [[ "$ADMIN_PASSWORD" == "$ADMIN_PASSWORD_CONFIRM" ]] || fail "两次输入的管理员密码不一致"
  unset ADMIN_PASSWORD_CONFIRM
fi

ROLLBACK_REQUIRED=true
INSTALL_ROOT_CREATED=false
CONFIG_DIR_CREATED=false
STATE_DIR_CREATED=false
ACME_WEBROOT_CREATED=false
USER_CREATED=false
DB_ROLE_CREATED=false
DB_CREATE_STARTED=false
SYSTEMD_UNIT_CREATED=false
NGINX_RELOAD_ATTEMPTED=false
DB_OWNERSHIP_TOKEN=""
cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if [[ "$ROLLBACK_REQUIRED" == true ]]; then
    log "安装未完成，正在撤销本次新建的 HL-panel 资源"
    if [[ "$SYSTEMD_UNIT_CREATED" == true ]]; then
      systemctl disable --now hl-panel-control-api.service >/dev/null 2>&1 || true
      systemctl stop hl-panel-control-api.service >/dev/null 2>&1 || true
    fi
    remove_owned_files
    systemctl daemon-reload >/dev/null 2>&1 || true
    restore_unit_state postgresql "$POSTGRESQL_ACTIVE_BEFORE" "$POSTGRESQL_ENABLED_BEFORE"
    restore_unit_state nginx "$NGINX_ACTIVE_BEFORE" "$NGINX_ENABLED_BEFORE"
    if [[ "$NGINX_RELOAD_ATTEMPTED" == true ]] && command -v nginx >/dev/null 2>&1 && systemctl is-active --quiet nginx; then
      nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    fi
    if [[ "$DB_CREATE_STARTED" == true ]]; then
      database_marker="$(runuser -u postgres -- psql -XAtqc "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = 'hl_panel_control'" 2>/dev/null || true)"
      role_marker="$(runuser -u postgres -- psql -XAtqc "SELECT shobj_description(oid, 'pg_authid') FROM pg_roles WHERE rolname = 'hl_panel_app'" 2>/dev/null || true)"
      database_owner="$(runuser -u postgres -- psql -XAtqc "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'hl_panel_control'" 2>/dev/null || true)"
      database_empty="$(runuser -u postgres -- psql -XAtqc "SELECT NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast', 'public')) AND NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public')" --dbname=hl_panel_control 2>/dev/null || true)"
      if [[ -n "$DB_OWNERSHIP_TOKEN" && "$database_owner" == hl_panel_app && "$role_marker" == "HL-panel installer $DB_OWNERSHIP_TOKEN" ]] && { [[ "$database_marker" == "HL-panel installer $DB_OWNERSHIP_TOKEN" ]] || [[ "$database_empty" == t ]]; }; then
        runuser -u postgres -- dropdb --if-exists hl_panel_control >/dev/null 2>&1 || log "警告：本次新建数据库 hl_panel_control 未能自动删除"
      fi
    fi
    if [[ "$DB_ROLE_CREATED" == true ]]; then
      if [[ -n "$DB_OWNERSHIP_TOKEN" ]] && runuser -u postgres -- psql -XAtqc "SELECT shobj_description(oid, 'pg_authid') FROM pg_roles WHERE rolname = 'hl_panel_app'" 2>/dev/null | grep -Fqx "HL-panel installer $DB_OWNERSHIP_TOKEN"; then
        runuser -u postgres -- dropuser --if-exists hl_panel_app >/dev/null 2>&1 || log "警告：本次新建角色 hl_panel_app 未能自动删除"
      fi
    fi
    if [[ "$CONFIG_DIR_CREATED" == true ]]; then rm -rf -- "$CONFIG_DIR"; fi
    if [[ "$STATE_DIR_CREATED" == true ]]; then rm -rf -- "$STATE_DIR"; fi
    if [[ "$ACME_WEBROOT_CREATED" == true ]]; then rm -rf -- "$ACME_WEBROOT"; fi
    if [[ "$INSTALL_ROOT_CREATED" == true ]]; then rm -rf -- "$INSTALL_ROOT"; fi
    if [[ "$USER_CREATED" == true ]]; then userdel hlpanel >/dev/null 2>&1 || true; fi
  fi
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
    rm -rf -- "$WORK_DIR"
  fi
  if [[ -n "${ADMIN_PASSWORD:-}" ]]; then unset ADMIN_PASSWORD; fi
  exit "$status"
}
trap cleanup EXIT
WORK_DIR="$(mktemp -d /tmp/hl-panel-install.XXXXXX)"

export DEBIAN_FRONTEND=noninteractive
log "安装运行依赖"
apt-get update
if command -v nginx >/dev/null 2>&1; then
  apt-get install -y --no-install-recommends ca-certificates curl openssl tar python3 certbot postgresql postgresql-client
else
  apt-get install -y --no-install-recommends ca-certificates curl openssl tar python3 nginx-core certbot postgresql postgresql-client
fi
check_nginx_capabilities
systemctl enable --now postgresql
check_database_names

if [[ -z "$PUBLIC_IP" ]]; then
  PUBLIC_IP="$(curl --ipv4 --fail --silent --show-error --max-time 8 https://api.ipify.org 2>/dev/null || true)"
  valid_public_ipv4 "$PUBLIC_IP" || fail "无法确认本机公网 IPv4；请重新运行并传入 --public-ip 公网IPv4"
fi

if [[ -x /usr/sbin/nginx ]]; then
  NGINX_CONFIG="$(/usr/sbin/nginx -T 2>/dev/null)" || fail "现有 Nginx 配置检查失败，未创建 HL-panel 文件"
  find_nginx_name_conflicts "$NGINX_CONFIG"
  if ((${#NGINX_CONFLICTS[@]})); then
    fail "现有 Nginx server_name 可能匹配目标域名或 IP：${NGINX_CONFLICTS[*]}"
  fi
fi

log "下载 GitHub release 并校验压缩包"
if [[ "$VERSION" == "latest" ]]; then
  RELEASE_BASE="https://github.com/$REPOSITORY/releases/latest/download"
else
  RELEASE_BASE="https://github.com/$REPOSITORY/releases/download/$VERSION"
fi
ARCHIVE="$WORK_DIR/hl-panel-linux-amd64.tar.gz"
curl --fail --location --silent --show-error --retry 3 "$RELEASE_BASE/hl-panel-linux-amd64.tar.gz" -o "$ARCHIVE"
curl --fail --location --silent --show-error --retry 3 "$RELEASE_BASE/hl-panel-linux-amd64.tar.gz.sha256" -o "$ARCHIVE.sha256"
EXPECTED_SHA="$(awk 'NR == 1 {print $1}' "$ARCHIVE.sha256")"
[[ "$EXPECTED_SHA" =~ ^[a-fA-F0-9]{64}$ ]] || fail "release SHA256 文件格式无效"
ACTUAL_SHA="$(sha256sum "$ARCHIVE" | awk '{print $1}')"
[[ "${ACTUAL_SHA,,}" == "${EXPECTED_SHA,,}" ]] || fail "release 压缩包 SHA256 不匹配"

tar -tzf "$ARCHIVE" > "$WORK_DIR/archive.list"
if awk '
  { path=$0; sub(/^\.\//, "", path); if (path ~ /^\// || path == ".." || path ~ /(^|\/)\.\.(\/|$)/) bad=1 }
  END { exit bad ? 0 : 1 }
' "$WORK_DIR/archive.list"; then
  fail "release 压缩包包含不安全路径"
fi

mkdir -p "$WORK_DIR/release"
tar -xzf "$ARCHIVE" -C "$WORK_DIR/release" --no-same-owner
RELEASE_DIR="$WORK_DIR/release"
[[ -x "$RELEASE_DIR/bin/control-api" ]] || fail "release 缺少 bin/control-api"
[[ -x "$RELEASE_DIR/bin/usage-migrate" ]] || fail "release 缺少 bin/usage-migrate"
[[ -x "$RELEASE_DIR/bin/edge-agent" ]] || fail "release 缺少 bin/edge-agent"
[[ -s "$RELEASE_DIR/web-admin/index.html" ]] || fail "release 缺少管理端页面"
[[ -f "$RELEASE_DIR/SHA256SUMS" ]] || fail "release 缺少 SHA256SUMS"
(cd "$RELEASE_DIR" && sha256sum --check --status SHA256SUMS) || fail "release 内部文件校验失败"
grep -Fq '__HL_PANEL_IP_HTTPS_PORT__' "$RELEASE_DIR/deploy/nginx/hl-panel.conf.template" \
  || fail "所选 release 不支持独立 IP HTTPS 端口；请使用包含此功能的新版 release 和同标签 install.sh"

log "创建 HL-panel 独立目录和服务账号"
useradd --system --home-dir "$STATE_DIR" --no-create-home --shell /usr/sbin/nologin hlpanel
USER_CREATED=true
mkdir "$INSTALL_ROOT"
INSTALL_ROOT_CREATED=true
chown root:root "$INSTALL_ROOT"
chmod 0755 "$INSTALL_ROOT"
install -d -o root -g root -m 0755 "$INSTALL_ROOT/releases"
mkdir "$STATE_DIR"
STATE_DIR_CREATED=true
chown hlpanel:hlpanel "$STATE_DIR"
chmod 0750 "$STATE_DIR"
mkdir "$CONFIG_DIR"
CONFIG_DIR_CREATED=true
chown root:hlpanel "$CONFIG_DIR"
chmod 0750 "$CONFIG_DIR"
install -d -o root -g hlpanel -m 0750 "$CONFIG_DIR/tls"
install -d -o root -g root -m 0700 "$INSTALL_ROOT/backups"
RELEASE_ID="${VERSION//[^A-Za-z0-9._-]/_}-$RUN_ID"
FINAL_RELEASE="$INSTALL_ROOT/releases/$RELEASE_ID"
install -d -o root -g root -m 0755 "$FINAL_RELEASE"
cp -a "$RELEASE_DIR/." "$FINAL_RELEASE/"
chown -R root:root "$FINAL_RELEASE"
chmod -R a-w "$FINAL_RELEASE"

log "创建新的 PostgreSQL 角色和空数据库"
DB_PASSWORD="$(openssl rand -hex 32)"
DB_OWNERSHIP_TOKEN="$(openssl rand -hex 24)"
DB_ROLE_CREATED=true
check_database_names
printf "BEGIN; CREATE ROLE hl_panel_app LOGIN PASSWORD '%s'; COMMENT ON ROLE hl_panel_app IS 'HL-panel installer %s'; COMMIT;\n" "$DB_PASSWORD" "$DB_OWNERSHIP_TOKEN" | runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 >/dev/null
DB_CREATE_STARTED=true
if runuser -u postgres -- createdb --owner=hl_panel_app hl_panel_control; then
  runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -c "COMMENT ON DATABASE hl_panel_control IS 'HL-panel installer $DB_OWNERSHIP_TOKEN'" >/dev/null
else
  DB_CREATE_STARTED=false
  fail "创建新的 PostgreSQL 数据库失败；已有同名数据库不会被删除"
fi
DATABASE_URL="postgresql://hl_panel_app:${DB_PASSWORD}@127.0.0.1:5432/hl_panel_control?sslmode=disable"
printf '%s\n' "$DATABASE_URL" > "$CONFIG_DIR/database-url"
unset DB_PASSWORD DATABASE_URL
chown root:hlpanel "$CONFIG_DIR/database-url"
chmod 0640 "$CONFIG_DIR/database-url"

printf '%s\n' "$(openssl rand -base64 48)" > "$CONFIG_DIR/customer-password-fingerprint-key"
chown root:hlpanel "$CONFIG_DIR/customer-password-fingerprint-key"
chmod 0640 "$CONFIG_DIR/customer-password-fingerprint-key"

ADMIN_HASH="$(printf '%s\n' "$ADMIN_PASSWORD" | "$FINAL_RELEASE/bin/control-api" hash-password)"
unset ADMIN_PASSWORD
cat > "$WORK_DIR/control-api.env" <<EOF
CONTROL_LISTEN_ADDRESS=127.0.0.1:$API_PORT
CONTROL_ALLOW_INSECURE_HTTP=false
CONTROL_ALLOW_VOLATILE_STORE=false
CONTROL_DATABASE_URL_FILE=$CONFIG_DIR/database-url
CONTROL_BOOTSTRAP_ADMIN_USERNAME=$ADMIN_USERNAME
CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH="$ADMIN_HASH"
CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_FILE=$CONFIG_DIR/customer-password-fingerprint-key
EOF
unset ADMIN_HASH
install -o root -g hlpanel -m 0640 "$WORK_DIR/control-api.env" "$CONFIG_DIR/control-api.env"

log "初始化 usage 数据结构"
"$FINAL_RELEASE/bin/usage-migrate" apply -dsn-file "$CONFIG_DIR/database-url"
"$FINAL_RELEASE/bin/usage-migrate" verify -dsn-file "$CONFIG_DIR/database-url"

log "配置 IP HTTPS 入口和 Nginx 反向代理"
install -d -o root -g root -m 0755 "$CONFIG_DIR/tls/domain-certificates/self-signed"
openssl req -x509 -nodes -newkey rsa:3072 -days 365 -sha256 \
  -keyout "$CONFIG_DIR/tls/domain-certificates/self-signed/privkey.pem" \
  -out "$CONFIG_DIR/tls/domain-certificates/self-signed/fullchain.pem" \
  -subj "/CN=$DOMAIN" \
  -addext "subjectAltName=DNS:$DOMAIN" >/dev/null 2>&1
openssl req -x509 -nodes -newkey rsa:3072 -days 365 -sha256 \
  -keyout "$CONFIG_DIR/tls/ip-privkey.pem" \
  -out "$CONFIG_DIR/tls/ip-fullchain.pem" \
  -subj "/CN=$PUBLIC_IP" \
  -addext "subjectAltName=IP:$PUBLIC_IP" >/dev/null 2>&1
chown root:root "$CONFIG_DIR/tls/"*.pem
chmod 0600 "$CONFIG_DIR/tls/"*-privkey.pem
chmod 0644 "$CONFIG_DIR/tls/"*-fullchain.pem
chown root:root "$CONFIG_DIR/tls/domain-certificates/self-signed/"*.pem
chmod 0600 "$CONFIG_DIR/tls/domain-certificates/self-signed/privkey.pem"
chmod 0644 "$CONFIG_DIR/tls/domain-certificates/self-signed/fullchain.pem"
CERTBOT_CERT_NAME="hl-panel-${DOMAIN//./-}-$RUN_ID"
ln -s "$CONFIG_DIR/tls/domain-certificates/self-signed" "$CONFIG_DIR/tls/domain-current"

mkdir "$ACME_WEBROOT"
ACME_WEBROOT_CREATED=true
chown root:root "$ACME_WEBROOT"
chmod 0755 "$ACME_WEBROOT"
install -d -o root -g root -m 0755 "$ACME_WEBROOT/.well-known/acme-challenge"
install_new_file "$FINAL_RELEASE/deploy/nginx/conf.d/hl-panel-rate-limit.conf" /etc/nginx/conf.d/hl-panel-rate-limit.conf root root 0644
install_new_file "$FINAL_RELEASE/deploy/nginx/snippets/hl-panel-security-headers.conf" /etc/nginx/snippets/hl-panel-security-headers.conf root root 0644
sed -e "s|http://127.0.0.1:8080;|http://127.0.0.1:$API_PORT;|" \
  "$FINAL_RELEASE/deploy/nginx/snippets/hl-panel-api-proxy.conf" > "$WORK_DIR/hl-panel-api-proxy.conf"
grep -Fxq "proxy_pass http://127.0.0.1:$API_PORT;" "$WORK_DIR/hl-panel-api-proxy.conf" || fail "HL-panel API 代理端口渲染失败"
install_new_file "$WORK_DIR/hl-panel-api-proxy.conf" /etc/nginx/snippets/hl-panel-api-proxy.conf root root 0644
install_new_file "$FINAL_RELEASE/deploy/nginx/snippets/hl-panel-app-locations.conf" /etc/nginx/snippets/hl-panel-app-locations.conf root root 0644
sed -e "s|__HL_PANEL_DOMAIN__|$DOMAIN|g" -e "s|__HL_PANEL_IP__|$PUBLIC_IP|g" \
  -e "s|__HL_PANEL_IP_HTTPS_PORT__|$IP_HTTPS_PORT|g" \
  "$FINAL_RELEASE/deploy/nginx/hl-panel.conf.template" > "$WORK_DIR/hl-panel.conf"
install_new_file "$WORK_DIR/hl-panel.conf" "$NGINX_AVAILABLE" root root 0644
if ! ln -s "$NGINX_AVAILABLE" "$NGINX_ENABLED"; then fail "Nginx 启用路径在安装期间被占用：$NGINX_ENABLED"; fi
OWNED_SYMLINKS["$NGINX_ENABLED"]="$NGINX_AVAILABLE"
install_new_file "$FINAL_RELEASE/deploy/systemd/hl-panel-control-api.service" /etc/systemd/system/hl-panel-control-api.service root root 0644
SYSTEMD_UNIT_CREATED=true
printf 'DOMAIN=%s\nPUBLIC_IP=%s\nIP_HTTPS_PORT=%s\nCERTBOT_CERT_NAME=%s\n' "$DOMAIN" "$PUBLIC_IP" "$IP_HTTPS_PORT" "$CERTBOT_CERT_NAME" > "$CONFIG_DIR/domain.conf"
chown root:root "$CONFIG_DIR/domain.conf"
chmod 0600 "$CONFIG_DIR/domain.conf"
install_new_file "$FINAL_RELEASE/deploy/enable-domain-tls.sh" /usr/local/sbin/hl-panel-enable-domain-tls root root 0755
install_new_file "$FINAL_RELEASE/deploy/update.sh" /usr/local/sbin/hl-panel-update root root 0755
install_new_file "$FINAL_RELEASE/deploy/configure-reality.sh" /usr/local/sbin/hl-panel-configure-reality root root 0755

TEMP_LINK="$INSTALL_ROOT/.current-$RUN_ID"
ln -s "$FINAL_RELEASE" "$TEMP_LINK"
mv -Tf "$TEMP_LINK" "$INSTALL_ROOT/current"
systemctl daemon-reload
nginx -t
check_api_port_available
check_ip_https_port_available
systemctl enable --now hl-panel-control-api.service

log "等待 API 本机健康检查（127.0.0.1:$API_PORT）"
for attempt in {1..20}; do
  if systemctl is-active --quiet hl-panel-control-api.service \
    && curl --fail --silent --show-error --connect-timeout 2 --max-time 5 "http://127.0.0.1:$API_PORT/healthz" >/dev/null \
    && systemctl is-active --quiet hl-panel-control-api.service; then break; fi
  if ((attempt == 20)); then
    systemctl --no-pager --full status hl-panel-control-api.service || true
    fail "API 未通过本机健康检查"
  fi
  sleep 1
done

NGINX_RELOAD_ATTEMPTED=true
systemctl enable --now nginx
systemctl reload nginx
ROLLBACK_REQUIRED=false

if [[ "$DOMAIN" == "hl-panel.invalid" ]]; then
  log "未设置域名；使用 IP 入口 https://$PUBLIC_IP:$IP_HTTPS_PORT/（自签名证书）"
else
log "检测域名解析；只在 A 记录指向本机时尝试签发证书"
DNS_MATCH=false
DNS_LOOKUP_OK=true
if ! DNS_A_RECORDS="$(resolve_domain_records A)"; then DNS_LOOKUP_OK=false; fi
if ! DNS_AAAA_RECORDS="$(resolve_domain_records AAAA)"; then DNS_LOOKUP_OK=false; fi
if [[ "$DNS_LOOKUP_OK" == true && "$DNS_A_RECORDS" == "$PUBLIC_IP" && -z "$DNS_AAAA_RECORDS" ]]; then DNS_MATCH=true; fi
if [[ "$DNS_MATCH" == true ]]; then
  if /usr/local/sbin/hl-panel-enable-domain-tls --email "$EMAIL"; then
    log "域名证书已启用"
  else
    log "域名证书申请未成功；已保留 IP HTTPS 入口，可之后运行 hl-panel-enable-domain-tls 重试"
  fi
else
  if [[ "$DNS_LOOKUP_OK" != true ]]; then log "提醒：DNS 查询失败，未申请域名证书；解析恢复后可重试"; fi
  log "提醒：$DOMAIN 的 A 记录必须全部指向本机 $PUBLIC_IP，且不能存在未经核对的 AAAA 记录；本次未修改 DNS，也未申请证书"
  log "当前 IP 入口：https://$PUBLIC_IP:$IP_HTTPS_PORT/（使用自签名证书）"
  log "DNS 指向本机后运行：hl-panel-enable-domain-tls"
fi
fi

DOMAIN_DISPLAY="$DOMAIN"
[[ "$DOMAIN" != "hl-panel.invalid" ]] || DOMAIN_DISPLAY="未设置（使用 IP 入口）"

cat <<EOF

HL-panel 安装完成
  版本：$RELEASE_ID
  域名：$DOMAIN_DISPLAY
  管理员：$ADMIN_USERNAME
  IP 入口：https://$PUBLIC_IP:$IP_HTTPS_PORT/
  API 本机监听：127.0.0.1:$API_PORT
  服务：hl-panel-control-api.service

EOF
if [[ "$ADMIN_DEFAULT_PASSWORD" == true ]]; then
  printf '  初始密码：123456\n  首次登录后必须到个人中心修改密码（新密码至少 8 个字符）。\n'
else
  printf '  密码：安装时设置的自定义密码（隐藏，不写入日志）。\n'
fi
