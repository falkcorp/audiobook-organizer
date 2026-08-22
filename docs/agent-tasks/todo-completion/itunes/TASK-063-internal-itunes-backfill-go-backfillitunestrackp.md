<!-- file: docs/agent-tasks/todo-completion/itunes/TASK-063-internal-itunes-backfill-go-backfillitunestrackp.md -->
<!-- version: 1.0.0 -->
<!-- guid: d03a2f29-2ebd-4122-bd96-a3ad7127ee62 -->
<!-- last-edited: 2026-08-21 -->

# TASK-063 — internal/itunes/backfill.go BackfillITunesTrackPIDs: same offset-pagination bug, not named in the TODO but identical pattern in the same file (PERF-5)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · itunes subagent · **Why:** same mechanical rewrite as part 1, smaller function · **Depends on:** TASK-062 · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4208 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**PERF-5**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-063-internal-itunes-backfill-go-backfillitunestrackp" -b agent/itunes-063-internal-itunes-backfill-go-backfillitunestrackp origin/main
cd "$REPO/.worktrees/itunes-063-internal-itunes-backfill-go-backfillitunestrackp"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Apply the identical cursor-pagination fix to BackfillITunesTrackPIDs's pidToBook/titleToBook index-build loop (internal/itunes/backfill.go:178-190ish), which has the same H7-style error-wrapping comment and the same cross-page-mutation exposure as part 1.

## Background (verify before editing)

- This loop builds an in-memory PID→book_id and lowercase-title→book_id index before streaming the iTunes XML — a book created or its PID changed between offset pages during this index build would be silently missed or double counted, same failure class as part 1.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "offset := 0" internal/itunes/backfill.go   # 2 hits total in the file, L60 and L178 — BackfillITunesTrackPIDs has its own offset-pagination loop building pidToBook/titleToBook indexes
  ```

### Reuse — don't invent

- Use `GetAllBooksFullFrom(afterID, limit) cursor pager` in `internal/database/pebble_store.go` (verify: `grep -n "func (p \*PebbleStore) GetAllBooksFullFrom" internal/database/pebble_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. Apply the same transformation described in part 1's steps to this second loop: afterID cursor instead of offset, preserving the existing `return 0, fmt.Errorf(...)` error wrap and the ctx.Err() cancellation check.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_itunes_063.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Same as part 1.

## Tests

- Extend the same backfill_test.go coverage from part 1 to also cover BackfillITunesTrackPIDs's index-build loop.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/itunes/... -run TrackPID passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/itunes/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_itunes_063.md`.

## Commit message

```
refactor(itunes): internal/itunes/backfill.go BackfillITunesTrackPIDs: same of (PERF-5)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Same file/PR as part 1 — do this as one combined change, not two separate PRs, since they're the identical bug pattern in the same file.
