<!-- file: docs/agent-tasks/todo-completion/database/TASK-028-guard-author-delete-paths-with-an-unfiltered-aut.md -->
<!-- version: 1.0.0 -->
<!-- guid: a2f13937-bb2f-4dd8-a4b3-4279fa8a6de0 -->
<!-- last-edited: 2026-08-21 -->

# TASK-028 — Guard author delete paths with an unfiltered author-reference counter (twin of the series-delete fix) (TODO.md L3526)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · database subagent · **Why:** touches the database interface layer (memdb + Pebble + capability interface + mocks) and two production delete handlers; on a prod-data delete path, so needs careful review despite following an exact existing template · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3526 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`BulkDeleteAuthors` and `DeleteEmptyAuthor` deci" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-05.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-028-guard-author-delete-paths-with-an-unfiltered-aut" -b agent/database-028-guard-author-delete-paths-with-an-unfiltered-aut origin/main
cd "$REPO/.worktrees/database-028-guard-author-delete-paths-with-an-unfiltered-aut"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add database.AuthorBookRefStore (interface) with GetAllAuthorBookRefCounts() (map[int]int, error) counting EVERY book row that references an author — via both Book.AuthorID (legacy) and the book_authors junction table — in ANY state (trashed, non-primary included), implemented for both MemStore and PebbleStore, exposed via AsAuthorBookRefStore(s any) mirroring AsSeriesBookRefStore exactly. Then change DeleteAuthor and BulkDeleteAuthors in internal/server/handlers/entities/handler.go to guard on this unfiltered counter instead of GetBooksByAuthorIDCore, failing closed (refuse to delete) when the store cannot answer the unfiltered question, exactly as seriesRefCounts does.

## Background (verify before editing)

