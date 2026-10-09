# Server sleep/wake schedule + Wi-Fi watchdog

Files: install.sh, uninstall.sh, relay-sleep.sh, relay-sleep.timer, relay-sleep.service, relay-sleep-warn.timer, relay-sleep-warn.service, relay-wake.timer, relay-wake.service, relay-schedule-apply.sh, relay-schedule.path, relay-schedule-apply.service, test/test-apply.sh, relay-wifi-fix.sh, relay-wifi-watchdog.timer, relay-wifi-watchdog.service

Sleep/wake times come from bookings made in the phone app (runner/internal/schedule). Morning
power-on and evening shutdown stay manual; bookings only drive the sleep gaps inside a day.

## Flows

**Plan → timers:** phone books → runner `schedule.Service` writes `/var/lib/relay/schedule-plan`
(next 14 days, `warn|sleep|wake YYYY-MM-DD HH:MM`; format in shared/API.md "Schedule plan file")
→ `relay-schedule.path` (`PathChanged`) → `relay-schedule-apply.service` → `/usr/local/bin/relay-schedule-apply.sh`:
snapshot plan (refuse symlink / non-regular file) → every line matches the pattern and is a real
date, else reject the **whole** plan and change nothing → write
`/etc/systemd/system/relay-{sleep-warn,sleep,wake}.timer.d/plan.conf` (`OnCalendar=` reset +
placeholder `2000-01-01` + one dated `OnCalendar=` per event) → `daemon-reload` + restart the
three timers → write `/var/lib/relay/schedule-applied` (sha256 of plan, time) → app shows
"Applied on server ✓".
Also runs once at boot (`relay-schedule-apply.service` WantedBy multi-user).
To change times: book in the app (or `POST /v1/schedule/bookings`). Never edit the drop-ins.

**Warn:** `relay-sleep-warn.timer` (plan `warn` lines, 5 min before each sleep) → `relay-sleep-warn.service` → `wall` to all
logged-in terminals (SSH included).
To change the message: `relay-sleep-warn.service` `ExecStart`.

**Sleep:** `relay-sleep.timer` (plan `sleep` lines) → `relay-sleep.service` → `/usr/local/bin/relay-sleep.sh`
→ `systemctl suspend` → `nvidia-suspend.service` hook → kernel S3 (`deep`).
If a block-mode inhibitor refuses (e.g. APT mid-upgrade) → retry every 60s, give up after 30 tries.
To change retry budget: `relay-sleep.sh` loop bound.

**Wake:** `relay-wake.timer` (plan `wake` lines, `WakeSystem=true`) → before each suspend systemd
programs the RTC alarm for the next elapse → firmware powers back to S0 → `relay-wake.service`
runs `/bin/true` (the wake itself is the point).

**Wi-Fi watchdog:** `relay-wifi-watchdog.timer` (3 min after boot, then every 2 min) →
`relay-wifi-watchdog.service` → `/usr/local/bin/relay-wifi-fix.sh`:
healthy? (`wlp0s20f3` `connected` in `nmcli` AND default gateway answers 1 of 3 pings; re-checked
for 30s before acting) → yes: clear counter, exit
→ flight mode on (`rfkill` soft block / `nmcli radio wifi` disabled)? → `rfkill unblock wifi`, exit
→ tries < 5? → PCI remove `0000:00:14.3` → 3s → PCI rescan → wait up to 60s for healthy → log
→ tries = 5 → log "gave up" once, then silent until reboot (counter in `/run/relay-wifi-tries`).
To change interval: `relay-wifi-watchdog.timer` `OnUnitActiveSec=`. Tries: `MAX_TRIES` in the script.

**Install:** copy `power/server/` to the server (`scp`) → `sudo sh install.sh` → creates
`/var/lib/relay` owned by the runner user (`RELAY_USER`, else `SUDO_USER`) → units into
`/etc/systemd/system/`, scripts into `/usr/local/bin/` → enables the timers, the path unit and
the apply service → applies once.
Test the applier without a server: `power/server/test/test-apply.sh` in a Debian container.
Check: `systemctl list-timers 'relay-*'`. Remove: `sudo sh uninstall.sh`.

## Technology Notes

- **Empty or missing plan = no sleeps.** The server just stays awake: the safe direction. That's
  also what happens if the runner is down for over 14 days (the plan runs out).
- **Base timers keep a placeholder `OnCalendar=2000-01-01`.** A timer with zero triggers refuses
  to start; a past date never fires (`Persistent=false`). When every dated entry has passed, the
  timer may show as elapsed in `list-timers` until the next apply. Not yet checked on the server.
- **The runner never has root.** It owns `/var/lib/relay` and writes only the plan; root validates
  every line. Side effect: the runner can also overwrite `schedule-applied`, so "Applied ✓" is only
  as trustworthy as the runner.
- **Plan rewrites:** on every booking change, at runner start, daily 00:05. Identical event
  lines → file untouched → no timer restart.

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
| Sleep / wake / warn times | bookings in the app → plan file → `.timer.d/plan.conf` (generated) |
| Plan validation / drop-in format | `relay-schedule-apply.sh` (`PATTERN`, `PLACEHOLDER`) |
| Plan / status paths | `PLAN`, `STATUS` in `relay-schedule-apply.sh`; runner side `RELAY_SCHEDULE_PLAN` |
| Warn lead time | `WarnMinutes` in runner/internal/schedule/types.go |
| Warning text | `relay-sleep-warn.service` `ExecStart` |
| Retry budget while inhibited | `relay-sleep.sh` loop (`seq 1 30`, `sleep 60`) |
| Timer precision | `AccuracySec=` in each `.timer` |
| Apply changes | `scp` to server, `sudo sh install.sh` |
| Remove everything | `sudo sh uninstall.sh` |
| Wi-Fi check interval | `relay-wifi-watchdog.timer` `OnUnitActiveSec=` |
| Wi-Fi reset attempts | `MAX_TRIES` in `relay-wifi-fix.sh` |
| Wi-Fi card name / PCI slot | `IFACE` / `PCI` in `relay-wifi-fix.sh` |
| Logs | `journalctl -u relay-schedule-apply -u relay-sleep -u relay-wake -u relay-wifi-watchdog`; Wi-Fi driver: `journalctl -k \| grep iwlwifi`; kernel: `journalctl -k \| grep 'PM: suspend'` |
