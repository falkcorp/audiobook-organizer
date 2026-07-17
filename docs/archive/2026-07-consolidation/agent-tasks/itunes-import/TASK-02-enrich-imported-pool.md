<!-- file: docs/agent-tasks/itunes-import/TASK-02-enrich-imported-pool.md -->
<!-- version: 1.0.0 -->
<!-- guid: c9c4e7fd-2e8c-41fc-b0c0-c1416c732d56 -->
<!-- last-edited: 2026-07-05 -->

# TASK-02 — Parallelize enrichImportedBooks metadata fetch with a bounded pool preserving the rate-limit circuit-breaker (CONC-11)

**Priority:** P3 · **Effort:** L · **Recommended subagent:** Sonnet-class · go-backend subagent · **Why:** Circuit-breaker semantics must survive parallelization — reframe 'consecutive' as a shared atomic. · ⚠ review-critical · **Depends on:** TASK-01

> **Depends on TASK-01 (same file `internal/itunes/service/importer.go`).** Do NOT start until TASK-01's PR is merged to `origin/main` and this worktree is rebased on top — running them concurrently guarantees a rebase conflict.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-import-enrich-imported-pool" -b agent/itunes-import-enrich-imported-pool origin/main
cd "$REPO/.worktrees/itunes-import-enrich-imported-pool"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Wrap the per-book FetchMetadataForBook loop in registry.RunItems[T] with a CONSERVATIVE Concurrency (network + external rate limit). CRITICAL: preserve the consecutiveErrors>=5 circuit-breaker semantics — a naive fan-out breaks it. Simplest safe shape: keep the breaker as a shared atomic counter that any worker can trip to cancel the ctx, so the bounded pool still aborts after 5 consecutive failures.

Reuse the existing concurrency primitive — do NOT invent a new worker-pool helper or a new concurrency constant:

- **`registry.RunItems[T]`** — `func RunItems[T any](ctx context.Context, r registry.Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...registry.RunItemsOptions) error` (in `internal/operations/registry/run_items.go`). Options: `registry.RunItemsOptions{Concurrency int, PerItemTimeout time.Duration, ErrMode ErrMode, Label func(i,total)}`. Concurrency<1 defaults to a safe value; Concurrency==1 takes the sequential path (runItemsSeq); >1 uses runItemsPar. Reporter carries UpdateProgress(current,total,msg) so progress reporting is preserved for free.
- Copy the invocation shape verbatim from a live caller — `internal/plugins/acoustid/backfill.go` (the `registry.RunItems(ctx, reporter, slice, func(ctx, b database.Book) error {...}, registry.RunItemsOptions{Concurrency: ...})` call). Re-locate it and the helper before using them (do not trust bare line numbers):
  ```bash
  grep -n "func RunItems\[" internal/operations/registry/run_items.go                                            # expect: 1 hit (the helper def)
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # expect: >=1 hit — copy this invocation shape verbatim
  ```

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): Rate-limited external calls with a circuit-breaker: bounded pool preserving the breaker (fix-pattern #4).
- Current behavior: Per-book metadata-fetch network call with deliberate rate-limit backoff + a consecutiveErrors>=5 breaker. Same file as TASK-01 (importer.go) → MUST serialize after it.
- **Shared mutable state / correctness constraint (READ TWICE):** The consecutiveErrors circuit-breaker is loop-carried serial state. Under a pool, 'consecutive' loses meaning — reframe as a shared atomic fail counter that cancels the context at a threshold, or keep enrich at low concurrency. Do NOT drop the breaker. Guard itunesImportStatus via its mutex.
- Source audit finding: `CONC-11` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n "func (imp \*Importer) enrichImportedBooks\|consecutiveErrors >= 5\|imp.mfs.FetchMetadataForBook" internal/itunes/service/importer.go   # expect: func ~1012, breaker ~1038, fetch ~1034
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
perf(itunes): parallelize enrichImportedBooks metadata fetch with a bounded pool preserving the rate-limit circuit-breaker (CONC-11)

Parallelize the previously single-threaded loop via registry.RunItems, guarding
the shared state noted in the brief so the parallel pass produces identical output.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `grep -n "registry.RunItems" internal/itunes/service/importer.go` — if the target loop already routes through RunItems, this task may be complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit; the loop returns to sequential, no data or schema is touched, siblings unaffected.
