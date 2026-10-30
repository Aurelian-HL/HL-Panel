#!/usr/bin/env bash
set -Eeuo pipefail
set +x
# Installs the HL node agent, native probe and both bundled forwarding engines.
repository="Aurelian-HL/HL-Panel"
version=""
origin=""
archive=""
checksum=""
enrollment_token=""
ca_file=""
node_address=""
apt_refreshed=0
while (($#)); do
  case "$1" in
    --repo|--version|--panel-url|--archive|--sha256|--token|--ca-file|--node-address)
      (($# >= 2)) || { echo "缺少参数值：$1" >&2; exit 2; }
      case "$1" in
        --repo) repository="$2" ;; --version) version="$2" ;; --panel-url) origin="$2" ;;
        --archive) archive="$2" ;; --sha256) checksum="$2" ;;
        --token) enrollment_token="$2" ;; --ca-file) ca_file="$2" ;; --node-address) node_address="$2" ;;
      esac
      shift 2 ;;
    *) echo '未知参数，请使用文档中的节点安装命令。' >&2; exit 2 ;;
  esac
done
[[ $(id -u) == 0 ]] || { echo '请在目标节点机使用 root 执行。' >&2; exit 1; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { echo '当前正式安装包支持 Linux amd64（x86_64）。' >&2; exit 1; }
[[ "$repository" == Aurelian-HL/HL-Panel ]] || { echo '仅支持 HL-panel 官方仓库。' >&2; exit 2; }
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo '请指定正式版本标签。' >&2; exit 2; }
[[ "$origin" =~ ^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$ ]] || { echo '面板地址必须是 HTTPS 域名或 IPv4，可带端口，不含路径。' >&2; exit 2; }
[[ -e /run/systemd/system ]] || { echo '节点安装需要 systemd。' >&2; exit 1; }
[[ "$enrollment_token" =~ ^[A-Za-z0-9_-]{8,256}$ ]] || { echo '缺少或无效的 --token，请在面板生成完整安装命令。' >&2; exit 2; }
if [[ -n "$ca_file" ]]; then
  [[ -f "$ca_file" && ! -L "$ca_file" ]] || { echo 'CA 文件不存在或是链接；未修改节点。' >&2; exit 2; }
fi

install_missing_packages() {
  command -v apt-get >/dev/null || { echo "缺少依赖：$*；当前自动补齐支持 Debian/Ubuntu 的 apt-get，尚未修改节点身份。" >&2; exit 1; }
  if [[ "$apt_refreshed" == 0 ]]; then
    DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -qq -o Acquire::Retries=2 update
    apt_refreshed=1
  fi
  DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -qq -o DPkg::Lock::Timeout=60 --reinstall install -y --no-install-recommends "$@"
}

ensure_time_synchronization() {
  local synchronized provider unit
  if ! command -v timedatectl >/dev/null 2>&1; then
    echo '[HL-panel 节点] 警告：系统没有 timedatectl，无法自动确认时间同步；请手动配置 NTP' >&2
    return 0
  fi
  synchronized="$(timedatectl show -p NTPSynchronized --value 2>/dev/null || true)"
  if [[ "$synchronized" == yes ]]; then
    echo '[HL-panel 节点] 系统时间已同步（NTP）'
    return 0
  fi
  timedatectl set-ntp true >/dev/null 2>&1 || true
  provider=""
  for unit in systemd-timesyncd.service chrony.service ntp.service; do
    if systemctl list-unit-files "$unit" --no-legend 2>/dev/null | awk '{print $1}' | grep -Fxq "$unit"; then
      provider="$unit"
      break
    fi
  done
  if [[ -z "$provider" && -z "$archive" && -x "$(command -v apt-get 2>/dev/null || true)" ]]; then
    if install_missing_packages systemd-timesyncd >/dev/null 2>&1; then
      provider="systemd-timesyncd.service"
    else
      echo '[HL-panel 节点] 警告：无法安装 systemd-timesyncd，安装继续；请手动配置 NTP' >&2
    fi
  fi
  if [[ -z "$provider" ]]; then
    echo '[HL-panel 节点] 警告：未找到可用的 NTP 服务，安装继续；请安装 systemd-timesyncd 或 chrony 后执行 timedatectl set-ntp true' >&2
    return 0
  fi
  systemctl enable --now "$provider" >/dev/null 2>&1 || echo "[HL-panel 节点] 警告：无法启动 $provider，请检查 systemd 服务状态" >&2
  for _ in {1..12}; do
    synchronized="$(timedatectl show -p NTPSynchronized --value 2>/dev/null || true)"
    if [[ "$synchronized" == yes ]]; then
      echo "[HL-panel 节点] 系统时间同步已启用：$provider"
      return 0
    fi
    sleep 1
  done
  echo "[HL-panel 节点] 警告：$provider 尚未报告已同步；TLS、节点租约和订阅有效期可能受时钟漂移影响" >&2
}

