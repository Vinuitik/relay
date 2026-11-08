#!/bin/sh
# Installs (or upgrades) the Relay runner on a Linux machine with systemd.
#
# One command, from your normal user account (that user will run the runner):
#   curl -fsSL https://github.com/Vinuitik/relay/releases/latest/download/install.sh | sudo sh
# Another user:  ... | sudo RELAY_USER=bob sh
# Local binary (old mode, e.g. a dev build):
#   sudo ./install.sh /path/to/relay-runner-linux-amd64 youruser
#   (keeperd is taken from $KEEPERD_BIN, else keeperd[-linux-<arch>] next to
#   that binary, else downloaded from the latest release)
#
# Steps (review before running with sudo - this touches /opt/relay,
# /etc/relay and /etc/systemd/system):
#   1. Downloads keeperd + relay-runner for this CPU from the latest GitHub
#      release and checks them against that release's SHA256SUMS
#   2. Tailscale is OPTIONAL and never installed here. If it is installed and
#      logged in, the runner listens on the Tailscale IP (direct phone-app
#      access + Android QR); otherwise on 127.0.0.1:7777 and the machine is
#      reached through the HabitTracker hub (step 8)
#   3. Installs Node.js/npm via apt if missing (apt systems only), then the
#      claude / codex CLIs via npm if missing
#   4. Installs both binaries to /opt/relay/bin, owned by the run user (keeperd
#      replaces them there without root on every release)
#   5. Writes /etc/relay/runner.env if missing (never overwrites it)
#   6. Writes the systemd unit relay-runner@.service (runs keeperd as the user)
#   7. Enables + restarts relay-runner@<user>
#   8. Unless already paired (hub.json exists): `relay-runner pair` - prints a
#      QR, scan it with your phone and tap Confirm in HabitTracker. Ctrl-C skips.
# Re-running it is an upgrade. It does NOT touch docker or project directories.
#
# Everything runs inside main(), called on the last line, so a download cut
# off halfway executes nothing.

set -eu

REPO="${RELAY_UPDATE_REPO:-Vinuitik/relay}"
BIN_DIR=/opt/relay/bin
ENV_FILE=/etc/relay/runner.env
UNIT_FILE=/etc/systemd/system/relay-runner@.service

die() { echo "error: $*" >&2; exit 1; }

# as_user CMD... - run as the service user (pairing / key files must be theirs).
as_user() {
    if command -v runuser >/dev/null 2>&1; then
        runuser -u "$RUN_USER" -- "$@"
    else
        sudo -u "$RUN_USER" -- "$@"
    fi
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo amd64 ;;
        aarch64|arm64) echo arm64 ;;
        armv7*|armv6*|armhf|arm) echo arm ;;
        *) die "unsupported CPU '$(uname -m)' - releases exist for amd64, arm64, arm" ;;
    esac
}

# resolve_tag - the tag "latest" points at right now. Downloading from
# /releases/download/<tag>/ (not /latest/) keeps the binaries and SHA256SUMS
# from the same release even if a new one is published mid-install.
resolve_tag() {
    url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")" \
        || die "cannot reach https://github.com/$REPO/releases/latest"
    tag="${url##*/}"
    case "$tag" in
        ""|latest|releases) die "no release found at https://github.com/$REPO/releases" ;;
    esac
    echo "$tag"
}

# fetch NAME... - download release assets NAME into $TMP and verify them.
fetch() {
    base="https://github.com/$REPO/releases/download/$TAG"
    curl -fsSL -o "$TMP/SHA256SUMS" "$base/SHA256SUMS" || die "download $base/SHA256SUMS failed"
    for name in "$@"; do
        echo "    $name"
        curl -fsSL -o "$TMP/$name" "$base/$name" || die "download $base/$name failed"
        line="$(grep -E "^[0-9a-f]{64}  \*?$name\$" "$TMP/SHA256SUMS" || true)"
        [ -n "$line" ] || die "$name is not listed in SHA256SUMS of $TAG"
        (cd "$TMP" && echo "$line" | sha256sum -c - >/dev/null) || die "$name: SHA256 mismatch - not installing"
    done
}

# install_bin SRC NAME - atomic replace (rename over a running binary is fine).
install_bin() {
    cp "$1" "$BIN_DIR/.$2.new"
    chmod 0755 "$BIN_DIR/.$2.new"
    mv -f "$BIN_DIR/.$2.new" "$BIN_DIR/$2"
}

