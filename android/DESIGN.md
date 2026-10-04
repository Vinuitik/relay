# Relay design spec

Merged from four advisory passes (2026-10-03): flow (impeccable, Operate mode), visual system
(material-3 + ui-ux-pro-max), motion (Emil Kowalski / improve-animations), aesthetic + logo
(frontend-design). Where they contradicted, one side was kept and the other dropped - noted under
"Dropped". This file is the source of truth for UI decisions; FLOWS.md describes behaviour.

## Principles (the rules everything below follows)

- **Operate mode** - the tool disappears into the task; standard Android patterns beat invented ones.
- **Visibility of status** - runner reachability and session state are visible *before* tapping in.
- **Recognition over recall** - every row says what it is (a session is its first message, not "claude").
- **Fewest taps for frequent jobs** - smart defaults instead of dialogs; restore where the user was.
- **One primary action per screen**; destructive actions are red, separated, and confirmed.
- **Colour is never the only signal** - every status has a lamp shape (lit/dark) + a word or icon.
- **Amber means "needs you"** and nothing else.
- **Frequency rule for motion** - the more often it happens, the less it animates. UI motion < 300ms,
  ease-out on enter/exit, exits faster than enters, nothing pops in from scale 0.
- **Tokens, never literals** - colours, type, shapes, spacing and motion come from `ui/theme/`.

## Aesthetic: "Signal Box"

Railway interlocking panel: flat enamel, lamps lit or dark, decisive. Not a hacker terminal, not
AI glow, no skeuomorphic texture. Personality lives only in: launcher icon, app-bar wordmark,
status lamps, empty states, blocking cards. Chat, lists, dialogs and fields stay plain M3.

Signature details:
1. **Status lamps** - 10dp disc with a 1.5dp bezel; lit = filled, dark = bezel only.
2. **Interlock bar** - 4dp amber leading edge on cards that hold an agent until you act
   (permission, quota, login expired). Nothing else gets it.
3. **Wordmark** - "Relay" in Barlow Semi Condensed SemiBold + a lamp that turns amber when any
   session is waiting on you.
4. **Empty states** show the open-contact drawing + one line telling you what to do.

## Colour

Seed **#4F6BD8** (muted indigo, TonalSpot) - cool, so it never competes with the status hues.
Dynamic (wallpaper) colour **off**: status recognition beats personalisation in a monitoring tool.
Dark stays the default (the user chose it in f49fceb); the light scheme is defined so a
System/Light/Dark toggle can come later at no design cost.

| Role | Light | Dark |
|---|---|---|
| primary / onPrimary | #4E5B92 / #FFFFFF | #B8C4FF / #1F2D61 |
| primaryContainer / on | #DDE1FF / #374379 | #374379 / #DDE1FF |
| secondary / on | #5A5D72 / #FFFFFF | #C2C5DD / #2C2F42 |
| secondaryContainer / on | #DEE1F9 / #424659 | #424659 / #DEE1F9 |
| tertiary / on | #75546F / #FFFFFF | #E4BAD9 / #43273F |
| tertiaryContainer / on | #FFD7F4 / #5C3D56 | #5C3D56 / #FFD7F4 |
| error / on | #BA1A1A / #FFFFFF | #FFB4AB / #690005 |
| errorContainer / on | #FFDAD6 / #93000A | #93000A / #FFDAD6 |
| surface / onSurface | #FBF8FF / #1A1B21 | #121318 / #E3E1E9 |
| onSurfaceVariant | #45464F | #C6C5D0 |
| surfaceContainer Lowest/Low/-/High/Highest | #FFFFFF/#F4F2FA/#EFEDF4/#E9E7EF/#E3E1E9 | #0D0E13/#1A1B21/#1F1F25/#292A2F/#34343A |
| outline / outlineVariant | #767680 / #C6C5D0 | #90909A / #45464F |
| inverseSurface / inverseOnSurface / inversePrimary | #2F3036 / #F2F0F7 / #B8C4FF | #E3E1E9 / #2F3036 / #4E5B92 |

Status colours (`RelayStatusColors`, a CompositionLocal; fg = lamp/icon/text, chip = bg/on):

