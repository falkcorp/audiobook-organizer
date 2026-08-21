<!-- file: docs/agent-tasks/todo-completion/server/TASK-128-fix-enableratelimit-false-not-actually-disabling.md -->
<!-- version: 1.0.0 -->
<!-- guid: 63f59c60-44e6-4a5f-a361-c1abefb97048 -->
<!-- last-edited: 2026-08-21 -->

# TASK-128 — Fix EnableRateLimit=false not actually disabling rate limiting (CFG-AUDIT)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Small, localized fix but touches a security-relevant gate on the HTTP server startup path. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 1317 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**CFG-AUDIT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-128-fix-enableratelimit-false-not-actually-disabling" -b agent/server-128-fix-enableratelimit-false-not-actually-disabling origin/main
cd "$REPO/.worktrees/server-128-fix-enableratelimit-false-not-actually-disabling"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make EnableRateLimit=false actually disable the rate limiter, independent of APIRateLimitPerMinute's value, so the two names mean what they say: EnableRateLimit is the master switch, APIRateLimitPerMinute is the threshold.

## Background (verify before editing)

- internal/server/server_lifecycle.go:1400-1408 currently: `apiRateLimiter := gin.HandlerFunc(func(c *gin.Context) { c.Next() }); if rpm := config.AppConfig.APIRateLimitPerMinute; rpm > 0 { ... apiRateLimiter = servermiddleware.NewIPRateLimiter(rpm, burst).Middleware() }`.
- internal/server/server_lifecycle.go:1417-1419 currently just warns when EnableRateLimit is false, then does nothing else with it.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'rpm := config.AppConfig.APIRateLimitPerMinute' internal/server/server_lifecycle.go   # 1 hit ~L1402 — limiter built only from APIRateLimitPerMinute > 0
  grep -n 'EnableRateLimit' internal/server/server_lifecycle.go   # 1 hit ~L1417 inside an `if !config.AppConfig.EnableRateLimit { slog.Warn(...) }` block, no other reference — EnableRateLimit only produces a warning, never gates
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/server/server_lifecycle.go, change the condition that builds the real limiter (around L1402) from `if rpm := config.AppConfig.APIRateLimitPerMinute; rpm > 0 {` to `if config.AppConfig.EnableRateLimit; rpm := config.AppConfig.APIRateLimitPerMinute; rpm > 0 {` — i.e. require BOTH EnableRateLimit==true AND rpm>0 (Go doesn't support that exact multi-clause `if` syntax; write it as `if config.AppConfig.EnableRateLimit && config.AppConfig.APIRateLimitPerMinute > 0 { rpm := config.AppConfig.APIRateLimitPerMinute; ... }`).
2. Update the WARN block at L1417-1418 to fire only when EnableRateLimit is false AND APIRateLimitPerMinute > 0 (i.e. the user configured a limit but disabled enforcement) so the warning stays meaningful; if EnableRateLimit is false and rpm is also 0/unset, no warning is needed since rate limiting was never going to run anyway.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_128.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- EnableRateLimit=true but APIRateLimitPerMinute=0: should still be unlimited/no-op, since a limiter needs a positive rate — that half of the existing behavior is unchanged.

## Tests

- internal/server/*_test.go (find the existing rate-limit middleware test file via `grep -rl NewIPRateLimiter internal/server/*_test.go`) — add a case: EnableRateLimit=false, APIRateLimitPerMinute=100 → assert requests are NOT rate-limited (the passthrough handler is installed).
- Anti-suppression twin: EnableRateLimit=true, APIRateLimitPerMinute=100 → assert requests ARE rate-limited after the threshold, proving the fix didn't also break the enabled case.

Anti-over-suppression test: `TestRateLimit_EnabledStillLimits (see tests above)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/...` passes including the new cases.
- [ ] `grep -n 'EnableRateLimit &&' internal/server/server_lifecycle.go` returns 1 hit
- [ ] Anti-over-suppression test: `TestRateLimit_EnabledStillLimits (see tests above)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_128.md`.

## Commit message

```
fix(server): Fix EnableRateLimit=false not actually disabling rate limiti (CFG-AUDIT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Part of CFG-AUDIT triage decision #1 from the owner's 2026-08-21 list (item 8 in the decisions is about review_apply_enabled, not this — this sub-item is independently actionable and not blocked by any owner decision).
