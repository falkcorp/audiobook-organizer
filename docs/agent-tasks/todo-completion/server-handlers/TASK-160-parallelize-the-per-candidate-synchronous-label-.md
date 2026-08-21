<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-160-parallelize-the-per-candidate-synchronous-label-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 39f38356-bbcb-4b21-bfe6-4358f0dccaa1 -->
<!-- last-edited: 2026-08-21 -->

# TASK-160 — Parallelize the per-candidate synchronous label/breakdown refresh in DismissDedupCluster (TODO.md L10521)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server-handlers subagent · **Why:** concurrency-safety review needed: UpdateCandidateStatus/UpsertLabeledExample must be safe under concurrent per-candidate calls; not a mechanical edit · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10521 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Async breakdown-refresh for bulk/cluster dismiss" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-14.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-160-parallelize-the-per-candidate-synchronous-label-" -b agent/server-handlers-160-parallelize-the-per-candidate-synchronous-label- origin/main
cd "$REPO/.worktrees/server-handlers-160-parallelize-the-per-candidate-synchronous-label-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Convert DismissDedupCluster's (internal/server/handlers/dedup/handler.go:1176) per-candidate loop from a sequential for-range into a bounded worker pool (errgroup.Group + SetLimit(runtime.NumCPU()) or similar), so a large cluster dismiss doesn't serialize N sequential UpdateCandidateStatus + refreshExampleBreakdown round-trips. Each candidate's dismiss+label-capture is independent (keyed by distinct candidate.ID / distinct book pairs), so workers can run in parallel without a shared-state lock beyond an atomic dismissed counter.

## Background (verify before editing)

- Loop body per candidate: es.UpdateCandidateStatus(cand.ID, "dismissed") then h.recordHumanLabel(...) which itself calls h.refreshExampleBreakdown(ctx, ex) synchronously before es.UpsertLabeledExample — two DB round-trips per candidate, done sequentially today.
- RemoveFromDedupCluster (same file, following function) has an analogous per-candidate loop and should get the same treatment for consistency.
- CLAUDE.md's concurrency mandate explicitly requires a bounded worker pool for any loop doing per-item DB read/write work at this shape.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "for _, cand := range candidates" internal/server/handlers/dedup/handler.go   # 1 hit ~L1206 — DismissDedupCluster loops candidates synchronously calling UpdateCandidateStatus + recordHumanLabel per pair
  grep -n "h.refreshExampleBreakdown" internal/server/handlers/dedup/label_capture.go   # 1 hit ~L92 — recordHumanLabel synchronously calls refreshExampleBreakdown before the label upsert
  ```

### Reuse — don't invent

- Use `errgroup.Group + SetLimit bounded worker pool pattern used elsewhere per CLAUDE.md concurrency mandate` in `internal/plugins/acoustid/backfill.go` (verify: `grep -n "RunItems" internal/plugins/acoustid/backfill.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/dedup/handler.go, locate DismissDedupCluster's loop (~L1206-1224).
2. Replace the sequential for-range with an errgroup.Group (import "golang.org/x/sync/errgroup") with g.SetLimit(runtime.NumCPU()); for each candidate spawn g.Go(func() error { ... }) doing the same UpdateCandidateStatus + recordHumanLabel work, using an atomic int64 (sync/atomic) for the dismissed counter instead of a plain int++.
3. Verify h (the Handler) and es (the shared embedding store) are safe for concurrent calls to UpdateCandidateStatus/UpsertLabeledExample — grep for other concurrent callers of these methods elsewhere in the codebase as a precedent check.
4. Apply the same pattern to RemoveFromDedupCluster's analogous loop in the same file.
5. Preserve the existing slog.Info per-candidate log line (best-effort, non-fatal on error) inside each goroutine.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_160.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Zero candidates in the cluster: loop/errgroup must handle nil/empty slice without spawning zero goroutines incorrectly.
- A candidate whose UpdateCandidateStatus fails should not abort the whole batch — do NOT use errgroup's error propagation to cancel siblings (mirror today's 'continue' semantics, not fail-fast).

## Tests

- internal/server/handlers/dedup/handler_test.go: extend TestDismissDedupCluster with a case that dismisses a cluster of ≥50 synthetic candidates and asserts the final dismissed count matches, to catch a race in the counter or a dropped goroutine result.
- Run with `go test -race ./internal/server/handlers/dedup/...` to catch any data race introduced by the parallelization.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test -race ./internal/server/handlers/dedup/...` passes.
- [ ] grep -n "errgroup" internal/server/handlers/dedup/handler.go shows the new import and usage.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_160.md`.

## Commit message

```
refactor(server-handlers): Parallelize the per-candidate synchronous label/breakdown re (TODO L10521)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

TODO.md item 7's own text hedges this as 'may need' — but the concurrency mandate in this repo's CLAUDE.md makes this a should-fix regardless of measured latency, since the shape (per-item DB round-trip in a for-range) is exactly what that policy targets.
