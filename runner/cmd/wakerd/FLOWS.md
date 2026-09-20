# Wakerd flows

Files: cmd/wakerd/main.go, internal/waker/config.go, internal/waker/api.go,
internal/wol/directed.go, internal/wol/wol.go, internal/wol/broadcast_unix.go,
internal/wol/broadcast_windows.go

Wakerd is a **separate daemon from runnerd**, and the Pi it runs on is a **distinct role,
not a runner**: no projects, no sessions, no agent CLI, no Docker. Its entire job is "send a
magic packet on the LAN and tell me whether the machine came back."

Why it exists: a Wake-on-LAN magic packet is an **L2 broadcast**, so only a device physically
on the target's broadcast domain can send one. Both user machines sleep and the phone is on
4G, so nothing on the LAN is awake to send it. An always-on Raspberry Pi Zero 2 W on WiFi
fills that gap. This replaces the earlier "runners wake each other" design, which was circular
— the waking runner had to stay awake, which is exactly what we were trying to avoid.

## Startup

main() → waker.Load() → `$WAKER_HOME` or `~/.waker` created (0700) → `config.json` read, or
created with a fresh key + empty machine list on first run → key printed once when
`FirstRun` → each configured machine logged (`id (name) mac=… probe=host:port`) →
waker.NewServer(cfg) binds `wol.DirectedSender` + `waker.TCPProber` →
http.ListenAndServe(cfg.ListenAddr)

To change config location: `waker.wakerHome()` (env `WAKER_HOME`)
To change listen address: `waker.Load()` (env `WAKER_LISTEN_ADDR`, default `127.0.0.1:7778` —
bind the Tailscale interface IP in production; not enforced by code)
To add/remove machines: **hand-edit `~/.waker/config.json` and restart** — there is no API or
UI for this in v1, and the file is never rewritten once it exists.

Config shape:

```json
{"key":"<64 hex chars>",
 "machines":[{"id":"dell","name":"Dell laptop","mac":"34:CF:F6:81:08:76",
              "probeHost":"192.168.1.20","probePort":7777}]}
```

`probeHost` falls back to `id` (so `{"id":"dell"}` works if the name resolves); `probePort`
falls back to `7777`, the runner's own port. To change those fallbacks:
`waker.Machine.Host()` / `waker.Machine.Port()`, constant `waker.DefaultProbePort`.

## Request path

phone app → HTTP + `X-Relay-Key` header → `Server.auth` middleware compares against
`cfg.Key` (401 if wrong; `/v1/health` exempt) → Go 1.22 ServeMux method+pattern route →
handler

Deliberately the **same header name and middleware shape as the runner's `internal/api`**, so
the phone app has one auth scheme rather than two — but the **key is a different secret**,
generated independently in `~/.waker/config.json`.

| Endpoint | Auth | Returns |
|---|---|---|
| `GET /v1/health` | no | `{"ok":true}` |
| `GET /v1/machines` | yes | `[{id,name,mac,up}]` — `up` is a live probe per machine |
| `GET /v1/machines/{id}/status` | yes | `{id,up}` |
| `POST /v1/machines/{id}/wake` | yes | `{id,woken,waitedSeconds}` |

To change routes/auth: `waker.Server.Routes()`, `waker.Server.auth()`

## Reachability probe

`Server.Prober.Probe(ctx, machine)` → `TCPProber` → `net.Dialer{Timeout: 2s}.DialContext("tcp",
host:port)` → dial succeeded = up, anything else = down → conn closed immediately

A sleeping host doesn't answer at all, so the probe costs the full timeout when down. That's
why `GET /v1/machines` probes **all machines concurrently** (`handleListMachines`, one
goroutine per machine) instead of serially.

To change probe timeout: `waker.DefaultProbeTimeout` / `TCPProber.Timeout`
To fake the probe in tests: implement `waker.Prober` (see `api_test.go`'s `fakeProber`)

## Wake flow

POST /v1/machines/{id}/wake → `Server.machine(id)` → **404** if unknown →
`wol.BuildMagicPacket(m.MAC)` → **400** if the hand-edited MAC is malformed (validated
*before* any send, so a typo is an actionable error rather than a silent no-op) →
`Prober.Probe` once → already up? return `{woken:true, waitedSeconds:0}` without sending →
`Server.Sender.SendBroadcast(packet)` → **500** on send error →
poll loop: `time.Ticker(3s)` → `Prober.Probe` → up? `{woken:true, waitedSeconds:N}` ·
past the 90s deadline? `{woken:false, waitedSeconds:N}` · request cancelled? return

A timeout is **200 with `woken:false`, not an error status**: the request itself succeeded,
the machine just didn't come back (NIC not armed, packet never reached the segment, host
unplugged).

