#!/usr/bin/env bash
set -Eeuo pipefail
set +x
# Installs only the HL node agent. Never installs a panel or changes other agents.
repository="Aurelian-HL/HL-Panel"
version=""
origin=""
archive=""
checksum=""
enrollment_token=""
ca_file=""
while (($#)); do
  case "$1" in
    --repo|--version|--panel-url|--archive|--sha256|--token|--ca-file)
      (($# >= 2)) || { echo "缺少参数值：$1" >&2; exit 2; }
      case "$1" in
        --repo) repository="$2" ;; --version) version="$2" ;; --panel-url) origin="$2" ;;
        --archive) archive="$2" ;; --sha256) checksum="$2" ;;
        --token) enrollment_token="$2" ;; --ca-file) ca_file="$2" ;;
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
if [[ -e /var/lib/hl-panel-edge/credentials.json ]]; then
  echo '此节点已注册，保留已有身份；无需重复安装。查看：systemctl status hl-panel-edge-agent' >&2
  exit 1
fi
if [[ -z "$archive" ]]; then
  if ! command -v python3 >/dev/null || ! command -v curl >/dev/null || [[ ! -e /etc/ssl/certs/ca-certificates.crt ]]; then
    command -v apt-get >/dev/null || { echo '请先安装 curl、python3、ca-certificates。' >&2; exit 1; }
    apt-get update
    apt-get install -y curl python3 ca-certificates
  fi
else
  command -v python3 >/dev/null || { echo '离线模式需要预装 python3。' >&2; exit 1; }
  [[ -f "$archive" && -f "$checksum" ]] || { echo '离线安装必须同时提供 --archive 和 --sha256 文件。' >&2; exit 2; }
fi
python3 - "$origin" "$ca_file" <<'PY'
import ssl, sys, urllib.parse
try:
    address = urllib.parse.urlsplit(sys.argv[1])
    if address.port is not None and not 1 <= address.port <= 65535: raise ValueError()
    if sys.argv[2]: ssl.create_default_context(cafile=sys.argv[2])
except (ValueError, OSError, ssl.SSLError):
    raise SystemExit('面板端口或 CA 证书无效；未修改节点')
