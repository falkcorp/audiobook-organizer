<!-- file: docs/agent-tasks/todo-completion/itunes/TASK-062-internal-itunes-backfill-go-backfillexternalids-.md -->
<!-- version: 1.0.0 -->
<!-- guid: cf3d0092-4ea0-4cbe-979c-ad3ee866451c -->
<!-- last-edited: 2026-08-21 -->

# TASK-062 — internal/itunes/backfill.go BackfillExternalIDs: replace offset pagination with GetAllBooksFullFrom cursor (PERF-5)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · itunes subagent · **Why:** loop-restructuring across a function with error-handling nuance (H7 comment about not silently breaking on read failure) that must be preserved · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4208 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**PERF-5**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-062-internal-itunes-backfill-go-backfillexternalids-" -b agent/itunes-062-internal-itunes-backfill-go-backfillexternalids- origin/main
cd "$REPO/.worktrees/itunes-062-internal-itunes-backfill-go-backfillexternalids-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace BackfillExternalIDs's offset-pagination loop (internal/itunes/backfill.go:60-68) with GetAllBooksFullFrom's ID-cursor pagination, closing the same cross-page snapshot-swap window class already fixed elsewhere (AssignOrphanVGs bug, per the TODO item, and the L4107 walkers).

## Background (verify before editing)

- The loop currently does: offset:=0; for { books, err := store.GetAllBooksCore(10000, offset); ...; offset += 10000 } — vulnerable to rows shifting between calls on a live, mutable memdb-backed store.
- The existing H7 comment in this exact function (visible at HEAD) warns that a silent break on read error used to mask a partial backfill being marked 'done' — any rewrite MUST preserve the 'return fmt.Errorf(...)' behavior on read failure, not revert to a silent break.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "offset := 0" internal/itunes/backfill.go   # hit at L60 (first of two occurrences in the file) — BackfillExternalIDs offset-paginates over a mutable snapshot
  grep -n "func (p \*PebbleStore) GetAllBooksFullFrom" internal/database/pebble_store.go   # 1 hit at L590 — GetAllBooksFullFrom(afterID string, limit int) exists as the cursor-based replacement
  ```

### Reuse — don't invent

- Use `GetAllBooksFullFrom(afterID, limit) cursor pager` in `internal/database/pebble_store.go` (verify: `grep -n "func (p \*PebbleStore) GetAllBooksFullFrom" internal/database/pebble_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. Replace the `offset := 0` / `for { ... GetAllBooksCore(10000, offset) ... offset += 10000 }` loop (L60-~76) with a cursor loop: `afterID := ""` then `for { books, err := store.GetAllBooksFullFrom(afterID, 10000); if err != nil { return fmt.Errorf(...) }; if len(books) == 0 { break }; ... process ...; afterID = books[len(books)-1].ID }` — keep the existing ctx.Err() cancellation check and the H7 error-wrapping exactly as-is.
2. Confirm GetAllBooksFullFrom's ID ordering (ULID-lexicographic, ascending) makes 'books[len(books)-1].ID' a valid resume cursor — check its doc comment / TestGetAllBooksFullFrom_PaginatesPastDoubleLimit in internal/database/pebble_books_pagination_test.go for the exact contract.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_itunes_062.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book deleted mid-backfill (its ID no longer exists) must not break the cursor — GetAllBooksFullFrom's contract for an unknown/stale cursor should be checked against TestGetAllBooksFullFrom_UnknownCursorEndsIteration.

## Tests

- internal/itunes/backfill_test.go: add or adapt an existing backfill test to seed >10000 books via a fake store and assert every book is visited exactly once even when a book is inserted mid-backfill (mirroring the reconcile-loop tests' cross-page-mutation coverage, e.g. TestWarmupWriteLoss_ConcurrentWritesAllVisible's style of test).

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/itunes/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/itunes/... -run Backfill passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/itunes/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_itunes_062.md`.

## Commit message

```
refactor(itunes): internal/itunes/backfill.go BackfillExternalIDs: replace off (PERF-5)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

See part 2 (same todo_line) for the second, unnamed-in-TODO-but-identical-pattern loop at BackfillITunesTrackPIDs (lines 178-184 in the same file) — Fix It Right's depth rule says this should be fixed alongside part 1 rather than left as a sequential duplicate of the same bug.