| Status | Meaning | Light fg · chip bg/on | Dark fg · chip bg/on |
|---|---|---|---|
| success | container running, turn done | #006C50 · #7FF9CB/#002116 | #61DCB0 · #00513C/#7FF9CB |
| warning (amber) | waiting for you | #895200 · #FFDCBC/#2C1700 | #FFB86A · #683D00/#FFDCBC |
| busy | agent working | primary · primaryContainer | primary · primaryContainer |
| idle | stopped / dormant | onSurfaceVariant · surfaceContainerHighest | same |
| error | failed, quota, login | error · errorContainer | same |

All pairs ≥ 4.5:1 (computed with material-color-utilities). `Color.White` on status colours is banned.

## Type

- UI text: Roboto (system). Titles + wordmark: **Barlow Semi Condensed** SemiBold (bundled).
- Code: **JetBrains Mono** Regular/Medium, bundled in `res/font` (downloadable fonts flicker in chat).
- Mapping: titleLarge = app bar · titleMedium = list headline/card title · titleSmall = section
  label · bodyLarge = chat text · bodyMedium = supporting text · bodySmall = metadata ·
  labelLarge = buttons · labelMedium = chips · labelSmall (11sp) = timestamps, the floor.
- `codeSmall` 12/16 = tool rows, file sizes/paths · `codeMedium` 13/20 = code blocks, commands.

## Shape & spacing

- Shapes: extraSmall 4 · small 8 · medium 12 (cards, bubbles) · large 16 · extraLarge 28 (sheets,
  dialogs) · full (buttons, chips). No inline `RoundedCornerShape`.
- Spacing: 4 / 8 / 12 / 16 / 24 / 32 only. Gutter 16, card padding 16, between cards 12, inline 8.
  Chat: 8 between messages, 4 between consecutive tool rows. Touch targets ≥ 48dp.

## Navigation & flow

```
Launch → restore last route ──────────────────────────────┐
Home  [runner switcher ▾ + online lamp]                    ▼
  ├─ "Needs you": waiting/busy sessions on this runner (waiting first) → Chat
  └─ Projects: name · last activity · containers lamp · ⋮ (New chat, Files, Containers, Up/Down)
        → Project  [title = project name; tabs: Chats | Files | Containers]
              Chats → Chat [title = first user message; subtitle = project · mode]
Runner switcher ▾ → Manage runners (pair via QR, rename, Sleep, Remove - Sleep/Remove in ⋮, confirmed)
Push → deep link relay://r/{host}/p/{project}/s/{session} → Chat with the permission card showing
```

Tap counts:

| Job | Now | After |
|---|---|---|
| Resume last chat | 3 + guessing | 0 (restored) or 1 (Needs you) |
| New chat in a project | 4 | 2 (project → New chat, last-used provider) |
| Approve from a push | 5 + searching | 2 (notification → Allow) |
| Containers up | 3 | 2 (row ⋮ → Up) |

Exactly one runner → the switcher is just a title, no runner screen ever shows.

## Screens

**Home** - projects load first, container lamps fill in per row afterwards (never block the list).
FAB = ExtendedFAB "Add project" → bottom sheet: Register existing folder / New empty project.

**Project › Chats** - ExtendedFAB "New chat" creates immediately with the last-used provider
(long-press for codex). Rows: first user message · relative time + last agent line · status chip;
waiting first, then most recent. Empty: inline composer "What should the agent do?" - the first
message creates the session.

**Project › Containers** - status card on top; one compose file → single card, several → one card
each (Up = Button, Switch here = FilledTonalButton, Down = OutlinedButton); Services list; at the
very bottom, separated, "Take everything down" (outlined, error colour, confirm dialog).
Empty: one sentence + the expected filename.

**Chat**
- Assistant text: no bubble, plain on surface. User: `primaryContainer` bubble, right-aligned.
- Tool rows: `codeSmall`, onSurfaceVariant, Material icons (check / error / progress) not glyphs.
- Permission card: **sticky above the composer** (always in thumb reach, visible when scrolled up),
  warning chip colours + interlock bar; Allow = Button, Deny = OutlinedButton in error.
- Quota/login cards: errorContainer + interlock bar (already exist as `ProblemCard`).
- Composer (VS Code Claude/Copilot style): one rounded `surfaceContainerHigh` box; text grows to 8
  lines then scrolls inside; toolbar row below = model/effort chip left, Send/Stop right.
  Failed send restores the text + Snackbar "Retry". Stop stays in the Send slot.
