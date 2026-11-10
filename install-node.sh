#!/usr/bin/env bash
set -Eeuo pipefail
set +x

# Resolve the latest official release once. Panel build metadata is not a
# downloadable release tag; use the installer and archive from the same tag.
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
bootstrap_packages=()
command -v curl >/dev/null || bootstrap_packages+=(curl)
[[ -s /etc/ssl/certs/ca-certificates.crt ]] || bootstrap_packages+=(ca-certificates)
if ((${#bootstrap_packages[@]})); then
  [[ $(id -u) == 0 ]] && command -v apt-get >/dev/null || { echo '请使用 root 并预装 curl、ca-certificates。' >&2; exit 1; }
  DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -o Acquire::Retries=2 update
  DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -o DPkg::Lock::Timeout=60 --reinstall install -y --no-install-recommends "${bootstrap_packages[@]}"
fi
if [[ "$version" == "latest" ]]; then
  [[ "$origin" =~ ^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$ ]] || { echo '请使用含 HTTPS 面板地址的完整安装命令。' >&2; exit 2; }
  # GitHub's latest URL selects a published, non-draft, non-prerelease release.
  # Resolve its redirect without sending the panel URL or enrollment token.
  release_url="$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 60 --output /dev/null --write-out '%{url_effective}' "https://github.com/$repository/releases/latest")" || {
    echo '无法获取最新正式版本，请检查 GitHub 网络连接后重试；未安装或注册节点。' >&2
    exit 1
  }
  case "$release_url" in
    "https://github.com/$repository/releases/tag/"*) version="${release_url##*/}" ;;
    *) echo '最新正式版本地址无效；未安装或注册节点。' >&2; exit 2 ;;
  esac
fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release tag' >&2; exit 2; }
temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT
curl --fail --silent --show-error --location --retry 2 --connect-timeout 15 --max-time 60 \
  --proto '=https' --proto-redir '=https' \
  "https://raw.githubusercontent.com/$repository/$version/deploy/install-node.sh" --output "$temporary/install-node.sh"
bash -n "$temporary/install-node.sh"
printf '[HL-panel 节点] 安装正式版本 %s（自动获取最新正式版，脚本与安装包使用同一标签）\n' "$version"
bash "$temporary/install-node.sh" "${arguments[@]}" --repo "$repository" --version "$version"
