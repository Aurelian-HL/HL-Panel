#!/usr/bin/env bash
set -Eeuo pipefail
version=latest
arguments=("$@")
while (($#)); do
  case "$1" in
    --version) (($#>=2)) || { echo '缺少版本号' >&2; exit 2; }; version="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[[ $EUID -eq 0 ]] || { echo '请在 VPS 上使用 root 执行更新命令' >&2; exit 1; }
command -v curl >/dev/null || { echo '请先安装 curl' >&2; exit 1; }
command -v python3 >/dev/null || { echo '请先安装 python3' >&2; exit 1; }
if [[ "$version" == latest ]]; then
  metadata=$(curl -fsSL --retry 2 --connect-timeout 15 --max-time 60 https://api.github.com/repos/Aurelian-HL/HL-Panel/releases/latest)
  version=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["tag_name"])' <<<"$metadata")
fi
[[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo '正式版本号格式无效' >&2; exit 2; }
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
curl -fsSL --retry 2 --connect-timeout 15 --max-time 60 \
  "https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/$version/deploy/update.py" -o "$temporary/update.py"
python3 "$temporary/update.py" "${arguments[@]}" --version "$version"