To change wake timeout / poll cadence: `waker.DefaultWakeTimeout` (90s),
`waker.DefaultPollInterval` (3s), or per-server `Server.WakeTimeout` / `Server.PollInterval`
(what the tests set to milliseconds)

## Magic packet send — directed broadcast

`wol.DirectedSender` (`wol.InterfaceSender`) → `net.Interfaces()` → skip any interface that is
down, loopback, or point-to-point → for each remaining IPv4 `*net.IPNet` address →
`wol.DirectedBroadcast(ip, mask)` = `ip | ^mask` (e.g. `192.168.1.37/24` → `192.168.1.255`) →
`sendFrom(ifaceIP, bcast, packet)` binds a UDP socket **to that interface's own IP** →
`setBroadcast` (SO_BROADCAST) → writes the 102-byte packet to `bcast:9` **and** `bcast:7` →
one log line naming every interface used → no usable interface at all? fall back to
`wol.DefaultSender` (limited broadcast) so a single-homed host still works

**The bug this fixes:** `wol.udpSender.SendBroadcast` (still there, still used by runnerd)
sends to the *limited* broadcast `255.255.255.255` from a socket bound to `0.0.0.0`, so the OS
picks the egress interface from the routing table. On a multi-homed host — Tailscale + Docker
bridges + WiFi all up at once — the packet frequently leaves via the wrong interface, with no
error reported anywhere. The wake just silently never happens. Binding to a specific interface
IP pins the egress; the directed broadcast address makes it a broadcast on *that* segment.

Ports 9 (discard) and 7 (echo) are both used: 9 is the convention, 7 is what some older NIC
firmware still listens on. Two extra 102-byte datagrams per interface, one fewer failure mode.

