#!/bin/sh
# Remove the Relay sleep/wake schedule and Wi-Fi watchdog. Run on the server: sudo sh uninstall.sh
# Leaves /var/lib/relay (the runner's plan file and the applied status) in place.
systemctl disable --now relay-schedule.path relay-schedule-apply.service
systemctl disable --now relay-sleep.timer relay-sleep-warn.timer relay-wake.timer \
                        relay-wifi-watchdog.timer
rm -f /etc/systemd/system/relay-sleep.service /etc/systemd/system/relay-sleep.timer \
      /etc/systemd/system/relay-sleep-warn.service /etc/systemd/system/relay-sleep-warn.timer \
      /etc/systemd/system/relay-wake.service /etc/systemd/system/relay-wake.timer \
      /etc/systemd/system/relay-schedule.path /etc/systemd/system/relay-schedule-apply.service \
      /etc/systemd/system/relay-wifi-watchdog.service /etc/systemd/system/relay-wifi-watchdog.timer \
      /etc/systemd/system/relay-sleep-warn.timer.d/plan.conf \
      /etc/systemd/system/relay-sleep.timer.d/plan.conf \
      /etc/systemd/system/relay-wake.timer.d/plan.conf \
      /usr/local/bin/relay-sleep.sh /usr/local/bin/relay-wifi-fix.sh \
      /usr/local/bin/relay-schedule-apply.sh
rmdir /etc/systemd/system/relay-sleep-warn.timer.d /etc/systemd/system/relay-sleep.timer.d \
      /etc/systemd/system/relay-wake.timer.d 2>/dev/null
systemctl daemon-reload
