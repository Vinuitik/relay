# Shopping list

Updated 2026-09-26, after measuring wake behaviour on the real hardware. The
"do not buy yet" hold from the earlier draft is **lifted for the networking
items** — they are now the thing blocking the project, and the reason is
measured, not guessed (see `runner/internal/idle/FLOWS.md`, "Per-machine wake
matrix").

## Why this list exists

Wake-on-LAN needs a magic packet delivered on the target's own LAN segment.
Testing today proved:

- wakerd sends correctly, and the router forwards broadcast to WiFi clients.
- **The Acer cannot be woken over WiFi, ever** — its firmware rfkills the
  radio on suspend, so the card is powered but deaf (`iwlwifi: Rfkill was
  toggled during suspend`). Not fixable in software.
- **The Dell's Ethernet is an Intel I219-LM** with `Wake on Magic Packet`
  already enabled — a reliable WoL NIC that wakes from S4.

So both machines need to be **wired** to be wakeable. That's the whole list.

## Buy now — unblocks everything (~£25)

| # | Item | Spec that matters | ~Cost |
|---|---|---|---|
| 1 | Ethernet patch cable ×2 | Cat5e or Cat6, either is fine. Measure the run to the router and add slack — too short is the common mistake. | £10 |
| 2 | Unmanaged Ethernet switch, 5-port | "Unmanaged" / "plug and play". Gigabit. **Not** a "network splitter" — those don't work for this. | £10–12 |

The router has one free Ethernet port and there are two machines to wire, so
the switch is what makes it work: router → switch, then switch → Acer and
switch → Dell. Magic packets cross an unmanaged switch by design (broadcast
frames go to every port), so this does not break WoL.

## Buy after the cables prove the wake works (~£25)

Test with the Dell acting as the waker first — that costs nothing and proves
the whole chain. Only then buy the Pi.

| # | Item | Spec that matters | ~Cost |
|---|---|---|---|
| 3 | Raspberry Pi Zero 2 W | The bare board is fine. **No Ethernet port and it doesn't need one** — it sends over WiFi, the router forwards to the wired side. | £15 |
| 4 | microSD card, 16GB+ | Class 10 / A1. This is the Pi's only storage — the OS lives on it. | £5 |
| 5 | Power supply | **micro-USB, NOT USB-C.** Either a micro-USB PSU (2.5A) or a USB-C→micro-USB cable to reuse an existing charger. | £3–8 |

Alternative to 3–5: a starter kit (Pi Hut / Pimoroni / SB Components, ~£35–50)
bundles board + case + UK PSU + microSD. Small premium, less to get wrong.

Not needed: case, HDMI cable, GPIO header, keyboard, monitor. The Pi is
headless permanently — setup is done via Raspberry Pi Imager's pre-flash
settings and SSH.

## Power draw

The Pi is mains-powered and always on, ~1W (~£2.30/year). It is not a battery
device; there is nothing to charge. Solar/battery was considered and rejected
— no payback period closes at that cost.

## Not a purchase, but required

- `sudo ethtool -s enp7s0 wol g` on the Acer once cabled, made persistent via
  `nmcli con mod "<name>" 802-3-ethernet.wake-on-lan magic`. It reverts to off
  on reboot otherwise, silently.
- Check the Dell appears in `powercfg /devicequery wake_armed` once its cable
  is in. It currently lists only the WWAN modem.