configure_tcp_bbr() {
  if ! python3 "$release/deploy/configure-node-bbr2.py"; then
    echo '[HL-panel 节点] 警告：节点安装已成功，但 TCP BBR 自动设置未完成；请核对上方原因及实际算法，安装不会因此撤销。' >&2
  fi
}

packages=()
command -v python3 >/dev/null || packages+=(python3)
command -v curl >/dev/null || packages+=(curl)
[[ -s /etc/ssl/certs/ca-certificates.crt ]] || packages+=(ca-certificates)
if ! command -v ip >/dev/null || ! command -v tc >/dev/null; then packages+=(iproute2); fi
command -v useradd >/dev/null || packages+=(passwd)
if ! command -v runuser >/dev/null || ! command -v flock >/dev/null; then packages+=(util-linux); fi
command -v systemd-analyze >/dev/null || packages+=(systemd)
command -v sysctl >/dev/null || packages+=(procps)
command -v modprobe >/dev/null || packages+=(kmod)
if [[ -n "$archive" ]]; then
  [[ -f "$archive" && -f "$checksum" ]] || { echo '离线安装必须同时提供 --archive 和 --sha256 文件。' >&2; exit 2; }
  ((${#packages[@]} == 0)) || { echo "离线模式缺少依赖：${packages[*]}；请预装后重试，尚未修改节点身份。" >&2; exit 1; }
elif ((${#packages[@]})); then
  install_missing_packages "${packages[@]}"
else
  echo '[HL-panel 节点] 系统安装依赖已齐全，跳过软件包安装。'
fi
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3,9) else "需要 Python 3.9 或更新版本；尚未修改节点身份")'
ensure_time_synchronization
# Public address is separate from the operating-system hostname.
if [[ -z "$node_address" ]]; then
  for endpoint in https://api.ipify.org https://ipv4.icanhazip.com; do
    candidate=$(curl -4 --fail --silent --show-error --connect-timeout 5 --max-time 10 "$endpoint" 2>/dev/null || true)
    if python3 - "$candidate" <<'PY'
import ipaddress,sys
try:
    address=ipaddress.IPv4Address(sys.argv[1].strip())
    assert address.is_global
except (ValueError,AssertionError): sys.exit(1)
PY
    then node_address="${candidate//$'\n'/}"; break; fi
  done
fi
[[ -n "$node_address" ]] || { echo '无法自动识别节点公网地址；在原命令末尾添加 --node-address 节点公网IP 重试。未修改节点。' >&2; exit 1; }
node_address=$(python3 - "$node_address" <<'PY'
import ipaddress,re,sys
value=sys.argv[1].strip()
try: print(ipaddress.ip_address(value)); sys.exit(0)
except ValueError: pass
if re.fullmatch(r'[0-9.]+', value):
    raise SystemExit('节点 IPv4 地址无效')
value=value.lower().removesuffix('.')
if len(value)>253 or not all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?',s) for s in value.split('.')):
    raise SystemExit('节点地址须为一个 IP 或域名，不含协议、端口或路径')
print(value)
PY
)
origin=$(python3 - "$origin" "$ca_file" <<'PY'
import ipaddress, re, ssl, sys, urllib.parse
try:
    address = urllib.parse.urlsplit(sys.argv[1])
    if address.scheme != 'https' or address.username is not None or address.password is not None or address.path or address.query or address.fragment:
        raise ValueError()
    host = address.hostname or ''
    try:
        ipaddress.IPv4Address(host)
    except ValueError:
        if re.fullmatch(r'[0-9.]+', host): raise ValueError()
        host = host.removesuffix('.')
        if len(host) > 253 or not all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label) for label in host.split('.')):
            raise ValueError()
    if address.port is not None and not 1 <= address.port <= 65535: raise ValueError()
    if sys.argv[2]: ssl.create_default_context(cafile=sys.argv[2])
    print('https://' + host + (':' + str(address.port) if address.port is not None else ''))
except (ValueError, OSError, ssl.SSLError):
    raise SystemExit('面板域名、端口或 CA 证书无效；未修改节点')
PY
)
temporary=$(mktemp -d)
created_install=0
install_ready=0
installed_ca=0
created_tc_dropin=0
cleanup_install() {
  if [[ "$created_install" == 1 && "$install_ready" == 0 && ! -e /var/lib/hl-panel-edge/credentials.json && ! -L /var/lib/hl-panel-edge/credentials.json ]]; then
    rm -f /opt/hl-panel/edge-agent /opt/hl-panel/enroll-node.sh /etc/hl-panel/edge-agent.json /etc/systemd/system/hl-panel-edge-agent.service
    rm -f /opt/hl-panel/node-engines/xray /opt/hl-panel/node-engines/gost /opt/hl-panel/node-engines/geoip.dat /opt/hl-panel/node-engines/geosite.dat
    if [[ "$installed_ca" == 1 ]]; then
      rm -f /etc/hl-panel/edge-agent-ca.pem
    fi
    if [[ "$created_tc_dropin" == 1 ]]; then
      rm -f /etc/systemd/system/hl-panel-edge-agent.service.d/30-traffic-control.conf
      rmdir /etc/systemd/system/hl-panel-edge-agent.service.d 2>/dev/null || true
    fi
    rmdir /opt/hl-panel/node-engines 2>/dev/null || true
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
  rm -rf -- "$temporary"
}
trap cleanup_install EXIT
trap 'exit 130' HUP INT TERM
if [[ -z "$archive" ]]; then
  archive="$temporary/hl-panel-linux-amd64.tar.gz"
  checksum="$archive.sha256"
  base="https://github.com/$repository/releases/download/$version"
  for asset in hl-panel-linux-amd64.tar.gz hl-panel-linux-amd64.tar.gz.sha256; do
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 300 "$base/$asset" --output "$temporary/$asset"
  done
fi
printf '[HL-panel 节点] 验证 %s 安装包…\n' "$version"
python3 - "$archive" "$checksum" "$temporary/release" <<'PY'
import hashlib, pathlib, re, sys, tarfile
archive, checksum, target = map(pathlib.Path, sys.argv[1:])
def require(condition, message):
    if not condition: raise SystemExit(message)
def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024*1024), b''): h.update(block)
    return h.hexdigest()
