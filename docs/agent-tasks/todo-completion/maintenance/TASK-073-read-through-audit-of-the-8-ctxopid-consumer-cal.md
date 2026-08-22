<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-073-read-through-audit-of-the-8-ctxopid-consumer-cal.md -->
<!-- version: 1.0.0 -->
<!-- guid: b4e587cc-2200-4153-b220-d6af6e080a21 -->
<!-- last-edited: 2026-08-21 -->

# TASK-073 — Read-through audit of the 8 ctxOpID consumer call sites now that op IDs actually arrive (TODO.md L4137)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · maintenance subagent · **Why:** requires reading 8 call sites plus their downstream CreateOperationChange consumers across several packages and reasoning about correctness under real (previously-always-empty) opID values — not mechanical · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4137 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Audit the eight `ctxOpID` consumers now that the" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-073-read-through-audit-of-the-8-ctxopid-consumer-cal" -b agent/maintenance-073-read-through-audit-of-the-8-ctxopid-consumer-cal origin/main
cd "$REPO/.worktrees/maintenance-073-read-through-audit-of-the-8-ctxopid-consumer-cal"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Read through each of the 8 ctxOpID call sites in internal/plugins/maintenance and trace opID downstream to its first CreateOperationChange call, confirming the payload built there (op_id field, change type, before/after snapshot) is correct now that opID is a real non-empty value for the first time in production — since these branches were previously silently skipped (opID==""), a wrong field name or nil-deref guarded only by 'if operationID != ""' would never have been exercised.

## Background (verify before editing)

- series.go:82 forwards opID into deps.ExecuteSeriesPrune (internal/plugins/maintenance/deps.go's ExecuteSeriesPrune interface method).
- opID is now non-empty in production because internal/server/op_run_context.go's opRunContextDecorator calls maintenanceplugin.WithOpID, wired via registry_wire.go:359 (see L4295 — that fix is already shipped and tested).
- CreateOperationChange itself is declared as an interface method in internal/plugins/maintenance/deps.go:114; the actual implementations live outside this package (internal/dedup, internal/organizer, internal/undo, internal/server) per grep -rln "CreateOperationChange(" .

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "opID := ctxOpID(ctx)" internal/plugins/maintenance/series.go   # 1 hit at L82 — series.go extracts opID and forwards it to ExecuteSeriesPrune
  grep -n "opID := ctxOpID(ctx)" internal/plugins/maintenance/cleanup.go   # 2 hits at L48, L120 — cleanup.go has two ctxOpID call sites
  grep -n "opID := ctxOpID(ctx)" internal/plugins/maintenance/write_back.go internal/plugins/maintenance/reconcile.go internal/plugins/maintenance/dedup_ops.go internal/plugins/maintenance/optimize.go internal/plugins/maintenance/metadata.go   # 5 hits, one per file at L59/L49/L124/L54/L121 respectively — write_back.go, reconcile.go, dedup_ops.go, optimize.go, metadata.go each have exactly one
  grep -rn "CreateOperationChange" internal/plugins/maintenance/*.go   # only the interface declaration in deps.go:114, confirming the actual write happens deeper (e.g. internal/dedup, internal/organizer, internal/undo) — CreateOperationChange itself is not called directly in these files — opID is threaded through to deps/executor functions
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. For each of series.go:82, cleanup.go:48, cleanup.go:120, write_back.go:59, reconcile.go:49, dedup_ops.go:124, optimize.go:54, metadata.go:121: read the function opID is passed into, follow it to wherever CreateOperationChange (or an equivalent recorder) is actually invoked, and confirm (a) opID is passed as the change's OperationID field (not silently dropped), (b) the branch guarded by 'if opID != ""' does not assume opID is always empty (e.g. no dead-code paths that were never reachable before), (c) no panic/nil-deref is possible now that this code path actually executes.
2. If a bug is found in any of the 8, fix it minimally in the same file (do not widen scope beyond the opID-correctness question) and add/adjust a unit test in that file's _test.go asserting CreateOperationChange is called with the expected OperationID.
3. If all 8 are clean, record that finding (which file/line, what was checked) rather than silently doing nothing — this task's whole point is that these branches were never exercised in prod before.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_073.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- opID == "" (context not decorated, e.g. a unit test that calls the Run function directly without going through opRunContextDecorator) must still behave as before: CreateOperationChange either skipped or called with an empty OperationID, whichever the pre-existing contract was — do not change that branch's behavior, only verify it.

## Tests

- internal/plugins/maintenance/series_test.go (or nearest existing _test.go for each of the 8 files): add/verify a test that runs the op with a context carrying a real opID (via maintenanceplugin.WithOpID) and asserts the recorded OperationChange's OperationID field equals that ID.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run OpID passes (or the nearest matching test name after tests are added).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_073.md`.

## Commit message

```
refactor(maintenance): Read-through audit of the 8 ctxOpID consumer call sites now  (TODO L4137)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true because series-prune and other maintenance ops here are the same family that deleted 326 series with zero audit trail on 2026-08-14 (see L4295's background) — a wrong opID payload here silently reintroduces the same class of bug.
