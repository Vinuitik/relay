#!/bin/sh
# One-time switch of a Linux runner machine to keeperd. Run with sudo:
#
#   sudo ./install-keeperd.sh youruser
#
# Expects /opt/relay/bin/keeperd (owned by youruser - keeperd replaces its
# own binaries there without root from then on). Points the existing
# relay-runner@ systemd unit at keeperd instead of the runner; keeperd then
# runs relay-runner as its child and keeps it updated. Settings stay in
# /etc/relay/runner.env.
set -e
RUN_USER="$1"
if [ -z "$RUN_USER" ] || [ "$(id -u)" -ne 0 ]; then
    echo "usage: sudo $0 youruser" >&2
    exit 1
fi
[ -x /opt/relay/bin/keeperd ] || { echo "/opt/relay/bin/keeperd missing" >&2; exit 1; }

UNIT=/etc/systemd/system/relay-runner@.service
sed -i 's#^ExecStart=.*#ExecStart=/opt/relay/bin/keeperd#' "$UNIT"
grep -q '^TimeoutStopSec=' "$UNIT" || sed -i '/^RestartSec=/a TimeoutStopSec=20' "$UNIT"
chown -R "$RUN_USER" /opt/relay
systemctl daemon-reload
systemctl restart "relay-runner@$RUN_USER"
sleep 3
systemctl --no-pager status "relay-runner@$RUN_USER" | head -12
echo "Logs: journalctl -u relay-runner@$RUN_USER -f"
