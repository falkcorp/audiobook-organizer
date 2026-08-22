<!-- file: docs/agent-tasks/todo-completion/server/TASK-132-fix-indexedstore-updatebook-to-enqueue-a-bleve-d.md -->
<!-- version: 1.0.0 -->
<!-- guid: 32f7314f-2565-4109-88d1-782c1cc2e1e4 -->
<!-- last-edited: 2026-08-21 -->

# TASK-132 — Fix indexedStore.UpdateBook to enqueue a Bleve DELETE when the update is a soft-delete transition (TODO.md L4329)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** small, precise change on a decorator that sits on every book mutation in the app — must not regress the ordinary (non-soft-delete) update path · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4329 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Make the soft-delete transition enqueue a Bleve DE" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-132-fix-indexedstore-updatebook-to-enqueue-a-bleve-d" -b agent/server-132-fix-indexedstore-updatebook-to-enqueue-a-bleve-d origin/main
cd "$REPO/.worktrees/server-132-fix-indexedstore-updatebook-to-enqueue-a-bleve-d"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change indexedStore.UpdateBook (internal/server/indexed_store.go:68-74) so that when the updated book (the `b *database.Book` argument, or the `updated` return value) has MarkedForDeletion set true, it enqueues a Bleve DELETE (enqueueIndex(id, true)) instead of an upsert (enqueueIndex(id, false)) — closing the gap where soft-deleting a book currently re-indexes it into Bleve instead of removing it, which is what produced the 3,953-doc pollution the periodic reconciler (L4186) now has to clean up after the fact.

## Background (verify before editing)

- RestoreAudiobook's reindex-on-UpdateBook is explicitly correct and must be preserved: search_coverage.go's own comment states 'A book restored from soft-delete later is re-indexed by the restore path itself: RestoreAudiobook goes through store.UpdateBook, and the indexedStore decorator enqueues a reindex on every UpdateBook' — so the fix must branch on the RESULTING MarkedForDeletion state, not special-case 'is this a restore or a delete call', to keep both directions correct through the same code path.
- internal/merge/service.go's SoftDeleteBook(store BookWriter, bookID string) is the primary soft-delete entry point and goes through store.UpdateBook, meaning it is exactly the call path this fix must intercept.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func (s \*indexedStore) UpdateBook" internal/server/indexed_store.go   # 1 hit at L68, body shows enqueueIndex(id, false) unconditionally — UpdateBook always enqueues an upsert (delete=false), never checking the new MarkedForDeletion state
  grep -n "func (b \*Book) IsSoftDeleted" internal/database/book_visibility.go   # 1 hit — database.Book.IsSoftDeleted() is the existing reusable predicate
  grep -n "func (s \*indexedStore) DeleteBook" internal/server/indexed_store.go   # 1 hit ~L94, calls enqueueIndex(id, true) — DeleteBook already shows the correct delete=true call shape to mirror
  ```

### Reuse — don't invent

- Use `(*database.Book).IsSoftDeleted()` in `internal/database/book_visibility.go` (verify: `grep -n "func (b \*Book) IsSoftDeleted" internal/database/book_visibility.go`) — do NOT write a parallel helper.
- Use `s.enqueueIndex(bookID string, del bool)` in `internal/server/indexed_store.go` (verify: `grep -n "func (s \*Server) enqueueIndex" internal/server/indexed_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/indexed_store.go's UpdateBook (currently L68-74), after `updated, err := s.Store.UpdateBook(id, b)` succeeds, check whether the resulting book is soft-deleted: prefer `updated.IsSoftDeleted()` if `updated` is non-nil (it reflects the actual persisted state), falling back to `b.IsSoftDeleted()` if `updated` is nil but err is also nil (defensive; check what the underlying Store.UpdateBook actually returns on success before assuming updated is always non-nil).
2. If soft-deleted, call `s.server.enqueueIndex(id, true)` (delete); otherwise keep the existing `s.server.enqueueIndex(id, false)` (upsert) — a restore (MarkedForDeletion transitioning true→false) naturally falls into the upsert branch, preserving the documented-correct restore behavior.
3. Bump the file's version header (currently 1.4.0) and last-edited date per this repo's mandatory file-header rule.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_132.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book updated with an UNRELATED field change (title edit, tag edit) while MarkedForDeletion stays false throughout must still enqueue an upsert as before — only the true-transition changes behavior.
- A book that is ALREADY soft-deleted and gets a further metadata update (MarkedForDeletion stays true, some other field changes) should still enqueue a delete (idempotent — DeleteBook on an already-absent Bleve doc is documented as a no-op per search_coverage.go's own comment), not accidentally re-upsert it back into the index.

## Tests

- See L4334 (todo_line 4334) for the dedicated regression test this fix requires — write it as part of the same change, not deferred.

Anti-over-suppression test: `TestIndexedStoreUpdateBook_RestoreStillReindexes (L4334's sibling happy-path test — restore, i.e. MarkedForDeletion true→false via UpdateBook, must still enqueue an upsert, not a delete, guarding against an over-broad fix that breaks RestoreAudiobook)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/server/... -run IndexedStore passes, including the new soft-delete-enqueues-delete test from L4334.
- [ ] Manual/integration check: soft-delete a book via the API, then GET /api/v1/search?q=<its exact title> returns 0 hits without waiting for reconcileSearchIndexCoverage to run.
- [ ] Anti-over-suppression test: `TestIndexedStoreUpdateBook_RestoreStillReindexes (L4334's sibling happy-path test — restore, i.e. MarkedForDeletion true→false via UpdateBook, must still enqueue an upsert, not a delete, guarding against an over-broad fix that breaks RestoreAudiobook)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_132.md`.

## Commit message

```
fix(server): Fix indexedStore.UpdateBook to enqueue a Bleve DELETE when t (TODO L4329)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: this decorator sits on every book mutation server-wide; a mistake here either re-breaks the soft-delete leak (under-fix) or breaks restore/normal updates (over-fix).
