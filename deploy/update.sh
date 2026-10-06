#!/usr/bin/env bash
# Managed by HL-panel. Only the packaged, fixed-purpose updater is executed.
set -Eeuo pipefail
exec python3 /opt/hl-panel/current/deploy/update.py "$@"
