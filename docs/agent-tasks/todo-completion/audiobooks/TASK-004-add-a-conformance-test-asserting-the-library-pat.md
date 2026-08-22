<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-004-add-a-conformance-test-asserting-the-library-pat.md -->
<!-- version: 1.0.0 -->
<!-- guid: a9956260-2ba7-4f0a-b162-6a9d5eb923ab -->
<!-- last-edited: 2026-08-21 -->

# TASK-004 — Add a conformance test asserting the library path and author path classify nil/true/false IsPrimaryVersion identically (TODO.md L3889)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · audiobooks subagent · **Why:** Requires understanding both call paths (library pushdown vs authorID branch + post-filter) well enough to build a fixture that actually exercises the divergent nil handling -- a naive fixture without a nil-flagged row would not catch the bug per the TODO's own warning. · **Depends on:** TASK-003 · **Wave:** 6 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3889 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Add a conformance test in the shape used by #2406/" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-004-add-a-conformance-test-asserting-the-library-pat" -b agent/audiobooks-004-add-a-conformance-test-asserting-the-library-pat origin/main
cd "$REPO/.worktrees/audiobooks-004-add-a-conformance-test-asserting-the-library-pat"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new test file that seeds one author with three books -- nil IsPrimaryVersion, explicit true, explicit false -- and asserts svc.GetAudiobooksWithTotal(...) returns the identical primary/non-primary classification whether called via the library path (authorID=nil, is_primary_version filter set) or the author path (authorID=that author, same filter).

## Background (verify before editing)

- TODO.md:3889-3892 explicitly requires 'one fixture containing a nil-flag book, an explicit-true book and an explicit-false book' and warns 'a fixture without a nil-flag row cannot catch this.'
- This test should be written to FAIL against pre-L3884 code (nil-as-false bug at service_query.go:346) and PASS after L3884's one-line fix -- write it before or alongside L3884 as the regression proof.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "^func Test" internal/database/book_visibility_conformance_test.go   # >=1 hit e.g. TestGetAllBooksCore_MemDBAndPebbleAgree — Existing conformance-test naming/structure precedent to follow
  grep -n "func (svc \*AudiobookService) GetAudiobooksWithTotal" internal/audiobooks/service_query.go   # 1 hit L41 — GetAudiobooksWithTotal is the single entry point exercising both the library (authorID=nil) and author (authorID!=nil) branches
  ```

### Reuse — don't invent

- Use `GetAudiobooksWithTotal` in `internal/audiobooks/service_query.go` (verify: `grep -n "func (svc \*AudiobookService) GetAudiobooksWithTotal" internal/audiobooks/service_query.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/audiobooks/service_query_isprimary_conformance_test.go using the same test-store setup helper other service_query_*_test.go files in this package use (check service_query_heavyfilter_sort_test.go for the setup pattern).
2. Seed one Author and three Books all linked to it: bookNil (IsPrimaryVersion left nil), bookTrue (IsPrimaryVersion=boolPtr(true)), bookFalse (IsPrimaryVersion=boolPtr(false)).
3. Call svc.GetAudiobooksWithTotal(ctx, 50, 0, "", nil, nil, ListFilters{IsPrimaryVersion: boolPtr(false)}) (library path, no authorID) and record which of the 3 books come back.
4. Call svc.GetAudiobooksWithTotal(ctx, 50, 0, "", &authorID, nil, ListFilters{IsPrimaryVersion: boolPtr(false)}) (author path) and record which of the 3 books come back.
5. Assert the two result sets are identical for is_primary_version=false, is_primary_version=true, and no filter.
6. Name the test TestIsPrimaryVersion_LibraryAndAuthorPathsAgree.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_004.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Book present via BOTH the legacy Book.AuthorID field and a BookAuthor junction row for the same author -- must not be double-counted (mirrors the existing dedup logic in getBooksByAuthorID).

## Tests

- internal/audiobooks/service_query_isprimary_conformance_test.go TestIsPrimaryVersion_LibraryAndAuthorPathsAgree -- the test itself IS the deliverable for this item.

Anti-over-suppression test: `TestIsPrimaryVersion_LibraryAndAuthorPathsAgree itself -- it is the anti-regression check for L3884's fix.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/audiobooks/... -run TestIsPrimaryVersion_LibraryAndAuthorPathsAgree -v exits 0 AFTER L3884's fix is applied.
- [ ] Temporarily reverting L3884's one-line change and re-running the test must FAIL (confirms the test actually exercises the bug).
- [ ] Anti-over-suppression test: `TestIsPrimaryVersion_LibraryAndAuthorPathsAgree itself -- it is the anti-regression check for L3884's fix.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_004.md`.

## Commit message

```
feat(audiobooks): Add a conformance test asserting the library path and author (TODO L3889)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`go test ./internal/audiobooks/... -run TestIsPrimaryVersion_LibraryAndAuthorPathsAgree -v exits 0 AFTER L3884's fix is applied.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Write this alongside or just before L3884 so it can be run against the buggy code first to confirm it actually fails (mutation-test discipline).
