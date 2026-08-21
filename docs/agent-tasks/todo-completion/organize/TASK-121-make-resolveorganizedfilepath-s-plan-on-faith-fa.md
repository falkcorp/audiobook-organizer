<!-- file: docs/agent-tasks/todo-completion/organize/TASK-121-make-resolveorganizedfilepath-s-plan-on-faith-fa.md -->
<!-- version: 1.0.0 -->
<!-- guid: 67b42bbf-60cf-4fd9-8610-aac612dfaf6d -->
<!-- last-edited: 2026-08-21 -->

# TASK-121 — Make resolveOrganizedFilePath's plan-on-faith fallback loud and verify-before-write (TODO.md L4919)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · organize subagent · **Why:** Prod-data path (organize writes book_file rows from this) with a subtle three-way branch and an already-diagnosed but partially-superseded root cause (the segment_title_format bug that dominated the measured 71,954-row population was separately fixed in c54721c7) — needs careful reasoning about what's still live vs. already fixed, not mechanical. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4919 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Answer why the organizer recorded destination ro" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/organize-121-make-resolveorganizedfilepath-s-plan-on-faith-fa" -b agent/organize-121-make-resolveorganizedfilepath-s-plan-on-faith-fa origin/main
cd "$REPO/.worktrees/organize-121-make-resolveorganizedfilepath-s-plan-on-faith-fa"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Implement the three suggested fixes from docs/audits/2026-08-17-orphan-destination-rows-root-cause.md against internal/organizer/service.go's resolveOrganizedFilePath (L1325) and its single-file caller: (1) log.Warn when the third branch (neither target nor source exists) fires, so an unverified row is visible in logs rather than indistinguishable from a verified one; (2) before falling to 'take the plan on faith', try os.ReadDir on filepath.Dir(dstPath) and match by file size against the source's known size, recovering the row instead of orphaning it, when a same-size file exists under a different name; (3) in the single-file (non-directory book) branch that assigns newBF.FilePath = newPath directly with no disk check, add an os.Stat(newPath) verification (falling back to the pre-existing srcPath / bf.FilePath when it fails, mirroring the directory-book behavior) before trusting it.

## Background (verify before editing)

- docs/audits/2026-08-17-orphan-destination-rows-root-cause.md identifies internal/organizer/service.go's resolveOrganizedFilePath as creating book_file rows for paths that were never verified to exist, and lists three unfixed remediations.
- docs/audits/2026-08-17-missing-file-audit-full-population.md later found the DOMINANT measured cause of the 71,954-row population was a different, already-fixed bug (the segment_title_format default, fixed in commit c54721c7 per internal/organizer/pathbuild.go:139-158) — so this item is not about re-diagnosing the historical incident, it is about a still-live code path that can independently create the same class of unverified row going forward, which is exactly what the item text means by 'without it the rows come back'.
- The orphan-destination-rows doc's own caveat applies: it 'identifies a mechanism, not a measured population' — this is a code hardening task (verify-before-write, log-on-fallback), not a fresh data cleanup.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func resolveOrganizedFilePath' internal/organizer/service.go   # 1 hit, L1325 — resolveOrganizedFilePath's third branch returns the unverified planned path with no logging
  grep -n 'newBF.FilePath = resolveOrganizedFilePath' internal/organizer/service.go   # 1 hit, L1515 — the row is created unconditionally from this value
  grep -n 'newBF.FilePath = newPath' internal/organizer/service.go   # >=1 hit — the single-file branch has no disk check at all before assigning newPath
  ```

### Reuse — don't invent

- Use `logger.Logger (already threaded into resolveOrganizedFilePath as the log param)` in `internal/organizer/service.go` (verify: `grep -n 'func resolveOrganizedFilePath(srcPath string, planned map\[string\]string, log logger.Logger)' internal/organizer/service.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/organizer/service.go, in resolveOrganizedFilePath (L1325-1338), add a log.Warn call on the final fallback branch (the one that currently does `return dstPath` with no log), before the return, stating that neither target nor source exists and the row is being written from the plan unverified — mirror the existing L1334 log.Warn call's style and argument shape.
2. Still inside resolveOrganizedFilePath, before falling to the final unverified branch: call os.ReadDir(filepath.Dir(dstPath)); if it succeeds, look for an entry whose size (via entry.Info() / os.Stat) matches os.Stat(srcPath)'s size (only if srcPath itself still resolves an on-disk size from an earlier stat you must add, OR skip this size check if srcPath is also gone — in that case there is nothing to compare against and the branch must fall through to the log-and-return-dstPath path from the prior step). If a same-size match is found under a different name in that directory, return that recovered path instead of dstPath, and log.Info the recovery (old name vs. recovered name) so it's auditable.
3. Find the single-file (non-directory book) caller referenced by the audit doc ('} else if !isDir { newBF.FilePath = newPath }', in the same function as the newBF.FilePath = resolveOrganizedFilePath(...) call at L1515 — search the surrounding ~50 lines for the isDir branch). Wrap the newPath assignment with an os.Stat(newPath) check; on stat failure, fall back to the pre-existing value (book.FilePath / bf.FilePath, matching whatever the directory-book branch falls back to) and log.Warn identically to the directory-book third branch.
4. Run `go build ./internal/organizer/...` to confirm no compile errors from the new os.ReadDir/os.Stat usage (imports for os and path/filepath are already present in service.go per the grep above).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_organize_121.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- srcPath itself no longer resolvable when attempting the size-match recovery — there is nothing to compare sizes against, so skip straight to the log-and-return-unverified path rather than erroring.
- filepath.Dir(dstPath) not existing at all (os.ReadDir returns an error) — treat identically to 'no match found', fall through to the logged unverified return, do not panic or propagate the ReadDir error upward.
- Multiple same-size files in the target directory — the audit's own tier-2 analysis (in the sibling doc) warns that same-size/same-basename matching without further identity checks is a known false-positive risk; if more than one candidate matches by size, do NOT auto-recover (log and fall through to the unverified branch) rather than guessing.

