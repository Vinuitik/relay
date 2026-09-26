# Idle-suspend flows

Files: idle.go, capability.go, capability_windows.go, capability_unix.go,
shutdown_windows.go, shutdown_unix.go

How a machine puts *itself* to sleep, and what it takes to get it back.
Nothing here wakes anything — waking is `wakerd`'s job, on a different
machine. See `../../cmd/wakerd/FLOWS.md`.

## Sleep flow

```
Monitor.Run() → ticker every cfg.CheckInterval
  → tick() → SessionStatus.IdleStatus()      (a) agent session busy?
           → Activity.LastActive()            (b) phone app in foreground?
           → LocalInput()                     (c) local keyboard/mouse?
  → all three quiet for cfg.Timeout → BeforeShutdown() → Shutdowner.Shutdown()
```

Any one of (a)(b)(c) being recent blocks suspend. To change the threshold:
env `RELAY_IDLE_TIMEOUT`. To change poll rate: `RELAY_IDLE_CHECK_INTERVAL`.
The whole feature is off unless `RELAY_IDLE_SUSPEND_ENABLED=true`.

Manual path (phone's "Sleep" button): `POST /v1/suspend` →
`api.Server.Suspend` → same `Shutdowner.Shutdown()`. Reuses the same env
gate, so a machine that can't auto-suspend can't be manually suspended
either. `409` if busy, `503` if the gate is unset.

To change what "suspend" actually runs: `osShutdowner.Shutdown()` in
`shutdown_windows.go` / `shutdown_unix.go`.

## Resume flow

The process **survives** S3 (it did not under the old S5 design), so:

```
tick() sees wall-clock gap > resumeGraceFactor × CheckInterval
  → treat as "we just resumed" → set resumeFloor = now → clear triggered
```

`resumeFloor` floors `idleSince`, giving a freshly-woken machine a full
`cfg.Timeout` of grace. Without it the pre-suspend turn-end timestamp would
already be older than the timeout and the machine would re-suspend seconds
after waking. To change the grace: `Monitor.resumeFloor` / `resumeGraceFactor`.

## Capability detection

Startup calls `idle.Detect()` and logs one line saying which state this
machine will actually land in and whether it needs a wakerd to come back.

```
Detect() → Windows: `powercfg /a` → parsePowercfg() → Capability
         → Linux:   read /sys/power/state, look for "mem"
```

`parsePowercfg` deliberately lives in `capability.go` (no build tag) so its
tests run on every platform — it is pure string handling.

It tracks which *section* of `powercfg /a` output it is in, because the
command prints an available list followed by a not-available list, and a
bare substring search would match a state listed as unavailable.

Precedence, and why: **hibernation wins.** `SetSuspendState` honours an
enabled hibernation file over its own "don't hibernate" argument, so a
machine with hibernation on lands in S4 no matter what was intended.
Reporting S3 or S0ix there would be a lie.

To change detection: `Detect()` per OS. To change the precedence: the
`switch` at the end of `parsePowercfg`.

## Per-machine wake matrix

Measured on this project's actual hardware, 2026-09-26. **This table is the
thing to check before enabling idle-suspend on any machine.**

| Machine | Deepest state | Wake path | Status |
|---|---|---|---|
| Acer Aspire A715 (Ubuntu, "server") | S3 | WoL over **Ethernet** | works once cabled — `Supports Wake-on: pumbg`, currently `Wake-on: d` |
| Acer, over WiFi | S3 | WoL over WiFi | **impossible** — firmware rfkills the radio on suspend |
| Dell (Windows, laptop) | S4 today, S0ix if hibernation off | none found | **not remotely wakeable** — see below |

### Acer: why WiFi can never work

`iw phy0 wowlan enable magic-packet` and `power/wakeup=enabled` both stick
and survive a resume. The packet still never lands. Resume log says:

```
kernel: iwlwifi 0000:00:14.3: Rfkill was toggled during suspend
```

The EC/BIOS switches the radio off on suspend; iwlwifi then aborts WoWLAN.
The card is powered but radio-blocked. Not fixable in software.

Proven separately, so the rest of the chain is not in doubt: a 102-byte
magic packet broadcast from another WiFi client reaches the Acer on
`192.168.1.255` while awake, so the router does forward directed broadcast
to WiFi clients.

Enable Ethernet WoL (not persistent — `ethtool -s` resets on reboot):

```
sudo ethtool -s enp7s0 wol g
sudo nmcli con mod "<connection-name>" 802-3-ethernet.wake-on-lan magic   # persists
```

`nmcli con show` gives the connection name; it only exists once a cable is in.

### Dell: why it is not remotely wakeable

`powercfg /a` reports `Standby (S0 Low Power Idle) Network Connected`, and
S3 as *"disabled when S0 low power idle is supported"* — this firmware has
no S3 at all. Hibernation is enabled, so `SetSuspendState` currently lands
in **S4**. `powercfg /devicequery wake_armed` lists only the WWAN modem —
neither the WiFi nor the Ethernet adapter is armed to wake the system.

Do not read "Network Connected" as "runnerd stays reachable". In Modern
Standby ordinary processes are frozen in DRIPS; the NIC can wake the system
for allowed traffic, but a plain HTTP listener is not that. If you want to
rely on it, **measure it on that machine** — suspend it and probe from
another host. Until then treat the Dell as a machine you wake by hand.

## Technology Notes

- **The suspend is fire-and-forget.** `Shutdown()` returns as the machine
  goes down; nothing confirms it. A failed call is logged and the Monitor
  backs off rather than retrying in a loop (see `tick`'s doc comment).
- **Linux suspend needs rights the service user usually lacks.**
  `systemctl suspend` asks logind over D-Bus, and polkit authorizes on the
  *session*, not the uid — a remote/non-seat session is not "active", so it
  is refused **even under sudo**. Grant a polkit rule for
  `org.freedesktop.login1.suspend`, or use `systemctl start
  systemd-suspend.service` (bypasses polkit, needs root).
- **Sleep targets can be masked entirely.** On the Acer,
  `sleep.target` / `suspend.target` / `hibernate.target` /
  `hybrid-sleep.target` were symlinked to `/dev/null`, which makes
  `CanSuspend` report `no` and every suspend fail with *"Unit sleep.target
  is masked"*. Check with `systemctl is-enabled sleep.target`; fix with
  `systemctl unmask`. This looks exactly like a permissions problem and is
  not one.
- **Windows hibernation silently overrides the requested state** — covered
  above. `powercfg /hibernate off` is the fix, and it also frees a
  RAM-sized file on disk.
- **Modern Standby machines have no S3.** Any wakerd entry aimed at one is
  pointless; the state it reaches is not WoL-revivable in the normal sense.
- **Detection is advisory, not enforced.** `Detect()` only logs. A machine
  with no wake path will still suspend if you enable the feature. That is
  deliberate — a hard block would make the laptop case (suspend freely,
  wake by opening the lid) impossible.
- **`ethtool -s <if> wol g` does not survive a reboot.** Persist it via
  NetworkManager, or it silently reverts to `d` and wake stops working with
  no error anywhere.

## Change Index

| To change | Go to |
|---|---|
| Idle threshold | env `RELAY_IDLE_TIMEOUT`, `LoadConfig()` |
| Poll interval | env `RELAY_IDLE_CHECK_INTERVAL`, `LoadConfig()` |
| Enable/disable the whole feature | env `RELAY_IDLE_SUSPEND_ENABLED`, `LoadConfig()` |
| What "suspend" actually executes | `osShutdowner.Shutdown()` (`shutdown_{windows,unix}.go`) |
| Which signals block a suspend | `Monitor.tick()` sources (a)(b)(c) |
| Post-resume grace period | `Monitor.resumeFloor`, `resumeGraceFactor` |
| Suspend-state detection | `Detect()` (`capability_{windows,unix}.go`) |
| `powercfg /a` parsing / precedence | `parsePowercfg()` (`capability.go`) |
| Startup capability log line | `Capability.String()`, `cmd/runnerd/main.go` |
| Pre-suspend hook (FCM push etc.) | `Monitor.BeforeShutdown`, `cmd/runnerd/main.go` |
| Manual suspend endpoint | `api.Server.Suspend`, `POST /v1/suspend` |
