#!/usr/bin/env bash
set -Eeuo pipefail

CONFIG_FILE="/etc/hl-panel/domain.conf"
ACME_WEBROOT="/var/lib/hl-panel-acme"
EMAIL=""
RENEWED_LINEAGE=""

fail() { printf '[HL-panel TLS] 错误：%s\n' "$*" >&2; exit 1; }

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

while (($#)); do
  case "$1" in
    --email) (($# >= 2)) || fail "--email 缺少参数"; EMAIL="$2"; shift 2 ;;
    --renewed-lineage) (($# >= 2)) || fail "--renewed-lineage 缺少参数"; RENEWED_LINEAGE="$2"; shift 2 ;;
    -h|--help) printf '用法：hl-panel-enable-domain-tls [--email EMAIL]\n'; exit 0 ;;
    *) fail "未知参数：$1" ;;
  esac
done

[[ $EUID -eq 0 ]] || fail "请使用 root 运行"
[[ -r "$CONFIG_FILE" ]] || fail "未找到 HL-panel 安装配置：$CONFIG_FILE"
# This file is root-owned and contains only validated values written by install.sh.
# shellcheck disable=SC1090
source "$CONFIG_FILE"
[[ "${DOMAIN:-}" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$ ]] || fail "安装配置中的域名无效"
[[ "${PUBLIC_IP:-}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || fail "安装配置中的 IP 无效"
# Existing installations without this value used 443; new installations save
# their dedicated IP HTTPS port in domain.conf.
IP_HTTPS_PORT="${IP_HTTPS_PORT:-443}"
[[ "$IP_HTTPS_PORT" =~ ^[0-9]{1,5}$ ]] || fail "安装配置中的 IP HTTPS 端口无效"
IP_HTTPS_PORT="$((10#$IP_HTTPS_PORT))"
((IP_HTTPS_PORT >= 1 && IP_HTTPS_PORT <= 65535)) || fail "安装配置中的 IP HTTPS 端口无效"
[[ "${CERTBOT_CERT_NAME:-}" =~ ^[A-Za-z0-9.-]{1,200}$ ]] || fail "安装配置中的 Certbot lineage 名称无效"

TLS_DIR="/etc/hl-panel/tls"
CERT_ROOT="$TLS_DIR/domain-certificates"
CURRENT_LINK="$TLS_DIR/domain-current"
CERT_NAME="$CERTBOT_CERT_NAME"
LIVE_DIR="/etc/letsencrypt/live/$CERT_NAME"
RENEWAL_CONFIG="/etc/letsencrypt/renewal/$CERT_NAME.conf"
RENEWAL_HOOK="/etc/letsencrypt/renewal-hooks/deploy/hl-panel-copy-certificate"

if [[ -n "$RENEWED_LINEAGE" ]]; then
  [[ "$RENEWED_LINEAGE" == "$LIVE_DIR" ]] || fail "续期 lineage 不属于当前 HL-panel 安装"
  [[ -s "$RENEWED_LINEAGE/fullchain.pem" && -s "$RENEWED_LINEAGE/privkey.pem" ]] || fail "续期 lineage 缺少证书或私钥"
else
  [[ -d "$ACME_WEBROOT" && ! -L "$ACME_WEBROOT" ]] || fail "未找到独立 ACME 公共目录：$ACME_WEBROOT"
  command -v python3 >/dev/null 2>&1 || fail "缺少 python3；请先安装 python3 后重试"
  DNS_A_RECORDS="$(resolve_domain_records A)" || fail "$DOMAIN 的 A 记录查询失败，未申请证书"
  DNS_AAAA_RECORDS="$(resolve_domain_records AAAA)" || fail "$DOMAIN 的 AAAA 记录查询失败，未申请证书"
  [[ "$DNS_A_RECORDS" == "$PUBLIC_IP" && -z "$DNS_AAAA_RECORDS" ]] || fail "$DOMAIN 必须只有指向 $PUBLIC_IP 的 A 记录，且不能存在未经核对的 AAAA 记录"

  if [[ -e "$RENEWAL_CONFIG" ]]; then
    awk -F= -v domain="$DOMAIN" '$1 == "domains" { value=$2; gsub(/[[:space:]]/, "", value); if (value == domain) found=1 } END { exit !found }' "$RENEWAL_CONFIG" || fail "同名 Certbot lineage 的域名不匹配"
    grep -Eq '^authenticator *= *webroot[[:space:]]*$' "$RENEWAL_CONFIG" || fail "同名 Certbot lineage 不是 HL-panel webroot 配置"
    grep -Fqx "webroot_path = $ACME_WEBROOT" "$RENEWAL_CONFIG" || fail "同名 Certbot lineage 使用了其他 webroot"
  elif [[ -e "$LIVE_DIR" || -L "$LIVE_DIR" ]]; then
    fail "Certbot lineage 路径已被占用，但找不到 HL-panel renewal 配置"
  else
    CERTBOT=(certbot certonly --webroot -w "$ACME_WEBROOT" --cert-name "$CERT_NAME" -d "$DOMAIN" --non-interactive --agree-tos)
    if [[ -n "$EMAIL" ]]; then
      CERTBOT+=(--email "$EMAIL")
    else
      CERTBOT+=(--register-unsafely-without-email)
    fi
    "${CERTBOT[@]}"
  fi
fi

CERT_FILE="$LIVE_DIR/fullchain.pem"
KEY_FILE="$LIVE_DIR/privkey.pem"
[[ -s "$CERT_FILE" && -s "$KEY_FILE" ]] || fail "Certbot 未生成完整证书"
openssl x509 -in "$CERT_FILE" -noout -checkhost "$DOMAIN" >/dev/null || fail "证书不包含 $DOMAIN"
CERT_KEY_HASH="$(openssl x509 -in "$CERT_FILE" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum | awk '{print $1}')"
PRIVATE_KEY_HASH="$(openssl pkey -in "$KEY_FILE" -pubout -outform DER | sha256sum | awk '{print $1}')"
[[ "$CERT_KEY_HASH" == "$PRIVATE_KEY_HASH" ]] || fail "证书与私钥不匹配"

[[ -L "$CURRENT_LINK" ]] || fail "域名证书活动路径不是受管软链接"
OLD_TARGET="$(readlink "$CURRENT_LINK")"
[[ -s "$CURRENT_LINK/fullchain.pem" && -s "$CURRENT_LINK/privkey.pem" ]] || fail "当前域名证书不完整，拒绝切换"
if [[ -e "$RENEWAL_HOOK" || -L "$RENEWAL_HOOK" ]]; then
  [[ -f "$RENEWAL_HOOK" && ! -L "$RENEWAL_HOOK" && "$(grep -Fxc '# Managed by HL-panel.' "$RENEWAL_HOOK" 2>/dev/null || true)" == 1 ]] || fail "证书续期钩子路径已被其他程序占用"
fi

install -d -o root -g root -m 0755 "$CERT_ROOT"
CERT_DIR="$(mktemp -d "$CERT_ROOT/certbot.XXXXXX")"
NEXT_LINK=""
HOOK_TMP=""
ACTIVATION_DONE=false
LINK_SWITCHED=false
cleanup_pending() {
  if [[ -n "${NEXT_LINK:-}" ]]; then rm -f -- "$NEXT_LINK"; fi
  if [[ -n "${HOOK_TMP:-}" ]]; then rm -f -- "$HOOK_TMP"; fi
  if [[ "${ACTIVATION_DONE:-false}" != true && "$LINK_SWITCHED" == true ]]; then
    local restore_link="$TLS_DIR/.domain-current-cleanup-$$"
    rm -f -- "$restore_link"
    if ln -s "$OLD_TARGET" "$restore_link" && mv -Tf "$restore_link" "$CURRENT_LINK"; then
      nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
      LINK_SWITCHED=false
    else
      printf '[HL-panel TLS] 警告：无法恢复上一版证书链接，保留当前证书文件以避免留下断链。\n' >&2
    fi
  fi
  if [[ "${ACTIVATION_DONE:-false}" != true && "$LINK_SWITCHED" != true && -n "${CERT_DIR:-}" && -d "$CERT_DIR" ]]; then rm -rf -- "$CERT_DIR"; fi
}
trap cleanup_pending EXIT
install -o root -g root -m 0644 "$CERT_FILE" "$CERT_DIR/fullchain.pem"
install -o root -g root -m 0600 "$KEY_FILE" "$CERT_DIR/privkey.pem"
chmod 0755 "$CERT_DIR"

NEXT_LINK="$TLS_DIR/.domain-current-$$"
ln -s "$CERT_DIR" "$NEXT_LINK"
mv -Tf "$NEXT_LINK" "$CURRENT_LINK"
LINK_SWITCHED=true

if ! nginx -t; then
  fail "新证书的 Nginx 配置检查失败，已恢复上一版证书"
fi
if ! systemctl reload nginx; then
  fail "Nginx reload 失败，已恢复上一版证书"
fi

install -d -o root -g root -m 0755 "$(dirname "$RENEWAL_HOOK")"
HOOK_TMP="$(mktemp "$(dirname "$RENEWAL_HOOK")/.hl-panel-hook.XXXXXX")"
cat > "$HOOK_TMP" <<'EOF'
#!/usr/bin/env bash
# Managed by HL-panel.
set -Eeuo pipefail
source /etc/hl-panel/domain.conf
[[ "${RENEWED_LINEAGE:-}" == "/etc/letsencrypt/live/${CERTBOT_CERT_NAME:?}" ]] || exit 0
exec /usr/local/sbin/hl-panel-enable-domain-tls --renewed-lineage "$RENEWED_LINEAGE"
EOF
chmod 0755 "$HOOK_TMP"
chown root:root "$HOOK_TMP"
if [[ -e "$RENEWAL_HOOK" || -L "$RENEWAL_HOOK" ]]; then
  mv -f "$HOOK_TMP" "$RENEWAL_HOOK"
else
  ln "$HOOK_TMP" "$RENEWAL_HOOK" || fail "证书续期 hook 路径在安装期间被占用"
  rm -f -- "$HOOK_TMP"
fi
HOOK_TMP=""
ACTIVATION_DONE=true

printf '[HL-panel TLS] 已启用 https://%s/，IP 入口：https://%s:%s/。\n' "$DOMAIN" "$PUBLIC_IP" "$IP_HTTPS_PORT"
