<!-- file: docs/agent-tasks/dedup-engine/TASK-01-booksigscan-shard.md -->
<!-- version: 1.0.0 -->
<!-- guid: 41860daf-fd12-4ec8-8f6d-f7039e85f967 -->
<!-- last-edited: 2026-07-05 -->

# TASK-01 — Shard BookSignatureScan's O(n²) pairwise loop across a bounded worker pool (CONC-1)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · go-backend subagent · **Why:** O(n²) hot path; sharding must guard the emitted map + DB write without changing dedup output — highest-risk change in the package. · ⚠ review-critical · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-engine-booksigscan-shard" -b agent/dedup-engine-booksigscan-shard origin/main
cd "$REPO/.worktrees/dedup-engine-booksigscan-shard"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Shard the OUTER loop (index i) across a bounded worker pool sized to runtime.NumCPU() (pure-CPU bitmask/Hamming compare). Each worker owns a contiguous i-range and runs its inner j-loop. Reuse registry.RunItems[T] over the outer index if it fits, or a plain errgroup/semaphore — the per-pair work is CPU-bound with no DB reads inside the inner loop.

Reuse the existing concurrency primitive — do NOT invent a new worker-pool helper or a new concurrency constant:

- **`registry.RunItems[T]`** — `func RunItems[T any](ctx context.Context, r registry.Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...registry.RunItemsOptions) error` (in `internal/operations/registry/run_items.go`). Options: `registry.RunItemsOptions{Concurrency int, PerItemTimeout time.Duration, ErrMode ErrMode, Label func(i,total)}`. Concurrency<1 defaults to a safe value; Concurrency==1 takes the sequential path (runItemsSeq); >1 uses runItemsPar. Reporter carries UpdateProgress(current,total,msg) so progress reporting is preserved for free.
- Copy the invocation shape verbatim from a live caller — `internal/plugins/acoustid/backfill.go` (the `registry.RunItems(ctx, reporter, slice, func(ctx, b database.Book) error {...}, registry.RunItemsOptions{Concurrency: ...})` call). Re-locate it and the helper before using them (do not trust bare line numbers):
  ```bash
  grep -n "func RunItems\[" internal/operations/registry/run_items.go                                            # expect: 1 hit (the helper def)
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # expect: >=1 hit — copy this invocation shape verbatim
  ```

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): Pairwise O(n²) with a shared dedup-set: shard outer loop, guard shared state (fix-pattern #2 in the audit).
- Current behavior: The inner loop does pure-CPU fingerprint.BookSignatureSimilarityMasked (skip if overlap<512) and, on sim>=FuzzyMinSimilarity, emit() → upsertCandidateWithLiveLabel (DB write + dataset-example build). sigs are already in the in-memory booksWithSig slice (no DB reads in the inner loop).
- **Shared mutable state / correctness constraint (READ TWICE):** The `emitted map[string]struct{}` (~line 3599, keyed by canonical pairKey) is read-check-then-written inside emit(). NOTE the triangular i<j loop already visits each unordered pair exactly once, so `emitted` never actually suppresses anything here — but sharding the outer loop still races the map AND the DB write. Guard `emitted` with a sync.Mutex (or a sharded map), OR drop it entirely and rely on upsertCandidateWithLiveLabel idempotency. Preserve the existing progress(done,total) callback (increment atomically).
- Source audit finding: `CONC-1` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n 'for j := i + 1; j < len(booksWithSig); j++' internal/dedup/engine.go   # expect: 1 hit (~line 3638)
  grep -n 'func (de \*Engine) BookSignatureScan\|emitted := make(map\[string\]struct{}' internal/dedup/engine.go   # expect: func ~3585, emitted map ~3599
  ```

## Step-by-step

1. Open `internal/dedup/engine.go` and locate the target loop(s) via the grep(s) above (never trust the line number in this brief). 
2. Replace the sequential `for` loop with `registry.RunItems[T]` over the same items, with a `Concurrency` value chosen per the Goal (CPU-bound → `runtime.NumCPU()`; network/rate-limited → a small fixed const with a named knob). Pass the existing Reporter/progress so progress reporting is preserved.
3. **Guard the shared state exactly as described above** — this is where a wrong change becomes a silent data race. Prefer per-worker-local state merged at the end, or a `sync.Mutex`/`sync.Map`; if you drop a dedup map, justify it by upsert idempotency in the commit body.
4. Keep the change purely additive to behavior: do NOT change the function signature, the emitted candidate semantics, or adjacent checks — the parallel version must produce the SAME output as the serial one.
5. Add a test proving the parallel pass produces the SAME candidate output as the serial version (no lost writes through the guarded shared state), plus a `-race` run.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test -race ./internal/dedup/... -count=1
make ci
```

## Acceptance criteria

- [ ] The target loop now runs through `registry.RunItems[T]` (verify: `grep -n "registry.RunItems" internal/dedup/engine.go` returns ≥1).
- [ ] `go test -race ./internal/dedup/...` is clean (no data race on the shared state named above).
- [ ] `TestParallel<Scan> SameCandidatesAsSerial` — the parallelized pass emits the EXACT same candidate set (and the guarded shared map/counter has no lost updates) as the pre-change serial version on a fixture library (anti-over-suppression / anti-race).
- [ ] `make ci` green; `go vet` clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-07-05" <file>`).

## Commit message

```
perf(dedup): shard BookSignatureScan's O(n²) pairwise loop across a bounded worker pool (CONC-1)

Parallelize the previously single-threaded loop via registry.RunItems, guarding
the shared state noted in the brief so the parallel pass produces identical output.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `grep -n "registry.RunItems" internal/dedup/engine.go` — if the target loop already routes through RunItems, this task may be complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit; the loop returns to sequential, no data or schema is touched, siblings unaffected.
