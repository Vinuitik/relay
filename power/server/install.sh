#!/bin/sh
# Install the Relay sleep/wake schedule and Wi-Fi watchdog. Run on the server: sudo sh install.sh
# Sleep/wake times come from the runner's plan file /var/lib/relay/schedule-plan, applied by
# relay-schedule-apply.sh whenever it changes (relay-schedule.path) and once at boot.
set -e
cd "$(dirname "$0")"
install -d -m 755 -o "${RELAY_USER:-${SUDO_USER:-victor}}" /var/lib/relay
install -m 755 relay-sleep.sh /usr/local/bin/relay-sleep.sh
install -m 755 relay-wifi-fix.sh /usr/local/bin/relay-wifi-fix.sh
install -m 755 relay-schedule-apply.sh /usr/local/bin/relay-schedule-apply.sh
install -m 644 relay-sleep.service relay-sleep.timer \
               relay-sleep-warn.service relay-sleep-warn.timer \
               relay-wake.service relay-wake.timer \
               relay-schedule.path relay-schedule-apply.service \
               relay-wifi-watchdog.service relay-wifi-watchdog.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now relay-sleep.timer relay-sleep-warn.timer relay-wake.timer \
                       relay-wifi-watchdog.timer relay-schedule.path
systemctl enable relay-schedule-apply.service
systemctl start relay-schedule-apply.service
systemctl list-timers 'relay-*' --no-pager
