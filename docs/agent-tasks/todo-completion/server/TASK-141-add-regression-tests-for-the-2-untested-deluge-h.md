<!-- file: docs/agent-tasks/todo-completion/server/TASK-141-add-regression-tests-for-the-2-untested-deluge-h.md -->
<!-- version: 1.0.0 -->
<!-- guid: 27683d63-1fdd-46f3-836e-ea0823cb38ec -->
<!-- last-edited: 2026-08-21 -->

# TASK-141 — Add regression tests for the 2 untested deluge hydrate sites (TODO.md L10525)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server subagent · **Why:** mechanical: mirror an existing, adjacent test pattern for 2 more call sites · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10525 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Regression tests for the 2 untested deluge hydra" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-14.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-141-add-regression-tests-for-the-2-untested-deluge-h" -b agent/server-141-add-regression-tests-for-the-2-untested-deluge-h origin/main
cd "$REPO/.worktrees/server-141-add-regression-tests-for-the-2-untested-deluge-h"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a regression test for each of the 2 untested hydrate-then-import call sites so a hydrate failure (GetBookFileByID returning an error or nil) is exercised: (1) internal/server/deluge_discovery.go's handleDiscoveryImport (line ~167), and (2) internal/maintenance/jobs/bulk_deluge_import.go's bulkDelugeImportJob.Run (line ~96). Each test should assert the failure path records an error result and does not proceed to ImportToLibrary.

## Background (verify before editing)

- handleDiscoveryImport (internal/server/deluge_discovery.go:115) iterates pending files and hydrates each with store.GetBookFileByID(f.BookID, f.ID) before calling delugeclient.ImportToLibrary; on hydrate error it appends a result with Error set and increments failed, per the comment at deluge_discovery.go:161-165.
- bulkDelugeImportJob.Run (internal/maintenance/jobs/bulk_deluge_import.go:43) has the identical hydrate-then-import shape at lines 96-102.
- internal/plugins/deluge/centralization_test.go:72 already demonstrates the mocking pattern (MockStore.GetBookFileByIDFunc returning an error) for the 3rd, already-tested site — mirror it exactly.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "GetBookFileByID" internal/server/deluge_discovery_test.go   # 0 hits — deluge_discovery_test.go never mocks GetBookFileByID
  ls internal/maintenance/jobs/bulk_deluge_import_test.go   # 0 hits — bulk_deluge_import_test.go does not exist (ls exits 1 with "No such file or directory") — bulk_deluge_import.go has no companion test file
  grep -n "GetBookFileByIDFunc" internal/plugins/deluge/centralization_test.go   # 1 hit ~L72 — centralization.go's hydrate site IS already tested (control, not part of this item)
  ```

### Reuse — don't invent

- Use `MockStore.GetBookFileByIDFunc pattern already used for the tested 3rd site` in `internal/plugins/deluge/centralization_test.go` (verify: `grep -n "GetBookFileByIDFunc" internal/plugins/deluge/centralization_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/deluge_discovery_test.go, add TestHandleDiscoveryImport_HydrateFailure: construct a MockStore whose GetBookFileByIDFunc returns (nil, error) for a pending file, call handleDiscoveryImport via its route/handler, and assert the response's failed count is 1 and the per-file result carries the error message (not silently dropped).
2. Create internal/maintenance/jobs/bulk_deluge_import_test.go (new file, needs a version header per CLAUDE.md file-header rules) with TestBulkDelugeImportJob_HydrateFailure: construct a JobStore mock whose GetBookFileByID returns nil/error for one pending file, run bulkDelugeImportJob.Run in dry-run=false mode, and assert the job logs/records the hydrate failure via reporter.Log or equivalent and does not call ImportToLibrary for that file.
3. For both new tests, also add a companion happy-path test if none exists exercising a successful hydrate (GetBookFileByID returns a full row) to avoid a false-negative-only test suite.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_141.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- hydrateErr nil but full == nil (book file not found without an error) must also hit the failure path per the existing `if hydrateErr != nil || full == nil` check — test both nil-error and non-nil-error variants.

## Tests

- internal/server/deluge_discovery_test.go: TestHandleDiscoveryImport_HydrateFailure — asserts failed=1 when hydrate errors.
- internal/maintenance/jobs/bulk_deluge_import_test.go: TestBulkDelugeImportJob_HydrateFailure — asserts the job does not call ImportToLibrary when hydrate fails.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/maintenance/jobs/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/... -run TestHandleDiscoveryImport_HydrateFailure` passes.
- [ ] `go test ./internal/maintenance/jobs/... -run TestBulkDelugeImportJob_HydrateFailure` passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/maintenance/jobs/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_141.md`.

## Commit message

```
feat(server): Add regression tests for the 2 untested deluge hydrate sites (TODO L10525)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``go test ./internal/server/... -run TestHandleDiscoveryImport_HydrateFailure` passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

TODO item calls this 'optional' — low priority, small effort, safe for a Haiku-tier agent.
