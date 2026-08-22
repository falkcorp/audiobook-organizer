<!-- file: docs/agent-tasks/todo-completion/server/TASK-204-guard-testserverstartgracefulshutdown-s-sigterm-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0f58441a-4690-478d-aa95-5a157c57684b -->
<!-- last-edited: 2026-08-21 -->

# TASK-204 — Guard TestServerStartGracefulShutdown's SIGTERM against future parallelism (TODO.md L280)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server subagent · **Why:** single-file comment/guard addition, no cross-package reasoning · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 280 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**entire test binary**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-204-guard-testserverstartgracefulshutdown-s-sigterm-" -b agent/server-204-guard-testserverstartgracefulshutdown-s-sigterm- origin/main
cd "$REPO/.worktrees/server-204-guard-testserverstartgracefulshutdown-s-sigterm-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make the process-wide-signal hazard in TestServerStartGracefulShutdown impossible to trip by accident: add a prominent guard comment directly above the syscall.Kill call in internal/server/server_more_test.go warning that this call raises SIGTERM against the ENTIRE test binary process (not a child), so no test in package `server` may ever be marked t.Parallel() while this test exists, and add a package-level doc comment (top of server_more_test.go or a new tiny doc block) stating the same constraint so a reviewer sees it before approving a t.Parallel() addition anywhere in the package.

## Background (verify before editing)

- internal/server/server_more_test.go:337 calls syscall.Kill(os.Getpid(), syscall.SIGTERM) directly against the test binary's own PID to simulate an operator's shutdown signal.
- internal/server/server_lifecycle.go:444 is where Server.Start registers the real signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) handler that this test relies on.
- Go by default runs tests in one package sequentially in the same OS process unless a test calls t.Parallel(); this file has no t.Parallel() call today (verified above), so the risk is latent.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "syscall.Kill(os.Getpid()" internal/server/server_more_test.go   # 1 hit, L337 — TestServerStartGracefulShutdown sends a process-wide SIGTERM
  grep -n "t.Parallel()" internal/server/server_more_test.go   # 0 hits — the trap has not fired yet — no test in the file currently calls t.Parallel()
  grep -n "signal.Notify(quit" internal/server/server_lifecycle.go   # 1 hit, L444 — the real SIGTERM handler is registered in Server.Start via signal.Notify
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open internal/server/server_more_test.go.
2. Immediately above line 337 (`_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)`), add a comment block explaining: this signals the WHOLE test binary process, not a subprocess; if ANY test in package `server` (this file or a sibling _test.go file in the same package) is ever given t.Parallel(), it will receive this SIGTERM mid-run as an unexpected process-wide event, and its outcome becomes undefined depending on what it happens to be doing when the signal lands.
3. Add a second short comment directly on/above `func TestServerStartGracefulShutdown(t *testing.T) {` (line 309) stating: 'Do not add t.Parallel() to this test OR to any other test in package server -- see the comment on the syscall.Kill call below.'
4. Do not add any runtime enforcement (no custom test framework hook) -- this is a documentation-only fix per the TODO's own framing ('a trap for whoever adds parallelism'); a runtime check is out of scope for this item.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_204.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- N/A -- comment-only change, no behavioral edge cases

## Tests

- No new test needed; this is a comment-only change. Confirm the package still compiles and the existing test still passes: go test ./internal/server/... -run TestServerStartGracefulShutdown -count=1 -v

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -n 'SIGTERM' -B3 internal/server/server_more_test.go shows the new warning comment directly above the syscall.Kill call
- [ ] go vet ./internal/server/... exits 0
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_204.md`.

## Commit message

```
feat(server): Guard TestServerStartGracefulShutdown's SIGTERM against futu (TODO L280)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`grep -n 'SIGTERM' -B3 internal/server/server_more_test.go shows the new warning comment directly above the syscall.Kill call`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Paired with todo_line 283 (same test, the fixed 6s sleep) -- both touch server_more_test.go but are independent edits and can land in the same PR without conflicting logically, though they touch overlapping line ranges so should be done as one PR, not two parallel worktrees, to avoid a rebase collision. This is a documentation/comment fix per the TODO's own wording ('it is a trap... it works today'); no behavior change is implied or required.
