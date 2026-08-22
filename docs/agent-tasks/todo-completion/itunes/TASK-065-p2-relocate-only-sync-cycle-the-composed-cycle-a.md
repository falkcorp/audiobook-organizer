<!-- file: docs/agent-tasks/todo-completion/itunes/TASK-065-p2-relocate-only-sync-cycle-the-composed-cycle-a.md -->
<!-- version: 1.0.0 -->
<!-- guid: 395dcec6-3108-41c1-a1f8-1dc00e5ea7db -->
<!-- last-edited: 2026-08-21 -->

# TASK-065 — P2 relocate-only sync cycle — the composed cycle already exists (RunRelocateSyncCycle); wire it to a caller and add an end-to-end test (TODO.md L10390)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · itunes subagent · **Why:** The hard/dangerous part (the composition, guard wiring, quiescence, oracle) is already built and reviewed-in; what remains is wiring a real write path to prod data plus an end-to-end test, which still needs careful review given the review_critical blast radius even though the diff is smaller than building the cycle from scratch · **Depends on:** none · **External blockers:** TODO.md L10383 (prod_run) — not a task in this package; coordinator confirms it is resolved or explicitly waives it before dispatch · **Wave:** 6 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10390 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**iTunes 2-way-sync P2 — relocate-only sync cycle " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-065-p2-relocate-only-sync-cycle-the-composed-cycle-a" -b agent/itunes-065-p2-relocate-only-sync-cycle-the-composed-cycle-a origin/main
cd "$REPO/.worktrees/itunes-065-p2-relocate-only-sync-cycle-the-composed-cycle-a"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Do NOT rebuild the relocate-only sync cycle — RunRelocateSyncCycle already implements it faithfully to the design. The remaining work is: (1) an end-to-end test that actually calls RunRelocateSyncCycle against a fixture library and asserts a full dry-run cycle completes with the expected SyncCycleResult; (2) an operational entry point (an HTTP handler or a maintenance op, following the existing itunes-sync handler patterns in internal/server) so an operator can actually invoke it, wired with cfg.Apply defaulting to false per this repo's standing 'apply=false default, owner runs apply' rule.

## Background (verify before editing)

- relocate_sync_cycle.go's own top-of-file comment already documents the 5-step PLAN/GUARD/VERIFY/QUIESCE/COMMIT design matching the TODO item's steps almost word for word, plus explicit SAFETY notes on single-flight locking and quiescence.
- Every prerequisite primitive the item lists as merged (LibrarySet #2040, cleanup census P3 no-op, cross-type+preservation proofs, VerifyRelocateWrite #2043, RefreshLibraryIdentity+PartitionedTrackCount #2044, AllowedWritebackRoot #2045) is referenced directly in relocate_sync_cycle.go's own doc comment, confirming the composition really does draw on all of them as designed.
- internal/server/itl_rebuild.go and internal/server/itl_cleanup.go are the existing sibling handlers for other iTunes operations — the new entry point should follow their registration pattern (see server_lifecycle.go's itunesGroup.POST(...) registrations, e.g. line ~1563 for rebuild-full).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func RunRelocateSyncCycle' internal/itunes/relocate_sync_cycle.go   # 1 hit ~L114 — RunRelocateSyncCycle already composes PLAN/GUARD/VERIFY/QUIESCE/COMMIT exactly as the item describes
  grep -rln 'RunRelocateSyncCycle' internal/   # 1 file: internal/itunes/relocate_sync_cycle.go itself — it has no caller anywhere in the codebase outside its own file
  grep -n '^func Test' internal/itunes/relocate_sync_cycle_test.go   # 4 hits, none named RunRelocateSyncCycle — its own test file does not directly test RunRelocateSyncCycle, only its helpers
  ```

### Reuse — don't invent

- Use `RunRelocateSyncCycle + SyncCycleConfig (do not rebuild — wire and test only)` in `internal/itunes/relocate_sync_cycle.go` (verify: `grep -n 'type SyncCycleConfig struct' internal/itunes/relocate_sync_cycle.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/itunes/relocate_sync_cycle.go end to end (all ~330 lines) including its PRE-APPLY VERIFICATION warning comment (~L34-38) before wiring anything — the warning explicitly says do not set Apply=true until a manual dry-run has confirmed the relocate targets match the AO library's real .itunes-writeback/iTunes Media/ root.
2. Create internal/server/itl_sync_cycle.go implementing a handler (e.g. syncCycleHandler) modeled on itl_cleanup.go's structure: resolve the ITL write path, build a SyncCycleConfig with AllowedWritebackRoot wired from the AO library's own LibrarySet media root (per the item's explicit instruction), Apply defaulting to false, call itunes.RunRelocateSyncCycle, and return the SyncCycleResult as JSON.
3. Register the new route in internal/server/server_lifecycle.go's itunesGroup alongside the existing rebuild-full registration (~L1563), e.g. `itunesGroup.POST("/sync-cycle", s.perm(auth.PermLibraryEditMetadata), s.syncCycleHandler)`.
4. Add internal/itunes/relocate_sync_cycle_e2e_test.go: TestRunRelocateSyncCycle_DryRun_ComputesPlanWithoutWriting — build a small fixture .itl + a matching book_file store, call RunRelocateSyncCycle with Apply=false, and assert the returned plan/result matches expectations and the fixture .itl file is byte-identical on disk afterward (dry-run never writes).
5. Bump version headers on all new/touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_itunes_065.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The PRE-APPLY VERIFICATION warning in the source must be respected operationally, not just in code — the new handler's default MUST be Apply=false and any UI/CLI surface added on top of it should require an explicit, separate confirmation step before ever setting Apply=true against prod, consistent with this repo's standing apply=false-default rule.

## Tests

- internal/itunes/relocate_sync_cycle_e2e_test.go: TestRunRelocateSyncCycle_DryRun_ComputesPlanWithoutWriting (see above).
- internal/itunes/relocate_sync_cycle_e2e_test.go: TestRunRelocateSyncCycle_Apply_WritesAndVerifies — with Apply=true against a fixture, assert the write happens, VerifyRelocateWrite passes, and a subsequent read of the .itl reflects the relocated paths.
- internal/server/itl_sync_cycle_test.go: TestSyncCycleHandler_DefaultsApplyFalse — POST with no apply param and assert cfg.Apply was false (no write occurred).

Anti-over-suppression test: `N/A — not a filter/guard addition` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/itunes/... ./internal/server/... -run 'SyncCycle'` passes
- [ ] `make ci` passes
- [ ] Anti-over-suppression test: `N/A — not a filter/guard addition` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/itunes/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_itunes_065.md`.

## Commit message

```
feat(itunes): P2 relocate-only sync cycle — the composed cycle already exi (TODO L10390)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``go test ./internal/itunes/... ./internal/server/... -run 'SyncCycle'` passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Depends conceptually on L10383's byte-preservation proof being run first (the PRE-APPLY VERIFICATION comment in relocate_sync_cycle.go effectively says the same thing) before this is ever pointed at prod with Apply=true.
