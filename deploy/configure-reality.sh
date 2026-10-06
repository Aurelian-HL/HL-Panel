#!/usr/bin/env bash
# Managed by HL-panel.
set -Eeuo pipefail
exec python3 /opt/hl-panel/current/deploy/configure-reality.py "$@"
