#!/bin/sh
# Install the Relay sleep/wake schedule and Wi-Fi watchdog. Run on the server: sudo sh install.sh
set -e
cd "$(dirname "$0")"
install -m 755 relay-sleep.sh /usr/local/bin/relay-sleep.sh
install -m 755 relay-wifi-fix.sh /usr/local/bin/relay-wifi-fix.sh
install -m 644 relay-sleep.service relay-sleep.timer \
               relay-sleep-warn.service relay-sleep-warn.timer \
               relay-wake.service relay-wake.timer \
               relay-wifi-watchdog.service relay-wifi-watchdog.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now relay-sleep.timer relay-sleep-warn.timer relay-wake.timer \
                       relay-wifi-watchdog.timer
systemctl list-timers 'relay-*' --no-pager
