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
#      already present, and brings up `tailscale up` if not already logged in
#      (this needs you to open the printed login URL yourself - can't be
#      automated headlessly)
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
    echo "    not logged in - run 'tailscale up' yourself, open the printed URL on any"
    echo "    device to authenticate, then re-run this script to pick up the IP"
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
else
    echo "    /etc/relay/runner.env already exists, leaving it alone"
fi

echo "5/6 installing systemd unit"
cp "$SCRIPT_DIR/relay-runner.service" "/etc/systemd/system/relay-runner@.service"
systemctl daemon-reload

echo "6/6 enabling + starting relay-runner@$RUN_USER"
systemctl enable --now "relay-runner@$RUN_USER"

echo
echo "done. check status with:"
echo "  systemctl status relay-runner@$RUN_USER"
echo "  journalctl -u relay-runner@$RUN_USER -f"
echo
echo "the key it generated on first run is in the journal output above (search"
echo "for 'RELAY KEY') and in \$RELAY_HOME/key.txt (default: ~$RUN_USER/.relay/key.txt)"
echo "- copy it into the phone app's known-runners list."
