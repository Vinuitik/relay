#!/bin/sh
# Suspend via systemd (runs the nvidia-suspend hook; raw rtcwake -m mem fails on
# this GPU). Block-mode inhibitors (e.g. APT mid-upgrade) refuse the suspend, so
# retry every minute for up to 30 min rather than forcing it with -i.
for i in $(seq 1 30); do
    systemctl suspend && exit 0
    echo "relay-sleep: suspend refused (attempt $i), inhibitors:"
    systemd-inhibit --list --no-pager --mode=block
    sleep 60
done
echo "relay-sleep: gave up after 30 attempts"
exit 1
