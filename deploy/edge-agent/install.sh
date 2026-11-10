#!/bin/sh
set -eu

# Install a separately staged HL edge-agent binary. Nezha is never modified.
if [ "$(id -u)" -ne 0 ]; then
  echo 'Run as root on the target node.' >&2
  exit 1
fi
if [ "$#" -lt 3 ] || [ "$#" -gt 7 ]; then
  echo 'Usage: install.sh AGENT_BINARY SYSTEMD_UNIT CONTROL_PLANE_ORIGIN [ENGINE_MODE] [XRAY_BINARY_PATH] [GOST_BINARY_PATH] [AUTO_START]' >&2
  exit 2
fi
binary=$1
unit=$2
origin=$3
engine_mode=${4:-dry_run}
xray_binary_path=${5:-}
gost_binary_path=${6:-}
auto_start=${7:-false}
case "$origin" in
  https://*) ;;
  *) echo 'Control plane origin must use HTTPS.' >&2; exit 2 ;;
esac
host=${origin#https://}
case "$host" in
  ''|*[!A-Za-z0-9.:-]*) echo 'Invalid control plane origin.' >&2; exit 2 ;;
esac
case "$engine_mode" in
  dry_run|xray|gost|mixed) ;;
  *) echo 'ENGINE_MODE must be dry_run, xray, gost or mixed.' >&2; exit 2 ;;
esac
case "$auto_start" in
  true|false) ;;
  *) echo 'AUTO_START must be true or false.' >&2; exit 2 ;;
esac
case "$engine_mode" in
  dry_run)
    if [ -n "$xray_binary_path" ] || [ -n "$gost_binary_path" ] || [ "$auto_start" = true ]; then
      echo 'dry_run does not accept engine paths or AUTO_START=true.' >&2
      exit 2
    fi
    ;;
  xray)
    if [ -z "$xray_binary_path" ]; then
      echo 'xray mode requires XRAY_BINARY_PATH.' >&2
      exit 2
    fi
    ;;
  gost)
    if [ -z "$gost_binary_path" ]; then
      echo 'gost mode requires GOST_BINARY_PATH.' >&2
      exit 2
    fi
    ;;
  mixed)
    if [ -z "$xray_binary_path" ] || [ -z "$gost_binary_path" ] || [ "$auto_start" != true ]; then
      echo 'mixed mode requires both engine paths and AUTO_START=true.' >&2
      exit 2
    fi
    ;;
esac
for engine_path in "$xray_binary_path" "$gost_binary_path"; do
  if [ -n "$engine_path" ]; then
    case "$engine_path" in
      /*) ;;
      *) echo 'Engine binary paths must be absolute.' >&2; exit 2 ;;
    esac
    case "$engine_path" in
      *[!A-Za-z0-9_./:-]*) echo 'Engine binary paths may only contain safe path characters.' >&2; exit 2 ;;
    esac
    if [ ! -f "$engine_path" ] || [ ! -x "$engine_path" ]; then
      echo "Engine binary must be an executable regular file: $engine_path" >&2
      exit 2
    fi
  fi
done
if [ ! -f "$binary" ] || [ ! -x "$binary" ] || [ ! -f "$unit" ]; then
  echo 'Staged binary and systemd unit must exist.' >&2
  exit 2
fi
if [ -e /opt/hl-panel/edge-agent ] || [ -e /etc/hl-panel/edge-agent.json ] || [ -e /var/lib/hl-panel-edge/credentials.json ] || [ -e /etc/systemd/system/hl-panel-edge-agent.service ]; then
  echo 'Existing HL edge-agent installation found; refusing to overwrite it.' >&2
  exit 1
fi
created_binary=0
created_unit=0
created_config=0
installed=0
cleanup() {
  if [ "$installed" -eq 0 ]; then
    if [ "$created_unit" -eq 1 ]; then rm -f /etc/systemd/system/hl-panel-edge-agent.service; fi
    if [ "$created_config" -eq 1 ]; then rm -f /etc/hl-panel/edge-agent.json; fi
    if [ "$created_binary" -eq 1 ]; then rm -f /opt/hl-panel/edge-agent; fi
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
}
trap cleanup 0
trap 'exit 130' HUP INT TERM
if ! id hl-edge >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/hl-panel-edge --shell /usr/sbin/nologin hl-edge
fi
if [ ! -d /opt/hl-panel ]; then install -d -m 0755 -o root -g root /opt/hl-panel; fi
if [ ! -d /etc/hl-panel ]; then install -d -m 0750 -o root -g hl-edge /etc/hl-panel; fi
# Permit hl-edge to traverse an existing panel config directory without changing
# its owner/group or exposing the panel's private files.
if ! runuser -u hl-edge -- test -x /etc/hl-panel; then
  echo 'Existing /etc/hl-panel is inaccessible to hl-edge; use a separate node VPS.' >&2
  exit 1
fi
install -d -m 0700 -o hl-edge -g hl-edge /var/lib/hl-panel-edge
created_binary=1
install -m 0755 -o root -g root "$binary" /opt/hl-panel/edge-agent
created_unit=1
install -m 0644 -o root -g root "$unit" /etc/systemd/system/hl-panel-edge-agent.service
created_config=1
printf '{"control_plane_url":"%s","data_dir":"/var/lib/hl-panel-edge","engine_mode":"%s","xray_binary_path":"%s","gost_binary_path":"%s","xray_auto_start":%s,"gost_auto_start":%s}\n' \
  "$origin" "$engine_mode" "$xray_binary_path" "$gost_binary_path" "$auto_start" "$auto_start" > /etc/hl-panel/edge-agent.json
chown root:hl-edge /etc/hl-panel/edge-agent.json
chmod 0640 /etc/hl-panel/edge-agent.json
systemd-analyze verify /etc/systemd/system/hl-panel-edge-agent.service
systemctl daemon-reload
installed=1
echo "HL edge-agent installed with engine_mode=$engine_mode (auto_start=$auto_start). Nezha was not changed."
if [ "$engine_mode" = dry_run ]; then
  echo 'Traffic accounting is disabled until an explicit xray, gost or mixed configuration is installed.'
fi
echo 'To enroll, create a one-use group token in HL-panel and run enroll.sh locally.'