fields = checksum.read_text().strip().split()
require(len(fields) in (1, 2) and re.fullmatch('[0-9a-f]{64}', fields[0]), '安装包 SHA256 文件无效')
require(archive.stat().st_size <= 256*1024*1024 and digest(archive) == fields[0], '安装包 SHA256 校验失败；未修改节点')
target.mkdir(mode=0o755)
names = set()
with tarfile.open(archive, 'r:gz') as source:
    total = 0
    for entry in source.getmembers():
        name = entry.name.removeprefix('./').rstrip('/')
        if name in ('', '.') and entry.isdir(): continue
        require(name and not name.startswith('/') and all(p not in ('', '.', '..') for p in name.split('/')) and ':' not in name and '\\' not in name, '安装包路径不安全')
        require(entry.isfile() or entry.isdir(), '安装包包含链接或特殊文件')
        require(name not in names, '安装包包含重复路径')
        names.add(name); total += entry.size
        require(total <= 1024*1024*1024, '安装包解压大小过大')
    if sys.version_info >= (3,12): source.extractall(target, filter='data')
    else: source.extractall(target)
verified = set()
require((target/'SHA256SUMS').is_file(), '缺少文件校验清单')
for line in (target/'SHA256SUMS').read_text().splitlines():
    expected, name = line.split('  ', 1); name = name.removeprefix('./')
    require(re.fullmatch('[0-9a-f]{64}', expected) and name in names and name not in verified, '文件摘要清单无效')
    require((target/name).is_file() and digest(target/name) == expected, '文件摘要校验失败')
    verified.add(name)
require({str(p.relative_to(target)) for p in target.rglob('*') if p.is_file()} == verified | {'SHA256SUMS'}, '存在未校验文件')
for name in ('bin/edge-agent', 'bin/xray', 'bin/gost', 'deploy/edge-agent/install.sh', 'deploy/edge-agent/enroll.sh', 'deploy/reset-unenrolled-node.py', 'deploy/configure-node-bbr2.py', 'deploy/configure-node-bbr.py', 'deploy/configure-node-traffic-control.py', 'deploy/systemd/hl-panel-edge-agent.service'):
    require(name in verified, '缺少节点安装文件：' + name)
(target/'bin/edge-agent').chmod(0o755)
PY
release="$temporary/release"
for asset in geoip.dat geosite.dat; do
  [[ -s "$release/bin/$asset" ]] || { echo '安装包缺少 Xray 资源文件；尚未修改节点。' >&2; exit 1; }
done
for asset in bin/xray bin/gost; do
  [[ -f "$release/$asset" ]] || { echo '安装包缺少转发引擎；未修改节点，请使用新版本。' >&2; exit 1; }
  chmod 0755 "$release/$asset"
done
python3 - "$release/bin/xray" <<'PY'
import subprocess, sys
result = subprocess.run([sys.argv[1], 'version'], capture_output=True, timeout=15)
if result.returncode or not result.stdout.startswith(b'Xray 26.7.28 '):
    raise SystemExit('Xray 版本未通过旧客户端兼容验收；要求 26.7.28，未修改节点。')
