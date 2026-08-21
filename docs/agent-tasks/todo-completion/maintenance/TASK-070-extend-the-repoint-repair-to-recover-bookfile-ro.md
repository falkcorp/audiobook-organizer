<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-070-extend-the-repoint-repair-to-recover-bookfile-ro.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1d8a7a92-a624-4ce2-b074-0de3d0122130 -->
<!-- last-edited: 2026-08-21 -->

# TASK-070 — Extend the REPOINT repair to recover BookFile rows via Book.FilePath (the #2372 fallback shape), not just the padded-filename shape (TODO.md L642)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · maintenance subagent · **Why:** extends an existing production-critical repair op's candidate-derivation strategy; must not change the existing padded-filename candidate path or its RequireSizeMatch safety gate · **Depends on:** TASK-071 · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 642 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`BookFile.FilePath` rows point at files that do " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-070-extend-the-repoint-repair-to-recover-bookfile-ro" -b agent/maintenance-070-extend-the-repoint-repair-to-recover-bookfile-ro origin/main
cd "$REPO/.worktrees/maintenance-070-extend-the-repoint-repair-to-recover-bookfile-ro"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a second candidate source to maintenance.missing-file-repoint: when a BookFile.FilePath does not resolve, in addition to the existing track-slash/padded-filename transform, also try the owning Book's Book.FilePath (via GetBookByID) as a repoint candidate when it resolves to a regular file and (subject to the existing RequireSizeMatch gate) matches the row's recorded FileSize. Never delete; apply=false stays the default; this only widens what counts as a recoverable row.

## Background (verify before editing)

- TODO.md L642-L147: 16,130 books library-wide (33.7% of single-file books) have a BookFile.FilePath that does not resolve; 86/88 (97.7%) sampled have a Book.FilePath that IS a regular file on disk -- so this is recoverable, not data loss.
- PR #2372 already mitigated this INSIDE chapters-backfill only (chapters_backfill.go:370-394): it falls back to Book.FilePath when the BookFile path does not resolve, and increments recoveredViaBook. The TODO explicitly frames the row repair as still open: 'That is a workaround inside ONE op -- the stale rows are still stale, and every other consumer that resolves a file by stored path still degrades silently on them.'
- missing_file_repoint.go's docstring (lines 15-25) is scoped to a DIFFERENT bug shape (per-track subdirectory flattened into a padded filename) and explicitly frames itself as the general 'never delete, only rewrite FilePath' repair vehicle -- the natural place to add a second candidate strategy rather than building a new op.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ID:          "maintenance.missing-file-repoint"' internal/plugins/maintenance/missing_file_repoint.go   # 1 hit L132 — missing_file_repoint.go exists, apply=false default, never deletes
  grep -n 'book.FilePath\|Book.FilePath\|GetBookByID' internal/plugins/maintenance/missing_file_repoint.go   # 0 hits — its candidate derivation never consults Book.FilePath or GetBookByID
  grep -n 'recoveredViaBook' internal/plugins/maintenance/chapters_backfill.go   # >=3 hits — chapters_backfill.go already has the Book.FilePath fallback pattern to port
  grep -n 'probe-failed=16130' internal/plugins/maintenance/chapters_backfill.go   # 1 hit ~L376 — 16,130 books library-wide have a BookFile.FilePath that does not resolve
  ```

### Reuse — don't invent

- Use `chaptersBackfillResolves(path) os.Stat regular-file check` in `internal/plugins/maintenance/chapters_backfill.go` (verify: `grep -n 'func chaptersBackfillResolves' internal/plugins/maintenance/chapters_backfill.go`) — do NOT write a parallel helper.
- Use `candidateItem / RequireSizeMatch scaffolding already in missing_file_repoint.go` in `internal/plugins/maintenance/missing_file_repoint.go` (verify: `grep -n 'type candidateItem struct' internal/plugins/maintenance/missing_file_repoint.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read missing_file_repoint.go fully (lines ~250-360) to see the exact candidateItem / Phase-1-stat / Phase-2-apply structure before editing.
2. Add a `bookFilePathFallback bool` field (default false) or simply always attempt it as a second-priority candidate: after the existing track-slash-derived candidate check finds `NoCandidateBytes`, before giving up on that row, call `p.deps.OpsStore().GetBookByID(file.BookID)` and check `book.FilePath` with the same os.Stat-regular-file check as chaptersBackfillResolves (chapters_backfill.go:229-232).
3. Gate the Book.FilePath candidate through the SAME RequireSizeMatch logic already in the file (missing_file_repoint.go:57-59) -- do not weaken the existing safety check for the new source.
4. Add a `recoveredViaBookPath atomic.Int64` counter to the run summary, mirroring chaptersBackfillCounters.recoveredViaBook, so a dry-run report distinguishes which source rescued each row.
5. Document in the file's header comment that this is now a two-source repair (track-slash transform AND owning-book fallback), citing the 16,130/86-of-88 measurement from chapters_backfill.go as the second population's justification.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_070.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Book.FilePath itself does not resolve either (the 26.4% 'fully broken' population, 16,265 books): row stays unrecovered, counted under the existing no-candidate-bytes reason -- do NOT touch these (owner decision #12: they stay parked/untouched).
- Book.FilePath resolves but points at a DIRECTORY (multi-file book path used incorrectly): must be rejected the same way chaptersBackfillResolves rejects directories (IsRegular() check), never repointed onto a folder.
- Book.FilePath collides with another book's Book.FilePath (see L670, 6.8% of rows) -- do not extend this fallback further until that collision count is re-verified, per the TODO's own caution.

## Tests

- internal/plugins/maintenance/missing_file_repoint_test.go: new test TestMissingFileRepoint_RecoversViaBookFilePath -- a BookFile row whose FilePath does not exist and has no track-slash-shape candidate, but whose owning Book.FilePath resolves to a regular file matching the row's FileSize, is reported as recoverable in dry-run and rewritten under apply=true.
- New test TestMissingFileRepoint_BookFilePathSizeMismatch_NotRecovered -- same setup but Book.FilePath's file size disagrees with the row's FileSize: NOT repointed (anti-over-suppression / safety-preserving test).

Anti-over-suppression test: `TestMissingFileRepoint_BookFilePathSizeMismatch_NotRecovered` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestMissingFileRepoint passes.
- [ ] A dry run over a synthetic fixture with both candidate shapes reports both recoveredViaBookPath and the existing padded-filename recovery count > 0.
- [ ] Anti-over-suppression test: `TestMissingFileRepoint_BookFilePathSizeMismatch_NotRecovered` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_070.md`.

## Commit message

```
feat(maintenance): Extend the REPOINT repair to recover BookFile rows via Book. (TODO L642)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run TestMissingFileRepoint passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Owner decision #12 ('build the REPOINT repair with apply=false default; owner runs apply; the 16,265 fully-broken books stay untouched/parked') is satisfied in SHAPE by the existing op but not in POPULATION -- this item is the gap between the op that exists and the specific recovery path the TODO and decision #12 were actually written about.
