<!-- file: docs/agent-tasks/provenance-hash-chain/TASK-02-integrity-alert.md -->
<!-- version: 1.0.0 -->
<!-- guid: c9a17e34-5b2f-4d8a-9c6e-1a3f7b8d0e2c -->
<!-- last-edited: 2026-07-01 -->

# TASK-02 — Integrity alert: file_hash != original_file_hash with no AO write on record (HASH-CHAIN-3)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/hc-integrity-alert" -b agent/hc-integrity-alert origin/main
cd "$REPO/.worktrees/hc-integrity-alert"
git rebase origin/main
```

## Goal
Add a report-only maintenance check that flags `BookFile` rows whose current `FileHash` differs from `OriginalFileHash`, with no AO (Audiobook Organizer) tag-write recorded on the file — i.e. a candidate for external modification or bit-rot that the app itself did not cause. This is a **detection/reporting feature only** — it must NOT modify, delete, or repair any files or records. Model it directly on the existing `maintenance.orphan-book-files-cleanup` op (report-only pattern).

## Background (verify before editing)
- `internal/database/store.go` — the `BookFile` struct already has all three fields needed, no schema change required:
  - `FileHash` (current on-disk hash, set by the scanner via `SetBookFileHash`)
  - `OriginalFileHash` (hash captured at import time)
  - `PostMetadataHash` (hash captured immediately after an AO metadata tag write — see `internal/database/pebble_store.go`, `UpdateBookFileHashes`, ~line 10503). This is the field that records "AO wrote tags to this file". Re-verify: `grep -n 'FileHash\|OriginalFileHash\|PostMetadataHash' internal/database/store.go`
  - Definition of "no AO write on record" for this task: `PostMetadataHash == ""` (AO never recorded a tag write for this file).
  - The integrity-flag predicate is therefore: `f.FileHash != "" && f.OriginalFileHash != "" && f.FileHash != f.OriginalFileHash && f.PostMetadataHash == ""`.
- `internal/plugins/maintenance/orphan_book_files.go` — this is the exact pattern to copy: an `OperationDef` builder method (`orphanBookFilesCleanupDef`, ~line 32), a `Params` struct with a report-only default, a `run...` function that logs start/progress/complete via `sdk.Reporter` and `sdk.NewProgress`, and a pure scan function (`findOrphanBookFiles`, ~line 134) that takes `(ctx, store)` and returns the flagged rows plus counts. Re-verify: `grep -n 'func.*orphanBookFilesCleanupDef\|func findOrphanBookFiles' internal/plugins/maintenance/orphan_book_files.go`
- `internal/plugins/maintenance/plugin.go` — `Register()` (~line 31-100) lists every `OperationDef` builder call, e.g. `p.orphanBookFilesCleanupDef(),` at ~line 43. Add your new def call in this list. Re-verify: `grep -n 'orphanBookFilesCleanupDef()' internal/plugins/maintenance/plugin.go`
- `internal/database/pebble_store.go` — `GetAllBookFiles()` (~line 9587) is the read method used by `findOrphanBookFiles` to iterate every `BookFile`; reuse it (it is already on the `database.Store` interface used by the maintenance plugin's `p.deps.Store()`).
- `internal/plugins/maintenance/orphan_book_files_test.go` — the test pattern to copy: builds a `database.MockStore{GetAllBookFilesFunc: ...}` and asserts the scan function returns exactly the expected flagged rows with no store mutation calls. Re-verify: `grep -n 'MockStore{' internal/plugins/maintenance/orphan_book_files_test.go`

## Step-by-step
1. Run the `grep` commands above to confirm current line numbers and field names before editing.
2. Create `internal/plugins/maintenance/integrity_check.go` (new file), modeled directly on `orphan_book_files.go`:
   - `IntegrityCheckParams` struct — this op takes no destructive action, so it can be an empty struct (or omit params entirely and accept `json.RawMessage` unused), since it is report-only by design (no `Delete`-style flag needed).
   - `integrityCheckDef()` method returning an `sdk.OperationDef` with:
     - `ID: "maintenance.file-integrity-check"`
     - `DisplayName: "File integrity check"`
     - `Description`: explain it flags files where `file_hash != original_file_hash` with no AO tag-write on record (`post_metadata_hash` empty) — candidate external modification or bit-rot. Report-only, takes no action.
     - `Capabilities: []sdk.Capability{sdk.CapLibraryRead}` (read-only — do NOT include `CapLibraryWrite`, this op must never write).
     - Pick a schedule slot that does not collide with existing ones — check `grep -n 'Schedule: *&sched' internal/plugins/maintenance/*.go` for cron strings already in use and choose a free daily slot in the 02:00-04:00 maintenance window (e.g. `"30 2 * * *"` if free).
     - `Run: p.runIntegrityCheck`
   - `runIntegrityCheck(ctx, raw, reporter)` — logs start via `reporter.Log`, calls a pure scan function `findIntegrityMismatches(ctx, store)`, logs the result count, and returns a summary via `reporter` (mirror `runOrphanBookFilesCleanup`'s reporting shape, including the `sdk.NewProgress` start/complete calls). It must NOT call any store write/delete method — this is report-only, full stop, unlike the orphan-cleanup op that supports an optional `Delete` action.
   - `findIntegrityMismatches(ctx, store database.Store) (flagged []database.BookFile, totalFiles int, err error)` — pure function: calls `store.GetAllBookFiles()`, iterates, and returns every file matching the predicate from the Background section above (`FileHash != "" && OriginalFileHash != "" && FileHash != OriginalFileHash && PostMetadataHash == ""`), plus the total file count scanned.
3. In `internal/plugins/maintenance/plugin.go`, add `p.integrityCheckDef(),` to the `Register()` list, near `p.orphanBookFilesCleanupDef(),` (same logical group — book-file hygiene checks).
4. Create `internal/plugins/maintenance/integrity_check_test.go`, modeled on `orphan_book_files_test.go`: build a `database.MockStore{GetAllBookFilesFunc: ...}` returning a mix of (a) a file with matching hashes (not flagged), (b) a file with `FileHash != OriginalFileHash` and empty `PostMetadataHash` (flagged), (c) a file with `FileHash != OriginalFileHash` but a non-empty `PostMetadataHash` (NOT flagged — AO wrote tags, expected drift), (d) a file with empty `OriginalFileHash` (not flagged — no baseline to compare). Assert `findIntegrityMismatches` returns exactly the expected flagged set and that no store write/delete method is ever invoked (the `MockStore` should have no write funcs set, or you assert they are never called if the mock records calls).
5. Bump file version headers on `internal/plugins/maintenance/integrity_check.go` (new — version `1.0.0`), `internal/plugins/maintenance/integrity_check_test.go` (new — version `1.0.0`), and `internal/plugins/maintenance/plugin.go` (bump patch version, update `last-edited`).

## How to test
```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/hc-integrity-alert
go build ./...
go test ./internal/plugins/maintenance/... -count=1
go test ./internal/database/... -count=1
```
All must pass with no failures.

## Acceptance criteria
- [ ] New `maintenance.file-integrity-check` `OperationDef` exists, registered in `plugin.go`'s `Register()`.
- [ ] The op is read-only: `Capabilities` contains only `CapLibraryRead`, and `runIntegrityCheck`/`findIntegrityMismatches` never call any store write or delete method.
- [ ] Predicate correctly flags `FileHash != OriginalFileHash && PostMetadataHash == ""` and excludes rows with a non-empty `PostMetadataHash` (AO-caused drift) or an empty `OriginalFileHash` (no baseline).
- [ ] New test file covers all four cases above (flagged, not-flagged-due-to-tag-write, not-flagged-due-to-no-baseline, not-flagged-matching-hashes) and asserts no writes occur.
- [ ] `go build ./...` and both `go test` commands pass.
- [ ] File version headers bumped/added on all touched/created files.

## Commit message
```
feat(maintenance): add report-only file integrity check (HASH-CHAIN-3)

Flags book_file rows where file_hash differs from original_file_hash
with no AO tag-write on record (post_metadata_hash empty) — a candidate
for external modification or bit-rot. Read-only; takes no action.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/hc-integrity-alert
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n 'maintenance.file-integrity-check' internal/plugins/maintenance/plugin.go` — if this string is already registered, the task is done. Rollback: `git revert <merge-commit-sha>` on `main` and re-run `make ci` to confirm the tree is clean; this op is additive-only (no schema/data changes) so rollback is a plain code revert with no data migration to undo.
