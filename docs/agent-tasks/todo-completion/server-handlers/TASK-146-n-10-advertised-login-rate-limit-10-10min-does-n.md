<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-146-n-10-advertised-login-rate-limit-10-10min-does-n.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7bfffcea-e639-4ea2-b952-a10e378f89e3 -->
<!-- last-edited: 2026-08-21 -->

# TASK-146 — N-10: advertised login rate limit (10/10min) does not match the real throttle (15/15min) (ABS-N10)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Why:** Two-constant correction, using already-exported values from absauth — fully mechanical, no design judgment needed since the real values are the source of truth. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 53 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🔌 **ABS coverage gaps N-1 … N-10** (audit:" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-146-n-10-advertised-login-rate-limit-10-10min-does-n" -b agent/server-handlers-146-n-10-advertised-login-rate-limit-10-10min-does-n origin/main
cd "$REPO/.worktrees/server-handlers-146-n-10-advertised-login-rate-limit-10-10min-does-n"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make the advertised /status rate-limit values in dto.go derive from (or exactly match) the real absauth throttle constants, so a client that paces itself to the advertisement is not surprised by the real 429 threshold.

## Background (verify before editing)

- absauth is already imported somewhere in the abs handler package or its wiring — check `grep -rn '"github.com/falkcorp/audiobook-organizer/internal/server/absauth"' internal/server/handlers/abs/*.go` to see whether dto.go can import it directly without a cycle, or whether the values need to be passed in via Handler config at construction time instead.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'RateLimitLoginRequests\|RateLimitLoginWindow' internal/server/handlers/abs/dto.go   # RateLimitLoginRequests: 10, RateLimitLoginWindow: 600000 — advertised limits are 10 requests / 600000ms
  grep -n 'MaxFailuresPerIP\|Window =' internal/server/absauth/throttle.go   # MaxFailuresPerIP = 15, Window = 15 * time.Minute — the real throttle is 15 failures / 15 minutes
  ```

### Reuse — don't invent

- Use `absauth.MaxFailuresPerIP / absauth.Window constants` in `internal/server/absauth/throttle.go` (verify: `grep -n 'MaxFailuresPerIP\|^ Window' internal/server/absauth/throttle.go`) — do NOT write a parallel helper.

## Step-by-step

1. Check for an import cycle risk: `grep -rn 'absauth' internal/server/handlers/abs/*.go` and `grep -n 'handlers/abs' internal/server/absauth/*.go` — if absauth does not import the abs package, dto.go can safely import absauth directly.
2. In internal/server/handlers/abs/dto.go, replace the hardcoded `RateLimitLoginRequests: 10` with `RateLimitLoginRequests: absauth.MaxFailuresPerIP` and `RateLimitLoginWindow: 600000` with `RateLimitLoginWindow: absauth.Window.Milliseconds()`.
3. Add the absauth import if not already present; bump dto.go's version header.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_146.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If step 1 finds an import cycle, fall back to passing the two values into abs.Handler via its constructor/config struct instead of importing absauth directly — do not introduce a cycle to satisfy this cosmetic fix.

## Tests

- internal/server/handlers/abs/abs_test.go: TestStatus_RateLimitMatchesRealThrottle — GET /status, assert RateLimitLoginRequests == absauth.MaxFailuresPerIP and RateLimitLoginWindow == absauth.Window.Milliseconds().

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/handlers/abs/... -run TestStatus_RateLimitMatchesRealThrottle -v` passes.
- [ ] `grep -n 'absauth.MaxFailuresPerIP' internal/server/handlers/abs/dto.go` returns 1 hit.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_146.md`.

## Commit message

```
refactor(server-handlers): N-10: advertised login rate limit (10/10min) does not match  (ABS-N10)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

N/A
