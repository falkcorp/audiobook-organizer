<!-- file: docs/agent-tasks/todo-completion/server/TASK-133-regression-test-soft-deleting-an-indexed-book-mu.md -->
<!-- version: 1.0.0 -->
<!-- guid: 95fc08ca-98d9-4381-8657-21cccf4a6189 -->
<!-- last-edited: 2026-08-21 -->

# TASK-133 — Regression test: soft-deleting an indexed book must be unsearchable without a boot reconcile (TODO.md L4334)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** must be written to FAIL against the current buggy UpdateBook (proving the bug) and then PASS after L4329's fix — sequencing matters · **Depends on:** TASK-132 · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4334 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Regression test: soft-delete an indexed book, asse" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-133-regression-test-soft-deleting-an-indexed-book-mu" -b agent/server-133-regression-test-soft-deleting-an-indexed-book-mu origin/main
cd "$REPO/.worktrees/server-133-regression-test-soft-deleting-an-indexed-book-mu"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add TestIndexedStore_SoftDeleteIsUnsearchableWithoutReconcile to internal/server/indexed_store_test.go: index a book, soft-delete it via the indexedStore.UpdateBook path (mirroring internal/merge/service.go's SoftDeleteBook, i.e. set MarkedForDeletion=true and call store.UpdateBook), then assert a title-search probe (via the searchIndex directly, not through reconcileSearchIndexCoverage) returns zero hits — proving the immediate soft-delete path removes the doc rather than relying on the periodic reconciler to clean it up later.

## Background (verify before editing)

- This is deliberately a DIFFERENT test surface than L4192's TestSearchCoverage_StaleDocsAreDeleted, which tests the periodic reconciler's cleanup of ALREADY-stale docs. This test proves the immediate, synchronous-enqueue path (indexedStore.UpdateBook) itself does the right thing at soft-delete time, with no reconcile pass in between — that's the literal ask: 'assert a title probe returns nothing WITHOUT a boot reconcile.'
- Written correctly, this test FAILS at current HEAD (before L4329's fix) and PASSES after it — write both together as one change per L4329's steps, not as two separate PRs, since the test has no value without the fix and the fix has no proof without the test.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "SoftDelete" internal/server/indexed_store_test.go   # 0 hits — no existing soft-delete-related test in indexed_store_test.go
  grep -n "^func Test" internal/server/indexed_store_test.go   # >=1 existing hit establishing file conventions to mirror — the test file already exists as the natural home for this test
  ```

### Reuse — don't invent

- Use `existing indexed_store_test.go test setup helpers (fake/mock searchIndex + store)` in `internal/server/indexed_store_test.go` (verify: `grep -n "^func Test" internal/server/indexed_store_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/indexed_store_test.go, add a test that: (1) creates a book via a test server/indexedStore setup with a real or fake Bleve index, (2) waits for/drains the initial index-create to land (check existing tests in this file for the drain idiom, e.g. drainQueue mentioned in indexed_store.go's enqueueIndex comment), (3) soft-deletes the book by setting MarkedForDeletion=true and calling indexedStore.UpdateBook(id, updatedBook), (4) drains the index queue again, (5) searches the index directly for the book's exact title and asserts 0 hits — critically, WITHOUT calling reconcileSearchIndexCoverage or reconcileOnce at any point.
2. Also add the sibling happy-path guard test named in L4329's anti_over_suppression field: TestIndexedStoreUpdateBook_RestoreStillReindexes — soft-delete then restore (MarkedForDeletion true→false) via UpdateBook, drain, and assert the title search DOES find it again (1 hit), proving the fix didn't break the restore path search_coverage.go documents as correct.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_133.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The test must use the queue-drain mechanism already established in this test file (not a raw time.Sleep) to avoid flakiness — check how existing tests in indexed_store_test.go synchronize with the async index worker.

## Tests

- This item IS the test — see steps.

Anti-over-suppression test: `TestIndexedStoreUpdateBook_RestoreStillReindexes (must be added alongside the delete-path test, per L4329's own anti_over_suppression note, so the fix isn't validated one-sided)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] Before L4329's fix: this new test FAILS (search still finds the soft-deleted book).
- [ ] After L4329's fix: go test ./internal/server/... -run 'TestIndexedStore_SoftDeleteIsUnsearchableWithoutReconcile|TestIndexedStoreUpdateBook_RestoreStillReindexes' passes both.
- [ ] Anti-over-suppression test: `TestIndexedStoreUpdateBook_RestoreStillReindexes (must be added alongside the delete-path test, per L4329's own anti_over_suppression note, so the fix isn't validated one-sided)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_133.md`.

## Commit message

```
feat(server): Regression test: soft-deleting an indexed book must be unsea (TODO L4334)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`Before L4329's fix: this new test FAILS (search still finds the soft-deleted book).`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Implement this together with L4329 (todo_line 4329) as one PR — the test has no standalone value against the current buggy code beyond proving the bug exists, and the coordinator should not schedule these as two independently-mergeable tasks.
