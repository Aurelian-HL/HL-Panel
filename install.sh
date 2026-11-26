#!/usr/bin/env bash
set -Eeuo pipefail
set +x

# Resolve once, then use the installer and archive from the same immutable tag.
repository="Aurelian-HL/HL-Panel"
version="latest"
arguments=("$@")
while (($#)); do
  case "$1" in
    --repo) (($# >= 2)) || { echo '--repo requires a value' >&2; exit 2; }; repository="$2"; shift 2 ;;
    --version) (($# >= 2)) || { echo '--version requires a value' >&2; exit 2; }; version="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || { echo 'Invalid repository' >&2; exit 2; }
[[ "$version" == latest || "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release tag' >&2; exit 2; }
[[ $(id -u) == 0 ]] || { echo '请使用 root 执行面板安装。' >&2; exit 1; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { echo '当前正式安装包支持 Linux amd64（x86_64）。' >&2; exit 1; }
bootstrap_packages=()
command -v curl >/dev/null || bootstrap_packages+=(curl)
[[ -s /etc/ssl/certs/ca-certificates.crt ]] || bootstrap_packages+=(ca-certificates)
if ((${#bootstrap_packages[@]})); then
  command -v apt-get >/dev/null || { echo '请先安装 curl 和 ca-certificates；自动补齐支持 Debian/Ubuntu。' >&2; exit 1; }
  DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -qq -o Acquire::Retries=2 update
  DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -qq -o DPkg::Lock::Timeout=60 --reinstall install -y --no-install-recommends "${bootstrap_packages[@]}"
fi
if [[ "$version" == "latest" ]]; then
  release_url="$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 60 --output /dev/null --write-out '%{url_effective}' "https://github.com/$repository/releases/latest")" || {
    echo '无法获取最新正式版本，请检查 GitHub 网络连接后重试；未创建面板实例。' >&2
    exit 1
  }
  case "$release_url" in
    "https://github.com/$repository/releases/tag/"*) version="${release_url##*/}" ;;
    *) echo '最新正式版本地址无效；未创建面板实例。' >&2; exit 2 ;;
  esac
fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release tag' >&2; exit 2; }
temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT
curl --fail --silent --show-error --location --retry 2 --connect-timeout 15 --max-time 60 \
  --proto '=https' --proto-redir '=https' \
  "https://raw.githubusercontent.com/$repository/$version/deploy/install.sh" --output "$temporary/install.sh"
bash -n "$temporary/install.sh"
printf '[HL-panel] 安装正式版本 %s（脚本与安装包使用同一标签）\n' "$version"
bash "$temporary/install.sh" "${arguments[@]}" --repo "$repository" --version "$version"
