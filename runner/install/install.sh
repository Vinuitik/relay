#!/bin/sh
# Installs a prebuilt relay-runner binary as a systemd service on Linux.
#
# Usage:
#   sudo ./install.sh /path/to/relay-runner-linux-amd64 youruser
#
# What it does (see the numbered steps below to review before running with
# sudo - this touches /usr/local/bin, /etc/relay, and systemd unit files):
#   1. Installs Tailscale (official install.sh, always latest - see
#      runner/FLOWS.md "Tailscale" for why we don't pin a version) if not
#      already present, and runs `tailscale up` if not already logged in -
#      it will print a login URL right here in this terminal and wait; open
#      that URL on any device to authenticate (a browser click can't be
#      scripted, that's the one manual step left)
#   2. Installs Node.js/npm via apt if not already present (apt-based systems
#      only), then installs the claude / codex CLIs via npm if not present
#   3. Copies the binary to /usr/local/bin/relay-runner
#   4. Creates /etc/relay/ and drops in runner.env.example (won't overwrite
#      an existing /etc/relay/runner.env), and auto-fills RELAY_LISTEN_ADDR
#      with the Tailscale IP from step 1
#   5. Installs the systemd unit as relay-runner@.service (templated on the
#      user to run as, so it never runs as root)
#   6. Enables + starts relay-runner@<user>.service
#
# It does NOT touch docker or any project directories.

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

echo "1/6 checking Tailscale"
if ! command -v tailscale >/dev/null 2>&1; then
    echo "    not found - installing via the official script (curl | sh, always latest)"
    curl -fsSL https://tailscale.com/install.sh | sh
else
    echo "    already installed ($(tailscale version | head -n1))"
fi
if ! tailscale ip -4 >/dev/null 2>&1; then
    echo "    not logged in - running 'tailscale up' now. It will print a login URL below:"
    echo "    open it on ANY device (phone, this laptop, doesn't matter) to authenticate."
    echo "    This script will wait for you."
    tailscale up || echo "    'tailscale up' didn't complete - you can run it again by hand later"
fi
if ! tailscale ip -4 >/dev/null 2>&1; then
    echo "    still not logged in - RELAY_LISTEN_ADDR will be left unset"
    TS_IP=""
else
    TS_IP="$(tailscale ip -4)"
    echo "    tailnet IP: $TS_IP"
fi

echo "2/6 checking claude / codex CLIs"
if ! command -v npm >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
        echo "    npm not found - installing Node.js via apt (nodejs, npm)"
        apt-get update -qq && apt-get install -y nodejs npm
    else
        echo "    npm not found and this isn't an apt-based system - skipping CLI install,"
        echo "    install Node.js/npm yourself then re-run this script (or install"
        echo "    manually: npm install -g @anthropic-ai/claude-code @openai/codex)"
    fi
fi
if command -v npm >/dev/null 2>&1; then
    if ! command -v claude >/dev/null 2>&1; then
        echo "    installing claude CLI (npm install -g @anthropic-ai/claude-code)"
        npm install -g @anthropic-ai/claude-code
    else
        echo "    claude CLI already installed"
    fi
    if ! command -v codex >/dev/null 2>&1; then
        echo "    installing codex CLI (npm install -g @openai/codex)"
        npm install -g @openai/codex
    else
        echo "    codex CLI already installed"
    fi
fi

echo "3/6 installing binary to /usr/local/bin/relay-runner"
install -m 0755 "$BINARY" /usr/local/bin/relay-runner

echo "4/6 setting up /etc/relay/"
mkdir -p /etc/relay
if [ ! -f /etc/relay/runner.env ]; then
    cp "$SCRIPT_DIR/runner.env.example" /etc/relay/runner.env
    if [ -n "$TS_IP" ]; then
        sed -i "s/^#RELAY_LISTEN_ADDR=.*/RELAY_LISTEN_ADDR=$TS_IP:7777/" /etc/relay/runner.env
        echo "    wrote /etc/relay/runner.env, RELAY_LISTEN_ADDR set to $TS_IP:7777"
    else
        echo "    wrote /etc/relay/runner.env from the example - RELAY_LISTEN_ADDR not set"
        echo "    (Tailscale wasn't logged in yet) - edit it in by hand once you run 'tailscale up'"
    fi
elif [ -n "$TS_IP" ] && grep -q "^#RELAY_LISTEN_ADDR=" /etc/relay/runner.env; then
    # File exists from an earlier run (e.g. before Tailscale was logged in)
    # and still has the commented-out placeholder - fill it in now that we
    # have a real IP, instead of leaving the runner stuck on loopback forever.
    sed -i "s/^#RELAY_LISTEN_ADDR=.*/RELAY_LISTEN_ADDR=$TS_IP:7777/" /etc/relay/runner.env
    echo "    /etc/relay/runner.env existed but had no RELAY_LISTEN_ADDR set - filled in $TS_IP:7777"
    NEEDS_RESTART=1
else
    echo "    /etc/relay/runner.env already exists, leaving it alone"
fi

echo "5/6 installing systemd unit"
cp "$SCRIPT_DIR/relay-runner.service" "/etc/systemd/system/relay-runner@.service"
systemctl daemon-reload

echo "6/6 enabling + starting relay-runner@$RUN_USER"
systemctl enable --now "relay-runner@$RUN_USER"
if [ "$NEEDS_RESTART" = "1" ]; then
    echo "    runner.env changed since the service last started - restarting to pick it up"
    systemctl restart "relay-runner@$RUN_USER"
fi

RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"
KEY_FILE="$RUN_HOME/.relay/key.txt"
KEY=""
for i in 1 2 3 4 5; do
    [ -f "$KEY_FILE" ] && KEY="$(cat "$KEY_FILE")" && break
    sleep 1
done

echo
echo "================================================================"
echo " RELAY INSTALL COMPLETE"
echo "================================================================"
if [ -n "$KEY" ]; then
    echo " Runner key  : $KEY"
else
    echo " Runner key  : not found yet at $KEY_FILE - check:"
    echo "               journalctl -u relay-runner@$RUN_USER | grep 'RELAY KEY'"
fi
if [ -n "$TS_IP" ]; then
    echo " Runner addr : $TS_IP:7777"
else
    echo " Runner addr : NOT SET - Tailscale wasn't logged in during install."
    echo "               Run 'tailscale up', then set RELAY_LISTEN_ADDR in"
    echo "               /etc/relay/runner.env and: systemctl restart relay-runner@$RUN_USER"
fi
echo "================================================================"
echo
if [ -n "$KEY" ] && [ -n "$TS_IP" ]; then
    echo "Scan this in the Relay app (Add Runner -> Scan QR) instead of typing the key:"
    echo
    RELAY_HOME="$RUN_HOME/.relay" RELAY_LISTEN_ADDR="$TS_IP:7777" /usr/local/bin/relay-runner -qr
else
    echo "Add to phone app manually: known-runners list, using the key + addr above."
    echo "(QR skipped - need both a generated key and a Tailscale IP to encode)"
fi
echo
echo "status: systemctl status relay-runner@$RUN_USER"
echo "logs:   journalctl -u relay-runner@$RUN_USER -f"
