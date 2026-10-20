#!/usr/bin/env bash
# First licensed-release preparation. Does not stop services or alter customer data.
set -Eeuo pipefail
umask 077
fail() { printf '[HL-panel 授权] %s\n' "$*" >&2; exit 1; }
log() { printf '[HL-panel 授权] %s\n' "$*"; }
[[ $EUID == 0 ]] || fail "请使用 root 执行"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
BINARY="$(dirname -- "$SCRIPT_DIR")/bin/control-api"
DOMAIN=""
CODE_FILE=""
LICENSE_TIER=""
while (($#)); do
  case "$1" in
    --binary|--domain|--license-code-file|--license-tier)
      (($# >= 2)) || fail "$1 缺少参数"
      case "$1" in --binary) BINARY="$2";; --domain) DOMAIN="$2";; --license-code-file) CODE_FILE="$2";; --license-tier) LICENSE_TIER="$2";; esac
      shift 2;;
    *) fail "用法：bash setup-panel-license.sh --domain 管理域名 [--binary 授权版control-api] [--license-tier free|licensed] [--license-code-file /root/授权码文件]";;
  esac
done
[[ "$DOMAIN" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$ ]] || fail "请提供管理域名，不能填写 IP、URL 或端口"
[[ -x "$BINARY" && ! -L "$BINARY" ]] || fail "缺少已校验发行包内的授权版 control-api"
id hlpanel >/dev/null 2>&1 || fail "未发现已安装的面板服务账号；新安装请使用 install.sh"
LICENSE_STATE_DIR=/var/lib/hl-panel-license
source "$SCRIPT_DIR/activate-panel-license.sh"
# If later steps fail, retain the successfully bound identity for retries.
trap 'if [[ -d "$LICENSE_STATE_DIR" ]]; then license_state_permissions "$LICENSE_STATE_DIR" hlpanel; fi' EXIT
if [[ -z "$LICENSE_TIER" ]]; then
  if [[ -n "$CODE_FILE" ]]; then LICENSE_TIER=licensed; else LICENSE_TIER=free; fi
fi
activate_panel_license "$BINARY" "$DOMAIN" "$LICENSE_STATE_DIR" "$CODE_FILE" "$LICENSE_TIER"
license_state_permissions "$LICENSE_STATE_DIR" hlpanel
DROPIN_DIR=/etc/systemd/system/hl-panel-control-api.service.d
DROPIN="$DROPIN_DIR/30-panel-license.conf"
[[ ! -L "$DROPIN_DIR" && ! -L "$DROPIN" && ! -L "$DROPIN.previous" ]] || fail "服务配置目录或文件不能为链接"
install -d -o root -g root -m 0755 "$DROPIN_DIR"
if [[ -e "$DROPIN" ]]; then
  [[ -f "$DROPIN" ]] || fail "授权服务配置不是普通文件"
  grep -qxF '# Managed by HL-panel: signed licensing state.' "$DROPIN" || fail "授权服务配置路径已被自定义文件占用"
  cp -p -- "$DROPIN" "$DROPIN.previous"
fi
PENDING="$(mktemp "$DROPIN_DIR/.license-XXXXXX")"
printf '# Managed by HL-panel: signed licensing state.\n[Service]\nReadWritePaths=/var/lib/hl-panel-license\n' > "$PENDING"
chmod 0644 "$PENDING"
mv -f -- "$PENDING" "$DROPIN"
systemctl daemon-reload
log "授权接入完成；旧版环境配置、账号、规则及订阅未改动，服务未重启。"
log "现在可以执行系统更新。若当前已为授权版，重启面板控制服务后读取新身份。"
log "身份保存在 $LICENSE_STATE_DIR，请单独安全备份；不要删除或复制给其他面板。"