To change ports: `wol.WakePorts`
To change the interface filter: `wol.InterfaceSender.SendBroadcast` flag checks
To change the broadcast math: `wol.DirectedBroadcast`
To redirect send logging: `wol.InterfaceSender.Logf`
To fake the send in tests: implement `wol.PacketSender` (see `api_test.go`'s `fakeSender`)

## Tests

`go test ./internal/waker/... ./internal/wol/...` — no test sends a real packet or touches the
real network: `fakeSender` records payloads, `fakeProber` scripts up/down by call count,
`DirectedBroadcast` is pure arithmetic, and config tests point `WAKER_HOME` at `t.TempDir()`.

## Deployment on the Pi

Cross-compiles to a single static binary: `GOOS=linux GOARCH=arm64 go build ./cmd/wakerd/`
(Pi Zero 2 W is ARM Cortex-A53 — `arm64` for 64-bit Raspberry Pi OS, `GOARCH=arm GOARM=7` for
the 32-bit image). No cgo, no runtime dependencies. [NOT IMPLEMENTED] there is no installer,
service unit, self-update, or pairing QR for wakerd — the runner's `install.sh` covers runnerd
only; wakerd is copied over and started by hand (or via a systemd unit written by hand) today.

## Technology Notes

- **A WoL magic packet cannot cross a router.** It's an L2 broadcast; routers don't forward
  broadcasts. The Pi must be on the *same broadcast domain* as the machines it wakes. Nothing
  about Tailscale changes this — Tailscale gets the *request* to the Pi, the Pi puts the
  *packet* on the wire. This is the entire reason the daemon exists as a separate device role.
- **WiFi Pi → wired target only works if the router bridges WiFi and LAN into one broadcast
  domain.** Most consumer routers do. Two things break it: **AP isolation** (a.k.a. client
  isolation / guest mode), which blocks station-to-station and broadcast traffic at the AP —
  wakes silently fail with no error anywhere; and a **guest SSID or separate WiFi VLAN**, which
  is a different subnet by design. Symptom in both cases is identical: `woken:false` after the
  full 90s, packet sent successfully. Check the AP's isolation setting first.
- **The target NIC must be armed for WoL and must keep standby power.** Three separate things
  have to be true: BIOS/UEFI "Wake on LAN" / "Power on by PCIe" enabled; the OS driver's
  "Allow this device to wake the computer" left on (Windows resets this after some driver
  updates, and **Fast Startup** puts the machine in a hybrid-shutdown state many NICs won't
  wake from); and the machine on S3/S5 with standby power, not fully unplugged or behind a
  switched power strip. Also: **WoL over WiFi on the target side (WoWLAN) mostly doesn't
  work** — the target should be wired.
- **Config is a hand-edited JSON file with no UI and no validation on write.** `Load()` never
  rewrites an existing file (clobbering a hand-maintained machine list would be
  unrecoverable), so adding a machine means editing the file and restarting the daemon. A
  malformed file fails startup loudly; a malformed *MAC* inside a valid file is only caught at
  wake time, as a 400.
- **Probing is "can I TCP-connect to port 7777", not "is this host alive".** If the runner
  isn't running, or a firewall drops the port, the machine reads as down even while awake — so
  a wake can report `woken:false` on a machine that woke perfectly. Windows Firewall blocking
  inbound connections from other devices is a known live issue for runnerd (see the runner's
  FLOWS.md) and hits this probe the same way. ICMP ping was avoided deliberately: raw sockets
  need privileges the daemon shouldn't hold.
- **State is entirely in the config file; the daemon holds no runtime state.** Machine list is
  read once at startup into `Server.Machines` and never reloaded — restart to pick up edits.
  Nothing is written at runtime, so there's nothing to lose on a crash or power cut.
- **A wake request blocks a goroutine for up to 90 seconds.** That's a long-held HTTP request
  by design (the caller wants to know if it worked), so client-side timeouts must exceed 90s
  or the phone will give up before the answer arrives. Concurrent wakes are fine — each is its
  own goroutine — but there's no dedupe: two wake requests for the same machine both send
  packets and both poll.
- **Default bind is loopback only (`127.0.0.1:7778`).** Nothing reaches it until
  `WAKER_LISTEN_ADDR` is set to the Tailscale interface IP. Never bind `0.0.0.0` — the auth
  key is the only protection, and the daemon's whole purpose is powering machines on.
- **Port 7778 was chosen to sit next to the runner's 7777** so the Pi could in principle run
  both; it has no other significance.

## Change Index

| Thing | Where |
|---|---|
| Config file location | `internal/waker/config.go` (`wakerHome`, env `WAKER_HOME`) |
| Listen address | `internal/waker/config.go` (`Load`, env `WAKER_LISTEN_ADDR`, `DefaultListenAddr`) |
| Key generation (32 random bytes hex) | `internal/waker/config.go` (`generateKey`, `loadOrCreateFile`) |
| Machine list / adding a machine | hand-edit `~/.waker/config.json`; shape in `internal/waker/config.go` (`File`, `Machine`) |
| Probe host/port fallbacks | `internal/waker/config.go` (`Machine.Host`, `Machine.Port`, `DefaultProbePort`) |
| Auth header check | `internal/waker/api.go` (`Server.auth`) |
| HTTP endpoint routing | `internal/waker/api.go` (`Server.Routes`) |
| Reachability probe mechanism | `internal/waker/api.go` (`TCPProber.Probe`), `DefaultProbeTimeout` |
| Wake timeout / poll interval | `internal/waker/api.go` (`Server.WakeTimeout`, `Server.PollInterval`), `internal/waker/config.go` (`DefaultWakeTimeout`, `DefaultPollInterval`) |
| Wake response shape / timeout-is-200 behaviour | `internal/waker/api.go` (`handleWake`, `wakeResponse`) |
| Concurrent probing of the machine list | `internal/waker/api.go` (`handleListMachines`) |
| Magic packet construction | `internal/wol/wol.go` (`BuildMagicPacket`, `ParseMAC`) |
| Directed broadcast address math | `internal/wol/directed.go` (`DirectedBroadcast`) |
| Per-interface send / interface filter | `internal/wol/directed.go` (`InterfaceSender.SendBroadcast`, `sendFrom`) |
| WoL target UDP ports (9 and 7) | `internal/wol/directed.go` (`WakePorts`) |
| Which sender wakerd uses | `internal/waker/api.go` (`NewServer` → `wol.DirectedSender`) |
| Legacy limited-broadcast sender (runnerd's) | `internal/wol/wol.go` (`udpSender`, `DefaultSender`, `broadcastAddr`) |
| SO_BROADCAST socket option | `internal/wol/broadcast_unix.go`, `broadcast_windows.go` (`setBroadcast`) |
| Send logging | `internal/wol/directed.go` (`InterfaceSender.Logf`), `internal/waker/api.go` (`Server.Logf`) |
| Startup logging / first-run key print | `cmd/wakerd/main.go` |
