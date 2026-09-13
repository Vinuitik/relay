#!/bin/sh
# Installs a prebuilt relay-runner binary as a systemd service on Linux.
#
# Usage:
#   sudo ./install.sh /path/to/relay-runner-linux-amd64 youruser
#
# What it does (see the numbered steps below to review before running with
# sudo - this touches /usr/local/bin, /etc/relay, and systemd unit files):
#   1. Copies the binary to /usr/local/bin/relay-runner
#   2. Creates /etc/relay/ and drops in runner.env.example (won't overwrite
#      an existing /etc/relay/runner.env)
#   3. Installs the systemd unit as relay-runner@.service (templated on the
#      user to run as, so it never runs as root)
#   4. Enables + starts relay-runner@<user>.service
#
# It does NOT touch Tailscale, docker, or any project directories - install
# and configure Tailscale yourself first (tailscale.com/download), and set
# RELAY_LISTEN_ADDR in /etc/relay/runner.env to your Tailscale IP afterward.

set -e

BINARY="$1"
RUN_USER="$2"

if [ -z "$BINARY" ] || [ -z "$RUN_USER" ]; then
    echo "usage: sudo $0 /path/to/relay-runner-linux-amd64 youruser" >&2
    exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
    echo "must run as root (sudo) - it writes to /usr/local/bin and /etc/systemd" >&2
    exit 1
fi
if ! id "$RUN_USER" >/dev/null 2>&1; then
    echo "user '$RUN_USER' does not exist - create it first (adduser $RUN_USER) or pass an existing user" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "1/4 installing binary to /usr/local/bin/relay-runner"
install -m 0755 "$BINARY" /usr/local/bin/relay-runner

echo "2/4 setting up /etc/relay/"
mkdir -p /etc/relay
if [ ! -f /etc/relay/runner.env ]; then
    cp "$SCRIPT_DIR/runner.env.example" /etc/relay/runner.env
    echo "    wrote /etc/relay/runner.env from the example - edit it before relying on this,"
    echo "    it does nothing by default (loopback-only, no providers configured)"
else
    echo "    /etc/relay/runner.env already exists, leaving it alone"
fi

echo "3/4 installing systemd unit"
cp "$SCRIPT_DIR/relay-runner.service" "/etc/systemd/system/relay-runner@.service"
systemctl daemon-reload

echo "4/4 enabling + starting relay-runner@$RUN_USER"
systemctl enable --now "relay-runner@$RUN_USER"

echo
echo "done. check status with:"
echo "  systemctl status relay-runner@$RUN_USER"
echo "  journalctl -u relay-runner@$RUN_USER -f"
echo
echo "the key it generated on first run is in the journal output above (search"
echo "for 'RELAY KEY') and in \$RELAY_HOME/key.txt (default: ~$RUN_USER/.relay/key.txt)"
echo "- copy it into the phone app's known-runners list."
