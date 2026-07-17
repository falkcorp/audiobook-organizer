<!-- file: docs/agent-tasks/provenance-hash-chain/TASK-01-download-hash-field.md -->
<!-- version: 1.0.0 -->
<!-- guid: d4f6eded-d1c8-4f55-8752-eceb4915e587 -->
<!-- last-edited: 2026-07-01 -->

# TASK-01 — Add DownloadHash field to book_files, populate from Deluge, manual-set API (HASH-CHAIN-1)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/hc-download-hash-field" -b agent/hc-download-hash-field origin/main
cd "$REPO/.worktrees/hc-download-hash-field"
git rebase origin/main
```

## Goal
Add a `DownloadHash` field to the `BookFile` struct (PebbleDB-backed; this repo has NO SQLite store — ignore any "SQLite migration" phrasing you may see referenced elsewhere, PebbleDB is the only store). Populate `DownloadHash` automatically from the existing Deluge import path (which already captures `DelugeHash`, the torrent info-hash). Also expose a manual-set path via the existing book-file PATCH API endpoint so a user/admin can set or correct the value by hand.

## Background (verify before editing)
- `internal/database/store.go` — the `BookFile` struct. The Deluge fields block is near line 764-771:
  ```go
  DelugeHash           string     `json:"deluge_hash,omitempty"`
  DelugeOriginalPath   string     `json:"deluge_original_path,omitempty"`
  ImportedFromDelugeAt *time.Time `json:"imported_from_deluge_at,omitempty"`
  ```
  Add `DownloadHash string \`json:"download_hash,omitempty"\`` right after `DelugeHash` in that block, with a short doc comment explaining it is the hash of the originally-downloaded file (distinct from `DelugeHash`, which is the torrent info-hash — `DownloadHash` may be manually set for files that did not arrive via Deluge).
  Re-verify the anchor: `grep -n 'DelugeHash\|DelugeOriginalPath\|ImportedFromDelugeAt' internal/database/store.go`
- `internal/database/pebble_store_mark_import.go` — `MarkFileImportedFromDeluge` (starts ~line 19) is where Deluge import populates `bf.DelugeHash = torrentHash` in two places (the "match by path" branch ~line 36, and the "match by torrent hash" fallback branch ~line 68). In BOTH places, immediately after the `bf.DelugeHash = torrentHash` / `f.DelugeHash = torrentHash` assignment, also set `DownloadHash` from the same `torrentHash` value **only if it is currently empty** (do not clobber a manually-set value):
  ```go
  if bf.DownloadHash == "" {
      bf.DownloadHash = torrentHash
  }
  ```
  Re-verify the anchor: `grep -n 'DelugeHash = torrentHash' internal/database/pebble_store_mark_import.go`
- `internal/server/handlers/audiobooks/handler_files.go` — `PatchBookFile` (starts ~line 166) is the existing PATCH endpoint pattern for `SkipScan` (route: `PATCH /audiobooks/:id/files/:file_id`, wired in `internal/server/wire_audiobooks_routes.go:40`). Extend the same handler to also accept and apply `download_hash`. The request body struct is currently:
  ```go
  var body struct {
      SkipScan *bool `json:"skip_scan"`
  }
  ```
  Add a `DownloadHash *string \`json:"download_hash\`` field, and after the existing `if body.SkipScan != nil { ... }` block add:
  ```go
  if body.DownloadHash != nil {
      file.DownloadHash = *body.DownloadHash
      slog.Info("file download_hash set",
          "book_id", bookID,
          "file_id", fileID,
          "download_hash", *body.DownloadHash,
      )
  }
  ```
  Re-verify the anchor: `grep -n 'PatchBookFile\|SkipScan' internal/server/handlers/audiobooks/handler_files.go`
- Also add `"download_hash": f.DownloadHash,` to the file-listing JSON map near line 154 of the same file (alongside the other hash fields like `post_metadata_hash`) so the field is visible via `GET /audiobooks/:id/files`. Re-verify: `grep -n 'post_metadata_hash' internal/server/handlers/audiobooks/handler_files.go`
- No SQLite store exists anywhere in this repo — PebbleDB (`internal/database/pebble_store.go`) is the only implementation of `database.Store`. Do not create a migration file of any kind.