## Tests

- internal/organizer/service_test.go (or a new resolveOrganizedFilePath_test.go in the same package) — TestResolveOrganizedFilePath_TargetExists: dstPath present on disk -> returns dstPath, no warning logged.
- TestResolveOrganizedFilePath_SourceExists: dstPath absent, srcPath present -> returns srcPath (existing behavior unchanged), one log.Warn call captured via a test logger/recorder.
- TestResolveOrganizedFilePath_NeitherExists_NoRecovery: both absent, directory has no size-matching sibling -> returns dstPath AND asserts a log.Warn was emitted (the new loud-fallback behavior) — this is the anti-over-suppression test: without it, a future refactor could silently drop the new warning and no test would catch it.
- TestResolveOrganizedFilePath_NeitherExists_RecoveredBySize: both absent, but the target directory contains a differently-named file of the same size as the source (simulate the #2479 scenario from the audit doc) -> returns the recovered sibling path, not dstPath, and logs the recovery at Info level.
- A test for the single-file branch's new os.Stat guard, in whatever existing test file exercises OrganizeOneBook/CreateOrganizedVersion for single-file books, asserting the book_file row is NOT created with a nonexistent newPath when the file failed to actually land there.

Anti-over-suppression test: `TestResolveOrganizedFilePath_NeitherExists_NoRecovery` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/organizer/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/organizer/... -run TestResolveOrganizedFilePath passes, including the new tests above
- [ ] grep -n 'log.Warn' internal/organizer/service.go shows a new call inside resolveOrganizedFilePath's final branch (previously only one log.Warn existed in the function, at the source-exists branch)
- [ ] make ci passes
- [ ] Anti-over-suppression test: `TestResolveOrganizedFilePath_NeitherExists_NoRecovery` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/organizer/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_organize_121.md`.

## Commit message

```
refactor(organize): Make resolveOrganizedFilePath's plan-on-faith fallback loud  (TODO L4919)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical because this writes book_file.FilePath on the organize path — a wrong recovery match (multiple same-size candidates) could point a row at the WRONG file, which is worse than the current status quo of an unverified-but-at-least-honest planned path. The 'do not auto-recover on ambiguous size match' edge case above is load-bearing, not optional.
