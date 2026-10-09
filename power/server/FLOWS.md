# Server sleep/wake schedule + Wi-Fi watchdog

Files: install.sh, uninstall.sh, relay-sleep.sh, relay-sleep.timer, relay-sleep.service, relay-sleep-warn.timer, relay-sleep-warn.service, relay-wake.timer, relay-wake.service, relay-wifi-fix.sh, relay-wifi-watchdog.timer, relay-wifi-watchdog.service

Sleep schedule runs on commute days only (Tue/Thu/Fri). Gaps 09:00–12:00 and 13:00–17:00 the server sleeps; everything
else (morning power-on, evening shutdown) is manual.

## Flows

**Warn:** `relay-sleep-warn.timer` (08:55, 12:55) → `relay-sleep-warn.service` → `wall` to all
logged-in terminals (SSH included).
To change the message: `relay-sleep-warn.service` `ExecStart`.

**Sleep:** `relay-sleep.timer` (09:00, 13:00) → `relay-sleep.service` → `/usr/local/bin/relay-sleep.sh`
→ `systemctl suspend` → `nvidia-suspend.service` hook → kernel S3 (`deep`).
If a block-mode inhibitor refuses (e.g. APT mid-upgrade) → retry every 60s, give up after 30 tries.
To change retry budget: `relay-sleep.sh` loop bound.

**Wake:** `relay-wake.timer` (12:00, 17:00, `WakeSystem=true`) → before each suspend systemd
programs the RTC alarm for the next elapse → firmware powers back to S0 → `relay-wake.service`
runs `/bin/true` (the wake itself is the point).
To change times/days: `OnCalendar=` lines in the `.timer` files, then `sudo sh install.sh` again.

**Wi-Fi watchdog:** `relay-wifi-watchdog.timer` (3 min after boot, then every 2 min) →
`relay-wifi-watchdog.service` → `/usr/local/bin/relay-wifi-fix.sh`:
healthy? (`wlp0s20f3` `connected` in `nmcli` AND default gateway answers 1 of 3 pings; re-checked
for 30s before acting) → yes: clear counter, exit
→ flight mode on (`rfkill` soft block / `nmcli radio wifi` disabled)? → `rfkill unblock wifi`, exit
→ tries < 5? → PCI remove `0000:00:14.3` → 3s → PCI rescan → wait up to 60s for healthy → log
→ tries = 5 → log "gave up" once, then silent until reboot (counter in `/run/relay-wifi-tries`).
To change interval: `relay-wifi-watchdog.timer` `OnUnitActiveSec=`. Tries: `MAX_TRIES` in the script.

**Install:** copy `power/server/` to the server (`scp`) → `sudo sh install.sh` → units into
`/etc/systemd/system/`, script into `/usr/local/bin/`, `enable --now` the three timers.
Check: `systemctl list-timers 'relay-*'`. Remove: `sudo sh uninstall.sh`.

## Technology Notes

- **Why the watchdog exists (seen 2026-10-08 and 2026-10-09):** the Intel AX201 firmware
  (`QuZ-a0-hr-b0-77`) crashes (`Microcode SW error`, `NMI_INTERRUPT_WDG`) and the driver's own
  restart then fails forever (`Failed to start RT ucode: -110`). Suspend/wake ran on time on the
  8th; the card was dead the whole day. Reloading the module does not fix it; PCI remove/rescan does.
- **Hardcoded `wlp0s20f3` / `0000:00:14.3`** in `relay-wifi-fix.sh`: a different Wi-Fi card or
  slot breaks the watchdog silently (it would never see "connected" and reset a non-existent path).
- **PCI reset can fail too** (the 2026-10-09 17:41 boot never got the card up). Only a full
  power-off (hold power 10s, unplug charger) clears that. Watchdog logs "gave up" in that case.
- **Wi-Fi power saving is left on** on purpose (user decision). It's the usual suspect for this
  firmware crash; turning it off (`iwlmvm power_scheme=1`) is the untried lever.
- **Monotonic timer:** the 2-min countdown pauses during suspend, so the first check after a wake
  lands within 2 min of it.
- **Flight mode is force-cleared.** The F2 key soft-blocks Wi-Fi and systemd-rfkill restores
  that across reboots; the watchdog undoes it within 2 min.

- **Times are server-local** (`Europe/London`). BST/GMT switches are handled by systemd; a
  timezone change on the server shifts the whole schedule.
- **Must suspend via `systemctl suspend`, never `rtcwake -m mem`** — the latter skips the
  NVIDIA hook and the kernel aborts the suspend (seen 2026-10-07).
- **`WakeSystem=true` only works across suspend, not poweroff.** If you shut down manually,
  the RTC alarm isn't armed by systemd and the 12:00/17:00 wakes won't happen. Waking from S5
  via RTC is firmware-dependent and untested on this Aspire.
- **One RTC alarm at a time.** systemd arms only the next `WakeSystem` elapse. Anything else
  writing `/sys/class/rtc/rtc0/wakealarm` (e.g. a manual `rtcwake`) can overwrite it.
- **Missed events are skipped** (`Persistent=false`): if the server is off at 09:00, it won't
  suspend at boot to "catch up".
- **Inhibitors win.** APT holds a block-mode `shutdown:sleep` lock while upgrading; the sleep
  retries rather than forcing `-i`. A long upgrade can push the suspend up to 30 min late.
- **Logged-in users don't block it.** The suspend runs as root from a service; SSH sessions
  just freeze and drop. The 5-min `wall` warning is the only notice — editors over a mount
  (not SSH) won't see it.
- **Runner idle-suspend must stay off** (`RELAY_IDLE_SUSPEND_ENABLED` in
  `/etc/relay/runner.env`): with no waker deployed, nothing would wake the server until the
  next scheduled wake.
- **GNOME idle suspend is off on AC** (`sleep-inactive-ac-type 'nothing'`). If re-enabled,
  the server could fall asleep again right after a timed wake.
- **Server must be on AC.** Suspend on battery drains it; a dead battery loses RAM state.

## Change Index

| Thing | Where |
|---|---|
| Sleep times / days | `relay-sleep.timer` `OnCalendar=` |
| Wake times / days | `relay-wake.timer` `OnCalendar=` |
| Warning times | `relay-sleep-warn.timer` `OnCalendar=` |
| Warning text | `relay-sleep-warn.service` `ExecStart` |
| Retry budget while inhibited | `relay-sleep.sh` loop (`seq 1 30`, `sleep 60`) |
| Timer precision | `AccuracySec=` in each `.timer` |
| Apply changes | `scp` to server, `sudo sh install.sh` |
| Remove everything | `sudo sh uninstall.sh` |
| Wi-Fi check interval | `relay-wifi-watchdog.timer` `OnUnitActiveSec=` |
| Wi-Fi reset attempts | `MAX_TRIES` in `relay-wifi-fix.sh` |
| Wi-Fi card name / PCI slot | `IFACE` / `PCI` in `relay-wifi-fix.sh` |
| Logs | `journalctl -u relay-sleep -u relay-wake -u relay-wifi-watchdog`; Wi-Fi driver: `journalctl -k \| grep iwlwifi`; kernel: `journalctl -k \| grep 'PM: suspend'` |
