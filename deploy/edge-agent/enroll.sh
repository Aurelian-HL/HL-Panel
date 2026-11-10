#!/bin/sh
set -eu
set +x

if [ "$(id -u)" -ne 0 ]; then
  echo 'Run as root on the target node.' >&2
  exit 1
fi
if [ -e /var/lib/hl-panel-edge/credentials.json ]; then
  echo 'This HL edge-agent is already enrolled; refusing to replace its identity.' >&2
  exit 1
fi
if systemctl is-active --quiet hl-panel-edge-agent.service; then
  echo 'HL edge-agent is already running; stop and inspect it before a new enrollment.' >&2
  exit 1
fi
if [ ! -f /etc/hl-panel/edge-agent.json ] || [ ! -f /etc/systemd/system/hl-panel-edge-agent.service ]; then
  echo 'Install the HL edge-agent first.' >&2
  exit 1
fi
if [ -e /etc/hl-panel/edge-agent-enrollment.env ]; then
  echo 'An enrollment token file already exists; inspect the previous attempt first.' >&2
  exit 1
fi
if [ -n "${HL_INSTALL_ENROLLMENT_TOKEN:-}" ]; then
  token=$HL_INSTALL_ENROLLMENT_TOKEN
  unset HL_INSTALL_ENROLLMENT_TOKEN
else
  if [ ! -t 0 ]; then
    echo 'Use the node installation command containing a token, or an interactive terminal.' >&2
    exit 1
  fi
  printf 'One-use group token: ' >&2
  stty -echo
  trap 'stty echo' 0
  trap 'exit 130' HUP INT TERM
  IFS= read -r token
  stty echo
  trap - 0 HUP INT TERM
  printf '\n' >&2
fi
case "$token" in
  ''|*[!A-Za-z0-9_-]*) echo 'Invalid token.' >&2; exit 2 ;;
esac
enrolled=0
cleanup_enrollment() {
  if [ "$enrolled" -eq 0 ]; then
    systemctl stop hl-panel-edge-agent.service >/dev/null 2>&1 || true
    systemctl disable hl-panel-edge-agent.service >/dev/null 2>&1 || true
    rm -f /etc/hl-panel/edge-agent-enrollment.env
  fi
}
trap cleanup_enrollment 0
trap 'exit 130' HUP INT TERM
umask 077
printf 'NYVP_ENROLLMENT_TOKEN=%s\n' "$token" > /etc/hl-panel/edge-agent-enrollment.env
unset token
chown root:root /etc/hl-panel/edge-agent-enrollment.env
chmod 0600 /etc/hl-panel/edge-agent-enrollment.env
systemctl enable hl-panel-edge-agent.service
# An expired token can exhaust the unit's bounded automatic restart budget.
# A new explicit enrollment attempt must clear that failure before starting.
systemctl reset-failed hl-panel-edge-agent.service
systemctl start hl-panel-edge-agent.service
attempt=0
while [ "$attempt" -lt 30 ]; do
  if [ -s /var/lib/hl-panel-edge/credentials.json ]; then
    rm /etc/hl-panel/edge-agent-enrollment.env
    systemctl restart hl-panel-edge-agent.service
    systemctl is-active --quiet hl-panel-edge-agent.service
    enrolled=1
    echo 'HL edge-agent credential persisted and service running; the one-use token file was removed.'
    exit 0
  fi
  attempt=$((attempt + 1))
  sleep 1
done
echo 'Enrollment not confirmed within 30 seconds. Service stopped and token file removed; inspect journalctl and issue a fresh token.' >&2
exit 1
