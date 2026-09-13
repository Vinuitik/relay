# Shopping list

**Do not buy yet** — per ARCHITECTURE.md's rollout order, build the app + runner first and
measure real resource usage on the laptop/server before finalizing hardware. This list is
provisional pending those numbers.

- **Relay device:** Raspberry Pi Zero 2 W (~£14–16) — or a preassembled starter kit (Pi Hut /
  Pimoroni / SB Components, ~£35–50) bundling case + UK power supply + microSD + cable, if a
  small premium for convenience is fine (it is, per user).
- **If buying the bare board instead of a kit:**
  - MicroSD card, 8GB+ (~£5–8)
  - USB-C-to-micro-USB cable (~£3–5) — reuse an existing USB-C charger, the Zero 2 W's power
    port is micro-USB, not USB-C, and none of the household chargers are micro-USB.
- No case, HDMI cable, or GPIO header needed for this project's purposes (headless, no display,
  no GPIO use).

## Open question this list depends on

Real CPU/RAM/network usage from running the runner + Tailscale on the laptop/server during
testing may confirm the Pi Zero 2 W is right-sized, or reveal it needs revisiting (unlikely to
go cheaper — it's already the floor for "runs Linux + Tailscale" — but could reveal a need for
more headroom).
