# Installer

Files: relay-setup.iss, ../install.ps1, ../install-keeperd.ps1, ../../../.github/workflows/runner-release.yml

## Installer (Windows GUI)

Build: push to main (runner/**) → job `release` (tag `runner-<sha7>`) → job `windows-installer` → `gh release download` 3 assets + copy install.ps1 → ISCC `/DAppVersion=<tag>` → `gh release upload Relay-Setup.exe Relay-Setup.exe.sha256`
→ user URL: https://github.com/Vinuitik/relay/releases/latest/download/Relay-Setup.exe
To change: workflow job `windows-installer`.

Install: Relay-Setup.exe (UAC admin) → [Files] stage 4 files in `{tmp}` → `CurStepChanged(ssPostInstall)` → `RunInstallScript()` writes `{tmp}\run-install.ps1` (`WriteWrapper()`) → powershell hidden → sets `RELAY_INSTALL_FROM={tmp}`, `RELAY_INSTALL_NOPAIR=1` → `install.ps1` → output to `%LOCALAPPDATA%\Relay\install.log` → exit 0/1
→ exit != 0 → `RaiseException` friendly message with log path, Setup aborts.
To change: `RunInstallScript()` / `WriteWrapper()`.

Finish page: `AlreadyPaired()` (`%USERPROFILE%\.relay\hub.json` exists) → no: `[Run]` `relay-runner.exe pair --open` (hidden, nowait) → browser "Connect <machine>?" → hub.json written; yes: open https://habittrackerdima.me/relay.
Start menu: `[Icons]` "Connect Relay to HabitTracker" → `relay-runner.exe pair --open`.

Uninstall: Apps & Features → `CurUninstallStepChanged(usUninstall)` → `run-uninstall.ps1`: stop+unregister "Relay keeper" / "Relay input watcher" → kill keeperd/relay-runner → delete `%LOCALAPPDATA%\Relay\bin` → strip it from user PATH → message that `%USERPROFILE%\.relay` was kept.
To change: `CurUninstallStepChanged()`.

[NOT IMPLEMENTED] code signing; ARM64 build (x64 binaries run emulated); silent-mode pairing.

## Change Index
| Thing | Where |
|---|---|
| App name / publisher / admin / x64 | `[Setup]` in relay-setup.iss |
| Version stamp | `/DAppVersion=` (workflow `$env:TAG`) |
| Bundled files | `[Files]`, workflow "Download release assets" |
| Install env vars / log path | `WriteWrapper()`, `LogPath()` |
| Failure message | `RunInstallScript()` |
| Finish checkbox / hub URL | `[Run]`, `AlreadyPaired()`, `PairFile` |
| Start-menu shortcut | `[Icons]` |
| Uninstall steps / kept data | `CurUninstallStepChanged()` |
| Release asset names | workflow job `windows-installer` |

## Technology Notes
- Unsigned: Windows SmartScreen shows "Windows protected your PC"; user clicks "More info" → "Run anyway". Reputation never builds without a cert; some AV may flag it.
- Admin elevation: `{localappdata}` is the elevating account's profile, same assumption as install.ps1. If a standard user elevates with a different admin's credentials, Relay installs into the admin's profile and the scheduled tasks run as that admin.
- Binaries are NOT tracked by Inno; the uninstaller is hand-written. If install.ps1 adds new artifacts (tasks, firewall rule "Relay runner (7777)", npm CLIs), uninstall will not remove them unless added to `CurUninstallStepChanged()`. The firewall rule currently stays.
- Uninstall leaves `%USERPROFILE%\.relay` (runner key, hub pairing) and `relay.env`/logs; reinstalling stays paired.
- Installer embeds the binaries of one release; keeperd auto-updates afterwards, so an old Relay-Setup.exe still installs an old build first.
- `latest/download/Relay-Setup.exe` 404s for a short window after a release until the `windows-installer` job finishes.
- Not verified on Windows: ISCC build and wizard behavior were desk-checked only.
