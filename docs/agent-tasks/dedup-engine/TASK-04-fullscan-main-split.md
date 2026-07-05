<!-- file: docs/agent-tasks/dedup-engine/TASK-04-fullscan-main-split.md -->
<!-- version: 1.0.0 -->
<!-- guid: a81ae58f-8713-4e4c-a498-9ebe653ba0b1 -->
<!-- last-edited: 2026-07-05 -->

# TASK-04 — Parallelize FullScan main-pass Layer-1 checks while keeping Layer-2 embedding batching serial (CONC-4)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · go-backend subagent · **Why:** Must split parallel Layer-1 from serial Layer-2 circuit-breaker state — design-adjacent, not mechanical. · ⚠ review-critical · **Depends on:** TASK-03

> **Depends on TASK-03 (same file `internal/dedup/engine.go`).** Do NOT start until TASK-03's PR is merged to `origin/main` and this worktree is rebased on top — running them concurrently guarantees a rebase conflict.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-engine-fullscan-main-split" -b agent/dedup-engine-fullscan-main-split origin/main
cd "$REPO/.worktrees/dedup-engine-fullscan-main-split"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Split the loop: parallelize the per-book-independent Layer-1 exact checks (checkExactFileHash/ISBN/Title/duration) via registry.RunItems[T], but KEEP the Layer-2 embedding batch-accumulation + flushChunk + circuit-breaker strictly serial (it is loop-carried state, not embarrassingly parallel). Simplest safe shape: a parallel Layer-1 stage over all books, then the existing serial Layer-2 batching stage — do NOT try to parallelize the flush.

Reuse the existing concurrency primitive — do NOT invent a new worker-pool helper or a new concurrency constant:

- **`registry.RunItems[T]`** — `func RunItems[T any](ctx context.Context, r registry.Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...registry.RunItemsOptions) error` (in `internal/operations/registry/run_items.go`). Options: `registry.RunItemsOptions{Concurrency int, PerItemTimeout time.Duration, ErrMode ErrMode, Label func(i,total)}`. Concurrency<1 defaults to a safe value; Concurrency==1 takes the sequential path (runItemsSeq); >1 uses runItemsPar. Reporter carries UpdateProgress(current,total,msg) so progress reporting is preserved for free.
- Copy the invocation shape verbatim from a live caller — `internal/plugins/acoustid/backfill.go` (the `registry.RunItems(ctx, reporter, slice, func(ctx, b database.Book) error {...}, registry.RunItemsOptions{Concurrency: ...})` call). Re-locate it and the helper before using them (do not trust bare line numbers):
  ```bash
  grep -n "func RunItems\[" internal/operations/registry/run_items.go                                            # expect: 1 hit (the helper def)
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # expect: >=1 hit — copy this invocation shape verbatim
  ```

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): Mixed: split parallelizable Layer-1 from inherently-serial Layer-2 batching (fix-pattern #1 for Layer-1 only).
- Current behavior: Layer-1 comment says 'cheap and synchronous, no API calls'. Layer-2 batches book.IDs and every 64 calls EmbedBooks (network) + findSimilarBooks.
- **Shared mutable state / correctness constraint (READ TWICE):** Loop-carried SERIAL state that makes this NOT embarrassingly parallel: `chunkIDs []string` (appended each iter, reset in flushChunk), `chunkStart int`, `embedConsecutiveFails int`, and `embeddingsGaveUp bool` (circuit-breaker). Parallelize ONLY Layer-1; the batch accumulation + circuit-breaker must stay sequential or you corrupt the batching and defeat the breaker.
- Source audit finding: `CONC-4` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n 'de.checkExactFileHash(&book, authorName)' internal/dedup/engine.go   # expect: 1 hit (~line 2482)
  ```

## Step-by-step

1. Open `internal/dedup/engine.go` and locate the target loop(s) via the grep(s) above (never trust the line number in this brief). 
2. Replace the sequential `for` loop with `registry.RunItems[T]` over the same items, with a `Concurrency` value chosen per the Goal (CPU-bound → `runtime.NumCPU()`; network/rate-limited → a small fixed const with a named knob). Pass the existing Reporter/progress so progress reporting is preserved.
3. **Guard the shared state exactly as described above** — this is where a wrong change becomes a silent data race. Prefer per-worker-local state merged at the end, or a `sync.Mutex`/`sync.Map`; if you drop a dedup map, justify it by upsert idempotency in the commit body.
4. Keep the change purely additive to behavior: do NOT change the function signature, the emitted candidate semantics, or adjacent checks — the parallel version must produce the SAME output as the serial one.
5. Add a test asserting the parallel loop yields the same result set / same writes as the serial version (order-independent), plus a `-race` run.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test -race ./internal/dedup/... -count=1
make ci
```

## Acceptance criteria

- [ ] The target loop now runs through `registry.RunItems[T]` (verify: `grep -n "registry.RunItems" internal/dedup/engine.go` returns ≥1).
- [ ] `go test -race ./internal/dedup/...` is clean (no data race on the shared state named above).
- [ ] Anti-over-suppression: N/A (this task adds no filter/guard/veto/skip/dedupe path — it parallelizes an existing loop; correctness = identical output to the serial version).
- [ ] `make ci` green; `go vet` clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-07-05" <file>`).

## Commit message

```
perf(dedup): parallelize FullScan main-pass Layer-1 checks while keeping Layer-2 embedding batching serial (CONC-4)

Parallelize the previously single-threaded loop via registry.RunItems, guarding
the shared state noted in the brief so the parallel pass produces identical output.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `grep -n "registry.RunItems" internal/dedup/engine.go` — if the target loop already routes through RunItems, this task may be complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit; the loop returns to sequential, no data or schema is touched, siblings unaffected.
