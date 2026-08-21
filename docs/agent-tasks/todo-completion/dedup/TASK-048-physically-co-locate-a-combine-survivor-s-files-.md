<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-048-physically-co-locate-a-combine-survivor-s-files-.md -->
<!-- version: 1.0.0 -->
<!-- guid: f2112318-6557-442f-a1b7-9c5d353d580d -->
<!-- last-edited: 2026-08-21 -->

# TASK-048 — Physically co-locate a Combine survivor's files under RootDir after CombineBooks (AP-1b)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · dedup subagent · **Why:** touches the file-move/organize path on a prod-data operation (Combine); needs careful review of the RootDir-only gate · **Depends on:** none · **Wave:** 5 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10574 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**AP-1b — physically co-locate survivor's files af" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-14.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-048-physically-co-locate-a-combine-survivor-s-files-" -b agent/dedup-048-physically-co-locate-a-combine-survivor-s-files- origin/main
cd "$REPO/.worktrees/dedup-048-physically-co-locate-a-combine-survivor-s-files-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

After CombineBooks (internal/merge/service.go:336) reassigns all files to the survivor book in the DB, if (and only if) every one of the survivor's file paths is already under config.AppConfig.RootDir (using organizer.ensureUnderRoot or equivalent — do NOT move files that live in abooks/newbooks or any external staging dir), physically move them into one co-located folder by invoking the existing Organizer.OrganizeBook on the survivor. Leave files outside RootDir untouched, matching the TODO note 'user wants co-location only inside the library.'

## Background (verify before editing)

- CombineBooks' doc comment (service.go:328-332) already anticipates this: 'Run organize afterward to physically co-locate if desired' — this item automates that instead of requiring a manual follow-up organize run.
- organizer.ensureUnderRoot(fullPath, rootDir) at organizer.go:573 is the exact existing helper for checking a path is under RootDir.
- Organizer.OrganizeBook(book) at organizer.go:171 is the existing single-book physical-move entrypoint.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "DB-only: files stay where they are" internal/merge/service.go   # 1 hit ~L330 — CombineBooks is DB-only today and doesn't move files
  grep -n "func ensureUnderRoot" internal/organizer/organizer.go   # 1 hit ~L573 — ensureUnderRoot helper already exists to gate root-relative moves
  grep -n "func (o \*Organizer) OrganizeBook" internal/organizer/organizer.go   # 1 hit ~L171 — Organizer.OrganizeBook is the existing physical-move entrypoint to reuse
  ```

### Reuse — don't invent

- Use `organizer.Organizer.OrganizeBook — physical file-move/co-location logic` in `internal/organizer/organizer.go` (verify: `grep -n "func (o \*Organizer) OrganizeBook" internal/organizer/organizer.go`) — do NOT write a parallel helper.
- Use `organizer.ensureUnderRoot — the exact RootDir-only gate AP-1b needs` in `internal/organizer/organizer.go` (verify: `grep -n "func ensureUnderRoot" internal/organizer/organizer.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/merge/service.go's CombineBooks (~L336), after the existing file-reassignment loop and before returning CombineResult, fetch the survivor's current BookFiles and check each file's path against config.AppConfig.RootDir using organizer.ensureUnderRoot (or an equivalent all-under-root check) — if ANY file's path is NOT under RootDir, skip physical co-location entirely for this combine and return the DB-only result as today.
2. If ALL of the survivor's files pass the RootDir check, obtain/construct an *organizer.Organizer (check how the Service struct already has access to config/organizer dependencies, or thread one in via the constructor) and call OrganizeBook(survivorBook) to move all its files into one target folder.
3. Wire OrganizeBook's returned error into CombineResult — do not silently swallow a physical-move failure; the DB state (files already reassigned) must remain consistent even if the physical move partially fails, so log a clear warning and leave CombineResult indicating files were NOT moved rather than reporting success.
4. Add a CombineResult field (e.g. FilesCoLocated bool) so callers/UI can tell whether physical co-location happened.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_048.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Mixed set: some survivor files under RootDir, some not — per the TODO's 'inside RootDir only' framing, treat this as NOT eligible for co-location (require ALL files under root) rather than partially moving only some.
- OrganizeBook failing partway through (e.g. disk full) must not leave the DB and filesystem inconsistent — the DB file-path records must reflect wherever the files actually ended up, not the DB-only pre-move paths.

## Tests

- internal/merge/service_test.go (or a new co-location-specific test file): TestCombineBooks_CoLocatesFilesUnderRootDir — survivor files all under a temp RootDir, assert after Combine they're all in one folder.
- TestCombineBooks_SkipsCoLocationOutsideRootDir — one file path outside RootDir (e.g. under a separate newbooks/abooks temp dir), assert Combine still succeeds (DB reassignment) but files are NOT physically moved and CombineResult reflects that.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/merge/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/merge/... -run TestCombineBooks_CoLocatesFilesUnderRootDir` passes.
- [ ] `go test ./internal/merge/... -run TestCombineBooks_SkipsCoLocationOutsideRootDir` passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/merge/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_048.md`.

## Commit message

```
feat(dedup): Physically co-locate a Combine survivor's files under RootDi (AP-1b)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``go test ./internal/merge/... -run TestCombineBooks_CoLocatesFilesUnderRootDir` passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical because this touches CombineBooks, a prod-data merge/apply path with real file moves.
