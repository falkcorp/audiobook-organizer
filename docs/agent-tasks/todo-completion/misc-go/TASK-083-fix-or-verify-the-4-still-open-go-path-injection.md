<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-083-fix-or-verify-the-4-still-open-go-path-injection.md -->
<!-- version: 2.0.0 -->
<!-- guid: 935ef220-7739-4aed-a9b3-42a761c667dc -->
<!-- last-edited: 2026-08-23 -->

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

> ## ⚠️ STATUS: PARTIALLY DONE. Rewritten 2026-08-23. Three claims below were FALSE.
>
> **PR #2781 merged but resolved only two of the four findings.** Do not treat
> this task as fresh, and do not re-triage what is already settled.
>
> | Alert | Location | State |
> |-------|----------|-------|
> | #1429 | `internal/metadata/assemble.go` | **DONE** — dismissed via API by #2781 |
> | #1105 | `internal/server/handlers/filesystem.go` | **DONE** — dismissed via API by #2781 |
> | **#1477** | `internal/fileops/safe_operations.go:134` | **OPEN — a REAL bug.** Your work. |
> | **#1478** | `internal/fileops/safe_operations.go:175` | **OPEN — a REAL bug.** Your work. |
>
> Re-confirm before starting (state, not memory):
> ```bash
> for n in 1429 1105 1477 1478; do printf '%s ' "$n"; \
>   gh api /repos/falkcorp/audiobook-organizer/code-scanning/alerts/$n \
>   -q '.state + " " + .most_recent_instance.location.path'; done
> ```
>
> **FALSE CLAIM 1 — "`lgtm[]` suppresses the finding".** It suppresses nothing
> in this repo; it is the legacy LGTM.com mechanism GitHub code scanning never
> adopted. Suppression here is a code-scanning **API dismissal**.
>
> **FALSE CLAIM 2 — "service_mutation.go:63 is already suppressed", used by this
> brief as the pattern to copy.** It is not. That line carries
> `// lgtm[go/path-injection]` **today**, and its alert **#1104** is still
> `open`. It is proof the mechanism does not work, not a template to follow:
> ```bash
> grep -n 'lgtm\[' internal/audiobooks/service_mutation.go
> gh api /repos/falkcorp/audiobook-organizer/code-scanning/alerts/1104 -q '.state'
> ```
>
> **FALSE CLAIM 3 — "#1477/#1478 just need a gate cited".** They are real bugs.
> `op.backupPath` is built by `safepath.Join(filepath.Dir(targetPath), …)`, so
> the containment ROOT is derived from the tainted value itself. `safepath.Join`
> is a lexical prefix check against whatever root it is handed, so rooting it at
> the taint proves nothing. Worked example:
> `targetPath = "foo/../../../etc/passwd"` → `filepath.Dir` → `"../../etc"` →
> `Join+Clean` → `"../../etc/.audiobook-backups"` → **the prefix check PASSES.**
>
> `safe_operations.go` already carries an accurate comment saying exactly this
> (added by #2781) and explicitly says "Left open deliberately; see TASK-083".
> Read it first — it names the real upstream constraints
> (`fileops.ValidateUserPath`, and the `IsAllowedPath` gate on the one
> request-controlled route into `Book.FilePath`) and notes that **neither
> resolves symlinks**. That symlink gap is part of your problem.
>
> **So: do NOT dismiss #1477/#1478. Fix them.**

## Goal

**Scope is now the two `safe_operations.go` findings only** (#1477, #1478).
`assemble.go` and `filesystem.go` were resolved by #2781 — leave them alone.

Make the backup path's containment root independent of the tainted input, so
that a `targetPath` containing traversal segments cannot relocate the backup
directory outside the library.

The fix is NOT a citation and NOT a dismissal. Concretely, it needs a
containment root that does not derive from `targetPath`:

1. Root containment at a **configured, trusted base** (the library root /
   `config.BackupDir` resolved against it), not at `filepath.Dir(targetPath)`.
2. Validate `targetPath` itself against that trusted root **before** deriving
   anything from it, and fail closed if it escapes.
3. Resolve symlinks (`filepath.EvalSymlinks`) before the containment check —
   the in-code comment records that neither existing upstream gate does this, so
   a symlink inside the library pointing out of it currently defeats both.

Use the repo's existing containment helper. Do **not** hand-roll a
`strings.HasPrefix` check: a lexical prefix says `/library-backup` is under
`/library`. That exact footgun is called out in TASK-048's notes and this repo
has hit it before.

## Background (verify before editing)

- 5 go/path-injection alerts were originally filed against safe_operations.go:122 (#1477), safe_operations.go:157 (#1478), assemble.go:272 (#1429), filesystem.go:271 (#1105), service_mutation.go:63 (#1104).
- service_mutation.go:63 has `// lgtm[go/path-injection]` plus a comment claiming "CodeQL does not model that custom allow-list barrier, so suppress the false positive." **This is NOT a template — it is the counter-example.** Its alert #1104 is still `open`, which proves the marker suppresses nothing. Read it only to understand what does not work. Note also that its `IsAllowedPath` gate does not resolve symlinks.

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
4. For #1477 and #1478 ONLY (the other two were resolved by #2781): implement the fix described in Goal — re-root containment at a trusted base independent of `targetPath`, validate `targetPath` against it before deriving from it, and resolve symlinks before checking. Do not add a suppression comment in place of the fix. Leave both alerts OPEN until the code change actually removes the flow; if CodeQL still flags them afterwards, report that rather than dismissing.
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

- [ ] Do NOT gate on `make ci` — it is red on `main` for pre-existing reasons
      unrelated to this task.
- [ ] Zero `lgtm[` markers added: `git diff | grep -c 'lgtm\['` returns 0. Any
      pre-existing marker you touch should be DELETED, not copied — it is inert
      and misleads the next reader.
- [ ] The containment root for `backupPath` no longer derives from
      `targetPath`. Demonstrate with a test, not an argument.
- [ ] **Negative control, run and pasted:** a test asserting that
      `targetPath = "foo/../../../etc/passwd"` (or equivalent) is REJECTED.
      Mutate the new guard to a no-op, re-run, and paste what it actually
      printed — a guard whose test passes with the guard disabled proves
      nothing. Commit before mutating; `git checkout` restores from the index.
- [ ] **Positive control:** a normal in-library `targetPath` still creates its
      backup and still rolls back successfully. A guard that refuses everything
      passes every negative test while breaking the feature.
- [ ] Symlink case covered: a symlink inside the library pointing outside it is
      rejected.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/fileops/... ./internal/metadata/... ./internal/server/handlers/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-23" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260823_misc-go_083.md` (NO file header — fragments are exempt).
- [ ] #1429 and #1105 were NOT touched — they are already resolved.

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
