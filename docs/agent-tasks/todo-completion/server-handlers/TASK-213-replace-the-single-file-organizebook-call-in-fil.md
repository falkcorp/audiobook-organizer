<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-213-replace-the-single-file-organizebook-call-in-fil.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e8bc5b6-4e28-43e8-be0d-d8c519ec1ddb -->
<!-- last-edited: 2026-08-21 -->

# TASK-213 — Replace the single-file OrganizeBook call in filesystem.go's auto-organize-after-browse block with OrganizeOneBook + counters (ORGANIZE-4TH-COPY)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** small, well-templated fix (copy an already-proven pattern from two sibling files) but touches a file-organize write path, warranting more care than a pure haiku mechanical edit · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2125 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ORGANIZE-4TH-COPY**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-213-replace-the-single-file-organizebook-call-in-fil" -b agent/server-handlers-213-replace-the-single-file-organizebook-call-in-fil origin/main
cd "$REPO/.worktrees/server-handlers-213-replace-the-single-file-organizebook-call-in-fil"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In internal/server/handlers/filesystem.go's auto-organize-after-browse block (the per-book loop currently calling org.OrganizeBook(dbBook) directly), replace the single-file OrganizeBook call with organizeService.OrganizeOneBook(org, dbBook, log) and add the same organized/failed/notInDB/lookupErrors counter tracking and completion log line that folder_autoscan_op.go:100-143 and server.go's AutoOrganizeFn (#2303) already use -- so a book whose file_path is a directory (a multi-file book, 'most of the library' per the TODO) organizes correctly instead of silently failing with a discarded, unlogged error on every bare `continue`.

## Background (verify before editing)

- This is the FOURTH copy of the same single-file/multi-file organize routing bug in this codebase, and per the TODO 'the worst-behaved of the four': unlike server.go's original copy (which at least logged a warning before the #2303 fix), this one discards the error via a bare `continue` with NO log at all, and collapses a DB lookup error and a missing row into the same `if err != nil || dbBook == nil` branch, hiding which one actually happened.
- The TODO's own five-site audit table records the other five callers of Organizer.OrganizeBook as CORRECT (including organizer/service.go:1000, which IS OrganizeOneBook's own single-file branch) -- do not touch any call site other than filesystem.go's, this fix is scoped to exactly one file.
- The TODO explicitly deferred this fix on purpose ('not fixed here on purpose... Wave 3 leaving it alone is the rule working, not an oversight') as part of a larger silent-failure-sweep wave plan with disjoint per-wave file sets, but also says to 're-rank it and fix it in Wave 12, or pull it forward on its own' -- this scout item treats it as pulled-forward standalone work, consistent with it being handed out individually in this scope file rather than as part of a wave batch.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '270,300p' internal/server/handlers/filesystem.go   # shows org.OrganizeBook(dbBook) inside a for-loop with `if err != nil { continue }` discarding the error with no log call — the defective single-file OrganizeBook call, in a per-book loop with a silent bare continue on error, is live at HEAD
  grep -n 'func.*OrganizeOneBook' internal/organizer/service.go   # 1 hit, L1220 — OrganizeOneBook -- the correct, multi-file-safe replacement -- already exists
  grep -n 'organized, failed, notInDB, lookupErrors' internal/server/folder_autoscan_op.go internal/server/server.go   # 2 hits: folder_autoscan_op.go:143 and server.go:983, each a fmt.Sprintf log line with all four counters — two sibling call sites already apply the correct fix with the exact counter pattern to copy
  ```

### Reuse — don't invent

- Use `organizeService.OrganizeOneBook(org, dbBook, log) -- the multi-file-safe organize call` in `internal/organizer/service.go` (verify: `grep -n 'func (orgSvc \*Service) OrganizeOneBook' internal/organizer/service.go`) — do NOT write a parallel helper.
- Use `the organized/failed/notInDB/lookupErrors counter-and-log pattern, verbatim reusable template` in `internal/server/folder_autoscan_op.go` (verify: `grep -n 'var failed, lookupErrors, notInDB int' internal/server/folder_autoscan_op.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/server/handlers/filesystem.go and locate the auto-organize block inside the folder-browse handler (the loop reading `for _, b := range books { dbBook, err := h.store.GetBookByFilePath(...) ... newPath, _, err := org.OrganizeBook(dbBook) ... }`, around line 270-300).
2. Add three int counters above the loop: `var failed, notInDB, lookupErrors int` (mirroring folder_autoscan_op.go:100), keeping the existing implicit `organized` tracking (or add an explicit `organized` counter if the loop does not already track it cleanly).
3. Split the collapsed `if err != nil || dbBook == nil { continue }` into two branches: on lookup err != nil, increment lookupErrors and log a warning (bounded, e.g. only the first 10 like folder_autoscan_op.go:105 does, to avoid flooding logs on a large failing scan) then continue; on dbBook == nil (no error but no row), increment notInDB and continue silently or with a debug-level log.
4. Replace `newPath, _, err := org.OrganizeBook(dbBook)` with `newPath, err := h.organizeService.OrganizeOneBook(org, dbBook, log)` -- check the exact field name for the organize service on the filesystem handler's receiver (h.*) by grepping the struct definition in this same file; if no such field exists yet, it needs to be added via the handler's constructor the same way s.organizeService is already available on the Server type used by folder_autoscan_op.go and server.go.
5. On OrganizeOneBook returning an error, increment failed, log a warning with the book title and error (matching folder_autoscan_op.go:128's message shape), and continue.
6. On success with newPath != dbBook.FilePath, keep the existing scanner.ApplyOrganizedFileMetadata + h.store.UpdateBook call, incrementing organized only on a successful UpdateBook (matching folder_autoscan_op.go:132-139's pattern of not counting a book as organized if the DB update itself fails).
7. After the loop, add a single summary log line: `slog.Info(fmt.Sprintf("Auto-organize complete: %d organized, %d failed, %d not in DB, %d lookup errors (of %d scanned)", organized, failed, notInDB, lookupErrors, len(books)))` (or whatever this file's existing logger convention is -- check whether it uses slog directly or a passed-in logger.Logger like folder_autoscan_op.go's scanLog).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_213.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A scan with zero books matching auto-organize criteria must still log the summary line with all-zero counters, not skip it (so 'nothing happened' is distinguishable from 'this endpoint no longer logs at all').
- Concurrency: this loop is NOT currently parallelized -- per CLAUDE.md's whole-library-scale concurrency mandate, check whether this specific loop's book count is realistically library-scale (it runs after a folder BROWSE, so likely scoped to one folder's contents, not the whole library) before deciding whether a worker pool is warranted here too; if the folder can itself be large, flag this as a related but SEPARATE concern from the routing-bug fix itself -- do not silently expand this item's scope to add concurrency unless the folder sizes observed in practice actually warrant it.

## Tests

- internal/server/handlers/filesystem_test.go: a new test seeding a multi-file book (file_path is a directory) alongside a single-file book in the same auto-organize-after-browse scan, asserting the multi-file book organizes successfully (previously it silently failed) and the single-file book's existing behavior is unchanged.
- A test asserting the completion log line reports correct counts when one book has a DB lookup error, one has no DB row, one organizes successfully, and one fails organize -- covering all four counter branches distinctly (this is the anti-over-suppression check: a bug that collapses two distinct non-events into one counter is exactly what this fix corrects, so the test must prove they stay distinguishable).

Anti-over-suppression test: `the distinct-counter test described above (lookupErrors vs notInDB vs failed vs organized, each independently verifiable) is the anti-over-suppression check for this item` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/server/handlers/... -run TestFilesystem -count=1 -v passes including the new multi-file-book-in-auto-organize test
- [ ] a book whose file_path is a directory, scanned through this endpoint with auto-organize enabled, no longer silently fails -- it either organizes correctly or is counted+logged as `failed` with a specific error message, never a bare unlogged continue
- [ ] go build ./... && go vet ./... exit 0
- [ ] Anti-over-suppression test: `the distinct-counter test described above (lookupErrors vs notInDB vs failed vs organized, each independently verifiable) is the anti-over-suppression check for this item` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_213.md`.

## Commit message

```
refactor(server-handlers): Replace the single-file OrganizeBook call in filesystem.go's (ORGANIZE-4TH-COPY)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: this is a file-organize (rename/move) write path bug fix -- an incorrect fix here could misfile books rather than merely mis-log them. Part of the larger silent-failure-sweep plan (Wave 12 per the TODO) but handed out here as a standalone pulled-forward fix; if the coordinator is also running a Wave 12 sweep separately, check for a collision on internal/server/handlers/filesystem.go before scheduling both.
