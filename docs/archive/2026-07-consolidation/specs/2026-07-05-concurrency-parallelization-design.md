<!-- file: docs/specs/2026-07-05-concurrency-parallelization-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: e62615dc-7ad9-47df-992a-1592e26e301e -->
<!-- last-edited: 2026-07-05 -->

# Concurrency Parallelization — Design Spec

**Status:** Draft <!-- flip to: Approved — ready for implementation planning, at Gate 2 -->
**Scope:** Go backend only — the single-threaded whole-library/large-set loops catalogued in the concurrency audit. No schema changes, no API-surface changes, no frontend.
**Parent audit:** `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`

---

## Motivation

On 2026-07-05 a prod `dedup.full-scan` run went **silent for 3+ hours at 100%+ CPU on a single core** — `Engine.FullScan`'s unified-scoring pass is a plain `for _, book := range books` loop over ~29,200 books with zero concurrency. PR #1805 added progress/ETA reporting for that pass, but the loop itself is still fully sequential. The follow-up audit (`docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`) swept the codebase with three read-only agents and catalogued **16 single-threaded hotspots** — 10 high-confidence, 6 medium — that do meaningful per-item CPU- or I/O-bound work with no worker pool, plus a fix-pattern taxonomy and a priority order.

This spec turns that audit into an execution-ready plan: **15 parallelization tasks across 5 workstreams**, each reusing the existing `registry.RunItems` primitive, plus two correctness-constrained items deferred to a design session. Every anchor in every task was re-verified at HEAD during planning (the audit's line numbers had already drifted).

**Goal:** eliminate the single-threaded whole-library hot paths by routing each catalogued loop through a bounded worker pool, with **identical output to the serial version** as the correctness bar.

## Goals

- Route every high/medium-confidence audit finding through `registry.RunItems` with an appropriately-sized `Concurrency`, preserving existing progress reporting.
- Preserve output semantics exactly — a parallelized scan emits the same dedup candidates; a parallelized backfill writes the same rows. No behavior change beyond wall-clock.
- Guard every shared mutable structure a pool touches (the audit's `emitted`/cache maps, counters, circuit-breakers) against data races, proven with `go test -race`.
- Keep each task weak-model-executable in isolation and conflict-free under parallel execution (collision-derived waves).

## Non-goals (v1)

- Redesigning correctness-constrained apply paths (`AutoResolveCertain`'s merge, `applyRegroupPlan`'s PID mutation) — deferred to Bucket 2; a blind pool there is unsafe.
- Tuning the LSH/HNSW index internals or the embedding backend — out of scope; we parallelize callers, not the stores.
- Introducing a new concurrency framework — `registry.RunItems` already exists and is the standard; adding a second primitive is explicitly rejected.
- Changing `Concurrency` defaults globally or adding a shared config surface — each task picks a local, documented value.

## Decisions (locked during design)

1. **Reuse `registry.RunItems[T]`, do not hand-roll pools.** `internal/operations/registry/run_items.go` already provides `RunItems[T](ctx, Reporter, items, fn, RunItemsOptions{Concurrency,...})` with a sequential path at `Concurrency==1`, a parallel path above, and Reporter-based progress. Live callers exist (`internal/plugins/acoustid/backfill.go:118`, `internal/reconcile/itunes_heal.go:731`, `internal/plugins/maintenance/intro_transcribe.go:190`). Rejected alternative: raw `errgroup`/`sync.WaitGroup` per site — would fragment the pattern and re-implement progress/error-mode handling five times.
2. **Correctness bar is "identical output," not "faster."** Every task ships a test asserting the parallel pass produces the same result set / same writes as the serial version (order-independent) plus a `-race` run. This is the anti-over-suppression discipline applied to concurrency: a pool that silently drops writes through an unguarded map still "passes" a naive speed test.
3. **Pool size by workload class, locally.** CPU-bound → `runtime.NumCPU()`; I/O-bound tag/file reads → `runtime.NumCPU()*4` (established precedent at `internal/itunes/service/path_repair_resolver.go`); network/rate-limited (embeddings, metadata sources) → a small fixed const with a named knob that respects the backend's existing rate limit. Rejected alternative: one global concurrency setting — wrong for the three different workload classes.
4. **Shared-state hazards are per-task, named, and guarded.** The scouts found the burden is uneven: `FullScan`-unified has none (independent per book), `BookSignatureScan` has one `emitted` map, `AcoustIDScan` has **four** maps + a counter, `FullScan`-main has serial circuit-breaker state that must NOT be parallelized. Each brief names its exact shared state and the guarding approach.
5. **`FullScan` main-pass is split, not blindly parallelized.** Its Layer-2 embedding batching + circuit-breaker (`chunkIDs`, `embedConsecutiveFails`, `embeddingsGaveUp`) is loop-carried serial state; only the Layer-1 exact checks parallelize. Rejected alternative: parallelize the whole loop — corrupts the batch accumulation and defeats the breaker.
6. **`acoustid_backfill.go` is deleted, not fixed.** The serial server variant (`backfillAcoustIDs`, with a per-item `time.Sleep`) duplicates the already-parallel plugin op `acoustid.backfill`. It has exactly one caller (`server_lifecycle.go:910`); the fix is to remove that call and delete the file, after verifying no shared helper (e.g. `synthesizeBookSignatureForBook`) is referenced elsewhere. Rejected alternative: add a pool to the server variant — keeps two divergent implementations of the same job.
7. **Design-judge panel skipped (auditable):** only one viable architecture per finding survives — the fix pattern is dictated by the workload class, and stakes are not an irreversible schema/data migration (every task is a revert-the-commit rollback). Per the plan-op rule, the skip is recorded here rather than paneled.

## Data model

No new persisted types. The only code-level shape introduced per task is the `RunItemsOptions` literal at each call site:

```go
// existing primitive — reused, not modified
func RunItems[T any](
    ctx context.Context,
    r registry.Reporter,
    items []T,
    fn func(ctx context.Context, item T) error,
    opts ...registry.RunItemsOptions,
) error

type RunItemsOptions struct {
    Concurrency    int           // <1 => safe default; ==1 => sequential path; >1 => parallel
    PerItemTimeout time.Duration
    ErrMode        ErrMode       // ErrModeFail | ErrModeCollect
    Label          func(i, total int) string
}
```

### Persistence

Unchanged. Every task reads and writes through the existing stores (`bookStore`, `embedStore`); the only new requirement is confirming those stores are goroutine-safe at the chosen `Concurrency` (Decision 2 gates enabling >1 on that confirmation).

## Components

### C1. dedup engine scans (`internal/dedup/engine.go`)

The four whole-library scan loops. All in one file, so they **serialize** (WS-1, four waves). `BookSignatureScan` is O(n²) — the worst algorithmic shape and highest value; `AcoustIDScan` carries the heaviest shared-state burden (four maps + counter); `FullScan`-main requires the Layer-1/Layer-2 split. Fail-safe: if store goroutine-safety cannot be confirmed, the task ships at `Concurrency==1` (sequential path, no behavior change) and flags the blocker.

### C2. whole-library backfills (`internal/plugins/**`, `internal/server/embedding_backfill.go`)

Embarrassingly-parallel per-item backfills in disjoint files (WS-2, one parallel wave): `embed-scan`, startup embedding backfill, tag backfill, and the gold-label/dataset backfills (which additionally need book-lookup memoization mirroring `drain_stale.go` before the pool). Network-bound members use conservative fixed concurrency; I/O-bound use `NumCPU*4`.

### C3. acoustid consolidation (`internal/server/acoustid_backfill.go`, `server_lifecycle.go`)

Delete/redirect (WS-3, single task, ⚠ review-critical). Not a pool-add — a caller-wiring change.

### C4. itunes import passes (`internal/itunes/service/importer.go`)

`organizeImportedBooks` (file I/O) and `enrichImportedBooks` (network + a `consecutiveErrors>=5` circuit-breaker). Same file → serialize (WS-4, two waves). The enrich task must reframe "consecutive failures" as a shared atomic that cancels the context, since "consecutive" is meaningless under a pool.

### C5. request-driven bulk ops (`internal/server/handlers/metadata/handler.go`, `internal/metadata/enhanced.go`, two remaining N+1 backfills)

Conservative request-scoped pools (WS-5, one parallel wave, P3). Sized low because they run inline on user-facing HTTP requests.

## Migration / integration

Each task is a localized, additive edit: replace one `for` loop with a `registry.RunItems` call over the same items, add shared-state guarding, add a `-race` + same-output test. No caller of the changed function sees a signature or semantics change. The one exception is C3, which removes a call site and a file — its brief lists the single caller and mandates a pre-delete symbol-reference grep.

## Milestones

- **M1 — WS-1 dedup-engine (P1).** The confirmed incident and its neighbors; serial waves in `engine.go`. Highest value (`BookSignatureScan` O(n²) first).
- **M2 — WS-2 backfill-pools (P2).** Parallel wave; the `/parallel-sweep` candidate.
- **M3 — WS-3 acoustid-consolidation (P2).** Single ⚠ task; removes a serial duplicate.
- **M4 — WS-4 itunes-import (P2).** Serial waves; rate-limit/circuit-breaker preservation.
- **M5 — WS-5 bulk-ops (P3).** Conservative request-scoped pools.

Each milestone is independently shippable and additive — no milestone depends on another (different files across workstreams), so M1–M5 may run concurrently across workstreams while serializing within the collision-bound ones.

## Files modified

| File | Change |
|---|---|
| `internal/dedup/engine.go` | 4 scan loops → pools (WS-1/T01–T04) |
| `internal/plugins/dedup/embed_scan.go` | pool (WS-2/T01) |
| `internal/server/embedding_backfill.go` | pool (WS-2/T02) |
| `internal/plugins/maintenance/tag_backfill.go` | pool (WS-2/T03) |
| `internal/plugins/dedup/mine_gold_labels.go`, `dataset_backfill.go` | memoize + pool (WS-2/T04) |
| `internal/server/acoustid_backfill.go`, `internal/server/server_lifecycle.go` | delete + redirect (WS-3/T01) |
| `internal/itunes/service/importer.go` | 2 import passes → pools (WS-4/T01–T02) |
| `internal/server/handlers/metadata/handler.go`, `internal/metadata/enhanced.go` | pools (WS-5/T01–T02) |
| `internal/plugins/maintenance/duration_backfill.go`, `internal/itunes/service/path_reconcile.go` | pools (WS-5/T03–T04) |

## Testing

| Test | Asserts |
|---|---|
| `Test<Scan>ParallelSameAsSerial` (per WS-1 task) | parallel pass emits the identical candidate set as the serial version on a fixture library; no lost writes through guarded shared state |
| `go test -race ./internal/dedup/...` etc. | no data race on any named shared map/counter |
| `Test<Backfill>ParallelSameWrites` (per WS-2/WS-5 task) | same rows written, order-independent |
| WS-3 caller-reference grep | no symbol in `acoustid_backfill.go` is referenced after deletion |
| `make ci` per PR | full gate green |

## Rollback

Every task is a single-commit `git revert` back to the sequential loop — no data, schema, or API is touched. WS-3 (the delete) reverts by restoring the file and its one call site. There is no feature flag because there is no behavior change to gate: the only observable difference is wall-clock, and `Concurrency==1` is always a safe fallback if a store turns out not to be goroutine-safe.

## Open questions (resolved — recorded for the plan)

1. ~~Are `bookStore`/`embedStore` goroutine-safe at `Concurrency>1`?~~ → Each WS-1/WS-2 task must confirm before enabling >1; if not, ship at `Concurrency==1` (sequential path) and file the store-safety blocker. Gating constraint, recorded in each brief.
2. ~~Parallelize `FullScan` main-pass wholesale?~~ → No — split Layer-1 (parallel) from Layer-2 batching + circuit-breaker (serial). Locked in Decision 5.
3. ~~Fix or delete the serial `acoustid_backfill.go`?~~ → Delete + redirect to the plugin op. Locked in Decision 6.
4. ~~Include `AutoResolveCertain` / `applyRegroupPlan`?~~ → No — Bucket 2 (design session first; correctness-constrained). See the BREAKDOWN.