## Step-by-step
1. Run the two `grep` commands above to confirm current line numbers before editing anything.
2. In `internal/database/store.go`, add the `DownloadHash` field to the `BookFile` struct as shown above, with a doc comment.
3. In `internal/database/pebble_store_mark_import.go`, add the empty-check-then-set logic for `DownloadHash` in both branches of `MarkFileImportedFromDeluge` where `DelugeHash` is currently set.
4. In `internal/server/handlers/audiobooks/handler_files.go`:
   a. Add `download_hash` to the file-listing JSON map.
   b. Extend the `PatchBookFile` request body struct with `DownloadHash *string`.
   c. Add the `if body.DownloadHash != nil { ... }` block after the existing `SkipScan` block, before the `store.UpsertBookFile(file)` call.
5. Add a test in `internal/database/` (e.g. a new file `internal/database/download_hash_test.go` or extend an existing PebbleDB round-trip test file if one covers `BookFile` persistence — check `grep -rln "func TestPebbleStore.*BookFile" internal/database/*_test.go` first and prefer extending an existing file) that: creates a `PebbleStore` against a temp dir, upserts a `BookFile` with `DownloadHash` set, re-reads it via `GetBookFileByID` (or the equivalent read method used elsewhere in that test file), and asserts the value round-trips.
6. Add a test for the `MarkFileImportedFromDeluge` population logic: assert that after calling it with a `torrentHash`, the resulting `BookFile.DownloadHash` equals `torrentHash` when it started empty, and is left untouched when it was pre-set to a different value (manual-set wins). Check `internal/database/pebble_store_mark_import.go` for any existing `_test.go` sibling to extend; if none exists, create `internal/database/pebble_store_mark_import_test.go` following the style of other PebbleDB tests in that package (temp-dir-backed `PebbleStore`, no mocks needed since this is a PebbleDB-native test).
7. Add a handler test for the manual-set API path in `internal/server/handlers/audiobooks/` — check for an existing `handler_files_test.go` (`grep -rln PatchBookFile internal/server/handlers/audiobooks/*_test.go`) and extend it with a case that PATCHes `{"download_hash": "abc123"}` and asserts the mock store's `UpsertBookFile` was called with that value set on the file.
8. Bump file version headers (per `.standards/instructions/file-headers.md`) on every file you touched: `internal/database/store.go`, `internal/database/pebble_store_mark_import.go`, `internal/server/handlers/audiobooks/handler_files.go`, and any new/edited test files. Bump patch version, update `last-edited` to today's date.

## How to test
```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/hc-download-hash-field
go build ./...
go test ./internal/database/... -count=1
go test ./internal/server/handlers/audiobooks/... -count=1
```
All must pass with no failures.

## Acceptance criteria
- [ ] `BookFile.DownloadHash` field exists in `internal/database/store.go` with `json:"download_hash,omitempty"` tag and a doc comment distinguishing it from `DelugeHash`.
- [ ] `MarkFileImportedFromDeluge` populates `DownloadHash` from the torrent hash only when it was previously empty, in both match branches.
- [ ] `PatchBookFile` accepts `download_hash` in its request body and persists it via `UpsertBookFile`.
- [ ] `download_hash` is included in the `GET /audiobooks/:id/files` JSON response.
- [ ] New/extended tests cover: PebbleDB round-trip persistence, Deluge-import population (empty-then-set, and manual-value-not-clobbered), and the PATCH API path.
- [ ] `go build ./...` and the two `go test` commands above pass.
- [ ] File version headers bumped on every touched/created file.

## Commit message
```
feat(database,server): add DownloadHash to BookFile, populate from Deluge, manual-set API (HASH-CHAIN-1)

Adds a DownloadHash provenance field to BookFile, auto-populates it from
the existing Deluge torrent-hash import path (without clobbering a
manually-set value), and extends the existing PatchBookFile endpoint
(PATCH /audiobooks/:id/files/:file_id) so it can be set/corrected by hand.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/hc-download-hash-field
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n 'DownloadHash' internal/database/store.go` — if the field already exists, this task is done; skip re-adding it and only fill in whatever step is missing (Deluge population, or the API field). Rollback: `git revert <merge-commit-sha>` on `main` and re-run `make ci` to confirm the tree is clean.
