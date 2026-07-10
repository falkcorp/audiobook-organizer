<!-- file: docs/agent-tasks/torrent-relocation/TASK-07-undo-repoint-closure.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e71a077-8fcd-49f0-a768-93e27b9244b4 -->
<!-- last-edited: 2026-07-10 -->

# TASK-07 — Close the deferred undo re-point item: mode-matrix cells in the EXISTING suites (INIT-5 T7)

**Gate:** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's behavior until the T2 spike is human-approved.
**File-ownership:** none cross-initiative. Within INIT-5: extends `internal/server/deluge_integration_test.go` + `internal/server/deluge_centralization_test.go` (no other task touches them); depends on TASK-03's merged changes to `internal/deluge/integration.go` but does not edit that file. Runs parallel to TASK-04/TASK-05 (disjoint files).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · test-coverage subagent · **Why:** tests-only against freshly-merged mode routing; needs careful mock/config setup but zero production-code risk · **Depends on:** TASK-03

**Dispatch-readiness (coordinator):** BLOCKED until TASK-03's PR is merged to `origin/main`.
Verified 2026-07-10 at HEAD `fce58498`: `grep -rn 'TorrentRelocation()' internal/deluge/integration.go`
returns **0 hits** (and `GetRelocationClient` / `TorrentRelocationMode` exist nowhere in the repo
yet), so the anchor-block STOP below WILL fire if this brief is dispatched today. Hold this brief
until TASK-03 merge is confirmed; the STOP firing before then is expected, not a defect.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/torrent-relocation-undo-repoint-closure" -b agent/torrent-relocation-undo-repoint-closure origin/main
cd "$REPO/.worktrees/torrent-relocation-undo-repoint-closure"
git rebase origin/main
```

## Goal

Close INIT-5's deferred item. The archived plan's "Task 7: Torrent move_storage on **undo**"
(`docs/archive/superpowers/plans/2026-04-15-bulk-organize-undo.md:97,100` — note: undo, NOT
organize; the file moved to docs/archive/) proposed wiring undo to Deluge. That wiring has since
shipped, AND the fan-out helpers already have happy/skip/error test coverage (see Background —
do NOT duplicate it). What is missing is ONLY the tri-state MODE dimension. Deliver: a
mode-matrix test (organize / undo / version-swap × off / physical-move / re-point-only = 9
cells) added to the EXISTING suites in `internal/server`, plus a closure note in the archived
doc. Do NOT create a new suite in `internal/deluge` — a parallel suite duplicating the existing
fakes and cases is exactly the twin-drift this initiative removes elsewhere.

## Background (verify before editing)

- The fan-out helpers live in `internal/deluge/integration.go`: `NotifyDelugeMoveStorage` (hub,
  ~:106 — after TASK-03 it switches on `config.AppConfig.TorrentRelocation()`),
  `NotifyDelugeAfterOrganize`, `NotifyDelugeAfterUndo`, `NotifyDelugeAfterVersionSwap`. Thin
  re-exports forward from `internal/server/deluge_integration.go:37-64`.
- **Existing coverage (the reason this task is mode-cells-only):**
  `internal/server/deluge_integration_test.go` already has
  `TestNotifyDelugeAfterUndo_Enabled/_Disabled/_NoHash/_DelugeError` (~:225-448),
  `TestNotifyDelugeAfterVersionSwap` (~:186), and `TestNotifyDelugeMoveStorage_EmptyHash/_NoClient`;
  `internal/server/deluge_centralization_test.go` has
  `TestNotifyDelugeAfterOrganize_CallsMoveStorage/_SkipsWhenDisabled/_SkipsWhenNoTorrentHash/_DelugeErrorIsBestEffort`
  (~:125-190). These stay the authority for happy/skip/error semantics — reuse their client
  fakes and config setup/teardown patterns for the new mode cells.
- Existing wiring (do NOT re-implement, only test): undo → `deluge.NotifyDelugeAfterUndo` is
  passed as the callback in `internal/server/undo_engine.go` (`RunUndoOperation`); organize →
  `deluge.NotifyDelugeAfterOrganize` in `internal/server/handlers/organize.go`.
- The helpers take small store interfaces (`database.BookVersionStore`, `database.BookReader`)
  — the existing tests already have in-test fakes; extend them, do not add mockery mocks
  (version-drift footgun — local mockery regenerates ALL mocks repo-wide).
- The hub reaches Deluge through the package singleton `GetClient()` (physical) and, after
  TASK-03, `GetRelocationClient()` (re-point). Inspect how the EXISTING tests fake the client
  (`grep -n 'GetClient\|httptest\|fake' internal/server/deluge_integration_test.go | head`);
  reuse that pattern for both singletons. If `GetRelocationClient` cannot be faked without a
  production-code change, STOP and report — do not modify `integration.go` in this task.
- Edge semantics to assert in the NEW cells only where the mode dimension changes them: mode
  `off` issues zero RPCs; `re-point-only` calls `UpdateStoragePath` (never MoveStorage);
  `physical-move` still calls MoveStorage (the existing happy-path tests double as
  anti-over-suppression once the config default resolves to physical-move).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func NotifyDelugeMoveStorage' internal/deluge/integration.go       # hub, ~:106, 1 hit
  grep -n 'TestNotifyDelugeAfterUndo_Enabled' internal/server/deluge_integration_test.go   # existing suite, 1 hit
  grep -n 'func TestNotifyDelugeAfterVersionSwap' internal/server/deluge_integration_test.go  # version-swap suite, ~:186, 1 hit
  grep -n 'func NotifyDelugeAfterUndo' internal/server/deluge_integration.go  # thin re-export block cited in Background (~:37-64), ~:51, 1 hit
  grep -n 'TestNotifyDelugeAfterOrganize_CallsMoveStorage' internal/server/deluge_centralization_test.go  # existing suite, 1 hit
  grep -n 'move_storage' docs/archive/superpowers/plans/2026-04-15-bulk-organize-undo.md  # deferred bullet, ~:97/:100
  grep -rn 'TorrentRelocation()' internal/deluge/integration.go               # TASK-03 landed, >=1 hit — 0 hits = TASK-03 not merged: STOP (expected before TASK-03 merges — see Dispatch-readiness)
  ```

