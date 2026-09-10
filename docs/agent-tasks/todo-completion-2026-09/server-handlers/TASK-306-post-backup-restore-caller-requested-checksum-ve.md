<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-306-post-backup-restore-caller-requested-checksum-ve.md -->
<!-- version: 1.0.0 -->
<!-- guid: dd58d727-201e-4647-b364-871131314349 -->
<!-- last-edited: 2026-09-10 -->

# TASK-306 — POST /backup/restore: caller-requested checksum verification is silently skipped, no signal in the response (SV-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SV-02` (audit_server_handlers.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Opus-class · server-handlers subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `SV-02` (audit_server_handlers.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-306" -b agent/server-handlers-306-post-backup-restore-caller-requested-che origin/main
cd "$REPO/.worktrees/server-handlers-306"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Either implement the checksum verification, or return req.Verify's outcome explicitly in the response body (e.g. "verified": false, "verify_requested": true) so a caller parsing the response -- not just the server log -- can tell the difference.

Why it matters: A caller who explicitly asked to verify a restore (presumably because they suspect corruption, or because it's a disaster-recovery path where they cannot easily re-check afterward) gets a success response indistinguishable from a verified restore. This is the exact 'success path claims did what wasn't done' shape the mission is hunting for.

## Background (verify before editing)

- RestoreBackup (handler.go:663-710) accepts `Verify bool` in the request body. Lines 697-699: `if req.Verify { slog.Warn("backup restore: checksum verification requested but not yet implemented; proceeding without verification") }` -- then it proceeds to backup.RestoreBackup(backupPath, targetPath, req.Verify) regardless, and on success returns 200 {"message": "backup restored successfully", "target": targetPath} with no mention that verification was skipped. The warning only reaches a server-side log (PermSettingsManage-gated route, wire_system_routes.go:48), which the calling client/UI never sees.
- Anchor: `internal/server/handlers/system/handler.go:697` (audit `SV-02`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f internal/server/handlers/system/handler.go   # the file the finding is anchored to still exists
  sed -n '691,703p' internal/server/handlers/system/handler.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/server/handlers/system/handler.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_306.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving the dry-run / guard path writes nothing (fail-closed on error).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_306.md`.

## Commit message

```
fix(server-handlers): POST /backup/restore: caller-requested checksum verification is silent (SV-02)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
