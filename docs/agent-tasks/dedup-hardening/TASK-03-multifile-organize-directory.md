<!-- file: docs/agent-tasks/dedup-hardening/TASK-03-multifile-organize-directory.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92c7d4cf-96dd-48a6-a1b6-08f635dd6145 -->
<!-- last-edited: 2026-07-01 -->

# TASK-03 — Route `BookFiles>1` books to `OrganizeBookDirectory` in `organizeOneBook` (CONS-FRAG-2)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none (independent files from TASK-01/TASK-02)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dh-multifile-organize-directory" -b agent/dh-multifile-organize-directory origin/main
cd "$REPO/.worktrees/dh-multifile-organize-directory"
git rebase origin/main
```

## Goal

Fix a silent no-op: a merged multi-file iTunes book (chapters/tracks scattered
across multiple source files) currently gets routed through the single-file
`OrganizeBook` path, which safely refuses to touch anything when `FilePath` is
a directory — so the book stays stuck at `library_state = "imported"` and never
organizes. Route books with more than one `BookFile` to the existing
`OrganizeBookDirectory` function instead, which already knows how to move
multiple per-track files into the correct target directory.

## Background (verify before editing)

- `Importer.organizeOneBook` in `internal/itunes/service/importer.go` currently
  calls only `org.OrganizeBook(book)` — the single-file organize path. Confirm
  the current body:
  ```bash
  grep -n "func (imp \*Importer) organizeOneBook" -A 25 internal/itunes/service/importer.go
  ```
- `organizer.OrganizeBook` (in `internal/organizer/organizer.go`) explicitly
  refuses to organize when `book.FilePath` resolves to a directory — it returns
  an error telling the caller to "use organizeDirectoryBook for multi-file
  books" instead of moving anything. Confirm:
  ```bash
  grep -n "func (o \*Organizer) OrganizeBook\b" -A 15 internal/organizer/organizer.go
  ```
- `organizer.OrganizeBookDirectory(book *database.Book, segmentPaths []string) (string, map[string]string, error)`
  already exists and does the right thing for multi-file books — it builds the
  target directory from the book's metadata and copies/links each segment path
  into it. Confirm:
  ```bash
  grep -n "func (o \*Organizer) OrganizeBookDirectory" -A 10 internal/organizer/organizer.go
  ```
- Per-track file paths are NOT a field on `database.Book` — they live in the
  `book_files` table and are fetched via
  `imp.store.GetBookFiles(book.ID) ([]database.BookFile, error)`. Each
  `database.BookFile` has a `FilePath string` field carrying the on-disk path
  for that segment. See existing usage pattern:
  ```bash
  grep -n "GetBookFiles" internal/itunes/service/importer.go
  ```

## Step-by-step

1. Re-run the greps above to confirm current line numbers and signatures
   (they drift — do not trust the numbers in this brief).
2. In `organizeOneBook` (`internal/itunes/service/importer.go`), before calling
   `org.OrganizeBook(book)`, fetch the book's files via
   `imp.store.GetBookFiles(book.ID)`.
3. If `len(files) > 1`, build a `[]string` of `f.FilePath` for each file (skip
   any empty `FilePath` entries defensively) and call
   `org.OrganizeBookDirectory(book, segmentPaths)` instead of `org.OrganizeBook(book)`.
   - `OrganizeBookDirectory` returns `(targetDir string, pathMap map[string]string, err error)`.
     On success, update `book.FilePath` to `targetDir` (mirroring what the
     existing single-file branch does with `newPath`), and for each
     `oldPath -> newPath` in `pathMap`, update the corresponding `BookFile.FilePath`
     via the store's book-file update method (check `internal/database/iface_misc.go`
     for the right update call, e.g. `UpdateBookFile` or `BatchUpsertBookFiles` —
     use whichever existing method already updates a single file's path safely;
     grep `grep -n "UpdateBookFile\|BatchUpsertBookFiles" internal/database/iface_misc.go`
     to confirm the exact signature before calling it).
   - Log success/failure the same way the existing single-file branch does
     (`log.Info("Organized '%s' to %s", ...)` / propagate the error).
4. If `len(files) <= 1` (0 or 1 file), keep the existing behavior unchanged —
   call `org.OrganizeBook(book)` exactly as before. This change must be
   strictly additive for single-file books; do not alter their code path.
5. This is a non-destructive change: do not delete or move files yourself in
   this task beyond what `OrganizeBookDirectory` already does — you are only
   wiring the existing function in, not writing new file-move logic.
6. Add a test (in `internal/itunes/service/*_test.go`, following the pattern of
   the existing `organizeOneBook` tests in `importer_error_paths_test.go`) with
   a book that has 2+ `BookFiles` (mock/fake store returning multiple files)
   and assert `organizeOneBook` now calls the directory path and the book's
   `FilePath` ends up pointing at the new organized directory rather than
   erroring out or being left untouched.
7. Bump file headers on every changed file.

## How to test

```bash
go build ./...
go test ./internal/itunes/... -count=1
go vet ./internal/itunes/...
```

## Acceptance criteria

- [ ] `organizeOneBook` routes books with `len(BookFiles) > 1` to
      `OrganizeBookDirectory` with the correct segment paths.
- [ ] Books with 0 or 1 `BookFile` are completely unaffected (still use
      `OrganizeBook`).
- [ ] On success, `book.FilePath` and the per-file `BookFile.FilePath` rows are
      updated to reflect the new organized locations.
- [ ] Errors from `OrganizeBookDirectory` propagate the same way single-file
      errors do today (no silent swallow).
- [ ] New test proves a multi-file book now organizes (previously it silently
      stayed at `library_state = "imported"`); `go test ./internal/itunes/...`
      green; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
fix(itunes): route multi-file books to OrganizeBookDirectory (CONS-FRAG-2)

organizeOneBook always called the single-file OrganizeBook path, which safely
no-ops on a directory FilePath — so merged multi-file iTunes books never left
library_state=imported. OrganizeBookDirectory already existed but was never
wired in; route books with more than one BookFile through it using their
per-track paths from book_files.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dh-multifile-organize-directory
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `organizeOneBook` already branches on `len(files) > 1` and calls
`OrganizeBookDirectory`, this task is done — verify with
`grep -n "OrganizeBookDirectory" internal/itunes/service/importer.go`. Rollback
= revert the commit; multi-file books return to the prior (safe but no-op)
behavior.