#!/bin/sh
# Remove the Relay sleep/wake schedule and Wi-Fi watchdog. Run on the server: sudo sh uninstall.sh
systemctl disable --now relay-sleep.timer relay-sleep-warn.timer relay-wake.timer \
                        relay-wifi-watchdog.timer
rm -f /etc/systemd/system/relay-sleep.service /etc/systemd/system/relay-sleep.timer \
      /etc/systemd/system/relay-sleep-warn.service /etc/systemd/system/relay-sleep-warn.timer \
      /etc/systemd/system/relay-wake.service /etc/systemd/system/relay-wake.timer \
      /etc/systemd/system/relay-wifi-watchdog.service /etc/systemd/system/relay-wifi-watchdog.timer \
      /usr/local/bin/relay-sleep.sh /usr/local/bin/relay-wifi-fix.sh
systemctl daemon-reload
