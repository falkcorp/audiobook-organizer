<!-- file: docs/agent-tasks/backfill-pools/TASK-04-gold-label-memoize-pool.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4208221f-a556-42ed-a778-b7d15740d81a -->
<!-- last-edited: 2026-07-05 -->

# TASK-04 — Add book-lookup memoization then parallelize mine_gold_labels and dataset_backfill (CONC-8)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · go-backend subagent · **Why:** Adds memoization AND a pool with a shared-cache guarding decision. · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/backfill-pools-gold-label-memoize-pool" -b agent/backfill-pools-gold-label-memoize-pool origin/main
cd "$REPO/.worktrees/backfill-pools-gold-label-memoize-pool"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Two-part, per file: FIRST add a book-lookup memoization cache mirroring the drain_stale.go / server_maintenance_deps.go bookCache pattern (a map[string]*database.Book + a getBook closure) so repeated candidate rows don't re-read the same book 4x; THEN wrap the candidate loop in registry.RunItems[T]. Because a shared cache under a pool needs guarding, prefer per-worker-local caches OR a sync.Mutex-guarded map.

Reuse the existing concurrency primitive — do NOT invent a new worker-pool helper or a new concurrency constant:

- **`registry.RunItems[T]`** — `func RunItems[T any](ctx context.Context, r registry.Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...registry.RunItemsOptions) error` (in `internal/operations/registry/run_items.go`). Options: `registry.RunItemsOptions{Concurrency int, PerItemTimeout time.Duration, ErrMode ErrMode, Label func(i,total)}`. Concurrency<1 defaults to a safe value; Concurrency==1 takes the sequential path (runItemsSeq); >1 uses runItemsPar. Reporter carries UpdateProgress(current,total,msg) so progress reporting is preserved for free.
- Copy the invocation shape verbatim from a live caller — `internal/plugins/acoustid/backfill.go` (the `registry.RunItems(ctx, reporter, slice, func(ctx, b database.Book) error {...}, registry.RunItemsOptions{Concurrency: ...})` call). Re-locate it and the helper before using them (do not trust bare line numbers):
  ```bash
  grep -n "func RunItems\[" internal/operations/registry/run_items.go                                            # expect: 1 hit (the helper def)
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # expect: >=1 hit — copy this invocation shape verbatim
  ```

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): N+1 read fixed by memoization, then embarrassingly parallel: memoize-then-pool (fix-pattern #1 + explicit memoization).
- Current behavior: Up to the 1,000,000-candidate op limit; 4 DB reads per candidate with NO memoization (worse than its memoized siblings dedup-exact-triage/drain-stale).
- **Shared mutable state / correctness constraint (READ TWICE):** If you share one memoization map across workers it must be mutex-guarded (or use sync.Map); simplest correct shape is a per-worker-local cache. The candidate processing itself is independent.
- Source audit finding: `CONC-8` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n "a, aErr := adapter.GetBook(c.EntityAID)" internal/plugins/dedup/mine_gold_labels.go   # expect: 1 hit (~line 101)
  grep -n "ex, err := dataset.BuildExample(adapter, c)" internal/plugins/dedup/dataset_backfill.go   # expect: 1 hit (~line 141)
  grep -n "cache := make(map\|lookup := func(id string)" internal/dedup/drain_stale.go   # expect: the memoized lookup closure
  ```

## Step-by-step

1. Open `internal/plugins/dedup/mine_gold_labels.go` and `internal/plugins/dedup/dataset_backfill.go` and locate the target loop(s) via the grep(s) above (never trust the line number in this brief). Apply the change to EACH of the files listed — they are separate loops with the same fix pattern.
2. Replace the sequential `for` loop with `registry.RunItems[T]` over the same items, with a `Concurrency` value chosen per the Goal (CPU-bound → `runtime.NumCPU()`; network/rate-limited → a small fixed const with a named knob). Pass the existing Reporter/progress so progress reporting is preserved.
3. **Guard the shared state exactly as described above** — this is where a wrong change becomes a silent data race. Prefer per-worker-local state merged at the end, or a `sync.Mutex`/`sync.Map`; if you drop a dedup map, justify it by upsert idempotency in the commit body.
4. Keep the change purely additive to behavior: do NOT change the function signature, the emitted candidate semantics, or adjacent checks — the parallel version must produce the SAME output as the serial one.
5. Add a test asserting the parallel loop yields the same result set / same writes as the serial version (order-independent), plus a `-race` run.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test -race ./internal/plugins/dedup/... -count=1
make ci
```

## Acceptance criteria

- [ ] The target loop now runs through `registry.RunItems[T]` (verify: `grep -n "registry.RunItems" internal/plugins/dedup/mine_gold_labels.go` returns ≥1).
- [ ] `go test -race ./internal/plugins/dedup/...` is clean (no data race on the shared state named above).
- [ ] Anti-over-suppression: N/A (this task adds no filter/guard/veto/skip/dedupe path — it parallelizes an existing loop; correctness = identical output to the serial version).
- [ ] `make ci` green; `go vet` clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-07-05" <file>`).

## Commit message

```
perf(backfill): add book-lookup memoization then parallelize mine_gold_labels and dataset_backfill (CONC-8)

Parallelize the previously single-threaded loop via registry.RunItems, guarding
the shared state noted in the brief so the parallel pass produces identical output.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `grep -n "registry.RunItems" internal/plugins/dedup/mine_gold_labels.go` — if the target loop already routes through RunItems, this task may be complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit; the loop returns to sequential, no data or schema is touched, siblings unaffected.
