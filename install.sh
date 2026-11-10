#!/usr/bin/env bash
set -Eeuo pipefail

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
command -v curl >/dev/null || { echo '请先安装 curl：apt-get update && apt-get install -y curl' >&2; exit 1; }
if [[ "$version" == "latest" ]]; then
  metadata="$(curl --fail --silent --show-error --location --retry 2 --connect-timeout 15 --max-time 60 "https://api.github.com/repos/$repository/releases/latest")"
  if [[ "$metadata" =~ \"tag_name\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]]; then
    version="${BASH_REMATCH[1]}"
  else
    echo '未找到正式发布版本，请稍后重试' >&2
    exit 1
  fi
fi
[[ "$version" =~ ^v[0-9][A-Za-z0-9._-]*$ ]] || { echo 'Invalid release tag' >&2; exit 2; }
temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT
curl --fail --silent --show-error --location --retry 2 --connect-timeout 15 --max-time 60 \
  "https://raw.githubusercontent.com/$repository/$version/deploy/install.sh" --output "$temporary/install.sh"
bash -n "$temporary/install.sh"
printf '[HL-panel] 安装正式版本 %s（脚本与安装包使用同一标签）\n' "$version"
bash "$temporary/install.sh" "${arguments[@]}" --repo "$repository" --version "$version"
