#!/bin/sh
# Installs a prebuilt wakerd binary as a systemd service on Linux - meant for
# a headless Raspberry Pi Zero 2 W (arm64) sitting on the target LAN. See
# runner/cmd/wakerd/FLOWS.md "Installing on a headless Pi" for the full
# no-screen bootstrap flow (Imager pre-flash settings, ssh, scp, this
# script, then scanning the QR this script prints at the end).
#
# Usage:
#   sudo ./install-wakerd.sh /path/to/wakerd-linux-arm64 youruser
#
# What it does (see the numbered steps below to review before running with
# sudo - this touches /opt/relay, /etc/waker, and systemd unit files):
#   1. Installs Tailscale (official install.sh, always latest) if not
#      already present, and runs `tailscale up` if not already logged in -
#      it will print a login URL right here in this terminal and wait; open
#      that URL on any device to authenticate (a browser click can't be
#      scripted - the Pi itself has no screen, this terminal is the only UI)
#   2. Copies the binary to /opt/relay/bin/wakerd (owned by the run user,
#      not root - same reasoning as install.sh's runner binary placement)
#   3. Creates /etc/waker/ and drops in waker.env.example (won't overwrite
#      an existing /etc/waker/waker.env), and auto-fills WAKER_LISTEN_ADDR
#      with the Tailscale IP from step 1
#   4. Installs the systemd unit as wakerd@.service (templated on the user
#      to run as, so it never runs as root)
#   5. Enables + restarts wakerd@<user>.service
#   6. Prints the pairing QR (wakerd -qr) straight to this terminal - the
#      Pi is headless, so this SSH session is the only place that QR can
#      ever be shown
#
# It does NOT install Node/npm or the claude/codex CLIs - wakerd has no
# agent CLI, no projects, no sessions (see package doc in internal/waker).

set -e

BINARY="$1"
RUN_USER="$2"

if [ -z "$BINARY" ] || [ -z "$RUN_USER" ]; then
    echo "usage: sudo $0 /path/to/wakerd-linux-arm64 youruser" >&2
    exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
    echo "must run as root (sudo) - it writes to /opt/relay, /etc/waker and /etc/systemd" >&2
    exit 1
fi
if ! id "$RUN_USER" >/dev/null 2>&1; then
    echo "user '$RUN_USER' does not exist - create it first (adduser $RUN_USER) or pass an existing user" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "1/5 checking Tailscale"
if ! command -v tailscale >/dev/null 2>&1; then
    echo "    not found - installing via the official script (curl | sh, always latest)"
    curl -fsSL https://tailscale.com/install.sh | sh
else
    echo "    already installed ($(tailscale version | head -n1))"
fi
if ! tailscale ip -4 >/dev/null 2>&1; then
    echo "    not logged in - running 'tailscale up' now. It will print a login URL below:"
    echo "    open it on ANY device (phone, laptop, doesn't matter) to authenticate."
    echo "    This script will wait for you."
    tailscale up || echo "    'tailscale up' didn't complete - you can run it again by hand later"
fi
if ! tailscale ip -4 >/dev/null 2>&1; then
    echo "    still not logged in - WAKER_LISTEN_ADDR will be left unset"
    TS_IP=""
else
    TS_IP="$(tailscale ip -4)"
    echo "    tailnet IP: $TS_IP"
fi

echo "2/5 installing binary to /opt/relay/bin/wakerd"
# NOT /usr/local/bin: the service runs as $RUN_USER, not root (see
# wakerd.service). /opt/relay/bin is owned by $RUN_USER instead - same
# layout install.sh uses for relay-runner, so a Pi that later also runs a
# runner shares one bin directory.
mkdir -p /opt/relay/bin
install -m 0755 "$BINARY" /opt/relay/bin/wakerd
chown -R "$RUN_USER":"$RUN_USER" /opt/relay

echo "3/5 setting up /etc/waker/"
mkdir -p /etc/waker
if [ ! -f /etc/waker/waker.env ]; then
    cp "$SCRIPT_DIR/waker.env.example" /etc/waker/waker.env
    if [ -n "$TS_IP" ]; then
        sed -i "s/^#WAKER_LISTEN_ADDR=.*/WAKER_LISTEN_ADDR=$TS_IP:7778/" /etc/waker/waker.env
        echo "    wrote /etc/waker/waker.env, WAKER_LISTEN_ADDR set to $TS_IP:7778"
    else
        echo "    wrote /etc/waker/waker.env from the example - WAKER_LISTEN_ADDR not set"
        echo "    (Tailscale wasn't logged in yet) - edit it in by hand once you run 'tailscale up'"
    fi
elif [ -n "$TS_IP" ] && grep -q "^#WAKER_LISTEN_ADDR=" /etc/waker/waker.env; then
    # File exists from an earlier run (e.g. before Tailscale was logged in)
    # and still has the commented-out placeholder - fill it in now that we
    # have a real IP, instead of leaving wakerd stuck on loopback forever.
    sed -i "s/^#WAKER_LISTEN_ADDR=.*/WAKER_LISTEN_ADDR=$TS_IP:7778/" /etc/waker/waker.env
    echo "    /etc/waker/waker.env existed but had no WAKER_LISTEN_ADDR set - filled in $TS_IP:7778"
else
    echo "    /etc/waker/waker.env already exists, leaving it alone"
fi

echo "4/5 installing systemd unit"
cp "$SCRIPT_DIR/wakerd.service" "/etc/systemd/system/wakerd@.service"
systemctl daemon-reload

echo "5/5 enabling + restarting wakerd@$RUN_USER"
# Always restart, not just enable --now - same reasoning as install.sh: a
# rerun of this script must always end up actually running the binary it
# just installed, not an old one from an already-open inode.
systemctl enable "wakerd@$RUN_USER"
systemctl restart "wakerd@$RUN_USER"

RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"

echo
echo "================================================================"
echo " WAKERD INSTALL COMPLETE"
echo "================================================================"
if [ -n "$TS_IP" ]; then
    echo " Waker addr  : $TS_IP:7778"
else
    echo " Waker addr  : NOT SET - Tailscale wasn't logged in during install."
    echo "               Run 'tailscale up', then set WAKER_LISTEN_ADDR in"
    echo "               /etc/waker/waker.env and: systemctl restart wakerd@$RUN_USER"
fi
echo " Machines    : none configured yet - hand-edit"
echo "               $RUN_HOME/.waker/config.json and restart the service."
echo "               See runner/cmd/wakerd/FLOWS.md \"Installing on a headless Pi\"."
echo "================================================================"
echo
if [ -n "$TS_IP" ]; then
    echo "Scan this in the Relay Android app (Add Waker -> Scan QR) instead of typing the key:"
    echo
    WAKER_HOME="$RUN_HOME/.waker" WAKER_LISTEN_ADDR="$TS_IP:7778" /opt/relay/bin/wakerd -qr
else
    echo "Pairing QR skipped - need a Tailscale IP to encode. Once 'tailscale up' has"
    echo "run and WAKER_LISTEN_ADDR is set, print it by hand from this same SSH session:"
    echo "  sudo -u $RUN_USER /opt/relay/bin/wakerd -qr"
fi
echo
echo "status: systemctl status wakerd@$RUN_USER"
echo "logs:   journalctl -u wakerd@$RUN_USER -f"
