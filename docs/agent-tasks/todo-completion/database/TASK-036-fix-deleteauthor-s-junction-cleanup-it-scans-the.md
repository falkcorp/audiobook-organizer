<!-- file: docs/agent-tasks/todo-completion/database/TASK-036-fix-deleteauthor-s-junction-cleanup-it-scans-the.md -->
<!-- version: 1.0.0 -->
<!-- guid: 628352be-d4d4-4959-9e7a-1e8ed01941d7 -->
<!-- last-edited: 2026-08-21 -->

# TASK-036 — Fix DeleteAuthor's junction cleanup: it scans the dead book_author: keyspace instead of book_authors: (TODO.md L5290)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · database subagent · **Why:** Bug fix on the prod-data author-deletion path with a clear existing pattern (GetAllAuthorBookCounts) to copy for the correct iteration, but must be careful to only rewrite affected books' junction rows, not all of them, and to keep it inside the existing batch/commit transaction. · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 5290 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`DeleteAuthor`'s junction cleanup is dead code.*" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-036-fix-deleteauthor-s-junction-cleanup-it-scans-the" -b agent/database-036-fix-deleteauthor-s-junction-cleanup-it-scans-the origin/main
cd "$REPO/.worktrees/database-036-fix-deleteauthor-s-junction-cleanup-it-scans-the"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In internal/database/pebble_store_authors.go's DeleteAuthor (~L157-207), replace the dead 'book_author:' singular-keyspace iteration (~L180-198) with a correct sweep of the 'book_authors:' plural keyspace: for every book_authors:<bookID> row, unmarshal its []BookAuthor array, and if any entry's AuthorID matches the author being deleted, rewrite that row (batch.Set) with the entry filtered out (or batch.Delete the row entirely if the filtered slice becomes empty), all within the same batch DeleteAuthor already commits.

## Background (verify before editing)

