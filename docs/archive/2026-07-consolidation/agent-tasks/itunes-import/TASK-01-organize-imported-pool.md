<!-- file: docs/agent-tasks/itunes-import/TASK-01-organize-imported-pool.md -->
<!-- version: 1.0.0 -->
<!-- guid: d8cb07c6-f159-4357-b42e-2e151ee3e019 -->
<!-- last-edited: 2026-07-05 -->

# TASK-01 — Parallelize organizeImportedBooks file-organize + UpdateBook over imported books (CONC-10)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · go-backend subagent · **Why:** File-move parallelism must ensure no two books resolve to the same target path. · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-import-organize-imported-pool" -b agent/itunes-import-organize-imported-pool origin/main
cd "$REPO/.worktrees/itunes-import-organize-imported-pool"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Wrap the per-book organize loop in registry.RunItems[T] (Concurrency for file I/O + DB write; NumCPU-ish). Guard the shared itunesImportStatus{mu sync.Mutex} counters (already mutex-bearing — use it). Preserve the operations.SaveCheckpoint progress cadence.

Reuse the existing concurrency primitive — do NOT invent a new worker-pool helper or a new concurrency constant:

- **`registry.RunItems[T]`** — `func RunItems[T any](ctx context.Context, r registry.Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...registry.RunItemsOptions) error` (in `internal/operations/registry/run_items.go`). Options: `registry.RunItemsOptions{Concurrency int, PerItemTimeout time.Duration, ErrMode ErrMode, Label func(i,total)}`. Concurrency<1 defaults to a safe value; Concurrency==1 takes the sequential path (runItemsSeq); >1 uses runItemsPar. Reporter carries UpdateProgress(current,total,msg) so progress reporting is preserved for free.
- Copy the invocation shape verbatim from a live caller — `internal/plugins/acoustid/backfill.go` (the `registry.RunItems(ctx, reporter, slice, func(ctx, b database.Book) error {...}, registry.RunItemsOptions{Concurrency: ...})` call). Re-locate it and the helper before using them (do not trust bare line numbers):
  ```bash
  grep -n "func RunItems\[" internal/operations/registry/run_items.go                                            # expect: 1 hit (the helper def)
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # expect: >=1 hit — copy this invocation shape verbatim
  ```

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): Embarrassingly parallel file I/O: bounded pool (fix-pattern #1), reusing the existing status mutex.
- Current behavior: Per-book file rename/organize + UpdateBook over all LibraryState=="imported" books; real file I/O + DB write per item. Uses itunesImportStatus (has sync.Mutex) and logger.Logger.
- **Shared mutable state / correctness constraint (READ TWICE):** itunesImportStatus already has a sync.Mutex — use it for the counters. File moves must not collide on paths; per-book book directories are disjoint, but verify no two imported books resolve to the same target path before enabling.
- Source audit finding: `CONC-10` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n "func (imp \*Importer) organizeImportedBooks" internal/itunes/service/importer.go   # expect: 1 hit (~line 1065)
  ```

## Step-by-step

1. Open `internal/itunes/service/importer.go` and locate the target loop(s) via the grep(s) above (never trust the line number in this brief). 
2. Replace the sequential `for` loop with `registry.RunItems[T]` over the same items, with a `Concurrency` value chosen per the Goal (CPU-bound → `runtime.NumCPU()`; network/rate-limited → a small fixed const with a named knob). Pass the existing Reporter/progress so progress reporting is preserved.
3. **Guard the shared state exactly as described above** — this is where a wrong change becomes a silent data race. Prefer per-worker-local state merged at the end, or a `sync.Mutex`/`sync.Map`; if you drop a dedup map, justify it by upsert idempotency in the commit body.
4. Keep the change purely additive to behavior: do NOT change the function signature, the emitted candidate semantics, or adjacent checks — the parallel version must produce the SAME output as the serial one.
5. Add a test asserting the parallel loop yields the same result set / same writes as the serial version (order-independent), plus a `-race` run.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test -race ./internal/itunes/service/... -count=1
make ci
```

## Acceptance criteria

- [ ] The target loop now runs through `registry.RunItems[T]` (verify: `grep -n "registry.RunItems" internal/itunes/service/importer.go` returns ≥1).
- [ ] `go test -race ./internal/itunes/service/...` is clean (no data race on the shared state named above).
- [ ] Anti-over-suppression: N/A (this task adds no filter/guard/veto/skip/dedupe path — it parallelizes an existing loop; correctness = identical output to the serial version).
- [ ] `make ci` green; `go vet` clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-07-05" <file>`).

## Commit message

```
perf(itunes): parallelize organizeImportedBooks file-organize + UpdateBook over imported books (CONC-10)

Parallelize the previously single-threaded loop via registry.RunItems, guarding
the shared state noted in the brief so the parallel pass produces identical output.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `grep -n "registry.RunItems" internal/itunes/service/importer.go` — if the target loop already routes through RunItems, this task may be complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit; the loop returns to sequential, no data or schema is touched, siblings unaffected.
