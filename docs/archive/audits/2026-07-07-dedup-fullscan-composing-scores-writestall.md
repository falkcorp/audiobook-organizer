<!-- file: docs/archive/audits/2026-07-07-dedup-fullscan-composing-scores-writestall.md -->
<!-- version: 1.0.1 -->
<!-- guid: 3a7d1c92-8e64-4b05-9f2a-6c1e0b8d4f57 -->
<!-- last-edited: 2026-08-11 -->

# Root cause: `dedup.full-scan` "Composing scores" freeze — a Pebble write-stall from per-candidate fsync-under-lock (#19)

**Status:** root cause identified statically; fix shipped (NoSync candidate writes);
prod clean-completion confirmation pending.

## Symptom (recap)

`dedup.full-scan` on prod (op `01KWTFW0T833JP6Y3PZCZK6YEG`, 2026-07-06) advanced its
unified-scoring "Composing scores" pass to **44%, then produced zero progress log
lines for 9+ hours** — genuinely frozen, not slow. `DELETE /operations/v2/:id`
(graceful cancel) had no effect; only `systemctl restart` recovered. Service showed
swap usage (456M, peak 772M) at the time. This recurred *after* CONC-2 parallelized
the pass and PR #1809 added a per-candidate `ctx.Err()` check — neither helped.

## What was ruled out (static trace, 2026-07-07)

Both originally-suspected causes are already handled at HEAD:

- **`de.mergeMu` deadlock** — `mergeMu` (`internal/dedup/engine.go`) guards only the
  Layer-1 *scan* phase (`handleFileHashMatch → MergeBooks`). The stall was in the
  *score* phase, which never takes it.
- **Ollama call ignoring ctx** — `internal/ai/embedding_client.go` already uses
  `http.NewRequestWithContext` with a 30 s per-attempt timeout and bounded retries,
  and exits on parent-ctx cancel. Moreover the score phase makes **no** network
  calls: embeddings are read from *stored* candidate rows, not recomputed.

## Root cause

The score phase runs `runUnifiedScoringForBook` across a `registry.RunItems` worker
pool at `runtime.NumCPU()` (`engine.go` FullScan, "score" phase). Each scored
candidate is persisted through `EmbeddingStore.UpsertCandidate → UpsertCandidateNew`
(`internal/database/embedding_store.go`), which:

1. Takes a **single store-wide `s.mu.Lock()`**, and
2. Holds it across **`b.Commit(pebble.Sync)` — a synchronous `fdatasync`.**

Two compounding failures result:

- **Serialization.** Every `NumCPU` worker contends on the one `s.mu`, each holding
  it across a disk fsync. The "parallel" pass is actually serial, and each critical
  section blocks on I/O. A goroutine parked in `sync.Mutex.Lock()` is **not
  ctx-cancellable**, so workers never reach `RunItems`' between-item `ctx.Done()`
  poll — which is exactly why graceful cancel did nothing.
- **Write-stall (the amplifier that turns minutes into hours).** Per-candidate
  `Sync` commits flood Pebble's L0. Once compaction falls behind — amplified by host
  swap pressure — Pebble's **write-stall** mechanism blocks *all* reads and writes
  DB-wide. Every worker then parks inside a Pebble call that never returns → zero
  progress, uncancellable, hard-restart-only. Corroboration: `reporter_db.go`'s
  progress path already carries a watchdog that stamps a lock-free clock "before any
  lock or DB write ... so it never fires due to a blocked `UpdateOpProgressV2` call
  during PebbleDB L0 compaction" — the team had already seen L0-compaction stalls.

"Zero progress log lines" fits precisely: workers are stuck *inside*
`runUnifiedScoringForBook`, before ever reaching the progress emit.

## Fix (this PR)

Per-row dedup-candidate writes (`UpsertCandidateNew` ×2 commit sites,
`DeleteCandidate`) switch `pebble.Sync → pebble.NoSync` via a single documented
`candidateWriteOpts` var. This removes the per-write fsync that (a) made each `s.mu`
critical section block on disk and (b) flooded L0. The critical section becomes a
fast in-memory batch apply, so `s.mu` releases immediately and `RunItems`'
between-item cancel poll works again; and L0 write pressure drops, so no write-stall.

**Correctness is unchanged:** NoSync alters only fsync durability, not atomicity or
visibility. The `s.mu`-guarded pair-uniqueness + `nextID` counter invariants are
untouched.

**Durability tradeoff (owner-approved):** NoSync writes are still written to the WAL
and become durable as Pebble flushes memtables to SST; a graceful `Close` (the
`systemctl restart` / SIGTERM path) flushes the WAL, so a graceful restart loses
nothing — locked in by `TestUpsertCandidate_SurvivesGracefulClose`. Only a hard
crash (kill -9 / power loss) can drop the last few seconds of candidate writes, and
dedup-candidate scores are recomputable derived data (a re-scan regenerates them).

Batch candidate ops (one commit for many rows: `MarkCandidatesAsMergedForEntity`,
`RemoveCandidatesForEntity`, `CanonicalizeCandidates`, `BackfillEntityIndex`) and the
embedding-vector / cache paths keep `pebble.Sync` — they are not the per-row flood.

## Regression guards

- `TestUpsertCandidate_SurvivesGracefulClose` — the durability contract the NoSync
  change depends on (write → graceful `Close` → reopen → row present).
- `TestCandidateWritePath_ConcurrentNoRace` — `-race` safety + same-pair
  no-duplication under a concurrent Upsert/Delete storm matching the score-phase pool.

## Follow-ups

- **Prod confirmation is the real gate** (not the static read): `make deploy-debug`,
  re-run `dedup.full-scan`, capture periodic `/debug/pprof/goroutine` + `heap` dumps,
  and require a **clean full completion** before closing #19. If any residual stall
  appears, the escalation is per-pair striped locks so commits run truly
  concurrently (Pebble WAL group-commit coalesces the fsyncs) — kept in reserve.
- A completed run clears the ~10,114 "unknown" candidate backlog (unblocks re-scoring
  + calibration).
