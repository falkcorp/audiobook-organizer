<!-- file: docs/agent-tasks/todo-completion/database/TASK-177-add-a-per-test-deadline-context-withtimeout-to-i.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3994829b-13b0-417d-82c0-e5a1e7c9a036 -->
<!-- last-edited: 2026-08-21 -->

# TASK-177 — Add a per-test deadline (context.WithTimeout) to internal/database's riskiest unbounded-wait test helpers (TODO.md L235)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** Requires judgment about which of the 12 files' specific .Wait()/<-chan/.Lock() call sites are the actually-risky ones worth wrapping with a bound (not a blind sweep, per L235's own edge_case about over-tight timeouts), plus picking a safe bound per call site. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 235 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Consider a per-test deadline (`t.Context()` / `con" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-177-add-a-per-test-deadline-context-withtimeout-to-i" -b agent/database-177-add-a-per-test-deadline-context-withtimeout-to-i origin/main
cd "$REPO/.worktrees/database-177-add-a-per-test-deadline-context-withtimeout-to-i"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each of the 12 files listed in exact_files, wrap the WaitGroup.Wait()/channel-receive/Lock() call(s) that lack any surrounding timeout with a bounded context (t.Context() combined with context.WithTimeout, or an explicit select-with-timer around a channel receive), sized generously above the call's legitimate duration, so a future hang fails in seconds naming the specific test rather than consuming the whole package's -short budget.

## Background (verify before editing)

- embedding_store_chaos_test.go's own comment (verified above) already documents a bare wg.Wait() causing a silent hang with no test failure -- this is direct, in-repo evidence the pattern this item targets is a real, previously-observed failure mode, not a hypothetical.
- This is explicitly independent of L232's blocked root-cause item -- it improves diagnosability for ANY future stall in these 12 files, not just whichever one L229's eventual dump names.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n '^go ' go.mod   # go 1.24.0 (per CLAUDE.md this should be bumped to 1.25/1.26; either way t.Context() is available) — go.mod targets a Go version supporting t.Context() (1.24+)
  grep -rn 't.Context()' internal/database/*.go | wc -l   # 0 — no test in internal/database currently uses t.Context()
  grep -rl '\.Wait()\|<-.*chan\|\.Lock()' internal/database/*_test.go internal/database/dbtest/*.go | sort | wc -l   # 12 — exactly 12 test files contain the unbounded-wait pattern
  grep -n '\.Wait()' internal/database/dataloss_concurrency_test.go   # 1 hit ~L109 — one concrete example -- dataloss_concurrency_test.go has a bare wg.Wait() with no timeout
  grep -n 'a bare wg.Wait() produced NO test failure' internal/database/embedding_store_chaos_test.go   # 1 hit ~L38 — embedding_store_chaos_test.go's own comment documents a bare wg.Wait() having hung before with no test failure
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. For each of the 12 files, read the specific .Wait()/<-chan/.Lock() call site(s) found by the grep and classify: is this a WaitGroup.Wait() for goroutines the test itself spawned (wrap with a timeout via a helper: run wg.Wait() in a goroutine, select on a done channel vs time.After), a blocking channel receive (add a select with a time.After branch), or a Lock() (Pebble/mutex locks rarely need this treatment -- skip unless the file's own comments flag a known contention risk, as embedding_store_chaos_test.go does).
2. Use t.Context() (Go 1.24+, auto-cancelled at the test's own -timeout) as the base context where a context-aware wait exists; otherwise use an explicit `select { case <-done: case <-time.After(30*time.Second): t.Fatal("timed out waiting for ...") }` pattern for raw WaitGroup/channel waits.
3. Pick a bound generously above legitimate slow-path duration -- start at 30s per blocking call (the package's own -short run is 200-280s total across the whole package, so an individual wait bound well under that avoids false-positives) and adjust based on observed CI timing if available.
4. Prioritize embedding_store_chaos_test.go first (it has direct in-repo evidence of a prior silent hang) and dataloss_concurrency_test.go second (concurrency-focused, most likely to contain the real risk), then sweep the remaining 10.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_177.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Overly tight timeouts on legitimately slow (but correct) tests in this list could introduce NEW flakiness -- validate against real run durations before tightening further; a newly-introduced timeout-flake is a signal the bound was set too tight, not that the test is broken.

## Tests

- A synthetic proof for at least one converted call site: temporarily make the underlying condition never satisfied (e.g. don't signal the done channel) and confirm the test now fails within the new bound (seconds) rather than hanging, then revert the synthetic break.

Anti-over-suppression test: `The bound must be generous enough that a genuinely slow-but-passing test does not start failing under normal CI load -- validate against actual historical run times (see L238) before picking a number, and treat any newly-introduced timeout-flake as a signal the bound was set too tight.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/database/... -short -v` completes with no individual test exceeding its new bound; a deliberately-broken wait (temporarily reintroduced for verification, then reverted) fails within the bound rather than stalling.
- [ ] Anti-over-suppression test: `The bound must be generous enough that a genuinely slow-but-passing test does not start failing under normal CI load -- validate against actual historical run times (see L238) before picking a number, and treat any newly-introduced timeout-flake as a signal the bound was set too tight.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_177.md`.

## Commit message

```
feat(database): Add a per-test deadline (context.WithTimeout) to internal/da (TODO L235)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``go test ./internal/database/... -short -v` completes with no individual test exceeding its new bound; a deliberately-broken wait (temporarily reintroduced for verification, then reverted) fails within the bound rather than stalling.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Independent of L232/L238 -- can be done in parallel or first. Corrects the prior scout's assumption that shared blocking helpers live in internal/database/dbtest/ (that directory only contains invariants.go/invariants_fires_test.go, unrelated to this pattern); the real candidates are the 12 _test.go files listed.
