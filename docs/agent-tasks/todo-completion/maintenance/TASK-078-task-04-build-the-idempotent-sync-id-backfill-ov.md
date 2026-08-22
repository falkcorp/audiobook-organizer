<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-078-task-04-build-the-idempotent-sync-id-backfill-ov.md -->
<!-- version: 1.0.0 -->
<!-- guid: 041efd82-d216-4acd-90e0-a8aabfb899ea -->
<!-- last-edited: 2026-08-21 -->

# TASK-078 — TASK-04: build the idempotent sync-ID backfill over the existing library (bounded worker pool required) (ABS-SYNC-TASK-04)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · maintenance subagent · **Why:** Full-library maintenance op with a mandatory bounded worker pool (CLAUDE.md concurrency rule) touching prod sync identity — needs careful idempotency and review · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10333 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ABS-SYNC: wave 3 — backfill + survival proof.** " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-078-task-04-build-the-idempotent-sync-id-backfill-ov" -b agent/maintenance-078-task-04-build-the-idempotent-sync-id-backfill-ov origin/main
cd "$REPO/.worktrees/maintenance-078-task-04-build-the-idempotent-sync-id-backfill-ov"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Read docs/agent-tasks/abs-sync/TASK-04-syncid-backfill.md in full and implement it: a new maintenance op (following the registry.RunItems pattern from internal/plugins/acoustid/backfill.go, NOT a bare `for range books` loop, per CLAUDE.md's mandatory concurrency rule) that walks every book lacking a sync identity and mints/repoints one idempotently, safe to re-run.

## Background (verify before editing)

- The three sync-identity repoint primitives this backfill needs are already merged: RepointSyncItem (#2070, internal/database/pebble_store_syncid.go:249), RepointSyncFile (#2068, internal/database/pebble_store_syncfile.go:207).
- internal/plugins/acoustid/backfill.go is the canonical worked example of a bounded-worker-pool, resumable, RunItems-based maintenance backfill in this codebase — CLAUDE.md explicitly calls this out as the pattern to copy for new full-library ops instead of writing a fresh sequential loop.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln 'backfill' internal/plugins/maintenance/*.go   # several hits, none combined with SyncItem/SyncID in the same file per a follow-up grep -l — no existing maintenance op backfills sync IDs
  ls docs/agent-tasks/abs-sync/TASK-04-syncid-backfill.md   # file exists — the TASK-04 brief already exists and defines scope
  grep -n 'registry.RunItems' internal/plugins/acoustid/backfill.go   # ≥1 hit ~L125 — the reusable bounded-worker-pool sibling pattern exists in the AcoustID backfill
  ```

### Reuse — don't invent

- Use `registry.RunItems bounded-worker-pool op pattern` in `internal/plugins/acoustid/backfill.go` (verify: `grep -n 'registry.RunItems(ctx, reporter, slice' internal/plugins/acoustid/backfill.go`) — do NOT write a parallel helper.
- Use `RepointSyncItem (Pebble primitive already merged, #2070)` in `internal/database/pebble_store_syncid.go` (verify: `grep -n 'func (p \*PebbleStore) RepointSyncItem' internal/database/pebble_store_syncid.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read docs/agent-tasks/abs-sync/TASK-04-syncid-backfill.md end to end for the exact scope/acceptance bar the brief author defined.
2. Create internal/plugins/maintenance/backfill_sync_id.go modeled on internal/plugins/acoustid/backfill.go's structure: an op definition (sdk.LivenessRunItems), a ListBookIDs-style memdb-cap-safe enumeration, and a registry.RunItems call with a bounded worker count (runtime.NumCPU() or similar) processing each book.
3. Per-book logic: check whether the book already has a sync identity (idempotency check via whatever getter pairs with RepointSyncItem/the abs_sess or sync-identity store — trace database.AsSyncIdentityStore usage in internal/merge/sync_follow.go for the accessor pattern); if absent, mint one and write it via the store's create/repoint primitive; if present, skip (no-op) so the op is safe to re-run.
4. Register the op in whatever registry the acoustid backfill registers itself in (grep the acoustid backfill's init/registration call site and mirror it).
5. Bump version headers on all new/touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_078.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with a PARTIAL sync identity (e.g. a SyncItem row but no matching SyncFile rows) must be handled explicitly — decide whether that counts as 'already backfilled' or needs completion, per the brief.

## Tests

- internal/plugins/maintenance/backfill_sync_id_test.go: TestBackfillSyncID_MintsForBooksWithoutIdentity — seed books with no sync identity, run the op, assert each now has one.
- TestBackfillSyncID_Idempotent_SkipsAlreadyBackfilled — run the op twice, assert the second run performs zero mutations (no double-minting, no overwritten IDs).
- TestBackfillSyncID_ConcurrentSafety — with the bounded worker pool active, assert no two workers mint conflicting IDs for the same book (a -race run).

Anti-over-suppression test: `TestBackfillSyncID_MintsForBooksWithoutIdentity is the happy-path counterpart to the idempotent-skip test — proves the skip logic doesn't also skip books that genuinely need backfilling` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test -race ./internal/plugins/maintenance/... -run BackfillSyncID` passes
- [ ] the op appears in the op registry the way other maintenance ops do (verify via the existing operations-listing test/endpoint)
- [ ] Anti-over-suppression test: `TestBackfillSyncID_MintsForBooksWithoutIdentity is the happy-path counterpart to the idempotent-skip test — proves the skip logic doesn't also skip books that genuinely need backfilling` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_078.md`.

## Commit message

```
feat(maintenance): TASK-04: build the idempotent sync-ID backfill over the exis (ABS-SYNC-TASK-04)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``go test -race ./internal/plugins/maintenance/... -run BackfillSyncID` passes`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Must use a bounded worker pool per CLAUDE.md's MANDATORY concurrency rule — this is exactly the whole-library-scale, per-item-DB-write shape that rule targets.
