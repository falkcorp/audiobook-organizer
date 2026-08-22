<!-- file: docs/agent-tasks/todo-completion/database/TASK-023-investigate-then-evict-dirty-flag-merged-away-bo.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e718e52-54d0-4e76-a8be-893d31f3cd8e -->
<!-- last-edited: 2026-08-21 -->

# TASK-023 — Investigate then evict/dirty-flag merged-away book/file IDs from every read cache so losers stop appearing after a merge (MERGE-CACHE-EVICT)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · database subagent · **Why:** Correctness-critical, multi-layer (memdb + Bleve + version-group index + a not-yet-located file-level merge path), and requires resolving 4 explicitly unverified questions before the actual fix — not a mechanical diff. · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 1627 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**MERGE-CACHE-EVICT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-023-investigate-then-evict-dirty-flag-merged-away-bo" -b agent/database-023-investigate-then-evict-dirty-flag-merged-away-bo origin/main
cd "$REPO/.worktrees/database-023-investigate-then-evict-dirty-flag-merged-away-bo"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

First resolve the item's 4 NOT-established questions by reading code (does UpdateBook already enqueue into the #2268 search dirty-set; is a soft-deleted book deleted-from or merely re-indexed-with-a-flag in Bleve, and is that flag filtered pre- or post-pagination; where is the file-level merge path and does it share SoftDeleteBook; does the version-group read path de-duplicate). THEN implement whichever fix those answers point to (explicit evict call in MergeBooks, or a forced reconcile-on-merge, or both), such that after MergeBooks returns success, no read path (library list, search, version-group endpoint) serves a loser ID, verified with NO sleep/refresh in the test.

## Background (verify before editing)

- internal/merge/service.go:544's SoftDeleteBook sets MarkedForDeletion/MarkedForDeletionAt and calls store.UpdateBook, falling back to DeleteBook only if that write fails.
- internal/database/memdb_sync.go:123/182 (UpsertBookToMemDB/DeleteBookFromMemDB) is the memdb write-through path and already calls InvalidateLibraryStats — the item explicitly says memdb is the LEAST likely fault, do not start there.
- internal/search/ (bleve_index.go, index_builder.go) is where the Bleve index lives — zero references from internal/merge or internal/dedup confirmed by grep.
- internal/server/search_reconciler.go exists and may already self-heal the index on a reconcile pass (added per #2268 per the item) — whether UpdateBook enqueues into its dirty set is the FIRST unresolved question and determines whether this is a correctness bug (explicit evict needed) or a latency bug (force a reconcile pass on merge).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func (ms \*Service) MergeBooks\|func.*SoftDeleteBook' internal/merge/service.go   # hits confirming both functions exist — merge entry point exists at the cited location
  grep -n 'func (p \*PebbleStore) UpsertBookToMemDB\|func (p \*PebbleStore) DeleteBookFromMemDB' internal/database/memdb_sync.go   # 2 hits: L123 (UpsertBookToMemDB), L195 (DeleteBookFromMemDB) — UpsertBookToMemDB/DeleteBookFromMemDB write through memdb (InvalidateLibraryStats itself now lives on PebbleStore in pebble_store.go, not here)
  grep -n 'func.*InvalidateLibraryStats' internal/database/pebble_store.go   # 1 hit at L237 — InvalidateLibraryStats is defined on PebbleStore in pebble_store.go
  grep -rln 'IndexBook\|bleve' internal/merge internal/dedup --include="*.go" | grep -v _test.go   # 0 hits — merge/dedup packages have zero search-index references
  ```

### Reuse — don't invent

- Use `InvalidateLibraryStats (existing 'a write invalidates a derived read' precedent, defined on PebbleStore in pebble_store.go, not memdb_sync.go)` in `internal/database/pebble_store.go` (verify: `grep -n 'func.*InvalidateLibraryStats' internal/database/pebble_store.go`) — do NOT write a parallel helper.
- Use `search dirty-set / reconciler added in #2268 (check if UpdateBook already enqueues into it before assuming it doesn't)` in `internal/server/search_reconciler.go` (verify: `grep -rn 'dirty' internal/server/search_reconciler.go internal/database/memdb_sync.go`) — do NOT write a parallel helper.

## Step-by-step

1. Answer NOT-established question 1: read internal/database/memdb_sync.go's UpdateBook path fully to determine whether it enqueues into any search dirty-set introduced by #2268 (`git log --oneline --all -- internal/server/search_reconciler.go | grep -i 2268` or grep the dirty-set's enqueue function name and check every UpdateBook call path for it).
2. Answer question 2: read internal/search/bleve_index.go's delete/update path to see whether a soft-deleted book is removed from the index entirely or re-indexed carrying MarkedForDeletion=true, and read the search query path (internal/audiobooks/service_query.go's searchWithBleve, confirmed to exist) to see whether MarkedForDeletion is filtered before or after pagination — cross-reference the already-recorded post-filter-after-pagination defect if this is the same shape.
3. Answer question 3: locate the file-level merge path (several files merged into one book) — grep for a second merge entry point distinct from internal/merge/service.go:125 and internal/dedup/book_dedup.go:395 (e.g. search for a MergeFiles or similar under internal/merge or internal/dedup) and confirm/deny whether it reuses SoftDeleteBook.
4. Answer question 4: read GetBooksByVersionGroup's read path (its pointer-index fix from #2288) to determine if it independently de-duplicates or purely trusts the VersionGroupID index, which a merge writes.
5. Based on answers 1-4, implement the fix: if the dirty-set already covers UpdateBook (question 1 = yes), the fix may be as small as forcing a synchronous reconcile pass at the end of MergeBooks/SoftDeleteBook rather than waiting for the async reconciler. If not, add an explicit evict-from-index call inside SoftDeleteBook (and any file-level merge path found in step 3) immediately after the soft-delete write succeeds, before MergeBooks returns.
6. Apply the same fix to whichever file-level merge path was found in step 3, since the item explicitly says do not assume it shares SoftDeleteBook.
7. If the version-group read path (question 4) does not already handle a stale VersionGroupID pointer correctly post-merge, add the same evict/refresh there.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_023.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A merge that fails partway through (e.g. 3 of 5 losers soft-deleted before an error) must not leave the successfully-processed losers un-evicted just because the overall call returned an error — evict per-loser as each soft-delete succeeds, not only on overall success.
- Concurrent merges of overlapping version groups (two merges racing on the same group) — out of scope for this fix per the item, but note if the investigation surfaces it.