write_env() {
    mkdir -p /etc/relay
    if [ ! -f "$ENV_FILE" ]; then
        cat > "$ENV_FILE" <<'EOF'
# Relay runner settings, read by keeperd + relay-runner at service start.
# After editing: sudo systemctl restart relay-runner@<user>
# Every setting, documented:
#   https://github.com/Vinuitik/relay/blob/main/runner/install/runner.env.example

# Where the runner keeps key.txt, hub.json, sessions. Default ~/.relay of the
# service user.
#RELAY_HOME=/home/youruser/.relay

# Direct phone-app access over Tailscale: set to this machine's Tailscale IP
# (tailscale ip -4). Unset = 127.0.0.1:7777, reachable only through the
# HabitTracker hub (relay-runner pair) or from this machine.
#RELAY_LISTEN_ADDR=100.x.y.z:7777
EOF
        echo "    wrote $ENV_FILE"
    else
        echo "    $ENV_FILE exists - kept"
    fi
    if [ -n "$TS_IP" ] && grep -q "^#RELAY_LISTEN_ADDR=" "$ENV_FILE" && ! grep -q "^RELAY_LISTEN_ADDR=" "$ENV_FILE"; then
        sed -i "s/^#RELAY_LISTEN_ADDR=.*/RELAY_LISTEN_ADDR=$TS_IP:7777/" "$ENV_FILE"
        echo "    RELAY_LISTEN_ADDR set to $TS_IP:7777 (Tailscale)"
    fi
}

# Keep in sync with runner/install/relay-runner.service (the reference copy).
write_unit() {
    cat > "$UNIT_FILE" <<'EOF'
[Unit]
Description=Relay runner (via keeperd)
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
Type=simple
User=%i
# keeperd runs relay-runner as its child and swaps in new releases - see
# runner/FLOWS.md "keeperd". It exits (code 3) after replacing its own binary;
# Restart=always brings up the new one.
ExecStart=/opt/relay/bin/keeperd
Restart=always
RestartSec=5
TimeoutStopSec=20
EnvironmentFile=-/etc/relay/runner.env
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF
}

install_clis() {
    if ! command -v npm >/dev/null 2>&1; then
        if command -v apt-get >/dev/null 2>&1; then
            echo "    npm not found - installing Node.js via apt (nodejs, npm)"
            apt-get update -qq </dev/null && apt-get install -y nodejs npm </dev/null
        else
            echo "    npm not found and this isn't an apt-based system - skipping CLI install"
            echo "    (install Node.js, then: npm install -g @anthropic-ai/claude-code @openai/codex)"
        fi
    fi
    if command -v npm >/dev/null 2>&1; then
        if ! command -v claude >/dev/null 2>&1; then
            echo "    installing claude CLI (npm install -g @anthropic-ai/claude-code)"
            npm install -g @anthropic-ai/claude-code </dev/null || echo "    claude CLI install failed - continuing"
        else
            echo "    claude CLI already installed"
        fi
        if ! command -v codex >/dev/null 2>&1; then
            echo "    installing codex CLI (npm install -g @openai/codex)"
            npm install -g @openai/codex </dev/null || echo "    codex CLI install failed - continuing"
        else
            echo "    codex CLI already installed"
        fi
    fi
}

