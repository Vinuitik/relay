# Handoff: move chats between machines (laptop ↔ server)

Start a fresh chat on Sonnet. Read this, then `runner/FLOWS.md` § "Shared chats" and
`runner/internal/session/shared.go`. Don't read more up front.

## Goal
A Claude chat lives on exactly one machine at a time. Its owner is wherever its transcript file sits
in `~/.claude/projects/<encoded cwd>/<id>.jsonl`. Moving the file = moving ownership. If a machine
can't see the file, it can't write to it, so we need no lock files and no expiry timers.
Same machine (Relay + VS Code on the laptop) keeps sharing the file directly, as it does today.

## Decided (don't re-ask)
- **Owner = file location.** Never move a chat while a turn is running.
- **Out:** opening a laptop chat on the phone means "I'm leaving". Laptop busy → wait for the turn
  to finish, then move to the server. Idle → move now.
- **Back:** the laptop runner pulls home any of its chats that have been idle on the server for 30
  min or more (the laptop asks, because it's the machine that sleeps). Make the 30 min configurable.
- **Transport:** runner-to-runner over Tailscale. The phone hands each runner the other's address
  and key once, at pairing time.
- **Old copy** goes outside Claude's folder (`~/.relay/handed-off/`) so VS Code can't see it.
  "Take over from last copy" for when the owner is asleep; the phone warns that newer turns are lost.
- **Project matching:** by git remote URL. Fallback: link the two projects manually on the phone.
- **Path rewrite:** the encoded folder name differs between Windows and Linux. Put the file in the
  folder for the target's project path. Check whether the `cwd` fields inside the jsonl matter for
  `session/load` (test it live before writing any rewrite code).
- **VS Code tracking via Claude Code hooks.** Relay installs them into `~/.claude/settings.json`:
  - `UserPromptSubmit` → busy. It also **blocks** the prompt if the chat has moved away ("this chat
    is on the server, reopen from history"). This fixes stale VS Code tabs.
  - `Stop` → idle (safe to move).
  - `Notification` → waiting for the user.
  - Each hook writes state to a small file the runner reads. The phone shows a health check:
    "VS Code tracking: OK / missing".
- **Machines:** laptop + server only.

## Stages (commit after each)
1. **Move logic:** project matching, runner-to-runner transfer endpoint, file move + handed-off
   copy, phone "Continue on server", auto-return after 30 min idle, take-over-from-copy.
2. **Hooks:** installer, state file, stale-tab block in `UserPromptSubmit`, health check on the
   phone, "wait for `Stop`, then move".
3. **Answer from the phone (important):** permission prompts in VS Code go to the phone and get
   answered there. Use a permission hook that holds the prompt, calls the runner, sends a push, and
   returns allow/deny. Verify the exact hook event name and its timeout limit first. Plain-text
   questions need nothing new: they end the turn, `Stop` fires, and the chat moves to the phone.
   Plus push notifications for "VS Code is waiting on you" and "chat moved".

## Constraints (this burned the allowance before)
- About 15 commands per message at most, then stop and report.
- Pipe build/test output through `tail`/`grep` for errors; never paste full Gradle/Docker logs.
- Update FLOWS.md once at the end of each stage, not after every edit.
- New chat per stage if context passes about 100k tokens.
