<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/TASK-300-mergesplitbookcluster-performs-an-unguarded-read.md -->
<!-- version: 1.6.0 -->
<!-- guid: d762bd18-5f6a-4435-9b24-94c393890bb0 -->
<!-- last-edited: 2026-09-10 -->

# TASK-300 — MergeSplitBookCluster performs an unguarded read-modify-write on book/file rows -- it never takes the shared merge.LockMergeRMW, unlike every other merge-family path (DA-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DA-01` (audit_dedup_activity.json) · adversarial re-check 2026-09-10: **CONFIRMED**

**Priority:** P0 · **Effort:** S · **Recommended subagent:** Opus-class · dedup subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `DA-01` (audit_dedup_activity.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-300" -b agent/dedup-300-mergesplitbookcluster-performs-an-unguar origin/main
cd "$REPO/.worktrees/dedup-300"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Wrap MergeSplitBookCluster's body in merge.LockMergeRMW()/defer merge.UnlockMergeRMW(), mirroring dedup.MergeBooks (book_dedup.go:402-403); update serialize.go's doc comment to list it as the fourth guarded path.

Why it matters: merge/service.go's own CombineBooks doc comment (service.go:784-792) names the exact failure class this guards against: 'two combines -- or a combine racing a MergeBooks on a shared book -- can otherwise interleave GetBookByID -> MoveBookFilesToBook -> ReassignExternalIDs -> DeleteBook -> UpdateBook and corrupt the same way #1930 fixed for MergeBooks.' MergeSplitBookCluster does the identical MoveBookFilesToBook+UpdateBook+soft-delete shape on the identical book rows. A user manually merging/combining a book via the dedup review UI (merge.Service.MergeBooks/CombineBooks, both properly locked) while a split-book-bulk-merge op or the single-candidate split-book handler touches the same book id (as keepID or a srcID) can interleave writes with no lock protecting either side -- reproducing the #1930 corruption class (a book left both primary and soft-deleted, files orphaned mid-move, or a row hard/soft-deleted by one path while another is still mutating it).

## Background (verify before editing)

- MergeSplitBookCluster (split_book_merge.go:67-176) does GetBookByID -> GetBookFiles -> MoveBookFilesToBook -> UpdateBook -> merge.SoftDeleteBook with no locking at all -- grep for LockMergeRMW/UnlockMergeRMW in internal/dedup/*.go finds it ONLY in internal/dedup/book_dedup.go:402-403 (the `dedup.MergeBooks` function). internal/merge/serialize.go:10-33 documents the invariant explicitly: 'All three unguarded paths in the codebase acquire this one lock so any two of them are mutually exclusive on a shared book row' and enumerates exactly three: merge.Service.MergeBooks, merge.Service.CombineBooks, and dedup.MergeBooks (book_dedup.go). MergeSplitBookCluster is a FOURTH unguarded read-modify-write over the same book rows (added split_book_merge.go last-edited 2026-09-02, v1.5.0 -- after serialize.go's design was finalised 2026-07-13) and was never wired into the lock. It is reachable from two call sites: internal/plugins/dedup/split_book_bulk_merge.go:73 (the `dedup.split-book-bulk-merge` op, ConcurrencyKey='dedup.split-book-merge' -- serializes against ITSELF only) and internal/server/handlers/split_book.go:142 (a synchronous HTTP handler, no concurrency key at all). Neither call site takes merge.LockMergeRMW either.
- Anchor: `internal/dedup/split_book_merge.go:67` (audit `DA-01`, confidence high, severity critical).
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — split_book_merge.go:67-176 GetBookByID/GetBookFiles/MoveBookFilesToBook/UpdateBook/SoftDeleteBook with zero locking; LockMergeRMW only in book_dedup.go:402-403; serialize.go documents three guarded paths, this is a real fourth.
  - Blast radius: split_book_merge_test.go; POST /dedup/split-book-candidates/:id/merge (PermLibraryEditMetadata, wire_library_routes.go:46); bulk op split_book_bulk_merge.go:73 (ConcurrencyKey serializes only against itself). Racer: merge.Service.MergeBooks/CombineBooks or dedup.MergeBooks on the same book id.
  - Existing tests to extend: internal/dedup/split_book_merge_test.go
  - Standing-ban contact: none
  - Note: Route reachable and auth-gated; brief's fix is the minimal correct one.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/dedup/split_book_merge.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '61,73p' internal/dedup/split_book_merge.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '170,182p' internal/dedup/split_book_merge.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '396,409p' internal/dedup/book_dedup.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '4,39p' internal/merge/serialize.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '67,79p' internal/plugins/dedup/split_book_bulk_merge.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '136,148p' internal/server/handlers/split_book.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/dedup/split_book_merge.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_dedup_300.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if the fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving the dry-run / guard path writes nothing (fail-closed on error). A pure code change (lock, bound, check, propagated error) does not need this — do not add a dry-run surface to satisfy it.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_dedup_300.md`.

## Commit message

```
fix(dedup): MergeSplitBookCluster performs an unguarded read-modify-write on book/ (DA-01)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
