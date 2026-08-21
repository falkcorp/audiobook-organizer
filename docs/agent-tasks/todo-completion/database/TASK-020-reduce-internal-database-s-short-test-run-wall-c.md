<!-- file: docs/agent-tasks/todo-completion/database/TASK-020-reduce-internal-database-s-short-test-run-wall-c.md -->
<!-- version: 1.0.0 -->
<!-- guid: b51ab25c-400c-41ff-b6a0-2abb5a665daf -->
<!-- last-edited: 2026-08-21 -->

# TASK-020 — Reduce internal/database's -short test-run wall-clock cost (currently 200-280s, most of the coverage gate's budget) (TODO.md L238)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** Requires profiling (go test -short -json timing analysis) to find the actual hot spots before optimizing -- not a blind rewrite; genuine performance-analysis judgment across 123 test files. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 238 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Reduce the wait-bound cost while there — 200–280s " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-020-reduce-internal-database-s-short-test-run-wall-c" -b agent/database-020-reduce-internal-database-s-short-test-run-wall-c origin/main
cd "$REPO/.worktrees/database-020-reduce-internal-database-s-short-test-run-wall-c"
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

1. In internal/database/pebble_store_versiongroup_backfill.go, change L98-99 from `LowerBound: []byte("book:0"), UpperBound: []byte("book:;")` to `LowerBound: []byte("book:"), UpperBound: []byte("book;")`.
2. Bump the file's version header (currently 1.2.1) and last-edited date per the mandatory file-header rule.
3. Bump the sentinel from `versionGroupBackfillKey = "system:backfill:versiongroup_index_v2_done"` to a v3 key (`..._v3_done`), following the same v1->v2 bump pattern documented at L23-30, so every deployment (including ones that already completed v2 under the narrower bounds) re-runs once under the wider bounds. This is MANDATORY, not optional — the bound fix has no effect on already-existing letter-leading book IDs unless the one-time gate is forced to re-run.
4. Add or extend a unit test with a synthetic book ID starting with a letter (e.g. 'A01...') stored under `book:A01...`, asserting it is now included by the widened scan and correctly indexed if it has a VersionGroupID.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_020.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Adding t.Parallel() to tests that share an underlying Pebble DB instance without proper key-space isolation could introduce flaky cross-test interference -- verify isolation before parallelizing.

## Tests

- No new test needed -- success is measured by the suite's own wall-clock time; ensure no existing test's correctness assumptions are broken by any added parallelism.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/database/... -run BackfillVersionGroupIndex` passes including the new letter-ID case.
- [ ] `grep -n 'LowerBound: \[\]byte("book:"),' internal/database/pebble_store_versiongroup_backfill.go` confirms the widened bounds are in place.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_024.md`.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_020.md`.

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
