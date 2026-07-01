<!-- file: docs/agent-tasks/perf-cleanup/TASK-02-metadata-fetch-ids-fastpath.md -->
<!-- version: 1.0.0 -->
<!-- guid: f71250f9-24eb-489a-b375-cb7f71734d13 -->
<!-- last-edited: 2026-07-01 -->

# TASK-02 — Per-book GetAuthorByID fast path when len(bookIDs)<100 (MAYDEPLOY-H5)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-metadata-fetch-ids-fastpath" -b agent/pc-metadata-fetch-ids-fastpath origin/main
cd "$REPO/.worktrees/pc-metadata-fetch-ids-fastpath"
git rebase origin/main
```

## Goal

`runBulkMetadataFetchForBookIDs` (the by-ID metadata-fetch op, used when a
user selects a small set of books rather than the whole library) always calls
`store.GetAllAuthors()` to build an `authorByID` lookup map — materializing
**every** author in the library even when fetching metadata for e.g. 3 books.
Add a fast path: when `len(bookIDs) < 100`, resolve each book's author
individually via `store.GetAuthorByID(id)` instead of loading the full author
set. Output (the `authorName` attached to each `bookWork`) must be byte-for-byte
identical to today.

**Scope note:** only `runBulkMetadataFetchForBookIDs` (the by-ID path) is in
scope. The sibling function `runBulkMetadataFetchAll` (the full-library path,
which also calls `GetAllAuthors()`) is intentionally **out of scope** — it
already processes every book in the library, so materializing every author is
the right tradeoff there and there is no "small batch" case to fast-path.

## Background (verify before editing)

Re-run — line numbers drift:
```bash
grep -n "func (s \*Server) runBulkMetadataFetchForBookIDs\|GetAllAuthors\|authorByID\|for _, id := range bookIDs" internal/server/metadata_ops.go
grep -n "func.*GetAuthorByID" internal/database/pebble_store.go internal/database/mock_store.go
```

At authoring time:
- `internal/server/metadata_ops.go:439` — `func (s *Server)
  runBulkMetadataFetchForBookIDs(ctx context.Context, opID string, bookIDs
  []string, params operations.BulkMetadataFetchParams, store database.Store,
  progress operations.ProgressReporter) error` — the function to change.
- `internal/server/metadata_ops.go:467` — `allAuthors, _ :=
  store.GetAllAuthors()` followed by the `authorByID := make(map[int]string,
  len(allAuthors))` build loop (lines ~468-471). This is the call to make
  conditional.
- `internal/server/metadata_ops.go:478` (`for _, id := range bookIDs {`) and
  the `author := ""` / `if b.AuthorID != nil { author = authorByID[*b.AuthorID]
  }` block a few lines below it — this is where `authorByID` is consumed per
  book. In the fast path, replace the map lookup with a direct
  `store.GetAuthorByID(*b.AuthorID)` call (nil-safe: skip if `b.AuthorID ==
  nil`, keep `author = ""`).
- `database.Store` (implemented by `*database.PebbleStore` at
  `internal/database/pebble_store.go:456` and `*database.MockStore` at
  `internal/database/mock_store.go:496`) already exposes `GetAuthorByID(id
  int) (*Author, error)` — no interface change needed.

## Step-by-step

1. Re-run the grep commands to confirm line numbers.
2. Replace the unconditional `allAuthors, _ := store.GetAllAuthors()` +
   `authorByID` map build with a branch:
   ```go
   var authorByID map[int]string
   if len(bookIDs) >= 100 {
       allAuthors, _ := store.GetAllAuthors()
       authorByID = make(map[int]string, len(allAuthors))
       for _, a := range allAuthors {
           authorByID[a.ID] = a.Name
       }
   }
   ```
   (i.e. `>= 100` keeps today's exact behavior for large batches; `< 100`
   leaves `authorByID` nil and resolves per-book below.)
3. At the per-book author-resolution site, change:
   ```go
   author := ""
   if b.AuthorID != nil {
       author = authorByID[*b.AuthorID]
   }
   ```
   to:
   ```go
   author := ""
   if b.AuthorID != nil {
       if authorByID != nil {
           author = authorByID[*b.AuthorID]
       } else if a, aerr := store.GetAuthorByID(*b.AuthorID); aerr == nil && a != nil {
           author = a.Name
       }
   }
   ```
   This preserves the existing "missing author → empty string, no error
   propagated" behavior (the original `GetAllAuthors()` call already ignored
   its error via `_`).
4. Do not touch `runBulkMetadataFetchAll` (around line 88) — it stays exactly
   as-is (see Scope note above).
5. Bump the file header in `internal/server/metadata_ops.go` (version 1.2.0 →
   1.3.0, `last-edited` → today's date).
6. Add a new test file `internal/server/metadata_ops_fastpath_test.go`
   (confirm no test file exists today: `find . -iname metadata_ops_test.go
   -not -path '*.worktrees*'`). Use `database.NewMockStore()` (check an
   existing `internal/server/*_test.go` file, e.g.
   `audiobook_update_service_test.go`, for the exact mock-store setup
   pattern used in this package). Cover:
   - `len(bookIDs) < 100`: seed a couple of books with distinct `AuthorID`s
     and assert the fetched-metadata op resolves the same author names as
     before (compare against calling the old `GetAllAuthors`-map path
     manually, or simply assert the correct author name shows up in the
     resulting work items / operation results).
   - `len(bookIDs) >= 100` (or force the `>=100` branch with a smaller
     boundary check if easier — assert `GetAllAuthors` code path is still
     taken): behavior identical to before.
   - A book with `AuthorID == nil` still resolves to an empty author string
     in both branches.

## How to test

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/pc-metadata-fetch-ids-fastpath
go build ./...
go test ./internal/server/... -run TestRunBulkMetadataFetchForBookIDs -v -count=1
go test ./internal/server/... -count=1
go vet ./internal/server/...
```
(`go test ./internal/server/...` is the full package — expect it to take a
few minutes per this repo's `internal/server` test-suite size; that's normal,
not a hang.)

## Acceptance criteria
- [ ] `runBulkMetadataFetchForBookIDs` resolves authors per-book via `GetAuthorByID` when `len(bookIDs) < 100`.
- [ ] `len(bookIDs) >= 100` still uses `GetAllAuthors()` + map (unchanged behavior).
- [ ] `runBulkMetadataFetchAll` (the full-library path) is untouched.
- [ ] Output author names are identical between the fast path and the old map-based path for the same input.
- [ ] New test proves parity for both branches plus the nil-`AuthorID` case.
- [ ] `go build`, `go test ./internal/server/...`, `go vet` all green.
- [ ] File headers bumped.

## Commit message
```
perf(server): add per-book author fast path to metadata-fetch-ids (MAYDEPLOY-H5)

When fewer than 100 book IDs are requested, resolve each book's author via
GetAuthorByID instead of materializing the entire author table with
GetAllAuthors. Output is unchanged; only the read pattern for small batches
is cheaper.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/pc-metadata-fetch-ids-fastpath
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n "len(bookIDs) >= 100" internal/server/metadata_ops.go` — if present, this task is done. Rollback: revert the commit; no schema/API change, safe at any time.
