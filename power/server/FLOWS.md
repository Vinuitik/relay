# Server sleep/wake schedule

Files: install.sh, uninstall.sh, relay-sleep.sh, relay-sleep.timer, relay-sleep.service, relay-sleep-warn.timer, relay-sleep-warn.service, relay-wake.timer, relay-wake.service

Commute days only (Tue/Thu/Fri). Gaps 09:00–12:00 and 13:00–17:00 the server sleeps; everything
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

**Install:** copy `power/server/` to the server (`scp`) → `sudo sh install.sh` → units into
`/etc/systemd/system/`, script into `/usr/local/bin/`, `enable --now` the three timers.
Check: `systemctl list-timers 'relay-*'`. Remove: `sudo sh uninstall.sh`.

## Technology Notes

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
| Logs | `journalctl -u relay-sleep -u relay-wake`; kernel: `journalctl -k \| grep 'PM: suspend'` |
