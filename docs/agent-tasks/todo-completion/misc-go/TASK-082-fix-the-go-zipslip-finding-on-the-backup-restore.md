<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-082-fix-the-go-zipslip-finding-on-the-backup-restore.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0fd14248-8491-4d27-af85-e3cbf501b08a -->
<!-- last-edited: 2026-08-21 -->

# TASK-082 — Fix the go/zipslip finding on the backup-restore extraction path (SEC-CODEQL-BACKLOG)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · misc-go subagent · **Why:** Well-understood, mechanical fix pattern (validate extracted path stays within the target directory) on a data-mutating restore path. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-082-fix-the-go-zipslip-finding-on-the-backup-restore" -b agent/misc-go-082-fix-the-go-zipslip-finding-on-the-backup-restore origin/main
cd "$REPO/.worktrees/misc-go-082-fix-the-go-zipslip-finding-on-the-backup-restore"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Before writing each extracted file in internal/backup/backup.go's restore loop, join header.Name against the target restore directory, clean the resulting path, and verify it is still lexically within the target directory (reject '../' escapes and absolute paths in header.Name) before creating the file — the standard Go zip-slip mitigation, applied here to a tar/gzip archive.

## Background (verify before editing)

- This is the restore path — a wrong answer here costs data (a maliciously or corruptly crafted backup archive could write outside the intended restore directory), not just log noise, per the item's own framing of why this alert is worth reading before the log-injection sweep.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '265,285p' internal/backup/backup.go   # tar.NewReader / tarReader.Next() loop with no filepath.Clean/prefix-check visible in this window — the restore path extracts tar entries by their archive-supplied name
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open internal/backup/backup.go and read the full extraction loop starting at the tar.NewReader call (~line 269) through wherever it calls os.Create/os.OpenFile for each entry.
2. Immediately after `header, err := tarReader.Next()`, compute `targetPath := filepath.Join(restoreDir, header.Name)`.
3. Add a check: `if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(restoreDir)+string(os.PathSeparator))` (or equivalent using filepath.Rel and checking for a leading '..') then skip the entry and record an error/warning rather than writing it.
4. Apply the same check to any directory-creation calls (header.Typeflag == tar.TypeDir) in the same loop, not just file writes.
5. Add `// lgtm[go/zipslip]` only if, after review, this turns out to already be safe for another reason discovered in step 1 — otherwise the fix in steps 2-4 is expected to close the alert.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_082.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A tar entry with an absolute path (header.Name starting with '/') must also be rejected, not just relative '../' traversal.
- Symlink entries (tar.TypeSymlink) pointing outside restoreDir are a related but distinct vector — check whether the current loop even follows/creates symlinks, and if so apply the same containment check to the link target.

## Tests

- internal/backup/backup_test.go: craft a tar.gz fixture with a header.Name of "../../etc/passwd" (or similar) and assert restore rejects/skips it rather than writing outside restoreDir.
- Existing restore-round-trip test (find it via grep -rn 'func Test.*Restore' internal/backup) must still pass with legitimate archive contents.

Anti-over-suppression test: `Test asserting a normal, non-malicious backup archive still restores all its files correctly after the fix.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/backup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./... and make ci pass.
- [ ] The new malicious-path test fails before the fix and passes after (verify by temporarily reverting the check).
- [ ] Anti-over-suppression test: `Test asserting a normal, non-malicious backup archive still restores all its files correctly after the fix.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/backup/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_082.md`.

## Commit message

```
fix(misc-go): Fix the go/zipslip finding on the backup-restore extraction  (SEC-CODEQL-BACKLOG)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Alert #13. Suggested to read before the log-injection sweep since it is on a file-mutating path.
