<!-- file: docs/agent-tasks/abs-sync/TASK-06-chapter-persistence.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f556180-72f6-4cb5-8a23-9a255e6673d7 -->
<!-- last-edited: 2026-07-30 -->

# TASK-06 — Persisted `Chapter` type + Pebble keyspace + store methods (abs-sync Phase 4)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · go-backend subagent ·
**Why:** new Pebble keyspace + CRUD on `PebbleStore`, no existing test infra to reuse verbatim, needs
careful TDD around the empty-chapters and delete-on-book-delete edge cases ·
**Depends on:** none (Wave 1 — independent of TASK-01/02/08/10)

**Gate:** EXECUTE AUTONOMOUSLY (worktree → implement → PR → CI → merge). Nothing here is
destructive or STOP-FOR-HUMAN.

**File-ownership:** primarily a **new file**, `internal/database/pebble_store_chapters.go`
(+ `pebble_store_chapters_test.go`) — no other abs-sync task in the wave table
(`docs/agent-tasks/abs-sync/README.md`) touches this file. **Exception, called out explicitly:** this
task also adds **one additive `batch.Delete(...)` call** inside the existing
`(p *PebbleStore) DeleteBook` function in `internal/database/pebble_store.go` (NOT `store.go` — that
file stays untouched per the wave-table rule). This is a single mechanical line next to a dozen
sibling `batch.Delete` calls already in that function; re-verify no sibling abs-sync task has since
claimed `pebble_store.go` by running `grep -rln 'pebble_store\.go' docs/agent-tasks/abs-sync/TASK-*.md`
before merging — if another task's brief now lists it, coordinate a rebase instead of dropping the hook.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-chapter-persistence" -b agent/abs-sync-chapter-persistence origin/main
cd "$REPO/.worktrees/abs-sync-chapter-persistence"
git rebase origin/main
```

## Goal

Give the audiobookshelf-sync work a durable, per-book chapter list on `PebbleStore`: a new `Chapter`
type with the four fields real ABS requires, a Pebble keyspace to store the whole ordered list per
book, and `Get`/`Save`/`Delete` methods — fully implemented (PebbleDB is the only production DB;
CLAUDE.md + `docs/specs/2026-07-29-abs-sync-api-design.md` §2). This is a **pure persistence layer**:
it does not call ffprobe, does not touch the scanner, and does not know about
`internal/audioutil.ProbeChapters`/`SynthesizeChapters` — TASK-07 (a separate task, do not do its work
here) is the one that extracts chapters and calls what you build.

## Background (verify before editing)

Ground truth on the shape (from `docs/specs/2026-07-29-abs-sync-api-design.md` §1.8.5 item 7): a real
ABS 2.36.0 server's `chapters[]` requires **all four** of `id:Int`, `start`, `end`, `title` — chapter
`id` is an **Int** (array index) while every other id in the API is a String. Do not add optional
fields the spec doesn't require; do not use a `*int`/`omitempty` for `ID` — `0` is a legitimate first
chapter's ID, not "absent".

- **No `Chapter` type exists anywhere in `internal/database` today.** Verify:
  ```bash
  grep -rn "type Chapter struct" internal/database/*.go
  ```
  Expected: 0 hits (confirms you're not duplicating an existing type).
- **The store's package-level `Store` interface lives in `internal/database/store.go`, which this
  task must NOT edit** (wave-table rule; another task owns adjacent work there). This means the new
  methods you add exist ONLY on the concrete `*PebbleStore` type, not on the `Store` interface. That is
  intentional and matches this task's scope — TASK-07 (scanner) will consume them via a type
  assertion (`store.(*database.PebbleStore)`) rather than through the interface; that's TASK-07's
  problem to handle, not yours. Verify the interface's location and that you are not being asked to
  touch it:
  ```bash
  grep -n "type Store interface" internal/database/store.go
  ```
  Expected: 1 hit, `internal/database/store.go:18` (do not open this file for editing).
- **Model this on the existing `metadata_cache:` keyspace** — same shape of problem (one JSON blob per
  book, keyed by book ID, no secondary indexes, no migration entry needed). Read it first:
  ```bash
  sed -n '1,64p' internal/database/pebble_store_metadata_cache.go
  ```
  Expected shape: a `metadataCacheKey(bookID string) []byte` helper returning
  `[]byte("metadata_cache:" + bookID)`, then `Get`/`Put`/`Delete` using `p.db.Get`/`p.db.Set`/
  `p.db.Delete` directly (no iterator needed — this is a single-key-per-book blob, not a
  prefix-scanned list). Follow this exact pattern with a `chapters:` prefix instead of
  `metadata_cache:`. Confirm no prefix collision:
  ```bash
  grep -rn '"chapters:' internal/database/*.go
  ```
  Expected: 0 hits before you start.
- **New Pebble keyspaces do not need a migrations.go entry** — PebbleDB is schemaless key-value and
  `metadata_cache:`/`book_file:` were both added without one. Verify:
  ```bash
  grep -n "metadata_cache\|book_file:" internal/database/migrations.go
  ```
  Expected: 0 hits (confirms the precedent — do not add a migration for this either).
- **Production deletion really does reach `PebbleStore.DeleteBook`, even through the HTTP-level
  indexing decorator.** `internal/server/indexed_store.go` wraps the store in an `*indexedStore` at
  runtime (`server_lifecycle.go`, after Bleve search opens); it overrides `DeleteBook` but forwards
  to the embedded store before doing its own indexing work:
  ```bash
  grep -n "func (s \*indexedStore) DeleteBook" -A 6 internal/server/indexed_store.go
  ```
  Expected: the body calls `s.Store.DeleteBook(id)` first and only then schedules the Bleve delete —
  confirming your cascade fires in production regardless of this decorator.
- **The `DeleteBook` cascade you're hooking into.** Re-verify the function and its batch-commit anchor
  (line numbers drift; match by content, not number):
  ```bash
  grep -n "^func (p \*PebbleStore) DeleteBook" internal/database/pebble_store.go
  # Expected: 1 hit, currently ~pebble_store.go:2217
  grep -n "if err := batch.Commit(pebble.Sync); err != nil {" internal/database/pebble_store.go | head -5
  ```
  The function deletes ~10 secondary-index rows (path, version-group, work-ID, 3 file-hash rows, ISBN
  rows, an embedding row) via `batch.Delete([]byte(fmt.Sprintf(...)), nil)` calls, all before the final
  `batch.Commit(pebble.Sync)`. Note it currently does **not** call `DeleteBookFilesForBook` either —
  that's a pre-existing gap, not something to fix here; stay scoped to adding the one chapters-delete
  line in the same style.
- **Test harness precedent** — `internal/database` tests are white-box (`package database`) and can
  call `NewPebbleStore(tmpdir)` directly to get a concrete `*PebbleStore` (not the `Store` interface),
  so no type assertion is needed in this task's own tests:
  ```bash
  grep -n "^func NewPebbleStore" internal/database/pebble_store.go
  # Expected: 1 hit, func NewPebbleStore(path string) (*PebbleStore, error)
  sed -n '21,46p' internal/database/pebble_store_test.go
  ```
  That shows `setupPebbleTestDB(t)` (returns the widened `Store` interface, for other tests); prefer
  calling `NewPebbleStore(t.TempDir())` directly in your new test file so you get the concrete type and
  never need a cast.

## Step-by-step (TDD — failing test first)

1. Create `internal/database/pebble_store_chapters_test.go` (package `database`) with the file header
   and these failing tests first:
   - `TestGetChaptersForBook_Absent_ReturnsNilNil` — a fresh book ID with no chapters ever saved:
     `GetChaptersForBook` returns `(nil, nil)`, not an error.
   - `TestSaveAndGetChaptersForBook_RoundTrip` — save 6 chapters (mirror the real Odyssey fixture:
     `ID` 0-5, `StartSec`/`EndSec` from the m4b ground truth in
     `docs/specs/2026-07-29-abs-sync-api-design.md` §5b / `internal/audioutil/chapters_test.go`, titles
     like `"Chapter 1: odyssey_01_homer_butler_64kb"`), read them back, assert exact equality
     including order (do not re-sort on read — the caller controls order on write).
   - `TestSaveChaptersForBook_EmptySlice_DeletesExistingEntry` — save a non-empty list, then save
     `nil`/`[]Chapter{}` — assert a subsequent `GetChaptersForBook` returns `(nil, nil)`, i.e. saving
     empty is equivalent to deleting, not storing an empty JSON array blob.
   - `TestDeleteChaptersForBook_Idempotent` — delete on a book that never had chapters must not error.
   - `TestDeleteBook_CascadesChapters` — create a book via `store.CreateBook`, save chapters for its
     ID, call `store.DeleteBook(id)`, then assert `GetChaptersForBook(id)` returns `(nil, nil)`.
2. Run the tests, confirm they fail to compile (the methods don't exist yet) — this is the "confirms it
   fails for the right reason" step.
3. Create `internal/database/pebble_store_chapters.go` with the file header (fresh GUID — this is a new
   file) and:
   ```go
   package database

   // Chapter is a single navigable chapter in a book's playback timeline, persisted
   // per-book in Pebble. Mirrors the four fields a real Audiobookshelf 2.36.0 server
   // requires verbatim (docs/specs/2026-07-29-abs-sync-api-design.md §1.8.5 item 7):
   // chapters[] needs id:Int, start, end, title, with id being an Int index while
   // every other ABS id is a String. ID 0 is a valid first chapter, so it is a plain
   // int, never a pointer/omitempty.
   type Chapter struct {
       ID       int     `json:"id"`
       StartSec float64 `json:"start_sec"`
       EndSec   float64 `json:"end_sec"`
       Title    string  `json:"title"`
   }
   ```
   Then, following the `metadata_cache:` pattern exactly:
   - `chaptersKey(bookID string) []byte` → `[]byte("chapters:" + bookID)`.
   - `func (p *PebbleStore) GetChaptersForBook(bookID string) ([]Chapter, error)` — `p.db.Get`, return
     `(nil, nil)` on `pebble.ErrNotFound`, else unmarshal `[]Chapter` and return it (nil slice on empty
     stored array, which should never happen because of the rule below).
   - `func (p *PebbleStore) SaveChaptersForBook(bookID string, chapters []Chapter) error` — if
     `len(chapters) == 0`, call `DeleteChaptersForBook` and return; else `json.Marshal` and
     `p.db.Set(key, data, pebble.Sync)`.
   - `func (p *PebbleStore) DeleteChaptersForBook(bookID string) error` — `p.db.Delete(key,
     pebble.Sync)`; per Pebble semantics deleting an absent key is not an error, so no `ErrNotFound`
     special-casing is needed (mirror `DeleteMetadataCache`, which does the same).
4. Open `internal/database/pebble_store.go`, find `(p *PebbleStore) DeleteBook` (re-verified anchor
   above). Immediately before the final `if err := batch.Commit(pebble.Sync); err != nil {` in that
   function, add:
   ```go
   // Delete this book's persisted chapter list (abs-sync TASK-06).
   if err := batch.Delete([]byte("chapters:"+id), nil); err != nil {
       batch.Close()
       return err
   }
   ```
   Match the exact style of the sibling deletes immediately above it in the same function.
5. Run the tests from step 1 again — they must now pass for the right reason (not just "compiles").
6. Bump file version headers: `pebble_store_chapters.go`/`_test.go` are new (fresh GUIDs, version
   `1.0.0`); `pebble_store.go` gets its version bumped and `last-edited` updated, guid unchanged.
7. Add a changelog fragment at `changelog.d/20260730_060600_abs-sync-chapter-persistence.md`:
   ```markdown
   <!-- file: changelog.d/20260730_060600_abs-sync-chapter-persistence.md -->
   <!-- version: 1.0.0 -->
   <!-- guid: <run: uuidgen | tr '[:upper:]' '[:lower:]'> -->
   <!-- last-edited: 2026-07-30 -->

   ### Added

   - **Persisted chapter storage (abs-sync Phase 4).** Added a `database.Chapter` type and
     `PebbleStore.{Get,Save,Delete}ChaptersForBook` methods, storing one ordered chapter list per book
     under a new `chapters:<bookID>` Pebble key. Chapters are deleted when their book is deleted. Pure
     persistence layer — extraction (ffprobe) and scanner wiring land in a follow-up task.
   ```

## How to test

```bash
gofmt -l internal/database/pebble_store_chapters.go internal/database/pebble_store_chapters_test.go internal/database/pebble_store.go
# Expected: empty output (no unformatted files)
go vet ./internal/database/...
go test ./internal/database/... -run 'Chapter' -race -count=1 -v
go test ./internal/database/... -race -count=1
```

Paste the actual `-run 'Chapter' -v` output (all 5 tests from step 1, each `PASS`) and the full-package
`go test` summary line in the PR body.

## Acceptance criteria

- [ ] `internal/database/pebble_store_chapters.go` exists with `Chapter` type + 3 methods, fully
      implemented on `*PebbleStore` (no interface changes, `store.go` untouched)
- [ ] `grep -c "func (p \*PebbleStore) DeleteBook" internal/database/pebble_store.go` still returns 1
      (only the batch-delete line was added, no duplicate function)
- [ ] All 5 tests named in Step 1 pass with `-race`
- [ ] `go test ./internal/database/... -race -count=1` passes (no regression in the rest of the
      package)
- [ ] `gofmt -l` and `go vet` clean on every changed file
- [ ] File headers present and bumped (new files: fresh GUID, `1.0.0`; `pebble_store.go`: version
      bumped, guid unchanged)
- [ ] Changelog fragment added at the exact path in Step 7

## Commit message

```
feat(abs-sync): add persisted Chapter type + Pebble keyspace on PebbleStore

New chapters:<bookID> Pebble key stores one ordered chapter list per book via
GetChaptersForBook/SaveChaptersForBook/DeleteChaptersForBook, modeled on the
existing metadata_cache: single-blob-per-book pattern. DeleteBook now cascades
the chapter list. Pure persistence layer for docs/specs/2026-07-29-abs-sync-
api-design.md Phase 4 -- extraction and scanner wiring are a separate task.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/abs-sync-chapter-persistence
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "func (p \*PebbleStore) GetChaptersForBook" internal/database/pebble_store_chapters.go`
already hits, the transform is already done — run the acceptance checks instead of redoing the work.
Rollback = revert the single commit; `chapters:<bookID>` keys already written to a prod Pebble
instance become simply unread dead keys (harmless orphan data, not referenced by any other code path
since nothing in this task wires a caller), and the `DeleteBook` cascade line reverting is a no-op
removal of a delete-call, not a data-loss risk.