- DeleteAuthor currently deletes author:<id>, author:name:<name>, cascades alias deletion, then attempts junction cleanup by iterating book_author: (singular) — a keyspace nothing in the codebase writes to, and whose bounds ('book_author:' to 'book_author;') don't even overlap the real 'book_authors:' (plural) keyspace lexicographically in a way that would accidentally catch it.
- The real junction data is one JSON-array-valued key per book: book_authors:<bookID> -> []BookAuthor (GetBookAuthors/SetBookAuthors, L462-491).
- Consequence today: deleting an author who HAS books leaves them referenced inside every book's book_authors array — harmless for the empty-author purge (author_purge_empty.go only deletes zero-book authors, so there are no references by definition), but a live bug for ANY other future DeleteAuthor caller (e.g. an author-merge/dedup apply path).
- GetAllAuthorBookCounts (pebble_store_authors.go, iterates 'book_authors:' to 'book_authors:~') already demonstrates the correct iteration bounds and per-row []BookAuthor unmarshal pattern to copy.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'LowerBound: \[\]byte("book_author:")' internal/database/pebble_store_authors.go   # 1 hit, inside DeleteAuthor, ~L180 — DeleteAuthor iterates the dead singular book_author: keyspace
  grep -n 'func (p \*PebbleStore) GetBookAuthors\|func (p \*PebbleStore) SetBookAuthors' internal/database/pebble_store_authors.go   # 2 hits, L462 and L480 — the live junction data lives under the plural book_authors:<bookID> key, one JSON array per book
  grep -rn '"book_author:' internal/database/*.go   # hits only inside DeleteAuthor's own iterator bounds, no writer anywhere — nothing else in the store writes the singular book_author: keyspace DeleteAuthor reads from
  ```

### Reuse — don't invent

- Use `GetBookAuthors / SetBookAuthors (correct read-modify-write pair to use instead of the singular-keyspace iterator)` in `internal/database/pebble_store_authors.go` (verify: `grep -n 'func (p \*PebbleStore) GetBookAuthors' internal/database/pebble_store_authors.go`) — do NOT write a parallel helper.
- Use `the 'book_authors:' iteration bounds already used correctly elsewhere (GetAllAuthorBookCounts) as the pattern to copy for iterating every book's junction row` in `internal/database/pebble_store_authors.go` (verify: `grep -n 'LowerBound: \[\]byte("book_authors:")' internal/database/pebble_store_authors.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/database/pebble_store_authors.go's DeleteAuthor, replace the iterator's LowerBound/UpperBound from []byte("book_author:")/[]byte("book_author;") to []byte("book_authors:")/[]byte("book_authors:~") (matching GetAllAuthorBookCounts's bounds exactly, since '~' sorts after all ASCII bookID characters the way ';' does not reliably for this keyspace's actual key shape).
2. Change the per-row unmarshal from `var ba BookAuthor; json.Unmarshal(val, &ba)` to `var authors []BookAuthor; json.Unmarshal(val, &authors)`, since each row's value is now known to be an array, not a single struct.
3. For each row, filter out any entry whose AuthorID == id (the author being deleted). If the filtered slice is non-empty and differs in length from the original, batch.Set(iter.Key(), <remarshaled JSON of the filtered slice>) into the same batch DeleteAuthor already builds. If the filtered slice is empty, batch.Delete(iter.Key(), nil) instead (removing the book's now-empty junction row entirely, consistent with how SetBookAuthors([]BookAuthor{}) would be interpreted elsewhere — verify this by checking whether any reader treats an absent book_authors:<bookID> key differently from a present-but-empty-array one; if it matters, prefer Set with an empty array over Delete to stay conservative).
4. Keep everything inside the SAME batch that DeleteAuthor already commits at the end (do not introduce a second commit) — this preserves the existing all-or-nothing durability the rest of the function relies on.
5. After the batch commits, since SetBookAuthors normally also calls p.ReplaceBookAuthorsInMemDB(bookID, authors) to keep the in-memory index in sync (L489), DeleteAuthor must call the equivalent ReplaceBookAuthorsInMemDB(bookID, filteredAuthors) for every book row it actually modified, alongside its existing p.DeleteAuthorFromMemDB(id) call at the end of the function — otherwise the memdb read path (the common one when UseMemDB is true) will keep showing the deleted author on affected books even though Pebble itself is now correct.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_036.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book whose book_authors:<bookID> row is malformed/unparseable JSON — mirror DeleteAuthor's existing tolerance pattern seen elsewhere in the function (skip and continue rather than aborting the whole batch), since a single bad row should not block deleting an author from every OTHER book that references them.
- The author being deleted appears MULTIPLE times in the same book's BookAuthor array (e.g. as both primary and secondary credit, if that's a representable state) — filter out ALL matching entries, not just the first.
- Very large libraries: this iterates every book_authors: row on every DeleteAuthor call, same as GetAllAuthorBookCounts already does — acceptable since author_purge_empty.go already does a full-library file-count pass in the same op, but flag to the coordinator per the concurrency-mandatory CLAUDE.md rule if DeleteAuthor is ever called in a tight per-author loop (e.g. from a future bulk-merge op) rather than one-at-a-time as today's only caller (the empty-author purge) does.

## Tests

- internal/database/pebble_store_authors_test.go — TestDeleteAuthor_RemovesFromBookAuthorsJunction: create an author with 2 books via SetBookAuthors (one book with just this author, one book with this author plus a co-author), call DeleteAuthor, then assert: (a) GetBookAuthors on the single-author book returns an empty slice (or the row is gone, per whichever convention step 4 above settles on), and (b) GetBookAuthors on the two-author book returns only the co-author, not the deleted one.
- internal/database/pebble_store_authors_test.go — TestDeleteAuthor_NoJunctionRows_StillSucceeds: delete a zero-book author (the existing empty-author-purge case) and assert no error and no unintended side effects — this is the regression guard proving the fix doesn't break the case that was 'harmless' before.
- internal/database/pebble_store_authors_test.go — TestDeleteAuthor_MemDBReflectsJunctionRemoval: with UseMemDB enabled, repeat the two-book scenario and assert the memdb-backed GetBookAuthors read path (not just the raw Pebble read) also no longer shows the deleted author — this is the anti-over-suppression test: without it, a fix that correctly rewrites Pebble but forgets ReplaceBookAuthorsInMemDB would pass a Pebble-only test while remaining broken on the read path most of the app actually uses.

Anti-over-suppression test: `TestDeleteAuthor_MemDBReflectsJunctionRemoval` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/database/... -run TestDeleteAuthor passes, including the three new tests above
- [ ] make ci passes
- [ ] grep -n 'LowerBound: \[\]byte("book_authors:")' internal/database/pebble_store_authors.go shows a new hit inside DeleteAuthor (previously only GetAllAuthorBookCounts and similar had this bound; DeleteAuthor's old book_author: singular bound is gone)
- [ ] Anti-over-suppression test: `TestDeleteAuthor_MemDBReflectsJunctionRemoval` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_036.md`.

## Commit message

```
fix(database): Fix DeleteAuthor's junction cleanup: it scans the dead book_ (TODO L5290)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical: true because this is the author-deletion code path and a subtle bug in the rewrite (e.g. deleting a book's ENTIRE junction row when only one of several authors should be removed) would silently strip legitimate co-author credits from unrelated books. The 'filter, don't wipe' distinction in the steps above is load-bearing.
