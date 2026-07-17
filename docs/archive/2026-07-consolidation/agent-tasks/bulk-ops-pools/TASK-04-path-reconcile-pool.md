<!-- file: docs/agent-tasks/bulk-ops-pools/TASK-04-path-reconcile-pool.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d83db9d-3db9-4fc7-9fd5-f789a32d02a8 -->
<!-- last-edited: 2026-07-05 -->

# TASK-04 — Parallelize the itunes path-reconcile per-book GetBookFiles loop (CONC-15)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku-class · go-backend subagent · **Why:** Mechanical mirror of TASK-03; fully specified. · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bulk-ops-pools-path-reconcile-pool" -b agent/bulk-ops-pools-path-reconcile-pool origin/main
cd "$REPO/.worktrees/bulk-ops-pools-path-reconcile-pool"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Switch the per-book loop to registry.RunItems[T] (Concurrency NumCPU-ish; DB-read-bound). Mechanical mirror of TASK-03 — copy the acoustid/backfill.go:118 invocation pattern.

Reuse the existing concurrency primitive — do NOT invent a new worker-pool helper or a new concurrency constant:

- **`registry.RunItems[T]`** — `func RunItems[T any](ctx context.Context, r registry.Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...registry.RunItemsOptions) error` (in `internal/operations/registry/run_items.go`). Options: `registry.RunItemsOptions{Concurrency int, PerItemTimeout time.Duration, ErrMode ErrMode, Label func(i,total)}`. Concurrency<1 defaults to a safe value; Concurrency==1 takes the sequential path (runItemsSeq); >1 uses runItemsPar. Reporter carries UpdateProgress(current,total,msg) so progress reporting is preserved for free.
- Copy the invocation shape verbatim from a live caller — `internal/plugins/acoustid/backfill.go` (the `registry.RunItems(ctx, reporter, slice, func(ctx, b database.Book) error {...}, registry.RunItemsOptions{Concurrency: ...})` call). Re-locate it and the helper before using them (do not trust bare line numbers):
  ```bash
  grep -n "func RunItems\[" internal/operations/registry/run_items.go                                            # expect: 1 hit (the helper def)
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # expect: >=1 hit — copy this invocation shape verbatim
  ```

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): Embarrassingly parallel DB-read: bounded pool (fix-pattern #1).
- Current behavior: Classic N+1 read at full-library scale; per-book GetBookFiles then path reconcile.
- **Shared mutable state / correctness constraint (READ TWICE):** Independent per book; guard any shared reconcile accumulator via mutex/atomics.
- Source audit finding: `CONC-15` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n "bookFiles, _ := r.store.GetBookFiles(b.ID)" internal/itunes/service/path_reconcile.go   # expect: 1 hit (~line 89)
  ```

## Step-by-step

1. Open `internal/itunes/service/path_reconcile.go` and locate the target loop(s) via the grep(s) above (never trust the line number in this brief). 
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

- [ ] The target loop now runs through `registry.RunItems[T]` (verify: `grep -n "registry.RunItems" internal/itunes/service/path_reconcile.go` returns ≥1).
- [ ] `go test -race ./internal/itunes/service/...` is clean (no data race on the shared state named above).
- [ ] Anti-over-suppression: N/A (this task adds no filter/guard/veto/skip/dedupe path — it parallelizes an existing loop; correctness = identical output to the serial version).
- [ ] `make ci` green; `go vet` clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-07-05" <file>`).

## Commit message

```
perf(bulk): parallelize the itunes path-reconcile per-book GetBookFiles loop (CONC-15)

Parallelize the previously single-threaded loop via registry.RunItems, guarding
the shared state noted in the brief so the parallel pass produces identical output.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `grep -n "registry.RunItems" internal/itunes/service/path_reconcile.go` — if the target loop already routes through RunItems, this task may be complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit; the loop returns to sequential, no data or schema is touched, siblings unaffected.
