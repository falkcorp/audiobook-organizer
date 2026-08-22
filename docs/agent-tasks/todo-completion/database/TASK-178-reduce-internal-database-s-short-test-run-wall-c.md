<!-- file: docs/agent-tasks/todo-completion/database/TASK-178-reduce-internal-database-s-short-test-run-wall-c.md -->
<!-- version: 1.0.0 -->
<!-- guid: a03c3955-fa5f-4166-9b81-469a9a565d5c -->
<!-- last-edited: 2026-08-21 -->

# TASK-178 — Reduce internal/database's -short test-run wall-clock cost (currently 200-280s, most of the coverage gate's budget) (TODO.md L238)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** Requires profiling (go test -short -json timing analysis) to find the actual hot spots before optimizing -- not a blind rewrite; genuine performance-analysis judgment across 123 test files. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 238 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Reduce the wait-bound cost while there — 200–280s " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-178-reduce-internal-database-s-short-test-run-wall-c" -b agent/database-178-reduce-internal-database-s-short-test-run-wall-c origin/main
cd "$REPO/.worktrees/database-178-reduce-internal-database-s-short-test-run-wall-c"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Profile internal/database's -short test suite to find what actually consumes the bulk of its 200-280s wall-clock time, and cut it meaningfully. Given no TestMain currently exists (confirmed above), the highest-leverage fix is likely hoisting duplicated expensive per-test Pebble-store setup into a shared fixture (introducing a TestMain or package-level helper) if profiling confirms that is the actual hot path -- but this must be measured first, not assumed.

## Background (verify before editing)

- Cutting this cost benefits every future CI run, independent of whether L232's stuck-test root cause is ever found.
- The absence of any TestMain across 123 test files in this package is itself suggestive: per-test setup/teardown that could be shared may instead be duplicated 123 times, though this must be confirmed by timing data, not assumed from file count alone.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'coverage-check-short\|test-short:' Makefile   # ≥1 hit each — coverage-check-short and test-short targets exist and this package's run time counts against them
  ls internal/database/*_test.go | wc -l   # 123 — internal/database has 123 test files -- large surface, profiling needed before picking a fix
  grep -rln 'func TestMain' internal/database/*.go   # 0 hits — no TestMain exists to hoist shared setup into
  ```

### Reuse — don't invent

- Use `existing coverage-gate Makefile target this run time counts against` in `Makefile` (verify: `grep -n 'coverage-check-short\|coverage-gate' Makefile`) — do NOT write a parallel helper.

## Step-by-step

1. Run `go test ./internal/database/... -short -v -json > /tmp/db_test_timing.json` and parse per-test durations (via `go tool test2json` output or a small script) to rank the slowest individual tests.
2. For the top offenders, check whether they share expensive per-test setup (e.g. a full Pebble DB open/close per test, as suggested by pebble_store_test.go's naming and size) that could be hoisted to a shared package-level fixture with per-test key-space isolation.
3. Check whether independent tests are unnecessarily serialized (missing `t.Parallel()`) where parallelization would be safe -- verify no shared mutable global/package-level state before adding it.
4. Apply the highest-leverage fix(es) found (likely landing primarily in pebble_store_test.go given its central role, but confirm against the actual profiling output rather than assuming) and re-measure.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_178.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Adding t.Parallel() to tests that share an underlying Pebble DB instance without proper key-space isolation could introduce flaky cross-test interference -- verify isolation before parallelizing.

## Tests

- No new test needed -- success is measured by the suite's own wall-clock time; ensure no existing test's correctness assumptions are broken by any added parallelism.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `time go test ./internal/database/... -short` shows a measurably lower wall-clock time than the 200-280s baseline, with before/after numbers recorded in the PR description.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_178.md`.

## Commit message

```
refactor(database): Reduce internal/database's -short test-run wall-clock cost ( (TODO L238)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

exact_files lists pebble_store_test.go as the most likely landing site based on file-count/naming evidence, but the actual fix location must be confirmed by the step-1 profiling pass before editing -- this is a best-effort placeholder for the collision matrix, not a guarantee of where the diff lands. Independent of L229/L232.