main() {
    LOCAL_BIN=""
    if [ $# -ge 1 ]; then
        LOCAL_BIN="$1"
        [ -f "$LOCAL_BIN" ] || die "$LOCAL_BIN is not a file (usage: sudo $0 [/path/to/relay-runner user])"
        RUN_USER="${2:-${RELAY_USER:-${SUDO_USER:-}}}"
    else
        RUN_USER="${RELAY_USER:-${SUDO_USER:-}}"
    fi

    [ "$(id -u)" -eq 0 ] || die "must run as root: curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sudo sh"
    if [ -z "$RUN_USER" ] || [ "$RUN_USER" = root ]; then
        die "the runner must not run as root. Run this with sudo from your normal account,
       or name the user: curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sudo RELAY_USER=youruser sh"
    fi
    id "$RUN_USER" >/dev/null 2>&1 || die "user '$RUN_USER' does not exist (adduser $RUN_USER first)"
    if [ ! -d /run/systemd/system ] || ! command -v systemctl >/dev/null 2>&1; then
        die "systemd not found - this installer only supports systemd Linux"
    fi
    command -v curl >/dev/null 2>&1 || die "curl not found"
    command -v sha256sum >/dev/null 2>&1 || die "sha256sum not found (coreutils)"

    ARCH="$(detect_arch)"
    RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"
    TMP="$(mktemp -d)"
    trap 'rm -rf "$TMP"' EXIT

    echo "1/8 downloading (user $RUN_USER, linux-$ARCH)"
    KEEPERD_SRC=""
    if [ -n "$LOCAL_BIN" ]; then
        dir="$(dirname "$LOCAL_BIN")"
        for k in "${KEEPERD_BIN:-}" "$dir/keeperd-linux-$ARCH" "$dir/keeperd"; do
            [ -n "$k" ] && [ -f "$k" ] && KEEPERD_SRC="$k" && break
        done
    fi
    if [ -z "$LOCAL_BIN" ] || [ -z "$KEEPERD_SRC" ]; then
        TAG="$(resolve_tag)"
        echo "    release $TAG"
        if [ -z "$LOCAL_BIN" ]; then
            fetch "keeperd-linux-$ARCH" "relay-runner-linux-$ARCH"
            LOCAL_BIN="$TMP/relay-runner-linux-$ARCH"
        else
            fetch "keeperd-linux-$ARCH"
        fi
        [ -n "$KEEPERD_SRC" ] || KEEPERD_SRC="$TMP/keeperd-linux-$ARCH"
    fi
    echo "    verified"

    echo "2/8 checking Tailscale (optional)"
    TS_IP=""
    if command -v tailscale >/dev/null 2>&1; then
        TS_IP="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
        if [ -n "$TS_IP" ]; then
            echo "    logged in, tailnet IP $TS_IP"
        else
            echo "    installed but not logged in - skipping (hub only)"
        fi
    else
        echo "    not installed - skipping (the HabitTracker hub doesn't need it)"
    fi

    echo "3/8 checking claude / codex CLIs"
    install_clis

    echo "4/8 installing binaries to $BIN_DIR"
    mkdir -p "$BIN_DIR"
    install_bin "$KEEPERD_SRC" keeperd
    install_bin "$LOCAL_BIN" relay-runner
    chown -R "$RUN_USER":"$RUN_USER" /opt/relay
    if [ -f /usr/local/bin/relay-runner ]; then
        echo "    removing stale binary at old location /usr/local/bin/relay-runner"
        rm -f /usr/local/bin/relay-runner
    fi

    echo "5/8 settings ($ENV_FILE)"
    write_env
    RELAY_HOME_DIR="$(sed -n 's/^RELAY_HOME=//p' "$ENV_FILE" | tail -n1 | tr -d '"')"
    [ -n "$RELAY_HOME_DIR" ] || RELAY_HOME_DIR="$RUN_HOME/.relay"
    LISTEN="$(sed -n 's/^RELAY_LISTEN_ADDR=//p' "$ENV_FILE" | tail -n1 | tr -d '"')"
    HUB="$(sed -n 's/^RELAY_HUB=//p' "$ENV_FILE" | tail -n1 | tr -d '"')"

    echo "6/8 installing systemd unit $UNIT_FILE"
    write_unit
    systemctl daemon-reload

    echo "7/8 enabling + restarting relay-runner@$RUN_USER"
    # Always restart, not just enable --now: on a running service enable --now
    # is a no-op and the old binaries would keep running.
    systemctl enable "relay-runner@$RUN_USER"
    systemctl restart "relay-runner@$RUN_USER"

    KEY=""
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        [ -f "$RELAY_HOME_DIR/key.txt" ] && KEY="$(cat "$RELAY_HOME_DIR/key.txt")" && break
        sleep 1
    done

    echo
    echo "================================================================"
    echo " RELAY INSTALL COMPLETE"
    echo "================================================================"
    if [ -n "$KEY" ]; then
        echo " Runner key  : $KEY"
    else
        echo " Runner key  : not created yet - journalctl -u relay-runner@$RUN_USER | grep 'RELAY KEY'"
    fi
    echo " Runner addr : ${LISTEN:-127.0.0.1:7777 (local only - reach it through the hub)}"
    echo " status      : systemctl status relay-runner@$RUN_USER"
    echo " logs        : journalctl -u relay-runner@$RUN_USER -f"
    echo "================================================================"
    echo
    case "$LISTEN" in
        ""|127.*|localhost*) ;;
        *)
            if [ -n "$KEY" ]; then
                echo "Android app (direct over Tailscale): Add Runner -> Scan QR:"
                echo
                as_user env RELAY_HOME="$RELAY_HOME_DIR" RELAY_LISTEN_ADDR="$LISTEN" "$BIN_DIR/relay-runner" -qr || true
                echo
            fi
            ;;
    esac

    echo "8/8 pairing with HabitTracker"
    # shared/HUB.md "QR pairing". The running service picks up hub.json by
    # itself (no restart). Last step on purpose: Ctrl-C here loses nothing.
    if [ -f "$RELAY_HOME_DIR/hub.json" ]; then
        echo "    already paired ($RELAY_HOME_DIR/hub.json) - skipping"
    else
        echo "    Ctrl-C skips; pair later with: sudo -u $RUN_USER $BIN_DIR/relay-runner pair"
        echo
        trap ':' INT
        # No --open here: this runs as root/sudo, and opening a browser from
        # there (or as the target user without their session bus) is unreliable.
        # GUI installers run `relay-runner pair --open` themselves, as the user.
        as_user env RELAY_HOME="$RELAY_HOME_DIR" ${HUB:+RELAY_HUB="$HUB"} "$BIN_DIR/relay-runner" pair \
            || echo "    pairing skipped - run later: sudo -u $RUN_USER $BIN_DIR/relay-runner pair"
        trap - INT
    fi
}

main "$@"
