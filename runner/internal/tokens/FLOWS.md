# Token usage (where did my plan go?)

Files: tokens.go, tokens_test.go (wired in cmd/runnerd/main.go `srv.Tokens`, served by api/tokens.go `handleTokens`)

Reads Claude Code's own transcripts on **this machine** and turns them into token numbers for the
phone's Tokens screen. Read-only: never writes to Claude's files, never calls Anthropic.

## Report flow

```
phone Tokens screen → GET /v1/tokens?days=N (default 7, max 90) → api.handleTokens
  → tokens.Scanner.Report(days)
  → files(): glob <claude dir>/projects/*/*.jsonl            (chats)
             + <claude dir>/projects/*/*/subagents/*.jsonl    (subagents, folded into their chat)
     → skip files older than the window (mtime) → parseFile() only if size/mtime changed (cache)
  → parseFile(): per transcript line
       assistant + usage → one record per message.id (max of each counter; early lines are partial)
       assistant tool_use blocks → tool / shell (Bash, PowerShell) counts
       user line the human typed (not a tool_result) → prompts++, first one = chat title
       cwd → project name (last folder; a .claude/worktrees/<x> path counts as its parent project)
  → aggregate: totals, byDay (local time, every day present), byModel, byProject, sessions (top 40)
  → flagsFor(chat) → Flagged = everything inside flagged chats
```

`<claude dir>` = `session.ClaudeDir()` ($CLAUDE_CONFIG_DIR, else ~/.claude) — the same place
`session/subagents.go` and `handoff` read. Subagent transcripts are added to the parent chat's
numbers (`subagentCalls`), so a chat's cost includes the agents it spawned.

## What the numbers mean

- **Counters:** `input` (uncached), `output`, `cacheWrite`, `cacheRead`. Every tool call is a new API
  call that re-reads the whole chat as `cacheRead` — that is why long chains of small calls cost
  roughly calls × chat length.
- **Units** = (input + 1.25·cacheWrite + 0.1·cacheRead + 5·output) × model factor
  (opus 5/3, sonnet 1, haiku 1/3 — list-price ratios). **An estimate for comparing chats and days,
  not your exact share of the plan limit** (Anthropic does not publish the plan's accounting).
- **Flags (the "wasted" heuristics):** `long-chain` ≥ 40 calls and ≥ 25 calls per typed prompt ·
  `huge-context` biggest call ≥ 200k tokens · `many-shell` ≥ 60 Bash/PowerShell calls ·
  `opus-mechanical` Opus, ≥ 20 calls, ≥ 60% of tool calls are shell.
  To change any threshold: the consts at the top of `tokens.go`.

To change the cost weights / model factors: `weight*` consts, `modelFactor()`.
To change the session cap: `MaxSessions`. Window cap: `MaxDays`.
To change what counts as a typed prompt / title: `userText()`, `fileData.consume()`.
To add a flag: a `Flag*` const + `flagsFor()` + the phone's `FLAG_HELP` (TokensScreen.kt).

## Technology Notes

- **Source of truth is Claude's transcripts, not Anthropic's billing.** If Claude Code changes the
  transcript format (field names `message.id`, `message.usage`, `isSidechain`, the `subagents/`
  layout), counts silently go to zero or low — `tokens_test.go` pins the current format.
- **Per-machine.** Only chats whose transcript lives on this runner appear; a chat run on the Dell
  does not show on the Acer's screen. Headless `claude -p` jobs (e.g. Obsidian Optimizer's
  host-wrapper) *are* included — they write transcripts like any chat.
- **In-memory parse cache** (`Scanner.cache`, keyed by path, invalidated on size or mtime change):
  lost on runner restart, single process, guarded by one mutex. First call after a restart re-parses
  everything in the window (~1 s for ~900 calls / 2 days on the Acer); a growing active chat is
  re-parsed whole on each change. Memory is a few KB per chat (compact records, not transcripts).
- **Deleted transcripts vanish from history** (Claude prunes old chats; `cleanupPeriodDays`).
- **Day buckets use the runner's local timezone** (`now.Location()`).
- **Dedupe by `message.id`:** a missing/empty id drops that line from the numbers.
- **Subscription vs API:** a flat plan hides the real per-token price; units are relative only.
- **Large lines:** transcripts can contain big tool outputs; lines are streamed with a 1 MiB buffer
  (`bufio.Reader.ReadBytes`, no line-size cap), so memory peaks at the largest single line.

## Change Index

| Thing | Where |
|---|---|
| Endpoint / `days` default and clamp | `internal/api/tokens.go` `handleTokens`; `tokens.MaxDays` |
| Route registration | `internal/api/api.go` (`GET /v1/tokens`) |
| Wiring / claude dir | `cmd/runnerd/main.go` (`srv.Tokens = tokens.New(session.ClaudeDir)`) |
| Flag thresholds ("wasted") | `tokens.go` consts `LongChainCalls` … `OpusMechanicsMinCall`, `flagsFor()` |
| Cost weights / model factor | `tokens.go` `weightCacheWrite/Read/Output`, `modelFactor()` |
| Session list size | `tokens.MaxSessions` |
| Project naming / worktrees | `tokens.go` `lastSegment()` |
| Parse cache | `Scanner.files()`, `Scanner.cache` |
| Phone screen | `android/.../ui/screens/TokensScreen.kt` (see android/FLOWS.md "Tokens") |
