# Installer (Linux .deb)

Files: build-deb.sh, relay-runner.service, relay-seed, relay-connect, relay-connect.desktop, relay-connect-autostart.desktop, postinst, prerm, ../install.sh, ../../../.github/workflows/runner-release.yml

## What the user does
1. Downloads `relay-runner_<ver>_amd64.deb` (or arm64) from the GitHub release → double-click → distro installer (GNOME Software / Discover) asks for the admin password once.
2. Opens "Relay — connect this computer" from the app menu (or logs out/in: the autostart entry opens it by itself while unpaired).
3. Browser opens the HabitTracker claim page `/relay/claim/<id>` → clicks Confirm. Done; no terminal.
Already paired → the same menu entry just (re)starts the service and opens https://habittrackerdima.me/relay.

## Build
push to main (runner/**) → job `release` → job `linux-deb`: `gh release download` keeperd+relay-runner (amd64, arm64) → `build-deb.sh <0.YYYYMMDD.sha7> <arch> dist out` → `gh release upload *.deb debs.sha256`.
Local: `build-deb.sh 0.1.0 amd64 <dir with keeperd-linux-amd64 relay-runner-linux-amd64> out`.
To change: workflow job `linux-deb`, `build-deb.sh`.

## Install (root, package only)
dpkg → files: `/opt/relay/bin/{keeperd,relay-runner}`, `/opt/relay/libexec/{relay-seed,relay-connect}`, `/usr/lib/systemd/user/relay-runner.service`, `/usr/share/applications/relay-connect.desktop`, `/etc/xdg/autostart/relay-connect.desktop`, `/usr/bin/relay-runner` symlink → `postinst`: `systemctl --global enable relay-runner.service` (works with no user session; every user's manager starts it at next login).
Remove: `prerm` → `systemctl --global disable`; user data stays.

## First run (as the user, in their session)
`relay-connect [--if-unpaired]` → exits if `--if-unpaired` and `~/.relay/hub.json` exists → `relay-seed` → `loginctl enable-linger` (best effort: runs at boot) → `systemctl --user enable` + `restart relay-runner` → `relay-runner pair --open` (same call as Windows' finish page) → log `~/.local/state/relay/connect.log`, `notify-send` on failure.
Run: user unit → `ExecStartPre=relay-seed` → `ExecStart=~/.local/share/relay/bin/keeperd` with `RELAY_KEEPER_DIR=~/.local/share/relay`; settings file `~/.config/relay/runner.env` (optional).
`relay-seed` copies the package binaries into `~/.local/share/relay/bin` when missing or when the package copy is newer — so keeperd can self-update without root, and a package upgrade still wins over an older home copy.
To change: `relay-connect`, `relay-seed`, `relay-runner.service`.

[NOT IMPLEMENTED] rpm/Fedora, Arch, AppImage; package signing / apt repo (manual download only); uninstall of `~/.local/share/relay` and `~/.relay`; npm claude/codex CLI install (install.sh does it; the .deb does not).

## Change Index
| Thing | Where |
|---|---|
| Package name/version/deps | heredoc in `build-deb.sh` |
| Install paths | `build-deb.sh` install lines |
| Unit contents | `relay-runner.service` |
| Menu / autostart entry | `relay-connect.desktop`, `relay-connect-autostart.desktop` |
| Pair + start logic | `relay-connect` |
| Home copy of binaries | `relay-seed`, `RELAY_KEEPER_DIR` in unit |
| Env settings | `~/.config/relay/runner.env` (`runner.env.example` lists keys) |
| Hub URL for "already paired" | `relay-connect` |
| Release assets | workflow job `linux-deb` |

## Technology Notes
- Needs systemd with user manager (Debian/Ubuntu/Mint/Pop). Non-systemd distros (Alpine, Void, Devuan): `Depends: systemd` blocks install or the unit is inert; use `install.sh` or run keeperd by hand.
- deb only: Fedora/openSUSE (rpm) and Arch are not covered.
- User service runs only while the user has a session unless lingering is on. `relay-connect` tries `loginctl enable-linger` (polkit usually allows it for yourself; locked-down systems refuse → silently ignored). Without linger the runner is off after logout and before login, unlike Windows' at-boot task.
- Postinst can't start the service or open a browser (root, no session). Hence the manual first run; the autostart entry covers "install, then log in again". A GUI installer that stays open (GNOME Software) gives no "launch" button for it — user finds the menu entry.
- `.desktop` quirks: GNOME Wayland sessions ignore `X-GNOME-Autostart-enabled` oddities and may delay autostart until the shell is up; KDE honors /etc/xdg/autostart. A menu entry may need a few seconds/relogin to appear. `Terminal=false`: errors only show as a notification (needs `libnotify-bin`, Recommends) or in `connect.log`.
- `xdg-open`/browser launch needs the user's DBUS/DISPLAY; works from the menu entry, fails over plain ssh.
- keeperd auto-updates the home copy of the binaries to the newest release regardless of the .deb version; an old .deb still installs an old build first.
- Unsigned, no apt repo: no automatic .deb upgrades (keeperd updates binaries, not the unit/desktop files). GNOME Software may show an "untrusted package" notice.
- Hardcoded: `/opt/relay` (libexec paths in unit and desktop files; moving it breaks them), hub URL in `relay-connect`.
- Not verified: real desktop session (menu entry, autostart, linger, browser open), arm64 package (built same script, not installed), `systemctl --user` start inside the container (no user bus there).
