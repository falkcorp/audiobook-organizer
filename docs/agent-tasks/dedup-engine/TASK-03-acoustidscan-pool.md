<!-- file: docs/agent-tasks/dedup-engine/TASK-03-acoustidscan-pool.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7fe1f5d6-e372-4aea-b82e-1a4c79970aab -->
<!-- last-edited: 2026-07-05 -->

# TASK-03 — Parallelize AcoustIDScan's per-book loop with a bounded pool and guard its four shared maps (CONC-3)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · go-backend subagent · **Why:** Four unguarded shared maps + a counter to guard correctly under a pool — easy to introduce a data race. · ⚠ review-critical · **Depends on:** TASK-02

> **Depends on TASK-02 (same file `internal/dedup/engine.go`).** Do NOT start until TASK-02's PR is merged to `origin/main` and this worktree is rebased on top — running them concurrently guarantees a rebase conflict.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-engine-acoustidscan-pool" -b agent/dedup-engine-acoustidscan-pool origin/main
cd "$REPO/.worktrees/dedup-engine-acoustidscan-pool"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Wrap the per-book loop in registry.RunItems[T] (Concurrency ~runtime.NumCPU(); mixed DB+CPU). The hard part is state: AcoustIDScan has FOUR unguarded shared maps plus a counter — guard each with a sync.Mutex (or use sync.Map / per-worker-local caches merged at the end).

Reuse the existing concurrency primitive — do NOT invent a new worker-pool helper or a new concurrency constant:

- **`registry.RunItems[T]`** — `func RunItems[T any](ctx context.Context, r registry.Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...registry.RunItemsOptions) error` (in `internal/operations/registry/run_items.go`). Options: `registry.RunItemsOptions{Concurrency int, PerItemTimeout time.Duration, ErrMode ErrMode, Label func(i,total)}`. Concurrency<1 defaults to a safe value; Concurrency==1 takes the sequential path (runItemsSeq); >1 uses runItemsPar. Reporter carries UpdateProgress(current,total,msg) so progress reporting is preserved for free.
- Copy the invocation shape verbatim from a live caller — `internal/plugins/acoustid/backfill.go` (the `registry.RunItems(ctx, reporter, slice, func(ctx, b database.Book) error {...}, registry.RunItemsOptions{Concurrency: ...})` call). Re-locate it and the helper before using them (do not trust bare line numbers):
  ```bash
  grep -n "func RunItems\[" internal/operations/registry/run_items.go                                            # expect: 1 hit (the helper def)
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # expect: >=1 hit — copy this invocation shape verbatim
  ```

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): Embarrassingly parallel per book BUT with heavy shared caches: bounded pool + mutex-guarded (or per-worker-local then merged) maps (fix-pattern #1 with #2's guarding discipline).
- Current behavior: Per book: GetBookFiles + per-file LSH lookup (up to 200 candidates each doing GetBookFileByID + WholeFileSimilarity) + Tier-1 walk of 7 segment strings each doing GetBookFileByAcoustID. emit() → upsertCandidateWithLiveLabel.
- **Shared mutable state / correctness constraint (READ TWICE):** FOUR unguarded maps + a counter, ALL mutated inside emit()/the loop and ALL must be guarded when sharding: `emitted map[string]struct{}` (~3348), `boilerplateBookCache map[string]bool` (~3328), `parentDirCache map[string]string` (~3358), `booksByID map[string]*database.Book` (~3323), and `identifierGateDrops int` (~3379). Heaviest shared-state burden of the engine.go findings — miss one and you get a data race. Preserve the progress callback.
- Source audit finding: `CONC-3` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n 'cands, _ := lshStore.LookupAcoustIDCandidates' internal/dedup/engine.go   # expect: 1 hit (~line 3486)
  grep -n 'func (de \*Engine) AcoustIDScan\|emitted := make\|booksByID :=\|boilerplateBookCache\|parentDirCache\|identifierGateDrops' internal/dedup/engine.go   # expect: all 5 shared-state sites: func ~3318; emitted ~3348; booksByID ~3323; boilerplateBookCache ~3328; parentDirCache ~3358; identifierGateDrops ~3379
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
perf(dedup): parallelize AcoustIDScan's per-book loop with a bounded pool and guard its four shared maps (CONC-3)

Parallelize the previously single-threaded loop via registry.RunItems, guarding
the shared state noted in the brief so the parallel pass produces identical output.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `grep -n "registry.RunItems" internal/dedup/engine.go` — if the target loop already routes through RunItems, this task may be complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit; the loop returns to sequential, no data or schema is touched, siblings unaffected.
