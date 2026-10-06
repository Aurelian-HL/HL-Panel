#!/usr/bin/env bash
set -Eeuo pipefail
set +x

# Resolve once, then use the installer and archive from the same immutable tag.
repository="Aurelian-HL/HL-Panel"
version="latest"
origin=""
ca_file=""
arguments=("$@")
while (($#)); do
  case "$1" in
    --repo) (($# >= 2)) || { echo '--repo requires a value' >&2; exit 2; }; repository="$2"; shift 2 ;;
    --version) (($# >= 2)) || { echo '--version requires a value' >&2; exit 2; }; version="$2"; shift 2 ;;
    --panel-url) (($# >= 2)) || { echo '--panel-url requires a value' >&2; exit 2; }; origin="$2"; shift 2 ;;
    --ca-file) (($# >= 2)) || { echo '--ca-file requires a value' >&2; exit 2; }; ca_file="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[[ "$repository" == Aurelian-HL/HL-Panel ]] || { echo 'Invalid repository' >&2; exit 2; }
command -v curl >/dev/null || { echo '请先安装 curl：apt-get update && apt-get install -y curl' >&2; exit 1; }
if [[ "$version" == "latest" ]]; then
  [[ "$origin" =~ ^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$ ]] || { echo '请使用含 HTTPS 面板地址的完整安装命令。' >&2; exit 2; }
  trust_arguments=()
  if [[ -n "$ca_file" ]]; then trust_arguments=(--cacert "$ca_file"); fi
  metadata="$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 60 "${trust_arguments[@]}" "$origin/api/v1/public/site-info")"
  if [[ "$metadata" =~ \"platform_version\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]]; then
    version="${BASH_REMATCH[1]}"
  else
    echo '无法核实面板版本，请检查面板 HTTPS 或先升级面板。' >&2
    exit 1
  fi
fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release tag' >&2; exit 2; }
temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT
curl --fail --silent --show-error --location --retry 2 --connect-timeout 15 --max-time 60 \
  "https://raw.githubusercontent.com/$repository/$version/deploy/install-node.sh" --output "$temporary/install-node.sh"
bash -n "$temporary/install-node.sh"
printf '[HL-panel 节点] 安装正式版本 %s（默认与面板同步，脚本与安装包使用同一标签）\n' "$version"
bash "$temporary/install-node.sh" "${arguments[@]}" --repo "$repository" --version "$version"
