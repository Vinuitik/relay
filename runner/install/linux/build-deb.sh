#!/bin/sh
# Builds relay-runner_<version>_<arch>.deb (needs dpkg-deb). CI: workflow job linux-deb.
#   build-deb.sh <version> <arch> <dist-dir> [out-dir]
#   version: Debian upstream version, digits first (e.g. 0.20261108.abc1234)
#   arch:    amd64 | arm64
#   dist-dir: has keeperd-linux-<arch> and relay-runner-linux-<arch>
set -eu
VER="${1:?version}"; ARCH="${2:?arch (amd64|arm64)}"; DIST="${3:?dist dir}"; OUT="${4:-.}"
HERE="$(cd "$(dirname "$0")" && pwd)"
for f in keeperd relay-runner; do [ -f "$DIST/$f-linux-$ARCH" ] || { echo "missing $DIST/$f-linux-$ARCH" >&2; exit 1; }; done

ROOT="$(mktemp -d)"; trap 'rm -rf "$ROOT"' EXIT
install -d "$ROOT/DEBIAN" "$ROOT/opt/relay/bin" "$ROOT/opt/relay/libexec" \
    "$ROOT/usr/lib/systemd/user" "$ROOT/usr/share/applications" "$ROOT/etc/xdg/autostart" "$ROOT/usr/bin"
install -m 0755 "$DIST/keeperd-linux-$ARCH"      "$ROOT/opt/relay/bin/keeperd"
install -m 0755 "$DIST/relay-runner-linux-$ARCH" "$ROOT/opt/relay/bin/relay-runner"
install -m 0755 "$HERE/relay-seed" "$HERE/relay-connect" "$ROOT/opt/relay/libexec/"
install -m 0644 "$HERE/relay-runner.service" "$ROOT/usr/lib/systemd/user/relay-runner.service"
install -m 0644 "$HERE/relay-connect.desktop" "$ROOT/usr/share/applications/relay-connect.desktop"
install -m 0644 "$HERE/relay-connect-autostart.desktop" "$ROOT/etc/xdg/autostart/relay-connect.desktop"
ln -s /opt/relay/bin/relay-runner "$ROOT/usr/bin/relay-runner"
install -m 0755 "$HERE/postinst" "$HERE/prerm" "$ROOT/DEBIAN/"
SIZE="$(du -sk "$ROOT" | cut -f1)"
cat > "$ROOT/DEBIAN/control" <<CTL
Package: relay-runner
Version: $VER
Architecture: $ARCH
Maintainer: Vinuitik <noreply@users.noreply.github.com>
Depends: systemd, xdg-utils
Recommends: libnotify-bin
Installed-Size: $SIZE
Section: net
Priority: optional
Homepage: https://github.com/Vinuitik/relay
Description: Relay runner - connect this computer to HabitTracker
 Installs the Relay runner and its updater (keeperd) as a per-user systemd
 service. After install, open "Relay - connect this computer" from the app
 menu (or log in again) and click Confirm in the browser.
CTL
mkdir -p "$OUT"
dpkg-deb --root-owner-group --build "$ROOT" "$OUT/relay-runner_${VER}_${ARCH}.deb"
