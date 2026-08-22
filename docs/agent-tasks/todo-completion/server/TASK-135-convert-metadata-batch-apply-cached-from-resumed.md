<!-- file: docs/agent-tasks/todo-completion/server/TASK-135-convert-metadata-batch-apply-cached-from-resumed.md -->
<!-- version: 1.0.0 -->
<!-- guid: 80a085c7-7301-475c-b42c-33fd18a2950b -->
<!-- last-edited: 2026-08-21 -->

# TASK-135 — Convert metadata.batch-apply-cached from ResumeDrop to real checkpoint/resume (TODO.md L4575)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server subagent · **Why:** Mechanical once the template is understood, but requires correctly reasoning about which fields must round-trip through the checkpoint (WriteBack flag, BookIDs, counters) so a resumed run does not silently downgrade its own semantics — the exact failure mode the template's own comment warns about. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4575 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Convert the remaining long-running `ResumeDrop` " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-135-convert-metadata-batch-apply-cached-from-resumed" -b agent/server-135-convert-metadata-batch-apply-cached-from-resumed origin/main
cd "$REPO/.worktrees/server-135-convert-metadata-batch-apply-cached-from-resumed"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Convert metadata.batch-apply-cached (internal/server/batch_apply_op.go) from ResumePolicy=ResumeDrop to a real checkpoint/resume design mirroring chapters_backfill.go: ResumePolicy=ResumeRestart, a ResumeFrom field added to batchApplyOpParams, and RunItemsOptions populated with ResumeFrom/CheckpointEvery/CheckpointStateFn so an interrupted batch-apply run (server restart, watchdog kill) resumes from its contiguous-completion watermark on the SAME book list instead of dropping and losing all progress on a potentially large batch.

## Background (verify before editing)

- This op applies metadata to a caller-supplied set of book_ids (batchApplyOpParams.BookIDs, batch_apply_op.go:37) and optionally writes tags back to files (WriteBack, line 41) — both of which MUST be preserved verbatim across a resume, exactly as chapters_backfill.go's CheckpointStateFn comment warns ('dropping Apply would silently downgrade a live run to a dry run on restart').
- This is one of the ops explicitly named by the owner decision list (item 9: 'Convert the ones that are both long-running and idempotent per item — metadata.batch-apply-cached, reconcile.apply and the full-library sweeps first').

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ResumePolicy' internal/server/batch_apply_op.go   # 1 hit at L70, value opsregistry.ResumeDrop — metadata.batch-apply-cached is currently ResumeDrop
  grep -n 'opsregistry.RunItems' internal/server/batch_apply_op.go   # 1 hit at L163 — it already uses registry.RunItems, just without ResumeFrom/CheckpointEvery/CheckpointStateFn
  grep -n 'ResumePolicy:\s*sdk.ResumeRestart\|CheckpointStateFn:' internal/plugins/maintenance/chapters_backfill.go   # hits at L195 and L459 — a working ResumeRestart + checkpoint template already exists to copy
  grep -n 'Checkpoint(state any) error' internal/operations/registry/reporter.go   # 1 hit at L24 — opsregistry.Reporter (the same reporter type batch_apply_op.go's Run receives) has a Checkpoint method, so the template transfers directly without needing the sdk plugin wrapper
  ```

### Reuse — don't invent

- Use `chapters_backfill.go's ResumeFrom/CheckpointEvery/CheckpointStateFn wiring, as a direct template` in `internal/plugins/maintenance/chapters_backfill.go` (verify: `grep -n 'ResumeFrom:\|CheckpointEvery:\|CheckpointStateFn:' internal/plugins/maintenance/chapters_backfill.go`) — do NOT write a parallel helper.
- Use `registry.RunItems' ResumeFrom/CheckpointEvery/CheckpointStateFn options (already exist, just unused here)` in `internal/operations/registry/run_items.go` (verify: `grep -n 'ResumeFrom\|CheckpointEvery\|CheckpointStateFn' internal/operations/registry/run_items.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add `ResumeFrom int json:"resume_from,omitempty"` to batchApplyOpParams (internal/server/batch_apply_op.go:36-42).
2. Change `ResumePolicy: opsregistry.ResumeDrop` (line 70) to `ResumePolicy: opsregistry.ResumeRestart`.
3. In the Run closure, slice bookIDs is already read at line 88; do NOT manually slice it — pass `ResumeFrom: p.ResumeFrom` into the existing RunItemsOptions (line 163-168) and let registry.RunItems handle the skip internally (it slices items[ResumeFrom:] and shifts progress offset itself, per run_items.go:58-65).
4. Add `CheckpointEvery: <a sensible constant, e.g. 200, matching chaptersBackfillCheckpointEvery's precedent>` to the same RunItemsOptions.
5. Add `CheckpointStateFn: func(ctx context.Context, watermark int) error { return reporter.Checkpoint(batchApplyOpParams{ BookIDs: p.BookIDs, WriteBack: p.WriteBack, ResumeFrom: watermark }) }` — every field of batchApplyOpParams must be explicitly carried, not just ResumeFrom, per the same warning chapters_backfill.go documents (an omitted field resets to its zero value on resume).
6. Verify (read internal/operations/registry/worker.go or wherever ResumeRestart is handled) that a restarted op actually re-decodes its checkpointed params as the new run's rawParams — confirm this mechanism before assuming it 'just works' by analogy to chapters_backfill.go; if it differs, adjust accordingly.
7. Bump the file's version header.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_135.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- ResumeFrom=0 (a fresh run, not a resume) must behave identically to today's unconverted behavior — no skipped books.
- A resume where the underlying book_ids list has since become invalid (a book was deleted between the original run and the resume) should skip that id gracefully, matching the existing runOne per-item error handling (ErrModeCollect), not abort the whole resumed run.

## Tests

- {'file': 'internal/server/batch_apply_op_test.go', 'name': 'TestBatchApplyOp_ResumeFrom_SkipsAlreadyAppliedBooks (new)', 'asserts': 'given ResumeFrom=N, the op applies only bookIDs[N:], not the full list — proving a resumed run does not re-apply (and potentially re-write-back-to-file) books already done'}
- {'file': 'internal/server/batch_apply_op_test.go', 'name': 'TestBatchApplyOp_Checkpoint_CarriesWriteBackFlag (anti-over-suppression, new)', 'asserts': 'a checkpoint taken mid-run with WriteBack=true produces a resume-params blob that still has WriteBack=true, not the zero value — proving the checkpoint does not silently downgrade a live-write run to a dry run'}

Anti-over-suppression test: `TestBatchApplyOp_Checkpoint_CarriesWriteBackFlag` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/... -run TestBatchApplyOp` passes.
- [ ] `grep -n 'ResumePolicy' internal/server/batch_apply_op.go` shows opsregistry.ResumeRestart, not ResumeDrop.
- [ ] Anti-over-suppression test: `TestBatchApplyOp_Checkpoint_CarriesWriteBackFlag` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_135.md`.

## Commit message

```
refactor(server): Convert metadata.batch-apply-cached from ResumeDrop to real  (TODO L4575)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: this op applies metadata to the database and optionally writes audio file tags back to disk at production scale — a resume bug here (e.g. double-applying or silently downgrading WriteBack) is a real data-integrity risk, not just an inconvenience.