## Step-by-step

1. In `internal/server/deluge_integration_test.go` (undo + version-swap flows) and
   `internal/server/deluge_centralization_test.go` (organize flow), add
   `TestUndoOrganizeVersionSwapModeMatrix` (one table-driven test, split across the two files
   only if the fakes force it — prefer one file + small helpers): 3 flows × 3 modes; per cell
   set `config.AppConfig.TorrentRelocationMode` (restore/reset with `t.Cleanup` — config is
   package-global, so do NOT use `t.Parallel()` across mode cells), drive the flow's Notify
   helper with the suite's existing fake stores, and assert against the faked client:
   `off` → no RPC; `physical-move` → the move_storage RPC; `re-point-only` → the re-point call
   (`UpdateStoragePath`) and NOT move_storage.
2. Do NOT re-add happy/skip/error cases — `TestNotifyDelugeAfterUndo_*` /
   `TestNotifyDelugeAfterOrganize_*` already own empty-hash, non-active-version, disabled, and
   error-is-best-effort semantics. If a mode cell would duplicate one of them, reference the
   existing test in a comment instead of duplicating.
3. Tests-only: do not edit `integration.go` or any production file (see STOP condition above).
   Anti-over-suppression: N/A as a new guard (none added) — the physical-move cells double as
   proof the legacy path still fires.
4. In `docs/archive/superpowers/plans/2026-04-15-bulk-organize-undo.md`, under the "Task 7"
   heading (find it with the grep above), append one line:
   `> CLOSED 2026-07: wiring shipped earlier (NotifyDelugeAfterUndo); happy/skip/error coverage pre-existed in internal/server; mode-matrix coverage added by docs/agent-tasks/torrent-relocation/TASK-07.`
   Bump that file's version header + last-edited; keep its guid.
5. Run the package tests with `-race` (concurrency house rule for anything touching the client
   singleton), then the gate.

## How to test

```bash
go test ./internal/server/... -race -count=1 -run 'ModeMatrix|NotifyDeluge'
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -rn "TestUndoOrganizeVersionSwapModeMatrix" internal/server` hits (in the existing suites — `test ! -f internal/deluge/integration_test.go` unless it pre-existed for other reasons)
- [ ] The matrix covers 9 cells (3 flows × 3 modes) — count the table entries; re-point cells assert `UpdateStoragePath` and NOT move_storage; off cells assert zero RPCs
- [ ] No duplicated happy/skip/error tests: the pre-existing `TestNotifyDelugeAfterUndo_*` / `TestNotifyDelugeAfterOrganize_*` cases are unmodified (`git diff origin/main` shows only additions around them)
- [ ] `grep -n "CLOSED 2026-07" docs/archive/superpowers/plans/2026-04-15-bulk-organize-undo.md` hits (deferred item closed)
- [ ] `git diff --name-only origin/main` shows NO production `.go` file (tests + docs only)
- [ ] Anti-over-suppression: physical-move cells green (legacy path still fires)
- [ ] `go test ./internal/server/... -race` green for the touched tests; `make ci` exits 0 (staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
test(server): mode-matrix coverage for organize/undo/version-swap torrent re-point (INIT-5 T7)

Closes the deferred "torrent move_storage on undo" item from the archived
2026-04-15 bulk-organize-undo plan: the wiring shipped earlier and
happy/skip/error coverage already existed in internal/server; this adds the
missing tri-state mode dimension (3 flows x 3 modes) to those existing suites,
incl. off-mode no-RPC and re-point-asserts-UpdateStoragePath cells.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/torrent-relocation-undo-repoint-closure
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -rn "TestUndoOrganizeVersionSwapModeMatrix" internal/server` hits, this task is already
applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; it
removes mode cells and one doc note only — zero runtime surface, no data or schema.

**Fallback note:** if the TASK-02 spike REJECTED re-point (TASK-03 blocked), this task shrinks
to the doc closure note + (optionally) off/physical-move cells only — drop the re-point cells.