- internal/database/memdb_reads.go:493-524 already documents the exact bug class for authors: 'GetBooksByAuthorIDWithRoleCore is what merges and deletes consult to find the links they must rewrite before removing an author. For that caller a missed link is data loss.'
- internal/database/series_bookref.go is a complete, already-shipped fix for the identical bug on the series side (fixed for executeSeriesPrune per #2400, then for the series delete handlers)
- internal/database/memdb_reads.go:162-215 GetAllAuthorBookCounts is the FILTERED (primary+not-deleted) author counter — do not reuse it for delete guards; it is the display-badge counter, structurally similar (two-pass: book_authors junction, then legacy Book.AuthorID) but must NOT be used for existence checks
- internal/database/pebble_store_authors.go:504-505,463,481 already establish the Pebble key range for book_authors (`book_authors:` prefix, `~` upper bound) as a working pattern to copy
- internal/database/author_getter_conformance_test.go already exists with a memdb-vs-Pebble agreement pattern (TestGetBooksByAuthorIDCore_MemDBAndPebbleAgree) that caught an 86-vs-84 link divergence between the two backends previously

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'GetBooksByAuthorIDCore(authorID)' internal/server/handlers/entities/handler.go   # 1 hit, L585 — DeleteAuthor uses the listing getter as its existence check
  grep -n 'GetBooksByAuthorIDCore(id)' internal/server/handlers/entities/handler.go   # 1 hit, L616 — BulkDeleteAuthors uses the same listing getter per-ID in a loop
  grep -n 'GetBooksByAuthorIDWithRoleCore is what merges and deletes consult' internal/database/memdb_reads.go   # 1 hit ~L512 — the getter's own doc comment says it is wrong for delete callers
  grep -n 'func AsSeriesBookRefStore\|func (m \*MemStore) GetAllSeriesBookRefCounts\|func (p \*PebbleStore) GetAllSeriesBookRefCounts' internal/database/series_bookref.go   # 3 hits — the series twin of this exact bug already has a working fix and template
  grep -n 'func seriesRefCounts' internal/server/handlers/entities/series_refcount.go   # 1 hit, L33 — the series fix's handler-layer wrapper + fail-closed error message is a direct template
  grep -n 'book_authors:%s' internal/database/pebble_store_authors.go   # >=2 hits — the book_authors junction table exists with a Pebble key prefix 'book_authors:<bookID>'
  grep -n 'func Test' internal/database/author_getter_conformance_test.go   # >=3 hits including TestGetBooksByAuthorIDCore_MemDBAndPebbleAgree — a conformance test file for author getters already exists and is the place to add the memdb-vs-Pebble agreement test
  ```

### Reuse — don't invent

- Use `SeriesBookRefStore interface + AsSeriesBookRefStore capability accessor` in `internal/database/series_bookref.go` (verify: `grep -n 'type SeriesBookRefStore interface' internal/database/series_bookref.go`) — do NOT write a parallel helper.
- Use `MemStore.GetAllSeriesBookRefCounts (memdb scan template)` in `internal/database/series_bookref.go` (verify: `grep -n 'func (m \*MemStore) GetAllSeriesBookRefCounts' internal/database/series_bookref.go`) — do NOT write a parallel helper.
- Use `PebbleStore.getAllSeriesBookRefCountsPebble (Pebble scan template, book: key range)` in `internal/database/series_bookref.go` (verify: `grep -n 'func (p \*PebbleStore) getAllSeriesBookRefCountsPebble' internal/database/series_bookref.go`) — do NOT write a parallel helper.
- Use `seriesRefCounts handler-layer fail-closed wrapper` in `internal/server/handlers/entities/series_refcount.go` (verify: `grep -n 'func seriesRefCounts' internal/server/handlers/entities/series_refcount.go`) — do NOT write a parallel helper.
- Use `GetAllAuthorBookCounts (existing FILTERED author counter — do not reuse for delete, but its two-pass junction+legacy-field scan shape is the template for the unfiltered version)` in `internal/database/memdb_reads.go` (verify: `grep -n 'func (m \*MemStore) GetAllAuthorBookCounts' internal/database/memdb_reads.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/database/author_bookref.go modeled EXACTLY on series_bookref.go: define `type AuthorBookRefStore interface { GetAllAuthorBookRefCounts() (map[int]int, error) }` and `func AsAuthorBookRefStore(s any) AuthorBookRefStore { ... }` using database.AsCapability[AuthorBookRefStore](s), with a doc comment explaining the two book_authors-vs-legacy-AuthorID passes and NO deletion/primary-version filtering.
2. In the same file, implement `func (m *MemStore) GetAllAuthorBookRefCounts() (map[int]int, error)`: two-pass like GetAllAuthorBookCounts (memdb_reads.go:165-215) but WITHOUT the IsPrimaryVersion/bookIsSoftDeleted filters — Pass 1 scans memTableBookAuthors via txn.Get(memTableBookAuthors, memIdxID), counting every ba.AuthorID and tracking bookHasJunction[ba.BookID]=true; Pass 2 scans ALL books via txn.Get(memTableBooks, memIdxID) (not the memIdxIsPrimaryVersion index), skipping books already counted via junction, counting b.AuthorID for every remaining book with a non-nil AuthorID.
3. Implement `func (p *PebbleStore) GetAllAuthorBookRefCounts() (map[int]int, error)` delegating to `p.mem().GetAllAuthorBookRefCounts()` when p.UseMemDB && p.mem() != nil (mirror series_bookref.go:94-99), else calling a new `getAllAuthorBookRefCountsPebble()`.
4. Implement `getAllAuthorBookRefCountsPebble()`: two Pebble iterations — one over `book_authors:` prefix keys (mirroring pebble_store_authors.go:504-516, unmarshal []BookAuthor per key, count AuthorID, track bookID as junction-covered), one over `book:` prefix keys with exactly-one-colon guard (mirroring series_bookref.go:104-134) unmarshaling each Book, skipping junction-covered book IDs, counting non-nil AuthorID for the rest.
5. Add `GetAllAuthorBookRefCounts() (map[int]int, error)` to internal/database/mock_store.go's MockStore (as a func field, following the existing MockStore field pattern) and to internal/database/mocks/mock_store.go's generated mock (regenerate via the repo's mock-generation target if one exists, or hand-add following the file's existing pattern for GetAllSeriesBookRefCounts).
6. Create internal/server/handlers/entities/author_refcount.go with `func authorRefCounts(store any) (map[int]int, error)` modeled exactly on series_refcount.go:33-41 — call database.AsAuthorBookRefStore(store), return a wrapped error 'store cannot count unfiltered author references (got %T); refusing to delete from a filtered count...' when nil, else call GetAllAuthorBookRefCounts().
7. In internal/server/handlers/entities/handler.go's DeleteAuthor (L579-600), replace the `books, err := h.store.GetBooksByAuthorIDCore(authorID)` / `len(books) > 0` check with `refCounts, err := authorRefCounts(h.store)` / `if refCounts[authorID] > 0`, mirroring DeleteEmptySeries (handler.go:1003-1011) exactly, including the InternalError wording change to 'failed to count author references'.
8. In BulkDeleteAuthors (handler.go:604-640), replace the per-ID `h.store.GetBooksByAuthorIDCore(id)` call inside the loop with a SINGLE `refCounts, err := authorRefCounts(h.store)` call before the loop (mirroring BulkDeleteSeries, handler.go:1037-1041), then check `refCounts[id] > 0` inside the loop instead of re-querying per author.
9. Add a conformance test in internal/database/author_getter_conformance_test.go: TestGetAllAuthorBookRefCounts_MemDBAndPebbleAgree, seeding a mix of trashed/non-primary/junction-only/legacy-only books and asserting MemStore and PebbleStore backends produce identical ref-count maps.
10. Bump version headers on every touched file.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_028.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- author referenced only by a trashed book — must count as referenced (ref count > 0), same as the series fix
- author referenced only by a non-primary (duplicate) version's book — must count as referenced
- author referenced via BOTH the book_authors junction row and a stale legacy Book.AuthorID pointing at the same book — must not double count
- store does not implement AuthorBookRefStore (e.g. a narrow test double) — DeleteAuthor/BulkDeleteAuthors must fail closed with an InternalError, never silently fall back to the old filtered getter

## Tests

- internal/database/author_getter_conformance_test.go: TestGetAllAuthorBookRefCounts_MemDBAndPebbleAgree — memdb and Pebble backends agree on ref counts for a mixed corpus (trashed, non-primary, junction-table-only, legacy-AuthorID-only books)
- internal/database/author_bookref_test.go: TestGetAllAuthorBookRefCounts_CountsTrashedAndNonPrimary — an author whose only book is soft-deleted (or non-primary) has a ref count of 1, not 0 (this is THE bug being fixed — anti-over-suppression)
- internal/database/author_bookref_test.go: TestGetAllAuthorBookRefCounts_NoDoubleCounting — a book present in BOTH the book_authors junction AND with a matching legacy AuthorID is counted exactly once
- internal/server/handlers/entities/handler_test.go: new test asserting DeleteAuthor returns 409/conflict for an author whose only book is trashed (previously this incorrectly succeeded and deleted the author)
- internal/server/handlers/entities/handler_test.go: new test asserting DeleteAuthor still succeeds for a genuinely zero-book author (happy path, proves the fix didn't just make delete always fail)

Anti-over-suppression test: `TestGetAllAuthorBookRefCounts_CountsTrashedAndNonPrimary + TestDeleteAuthor_ZeroBookAuthorStillDeletable (proves the fix doesn't make every delete fail)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/handlers/entities/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/database/... -run AuthorBookRef` passes
- [ ] `go test ./internal/server/handlers/entities/... -run DeleteAuthor` passes
- [ ] `grep -n 'GetBooksByAuthorIDCore' internal/server/handlers/entities/handler.go` no longer matches inside DeleteAuthor or BulkDeleteAuthors (GetAuthorBooks at L649 legitimately keeps using it — that's a display listing, not a delete guard)
- [ ] make ci passes
- [ ] Anti-over-suppression test: `TestGetAllAuthorBookRefCounts_CountsTrashedAndNonPrimary + TestDeleteAuthor_ZeroBookAuthorStillDeletable (proves the fix doesn't make every delete fail)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/handlers/entities/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_028.md`.

## Commit message

```
fix(database): Guard author delete paths with an unfiltered author-referenc (TODO L3526)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is the author-side twin the TODO explicitly asks for; the series-side fix (internal/database/series_bookref.go, internal/server/handlers/entities/series_refcount.go) is a nearly line-for-line template. Standing repo rule 'never delete files/rows in any repair' does not apply here — DeleteAuthor already only ever deletes a genuinely-zero-referenced author row; this task fixes what counts as zero, it does not add new deletion capability.
