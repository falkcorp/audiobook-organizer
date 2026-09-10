<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-331-deletebook-never-deletes-the-book-authors-book-n.md -->
<!-- version: 1.7.0 -->
<!-- guid: 6d8213c7-aff9-4742-a05a-b53945ac64b3 -->
<!-- last-edited: 2026-09-10 -->

# TASK-331 — DeleteBook never deletes the book_authors:/book_narrators: sidecar rows it created via SetBookAuthors/SetBookNarrators (SQ-03)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SQ-03` (audit_schema_queries.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · database subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SQ-03` (audit_schema_queries.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-331" -b agent/database-331-deletebook-never-deletes-the-book-author origin/main
cd "$REPO/.worktrees/database-331"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add `batch.Delete([]byte("book_authors:"+id), nil)` and `batch.Delete([]byte("book_narrators:"+id), nil)` to DeleteBook's batch, mirroring the bookSigKey/work/hash teardown already present in the same function.

Why it matters: This is the identical dangling-row class DeleteBook's own comments describe having been bitten by twice already in this exact function (work-ID index, three file-hash indexes) -- "added to this function after a writer got ahead of it". Every hard delete (purge, merge cleanup, archive sweep, etc.) now permanently leaks one book_authors row and, when present, one book_narrators row. It is a storage leak that compounds with every purge (this codebase's own memory notes ~115K missing-file repoint operations and multiple purge/repoint sweeps at prod scale), and any future code path that ever calls GetBookAuthors(deletedID) directly (bypassing the GetBookByID existence check) will silently see a phantom credit list for a book that no longer exists.

## Background (verify before editing)

- SetBookAuthors (internal/database/pebble_store_authors.go:590-601) writes `book_authors:<bookID>`; SetBookNarrators (pebble_store_authors.go:911-929, same pattern) writes `book_narrators:<bookID>`. DeleteBook (pebble_store.go:3112-3291) deletes the main book key, the book_sig: sidecar (L3137, explicitly comparing itself to "the work/hash indexes below"), the path/versiongroup/work/hash-x3/ISBN-ASIN indexes, the embedding row, dedup candidates, and the chapters row -- but never touches `book_authors:<id>` or `book_narrators:<id>`. DeleteBookFromMemDB (internal/database/memdb_sync.go:255-278), called at the very end of DeleteBook (pebble_store.go:3289), DOES clear the in-memory memdb tables for both via txn.DeleteAll(memTableBookAuthors/...Narrators, memIdxBookID, bookID) -- so the in-memory projection is correct but the underlying persisted Pebble row is not.
- Anchor: `internal/database/pebble_store.go:3112` (audit `SQ-03`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/database/pebble_store.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '3106,3118p' internal/database/pebble_store.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '3283,3297p' internal/database/pebble_store.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '249,284p' internal/database/memdb_sync.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '584,607p' internal/database/pebble_store_authors.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/database/pebble_store.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_331.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_331.md`.

## Commit message

```
fix(database): DeleteBook never deletes the book_authors:/book_narrators: sidecar row (SQ-03)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