PY
temporary=$(mktemp -d)
created_install=0
install_ready=0
cleanup_install() {
  if [[ "$created_install" == 1 && "$install_ready" == 0 ]]; then
    rm -f /opt/hl-panel/edge-agent /opt/hl-panel/enroll-node.sh /etc/hl-panel/edge-agent.json /etc/systemd/system/hl-panel-edge-agent.service
    rm -f /opt/hl-panel/node-engines/xray /opt/hl-panel/node-engines/gost
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
for name in ('bin/edge-agent', 'bin/xray', 'bin/gost', 'deploy/edge-agent/install.sh', 'deploy/edge-agent/enroll.sh', 'deploy/systemd/hl-panel-edge-agent.service'):
    require(name in verified, '缺少节点安装文件：' + name)
(target/'bin/edge-agent').chmod(0o755)
PY
release="$temporary/release"
for asset in bin/xray bin/gost; do
  [[ -f "$release/$asset" ]] || { echo '安装包缺少转发引擎；未修改节点，请使用新版本。' >&2; exit 1; }
  chmod 0755 "$release/$asset"
done
for directory in /opt/hl-panel /opt/hl-panel/node-engines /etc/hl-panel /var/lib/hl-panel-edge; do
  [[ ! -L "$directory" ]] || { echo '安装目录是链接，拒绝修改。' >&2; exit 1; }
done
# Check the exact bundled executables before creating an installation.
"$release/bin/xray" version >/dev/null
"$release/bin/gost" -V >/dev/null
if [[ -e /etc/hl-panel/edge-agent.json || -e /opt/hl-panel/edge-agent || -e /opt/hl-panel/enroll-node.sh || -e /etc/systemd/system/hl-panel-edge-agent.service ]]; then
  [[ -f /opt/hl-panel/enroll-node.sh && -f /opt/hl-panel/edge-agent && -f /etc/systemd/system/hl-panel-edge-agent.service ]] || { echo '发现不完整的旧安装，未覆盖任何文件；请检查已有安装。' >&2; exit 1; }
  python3 - "$origin" "$release" <<'PY'
import hashlib, json, pathlib, sys
path = pathlib.Path('/etc/hl-panel/edge-agent.json')
if not path.is_file() or path.is_symlink() or path.stat().st_uid != 0 or path.stat().st_mode & 0o022:
    raise SystemExit('现有配置不安全，拒绝重新注册')
config = json.loads(path.read_text())
if config.get('control_plane_url') != sys.argv[1]:
    raise SystemExit('现有节点属于其他面板；未覆盖配置或身份')
expected = {'engine_mode':'mixed', 'xray_auto_start':True, 'gost_auto_start':True,
            'data_dir':'/var/lib/hl-panel-edge', 'xray_binary_path':'/opt/hl-panel/node-engines/xray',
            'gost_binary_path':'/opt/hl-panel/node-engines/gost'}
if any(config.get(k) != v for k,v in expected.items()):
    raise SystemExit('现有引擎配置不匹配，未覆盖；请检查原安装')
release = pathlib.Path(sys.argv[2])
for installed, source in [('/opt/hl-panel/edge-agent','bin/edge-agent'),
        ('/opt/hl-panel/node-engines/xray','bin/xray'), ('/opt/hl-panel/node-engines/gost','bin/gost'),
        ('/opt/hl-panel/enroll-node.sh','deploy/edge-agent/enroll.sh'),
        ('/etc/systemd/system/hl-panel-edge-agent.service','deploy/systemd/hl-panel-edge-agent.service')]:
    p = pathlib.Path(installed)
    if not p.is_file() or p.is_symlink() or p.stat().st_uid != 0 or p.stat().st_mode & 0o022:
        raise SystemExit('现有安装文件不安全，拒绝继续注册')
    if hashlib.sha256(p.read_bytes()).digest() != hashlib.sha256((release/source).read_bytes()).digest():
        raise SystemExit('现有安装版本不同；请指定原 --version 重试，不覆盖安装')
PY
  echo '[HL-panel 节点] 安装文件已存在，继续尚未完成的注册。'
else
  [[ ! -e /opt/hl-panel/node-engines ]] || { echo '已有引擎目录，拒绝覆盖。' >&2; exit 1; }
  created_install=1
  install -d -m 0755 -o root -g root /opt/hl-panel/node-engines
  install -m 0755 -o root -g root "$release/bin/xray" /opt/hl-panel/node-engines/xray
  install -m 0755 -o root -g root "$release/bin/gost" /opt/hl-panel/node-engines/gost
  if ! sh "$release/deploy/edge-agent/install.sh" "$release/bin/edge-agent" "$release/deploy/systemd/hl-panel-edge-agent.service" "$origin" mixed /opt/hl-panel/node-engines/xray /opt/hl-panel/node-engines/gost true; then
    rm -f /opt/hl-panel/node-engines/xray /opt/hl-panel/node-engines/gost
    rmdir /opt/hl-panel/node-engines
    exit 1
  fi
  install -m 0700 -o root -g root "$release/deploy/edge-agent/enroll.sh" /opt/hl-panel/enroll-node.sh
fi
install_ready=1
if [[ -n "$ca_file" ]]; then
  install -m 0640 -o root -g hl-edge "$ca_file" /etc/hl-panel/edge-agent-ca.pem
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
echo '[HL-panel 节点] 自动注册并启用开机自启…'
HL_INSTALL_ENROLLMENT_TOKEN="$enrollment_token" sh /opt/hl-panel/enroll-node.sh
unset enrollment_token
echo '[HL-panel 节点] 服务已注册并启动。请回面板刷新设备组成员与探针，等待最多 30 秒核对在线状态。'
echo '状态：systemctl status hl-panel-edge-agent --no-pager'
echo '排障：journalctl -u hl-panel-edge-agent -n 50 --no-pager'
echo '已安装主机探针、Xray 和 GOST（mixed 自动启动）。请在面板创建并下发转发规则后验收业务。'