## Tests

- New integration test (per the item's own acceptance criteria): merge N books into a version group, then IMMEDIATELY (no sleep, no manual refresh call) re-query (a) the library list endpoint, (b) a search query that would have matched a loser, (c) the version-group endpoint — assert every loser ID appears in NONE of the three responses.
- A second test with the file-level merge path (once located in step 3) repeating the same immediate-requery assertion.
- Anti-suppression: a test with a sleep/delay confirming the reconciler ALSO eventually converges (if that's part of the design) — but this is NOT a substitute for the immediate-consistency test above, which is the one that matters per the item's explicit warning.

Anti-over-suppression test: `N/A — this is a correctness/visibility fix, not a filter; the anti-pattern to avoid here is the inverse: a test that passes only because it slept long enough for the reconciler, which the item explicitly calls out as 'measuring the reconciler, not the fix.'` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/merge/... ./internal/search/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The 3-endpoint immediate-requery test above passes with zero sleep/delay.
- [ ] `go test ./internal/merge/... ./internal/dedup/... ./internal/search/...` passes.
- [ ] Anti-over-suppression test: `N/A — this is a correctness/visibility fix, not a filter; the anti-pattern to avoid here is the inverse: a test that passes only because it slept long enough for the reconciler, which the item explicitly calls out as 'measuring the reconciler, not the fix.'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/merge/... ./internal/search/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_023.md`.

## Commit message

```
refactor(database): Investigate then evict/dirty-flag merged-away book/file IDs  (MERGE-CACHE-EVICT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: this is exactly the class of prod-data-path bug (merge/dedup apply visibility) called out in CLAUDE.md's review-critical definition. High owner-trust impact per the item ('a merge that visibly does nothing teaches the owner not to believe the merge button').