PY
if [[ -e /var/lib/hl-panel-edge/credentials.json ]]; then
  python3 "$release/deploy/install-node-update.py" "$release" --preflight
  [[ -f "$release/deploy/reinstall-node.py" ]] || { echo '该版本不支持重装换组，请先更新面板后重新复制安装命令。' >&2; exit 1; }
  echo '[HL-panel 节点] 发现已有节点，按本次有效命令重新安装并切换设备组…'
  HL_INSTALL_ENROLLMENT_TOKEN="$enrollment_token" python3 "$release/deploy/reinstall-node.py" \
    --release "$release" --panel-url "$origin" --node-address "$node_address" --version "$version" --ca-file "$ca_file"
  unset enrollment_token
  configure_tcp_bbr
  echo '[HL-panel 节点] 完成，请返回面板查看在线状态。'
  exit 0
fi
# Check the exact bundled executables before creating an installation.
python3 "$release/deploy/install-node-update.py" "$release" --preflight --candidate-only
"$release/bin/xray" version >/dev/null
"$release/bin/gost" -V >/dev/null
python3 "$release/deploy/reset-unenrolled-node.py" --prepare-lock
exec {node_install_lock}<> /opt/hl-panel/.node-update.lock
flock -n "$node_install_lock" || { echo '已有节点安装或更新正在执行，请稍后重跑同一条命令。' >&2; exit 1; }
python3 "$release/deploy/reset-unenrolled-node.py"
created_install=1
install -d -m 0755 -o root -g root /opt/hl-panel/node-engines
install -m 0755 -o root -g root "$release/bin/xray" /opt/hl-panel/node-engines/xray
install -m 0755 -o root -g root "$release/bin/gost" /opt/hl-panel/node-engines/gost
install -m 0644 -o root -g root "$release/bin/geoip.dat" /opt/hl-panel/node-engines/geoip.dat
install -m 0644 -o root -g root "$release/bin/geosite.dat" /opt/hl-panel/node-engines/geosite.dat
sh "$release/deploy/edge-agent/install.sh" "$release/bin/edge-agent" "$release/deploy/systemd/hl-panel-edge-agent.service" "$origin" mixed /opt/hl-panel/node-engines/xray /opt/hl-panel/node-engines/gost true
install -m 0700 -o root -g root "$release/deploy/edge-agent/enroll.sh" /opt/hl-panel/enroll-node.sh
if [[ -n "$ca_file" ]]; then
  install -m 0640 -o root -g hl-edge "$ca_file" /etc/hl-panel/edge-agent-ca.pem
  installed_ca=1
  python3 - <<'PY'
import json,os,pathlib,tempfile
p=pathlib.Path('/etc/hl-panel/edge-agent.json')
c=json.loads(p.read_text()); c['ca_file']='/etc/hl-panel/edge-agent-ca.pem'
fd,name=tempfile.mkstemp(dir=p.parent)
with os.fdopen(fd,'w') as f: f.write(json.dumps(c)+'\n')
os.chown(name, p.stat().st_uid, p.stat().st_gid); os.chmod(name,0o640)
os.replace(name,p)
PY
fi
python3 - "$node_address" <<'PY'
import json,os,pathlib,tempfile
p=pathlib.Path('/etc/hl-panel/edge-agent.json'); c=json.loads(p.read_text())
# Keep an unfinished enrollment attempt stable, including its dial address.
if c.get('dial_host') and c['dial_host'] != __import__('sys').argv[1]:
    raise SystemExit('已有注册配置的节点地址不同；使用原 --node-address 重试')
c['dial_host']=__import__('sys').argv[1]
c['heartbeat_interval']='1s'
fd,name=tempfile.mkstemp(dir=p.parent)
with os.fdopen(fd,'w') as f: f.write(json.dumps(c)+'\n')
os.chown(name,p.stat().st_uid,p.stat().st_gid); os.chmod(name,0o640); os.replace(name,p)
PY
if [[ "$created_install" == 1 && ! -e /etc/systemd/system/hl-panel-edge-agent.service.d/30-traffic-control.conf && ! -L /etc/systemd/system/hl-panel-edge-agent.service.d/30-traffic-control.conf ]]; then
  created_tc_dropin=1
fi
python3 "$release/deploy/configure-node-traffic-control.py"
python3 "$release/deploy/install-node-update.py" "$release"
echo '[HL-panel 节点] 自动注册并启用开机自启…'
HL_INSTALL_ENROLLMENT_TOKEN="$enrollment_token" sh /opt/hl-panel/enroll-node.sh
unset enrollment_token
install_ready=1
configure_tcp_bbr
echo '[HL-panel 节点] 安装完成，请返回面板查看在线状态。'