- Overflow: Files (check the agent's edits without leaving the chat).

**Manage runners** - QR pairing is primary; name prefilled with hostname, editable inline.

## States

- Loading: skeleton rows on first load only. Refresh keeps content + thin progress bar under the
  top bar. Returning to a screen never blanks it (state lives in a ViewModel per back-stack entry).
- Pull-to-refresh on every list.
- Errors: full-screen only when there's no data - icon, plain cause (`friendlyErrorMessage`), likely
  fix ("Is Tailscale on? Is the runner awake?"), Retry. With stale data: keep it, Snackbar + Retry.
  No "Error:" prefix anywhere.
- Unreachable runner: projects greyed + banner "Runner offline · last seen 2h ago".
- Long operations: status line with elapsed time, so slow is distinguishable from stuck.

## Motion

Tokens (`ui/theme/Motion.kt`): `EaseOut = CubicBezier(0.23, 1, 0.32, 1)`,
`EaseInOut = CubicBezier(0.77, 0, 0.175, 1)`; short 150 · medium 220 · screen 280 ms.

| What | Spec |
|---|---|
| Forward navigation | slide in 10% width + fade (fade 200ms, 60ms delay), 280ms EaseOut; old screen fades out in 100ms |
| Back | mirror, 240/200ms - back is faster |
| Tabs (Chats/Files/Containers) | crossfade 150ms, no slide |
| QR scan | fade only, 200ms |
| Chat items | keyed by index; only items appended after first load enter (8dp up + fade, 180ms) |
| Streaming text | not animated; follow-bottom only if the user was already at the bottom (non-animated stick while streaming; one animated scroll per new item; "↓ New" pill if scrolled up) |
| Send | optimistic user bubble at 60% alpha → 100% when the runner echoes it |
| Busy bar | fixed 4dp slot, alpha 0↔1 over 200ms - no layout jump |
| Send ↔ Stop | crossfade + scale 0.9→1 in a fixed 48dp box, 150/100ms |
| Permission card | fade + slide up 1/6 + expand, 220ms; exit fade 120 + shrink 150; one haptic on appear |
| Container status | colours `animateColorAsState` 250ms; title AnimatedContent fade 150/100; button crossfade 150 |
| Press | scale 0.97 on custom clickables (100ms in, 160ms out); M3 buttons keep ripple |
| Haptics | light tick on Send; LongPress on Stop, Allow/Deny, Take everything down; nothing on navigation or polls |

Don'ts: no animated scroll per poll tick, no typewriter/shimmer on streamed text, no stagger on
list loads or back navigation, no bounce, no ease-in, nothing over 300ms, no re-triggered enter
animations from polling.

## Logo: "Thrown contact"

A relay contact mid-throw: lead → pivot → lever swinging toward a second contact, lamp lit above -
signal received, circuit closing. Background: deep indigo **#1F2D61** (primary, dark onPrimary);
lines bone **#EDE8DC**; lamp amber **#FFB86A** (warning fg). Within the 66dp safe zone.

```
lead     M28,66 H42             stroke 7, round caps
lever    M42,66 L70,50          stroke 7, round caps
contact  M70,66 H80             stroke 7, round caps
pivot    circle r5.5 at (42,66) filled bone
lamp     circle r6 at (70,36)   filled amber
```
Monochrome variant (Android 13 themed icons): same paths, all #FFFFFF. Closed-lever variant
(`M42,66 L70,66`) for empty states/splash = connected.

## Dropped (contradicted the merged set)

- Enamel green seed #2F5D50 (aesthetic pass) - collides with the success-green status hue; the
  indigo seed keeps brand and status apart. Signal Box survives as lamps, amber, logo, wordmark.
- Follow-system theme by default (visual pass) - user chose dark in f49fceb; light scheme kept
  ready for a later toggle.
- Permission card as a keyed list item (motion pass) - replaced by sticky-above-composer (flow
  pass); its enter/exit spec carries over.
- Containers chip as a button on project rows + Containers row on the session list - replaced by
  the Project tabs + row ⋮ menu; the row keeps a status lamp only.
- IBM Plex Sans body font - Roboto + Barlow titles is enough personality.
- "Track line" hierarchy rule - Home merges runners into a switcher, so there's no tree to draw.
