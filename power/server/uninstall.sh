#!/bin/sh
# Remove the Relay sleep/wake schedule. Run on the server: sudo sh uninstall.sh
systemctl disable --now relay-sleep.timer relay-sleep-warn.timer relay-wake.timer
rm -f /etc/systemd/system/relay-sleep.service /etc/systemd/system/relay-sleep.timer \
      /etc/systemd/system/relay-sleep-warn.service /etc/systemd/system/relay-sleep-warn.timer \
      /etc/systemd/system/relay-wake.service /etc/systemd/system/relay-wake.timer \
      /usr/local/bin/relay-sleep.sh
systemctl daemon-reload
