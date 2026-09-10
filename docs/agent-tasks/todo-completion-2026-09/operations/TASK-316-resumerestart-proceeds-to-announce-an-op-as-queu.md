<!-- file: docs/agent-tasks/todo-completion-2026-09/operations/TASK-316-resumerestart-proceeds-to-announce-an-op-as-queu.md -->
<!-- version: 1.0.0 -->
<!-- guid: 819b2d7c-59e6-4abd-a3ee-5346a5a0185b -->
<!-- last-edited: 2026-09-10 -->

# TASK-316 — resumeRestart proceeds to announce an op as "queued" (publishOpCreated + pingDispatch) even when ResetOperationV2ForResume fails to persist that status, producing a ghost op the dispatcher will never pick up (OPS-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `OPS-01` (audit_database_operations.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · operations subagent · **Depends on:** none · **Wave:** 1 

Source: Wave 3 audit finding `OPS-01` (audit_database_operations.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/operations-316" -b agent/operations-316-resumerestart-proceeds-to-announce-an-op origin/main
cd "$REPO/.worktrees/operations-316"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

On ResetOperationV2ForResume failure, either retry with backoff, or skip publishOpCreated/pingDispatch for that row and instead route it through the same resumeDrop/resumeAsk path used for other failure modes, so the UI and dispatcher state stay consistent with the store.

Why it matters: On a PebbleDB write failure during boot-time resume (disk pressure, compaction stall, etc.), this produces a phantom operation: visible in the UI as queued/about to run, but structurally unable to ever dispatch, with no further error surfaced anywhere. A user has no way to notice other than the op never starting.

## Background (verify before editing)

- In resumeRestart (lines 219-299): if `r.store.ResetOperationV2ForResume(row.ID)` fails, the code only logs a warning (lines 265-268) and falls through. It then unconditionally sets `row.Status = "queued"` on the in-memory struct (line 295) and calls `r.publishOpCreated(row, true)` (line 296) and `r.pingDispatch()` (line 298). The dispatcher (dispatcher.go:40, `dispatchCycle`) only ever dispatches ops returned by `r.store.ListQueuedOperationsV2()` -- a fresh DB read -- so if the DB row's status was never actually flipped to "queued" (the write failed), the op is invisible to the dispatcher forever, while every connected client was just told (via the published op.created event) that it is queued.
- Anchor: `internal/operations/registry/resume.go:265` (audit `OPS-01`, confidence medium, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f internal/operations/registry/resume.go   # the file the finding is anchored to still exists
  sed -n '259,271p' internal/operations/registry/resume.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/operations/registry/resume.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_operations_316.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_operations_316.md`.

## Commit message

```
fix(operations): resumeRestart proceeds to announce an op as "queued" (publishOpCreated (OPS-01)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing.

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
