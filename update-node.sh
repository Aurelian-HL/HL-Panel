#!/usr/bin/env bash
set -Eeuo pipefail
# Download updater and verifier from one immutable tag. No enrollment token.
[[ $(id -u) == 0 ]] || { echo '请使用 root 执行节点更新。' >&2; exit 1; }
version=""
if (($#)); then
  [[ $# == 2 && $1 == --version ]] || { echo '用法：update-node.sh [--version vX.Y.Z]' >&2; exit 2; }
  version="$2"
else
  version=$(python3 - <<'PY'
import json,ssl,urllib.request
from pathlib import Path
p=Path('/etc/hl-panel/edge-agent.json')
if not p.is_file() or p.is_symlink() or p.stat().st_uid!=0 or p.stat().st_mode&0o022:
    raise SystemExit('缺少安全的受管节点配置')
c=json.loads(p.read_text())
ctx=ssl.create_default_context(cafile=c.get('ca_file') or None)
with urllib.request.urlopen(c['control_plane_url']+'/api/v1/public/site-info',context=ctx,timeout=15) as r:
    print(json.load(r)['platform_version'])
PY
  )
fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo '面板未运行正式版本。' >&2; exit 1; }
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
for source in update-node.py update.py; do
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 60 \
    "https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/$version/deploy/$source" -o "$temporary/$source"
done
python3 "$temporary/update-node.py" --version "$version"
