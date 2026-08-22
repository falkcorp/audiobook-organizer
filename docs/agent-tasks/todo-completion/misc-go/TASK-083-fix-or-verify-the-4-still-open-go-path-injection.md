<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-083-fix-or-verify-the-4-still-open-go-path-injection.md -->
<!-- version: 1.0.0 -->
<!-- guid: 30e9cd2c-f7cf-4fe3-9319-dde8f0ab8116 -->
<!-- last-edited: 2026-08-21 -->

# TASK-083 — Fix or verify the 4 still-open go/path-injection findings (1 of the original 5 is already suppressed) (SEC-CODEQL-BACKLOG)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · misc-go subagent · **Why:** 4 similar findings needing the same allow-list-gate pattern already used successfully in service_mutation.go — mechanical once the pattern is confirmed, but each site touches file-mutating code. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-083-fix-or-verify-the-4-still-open-go-path-injection" -b agent/misc-go-083-fix-or-verify-the-4-still-open-go-path-injection origin/main
cd "$REPO/.worktrees/misc-go-083-fix-or-verify-the-4-still-open-go-path-injection"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each of safe_operations.go (2 findings, around its rollback/restore-from-backup logic), assemble.go:272 area (listAudioFiles), and filesystem.go:271 area (the synchronous-scan fallback), determine the real current line of the flagged operation (line numbers have drifted since the alerts were filed), and either (a) confirm the path already passes through fileops.IsAllowedPath or an equivalent gate — same reasoning as the already-suppressed service_mutation.go:63 — and add a matching `// lgtm[go/path-injection]` comment citing the gate, or (b) if no such gate exists on that path, add one before the file operation.

## Background (verify before editing)

- 5 go/path-injection alerts were originally filed against safe_operations.go:122 (#1477), safe_operations.go:157 (#1478), assemble.go:272 (#1429), filesystem.go:271 (#1105), service_mutation.go:63 (#1104).
- service_mutation.go:63 already has `// lgtm[go/path-injection]` plus a comment: "absNewPath is gated by fileops.IsAllowedPath above; CodeQL does not model that custom allow-list barrier, so suppress the false positive." — this is the template to follow or refute for the other 4.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'lgtm\[go/path-injection\]' internal/audiobooks/service_mutation.go   # 1 hit, preceded by a comment citing fileops.IsAllowedPath as the validating gate — service_mutation.go's path-injection alert is already suppressed with justification
  grep -n 'os.Stat(op.backupPath)\|copyFile(op.backupPath' internal/fileops/safe_operations.go   # ≥2 hits, no lgtm suppression nearby — safe_operations.go still performs unchecked file writes in a rollback path
  ```

### Reuse — don't invent

- Use `fileops.IsAllowedPath` in `internal/fileops/*.go` (verify: `grep -rn 'func IsAllowedPath' internal/fileops/`) — do NOT write a parallel helper.

## Step-by-step

1. Re-locate the current line numbers: grep -n 'os.Stat\|copyFile' internal/fileops/safe_operations.go to find the rollback logic near the two originally-flagged lines.
2. Trace backward from each flagged operation to see whether op.backupPath / op.targetPath were validated through fileops.IsAllowedPath (or an equivalent) before this point in the call chain.
3. Do the same trace for internal/metadata/assemble.go's listAudioFiles (dirPath parameter) and internal/server/handlers/filesystem.go's synchronous-scan fallback (folder.Path).
4. For each of the 4, either add the lgtm suppression with a specific cited gate (matching the service_mutation.go template exactly), or add the missing fileops.IsAllowedPath check if none exists.
5. Do NOT touch service_mutation.go:63 — it is already correctly handled; re-verify only, do not re-suppress or modify.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_083.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- safe_operations.go's rollback path runs AFTER a failure has already occurred — adding a validation check there must not itself fail in a way that leaves the corrupt target file in place with no rollback attempted at all (the surrounding comments already describe this exact failure mode as a known prior bug).

## Tests

- For any site where a missing gate is added: a test asserting a path outside the allowed import-path set is rejected (mirror the pattern in internal/fileops's existing IsAllowedPath tests).

Anti-over-suppression test: `N/A — this class of finding is either fixed or suppressed-with-citation, not silenced with a bare nolint.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/fileops/... ./internal/metadata/... ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] make ci (Go) passes.
- [ ] grep -c 'lgtm\[go/path-injection\]' across internal/fileops/safe_operations.go internal/metadata/assemble.go internal/server/handlers/filesystem.go internal/audiobooks/service_mutation.go equals 5 once all 4 remaining are resolved (1 already present + 4 newly resolved).
- [ ] Anti-over-suppression test: `N/A — this class of finding is either fixed or suppressed-with-citation, not silenced with a bare nolint.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/fileops/... ./internal/metadata/... ./internal/server/handlers/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_083.md`.

## Commit message

```
fix(misc-go): Fix or verify the 4 still-open go/path-injection findings (1 (SEC-CODEQL-BACKLOG)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Read before the log-injection sweep per the item's suggested order, since these are on file-mutating paths.
