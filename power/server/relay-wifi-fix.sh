#!/bin/sh
# Wi-Fi watchdog. The AX201 firmware crashes (NMI_INTERRUPT_WDG) and the driver's own
# restart then fails forever with "Failed to start RT ucode: -110". Removing the card
# from the PCI bus and rescanning brings it back. Run by relay-wifi-watchdog.timer.
IFACE=wlp0s20f3
PCI=0000:00:14.3
MAX_TRIES=5
TRIES_FILE=/run/relay-wifi-tries   # /run is wiped on boot

healthy() {
    nmcli -t -f DEVICE,STATE dev | grep -qx "$IFACE:connected" || return 1
    gw=$(ip -4 route show default dev "$IFACE" | awk '{print $3; exit}')
    [ -n "$gw" ] && ping -c 3 -W 2 -I "$IFACE" "$gw" >/dev/null 2>&1
}

# Unhealthy right after a resume is normal for a few seconds; re-check before acting.
for i in 1 2 3 4 5 6; do
    if healthy; then
        rm -f "$TRIES_FILE"
        exit 0
    fi
    sleep 5
done

# Flight mode (F2 on this Acer): never wanted on the server, turn it off.
if rfkill list wifi | grep -q "Soft blocked: yes" || [ "$(nmcli radio wifi)" = "disabled" ]; then
    echo "relay-wifi: flight mode was on, unblocking"
    rfkill unblock wifi
    nmcli radio wifi on
    exit 0
fi

tries=$(cat "$TRIES_FILE" 2>/dev/null || echo 0)
if [ "$tries" -ge "$MAX_TRIES" ]; then
    if [ "$tries" -eq "$MAX_TRIES" ]; then
        echo "relay-wifi: gave up after $MAX_TRIES resets, needs a full power-off"
        echo $((tries + 1)) > "$TRIES_FILE"
    fi
    exit 1
fi
tries=$((tries + 1))
echo "$tries" > "$TRIES_FILE"

echo "relay-wifi: $IFACE down ($(nmcli -t -f DEVICE,STATE dev | grep "^$IFACE:")), PCI reset attempt $tries/$MAX_TRIES"
echo 1 > "/sys/bus/pci/devices/$PCI/remove"
sleep 3
echo 1 > /sys/bus/pci/rescan

for i in $(seq 1 12); do
    sleep 5
    if healthy; then
        echo "relay-wifi: back online after attempt $tries"
        rm -f "$TRIES_FILE"
        exit 0
    fi
done
echo "relay-wifi: still down after attempt $tries"
exit 1
