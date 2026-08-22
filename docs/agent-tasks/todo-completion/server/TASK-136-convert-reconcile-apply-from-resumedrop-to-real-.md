<!-- file: docs/agent-tasks/todo-completion/server/TASK-136-convert-reconcile-apply-from-resumedrop-to-real-.md -->
<!-- version: 1.0.0 -->
<!-- guid: b5205494-5c02-4147-a512-2a2815a0759b -->
<!-- last-edited: 2026-08-21 -->

# TASK-136 — Convert reconcile.apply from ResumeDrop to real checkpoint/resume (TODO.md L4575)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server subagent · **Why:** Same mechanical-but-careful conversion as part 1, applied to a second op whose params shape (Matches, a list of merge decisions) must round-trip through the checkpoint correctly to avoid re-applying or dropping merge decisions on resume. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4575 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Convert the remaining long-running `ResumeDrop` " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-136-convert-reconcile-apply-from-resumedrop-to-real-" -b agent/server-136-convert-reconcile-apply-from-resumedrop-to-real- origin/main
cd "$REPO/.worktrees/server-136-convert-reconcile-apply-from-resumedrop-to-real-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Convert reconcile.apply (internal/server/reconcile_ops.go) from ResumePolicy=ResumeDrop to real checkpoint/resume, following the exact same pattern as part 1 (metadata.batch-apply-cached): add a ResumeFrom field to reconcileApplyOpParams, switch to ResumeRestart, and wire ResumeFrom/CheckpointEvery/CheckpointStateFn through whatever RunItems (or equivalent per-item loop) drives the apply.

## Background (verify before editing)

- reconcile.apply is a merge/apply operation — it acts on the dedup 'apply matched reconcile decisions' path, which is explicitly in the review-critical 'dedup merge/apply' category per this scout's own instructions.
- This is the second of the three ops the owner decision (item 9) named for first conversion, alongside metadata.batch-apply-cached (part 1) and the full-library sweeps.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'RegisterReconcileApplyOp\|"reconcile.apply"' internal/server/reconcile_ops.go   # hits at L78 and L81 — reconcile.apply is registered in internal/server/reconcile_ops.go
  grep -n 'reconcileApplyOpParams' internal/server/reconcile.go internal/server/reconcile_ops.go   # >=2 hits — reconcile.apply is invoked with a LegacyOpID + Matches params shape today
  ```

### Reuse — don't invent

- Use `same chapters_backfill.go template as part 1` in `internal/plugins/maintenance/chapters_backfill.go` (verify: `grep -n 'ResumeFrom:\|CheckpointEvery:\|CheckpointStateFn:' internal/plugins/maintenance/chapters_backfill.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/server/reconcile_ops.go:78-130 in full to find the actual per-item loop reconcile.apply uses today (confirm whether it already calls opsregistry.RunItems like batch_apply_op.go, or has a different shape — the item text calling this out separately from batch-apply-cached suggests it may not yet use RunItems at all, in which case converting it to RunItems is itself part of the prerequisite work, not just adding the three resume fields).
2. If it does not yet use RunItems: convert its Matches-processing loop to opsregistry.RunItems first (bounded concurrency, per-item error collection), following the batch_apply_op.go Run closure as the shape to match, BEFORE adding resume support — ResumeFrom/CheckpointEvery/CheckpointStateFn are RunItems-specific options.
3. Add `ResumeFrom int` to reconcileApplyOpParams; change ResumePolicy to opsregistry.ResumeRestart; add CheckpointEvery + a CheckpointStateFn that carries LegacyOpID, the full Matches list, and the new watermark — exactly as part 1 does for batchApplyOpParams.
4. Bump the file's version header.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_136.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A Match whose target book was deleted between the original run and a resume must be skipped gracefully (matching whatever error-collection behavior the existing loop has), not crash the resumed run.
- Re-applying an already-applied merge decision must be provably impossible (not just unlikely) given ResumeFrom skips a contiguous prefix — verify the underlying merge-apply function is itself idempotent per-item (applying the same Match twice is a no-op) as defense in depth, since RunItems' watermark is a completion count, not a content hash.

## Tests

- {'file': 'internal/server/reconcile_ops_test.go', 'name': 'TestReconcileApplyOp_ResumeFrom_SkipsAlreadyAppliedMatches (new)', 'asserts': 'given ResumeFrom=N, only Matches[N:] are re-applied on a resumed run'}
- {'file': 'internal/server/reconcile_ops_test.go', 'name': 'TestReconcileApplyOp_Checkpoint_CarriesFullMatchList (anti-over-suppression, new)', 'asserts': 'a checkpoint mid-run preserves the full original Matches list and LegacyOpID, not a truncated or zeroed one'}

Anti-over-suppression test: `TestReconcileApplyOp_Checkpoint_CarriesFullMatchList` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/... -run TestReconcileApplyOp` passes.
- [ ] `grep -n 'ResumePolicy' internal/server/reconcile_ops.go` shows opsregistry.ResumeRestart.
- [ ] Anti-over-suppression test: `TestReconcileApplyOp_Checkpoint_CarriesFullMatchList` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_136.md`.

## Commit message

```
refactor(server): Convert reconcile.apply from ResumeDrop to real checkpoint/r (TODO L4575)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: reconcile.apply is a dedup merge/apply path — the highest-severity category in this scout's own review_critical rubric. Do not treat this as equivalent-risk to part 1; verify merge idempotency explicitly before shipping.
