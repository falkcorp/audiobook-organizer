<!-- file: docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f8b2d6e-9c1a-4e3f-b7d5-2a6c8e0f1b3d -->
<!-- last-edited: 2026-07-05 -->

# Concurrency Audit — Single-Threaded Hotspots

**Date:** 2026-07-05
**Branch:** `docs/concurrency-audit`
**Scope:** Read-only sweep for sequential (single-threaded) loops over large collections
(whole-library or large-candidate-set scale) that do meaningful per-item CPU- or I/O-bound
work with no goroutines, worker pool, or `errgroup` — and would plausibly run materially
faster if parallelized.
**Method:** Three parallel read-only agents covering (1) `internal/dedup/` + `internal/ai/`,
(2) `internal/database/` + `internal/plugins/dedup/` + `internal/plugins/maintenance/` +
`internal/itunes/`, and (3) `internal/metadata/` + `internal/server/` +
transcription/whisper code. No production code changed in this branch — this document is
the artifact for follow-up implementation planning (a separate design pass, not code fixes).

## Why this audit happened

`dedup.full-scan` was run on prod on 2026-07-05 and its "unified composite scoring" pass
(`Engine.FullScan`'s second loop, `internal/dedup/engine.go`) went **silent for over 3
hours** at 100%+ CPU on a single core — a plain `for _, book := range books { ...
runUnifiedScoringForBook ... }` loop over ~29,200 books with zero concurrency. A same-night
fix (PR #1805) added progress/ETA reporting for that pass, but the underlying loop is still
fully sequential — this audit exists to find every other instance of the same anti-pattern
before deciding what to fix and in what order.

## Findings

Ranked by confidence that parallelizing it would produce a material speedup. "Item scale"
is the rough population the loop iterates at typical/current library size (~29,200 books,
~9,081 authors, ~15,233 series, historically up to ~380K pending dedup candidates before
recent cleanup work — see the July 3-5 gold-label/orphan-embedding cleanup entries in
CHANGELOG.md).

### High confidence

| file:line | what's serial | item scale | why it matters |
|---|---|---|---|
| `internal/dedup/engine.go` `FullScan` unified-scoring pass (~line 2547) | `for _, book := range books { runUnifiedScoringForBook(...) }` — composite scoring collectors (exact-file, ISBN, metadata-fuzzy, duration, AcoustID/LSH) per book | ~29,200 books | **Confirmed on prod**: 3+ hours, 100%+ CPU, single core. Reference case for this whole audit. |
| `internal/dedup/engine.go` `BookSignatureScan` (~line 3634) | Nested `for i, bookA := range booksWithSig { for j := i+1; ... }` — pairwise `fingerprint.BookSignatureSimilarityMasked` (bitmask/Hamming compare) + DB upsert per matching pair | O(n²/2) over all books with a `BookSigV1` signature (subset of ~29K) | **Worst algorithmic shape found** — quadratic, not linear. At even 10-20K signed books this is hundreds of millions of comparisons, fully sequential. Best parallelization candidate: shard the outer loop across workers with a sharded/locked `emitted` map. |
| `internal/dedup/engine.go` `AcoustIDScan` (~line 3438) | `for i, book := range books { GetBookFiles(...); per-file LSH lookup + per-candidate Hamming sim; per-segment exact-match DB lookup }` | ~29,200 books × files × up to 7 segments × up to 200 LSH candidates each | Several DB calls per book plus fingerprint math, all sequential. The LSH index itself is sub-linear per book, but the outer per-book loop is still serial at full-library scale. |
| `internal/server/acoustid_backfill.go:127` `backfillAcoustIDs` | `for _, b := range books { for _, f := range files { fingerprintBookFile(...); time.Sleep(10ms) } }` — CPU-bound fpcalc/ffmpeg fingerprinting, serial, **plus an explicit sleep after every successful fingerprint** | Whole library (all books × all files) | Compounds a CPU-bound serial loop with an artificial per-item delay. Its sibling `internal/plugins/acoustid/backfill.go` does the same work but is correctly parallelized via `registry.RunItems` with a configured `Concurrency` — this server-side variant was apparently never updated to match and should probably be deleted/redirected to the parallel plugin path rather than fixed in place. |
| `internal/server/embedding_backfill.go` `runEmbeddingBackfill` (~lines 74, 114) | Loops all books/authors serially calling `EmbedBook`/`EmbedAuthor` (embedding compute + DB write), no goroutines | ~24-29K books/authors per startup backfill | Whole-library, startup-path backfill with no concurrency. |
| `internal/plugins/dedup/embed_scan.go:125` `dedup.embed-scan` (sync path) | Calls `engine.EmbedBook` (network call) per book, one at a time | ~29,200 books | Direct `FullScan` analogue — network-bound, embarrassingly parallel (respecting the embedding backend's own rate limits). |
| `internal/plugins/maintenance/tag_backfill.go:125` | `metadata.ExtractMetadata` (audio tag file parse) per book_file, sequential | All book_files needing tags (potentially 29K+) | Real file I/O + CPU per item, no pool. |
| `internal/plugins/dedup/mine_gold_labels.go:85` and `internal/plugins/dedup/dataset_backfill.go:122` | Per-candidate `GetBook`×2 + `GetBookFiles`×2, **uncached** (unlike `dedup-exact-triage`/`drain-stale`, which memoize book lookups) | Up to the 1,000,000-candidate op limit | Worse than its memoized siblings — 4 DB reads per candidate with no memoization, let alone concurrency. |
| `internal/itunes/service/importer.go:1073` `organizeImportedBooks` | Per-book file rename/organize + `UpdateBook`, sequential | All books with `LibraryState=="imported"` (full library on bulk import) | Real file I/O + DB write per item. |
| `internal/itunes/service/importer.go:1026` `enrichImportedBooks` | Per-book metadata-fetch network call, sequential, with deliberate rate-limit backoff sleeps | Same imported-books subset | Network-bound; any parallelization here must preserve the external rate limit (bounded pool, not naive fan-out). |

### Medium confidence

| file:line | what's serial | item scale | why it matters |
|---|---|---|---|
| `internal/dedup/engine.go` `PurgeStaleCandidates` (~line 2780) | Per-candidate `GetBookByID` on cache miss (memoized, but the miss path is still serial) | Up to 1,000,000 pending candidate rows | Memoization caps DB calls to roughly unique-book count, but that's still a large serial fetch loop for a big backlog. |
| `internal/dedup/engine.go` `FullScan` main pass (~line 2464) | `for i, book := range books { GetAuthorByID; checkExactFileHash; checkExactISBN; checkExactTitle; checkDurationMatch }` | ~29,200 books | Each item is individually cheap ("cheap and synchronous, no API calls" per its own comment), but at 29K books this is the same loop shape as the confirmed bottleneck and worth profiling alongside it rather than assuming it's fine. |
| `internal/plugins/maintenance/duration_backfill.go:112` and `internal/itunes/service/path_reconcile.go:87` | Per-book `GetBookFiles` DB read, sequential | All books (~29K, up to 100K op cap) | Classic N+1 read pattern at full-library scale. |
| `internal/server/handlers/metadata/handler.go:794` `bulkFetchMetadataImpl` | Per-book DB read + external metadata-source network search, serial | User-selected `req.BookIDs` (UI "select all" could be hundreds/thousands) | Real bottleneck on large bulk selections, though request-driven rather than a fixed whole-library sweep. |
| `internal/metadata/enhanced.go:166` `BatchUpdateMetadata` (and `:635` `ImportMetadata`) | Per-item `ValidateMetadata` + `GetBookByID` + `UpdateBook`, serial | Scales with bulk-edit request payload size | Same "request-driven bulk op, serial DB round-trips" pattern as the fetch handler above. |
| `internal/plugins/maintenance/intro_transcribe.go` silence-retry ffmpeg extraction (~lines 576-586, 608-622) | Serial `exec.CommandContext(ctx, "ffmpeg", ...)` subprocess per book in the silent-retry queue, unlike the main Step 2 extraction (~lines 428-484) which correctly uses `sync.WaitGroup` + goroutines | Tens of books per run (the silent-transcript subset of a 200-book page), not thousands | Real serial CPU-bound work, but much smaller scale than the other findings. |

### Lower confidence / already partially mitigated (listed for completeness, not urgent)

- `internal/dedup/auto_resolve.go:112` `AutoResolveCertain` — serial `GetBookByID`×2 + apply-time `MergeBooks` per CERTAIN-band candidate. **Correctness-constrained**, not a "just add a worker pool" fix: the apply path must avoid double-merging a book across two pairs processed concurrently in the same run. Needs a partition-by-disjoint-book-ID-set redesign, not a blind fan-out.
- `internal/plugins/maintenance/dedup_triage.go` (`server_maintenance_deps.go:294`) and `internal/dedup/drain_stale.go` — per-candidate but book-lookup is memoized; cheap classify-only CPU work.
- `internal/database/migrations.go:621` `migration014UpPebble` — per-book `UpdateBook` write, but a one-time migration, not a recurring op.
- `internal/itunes/backfill.go:83` `BackfillExternalIDs` — per-book `GetBookFiles` N+1, already flagged in-code as `TODO(PERF-5)`; one-time backfill.
- `internal/plugins/maintenance/title_backfill.go:141` — `UpdateBook` writes only for the subset of titles needing a strip fix, not full library.
- `internal/plugins/maintenance/itunes_regroup.go:244` `applyRegroupPlan` — sequential DB writes per group; likely *intentionally* serialized since it's mutating shared book/PID state, not a naive missed-opportunity.
- `internal/plugins/maintenance/auto_match_transcribed.go` — explicit `Concurrency: 1`, but only over books with a non-nil `TranscribedTitle` (small subset); may be intentionally rate-limited against a search API.
- `internal/itunes/service/path_repair.go:218` — per-track `os.Stat`/DB lookup only for *missing* tracks; the genuinely expensive step (tag scan) is already parallelized (`NumCPU()*4` workers).

### Checked and confirmed already parallel (excluded, listed so they aren't re-flagged later)

- `internal/ai/embedding_client.go` (`EmbedBatch`/`embedBatchRaw`) and `internal/ai/embedding_batch.go` / `openai_batch.go` — genuine single batched HTTP call per miss-set, not per-item serial.
- `internal/ai/llm_scorer.go` / `metadata_llm_review.go` (`ScoreMetadataCandidates`) — batches of 25 candidates per LLM call; interactive per-search scope, not whole-library scale.
- `internal/plugins/maintenance/intro_transcribe.go` Step 2 WAV extraction (`sync.WaitGroup` + `go func`) and Step 3 Whisper calls (`TranscribeBatch`).
- `internal/plugins/acoustid/fingerprint_rescan.go` (`sync.WaitGroup`, `fpRescanWorkers()`).
- `internal/plugins/acoustid/backfill.go` (`registry.RunItems` with configured `Concurrency`) — the correctly-parallelized sibling of the flagged `internal/server/acoustid_backfill.go`.
- `internal/server/server_lifecycle.go` cache warmers (`startCacheWarmers`) — each warmer is its own goroutine; internal loops iterate small fixed-size query sets, not whole-library scale.
- `internal/itunes/service/path_repair.go` tag-scan step — `NumCPU()*4` worker pool.

## Recommended fix patterns (for the follow-up design pass, not decided here)

Different findings need different concurrency shapes — a blind `go func()` fan-out is wrong
for some of these:

1. **Embarrassingly parallel, no shared mutable state across items** (e.g. `embed-scan`,
   `tag_backfill`, per-book fingerprinting): bounded worker pool (`errgroup.Group` +
   `SetLimit`, or a semaphore channel) sized to `runtime.NumCPU()` for CPU-bound work, or a
   smaller fixed concurrency for network-bound work respecting the backend's own rate
   limits.
2. **Pairwise O(n²) comparison with a shared dedup-set** (`BookSignatureScan`): shard the
   outer loop across workers, either with a sharded/locked `emitted` map or by pre-bucketing
   books so each worker owns disjoint output keys.
3. **Correctness-constrained apply paths** (`AutoResolveCertain`, `itunes_regroup`'s
   `applyRegroupPlan`): partition work into disjoint sets (e.g. by book ID or group ID) so
   parallel workers can never touch the same row, rather than adding raw concurrency to a
   loop that currently relies on sequential ordering for safety.
4. **Rate-limited external calls** (`enrichImportedBooks`, acoustid fpcalc throttling):
   bounded pool sized to respect the existing rate-limit/backoff logic, not unlimited
   fan-out — the goal is fewer wall-clock hours, not tripping the same throttle harder.
5. **Request-driven bulk ops** (`bulkFetchMetadataImpl`, `BatchUpdateMetadata`,
   `ImportMetadata`): same worker-pool pattern as #1, sized conservatively since these run
   inline on a user-facing HTTP request rather than as a background op with its own budget.

## Suggested priority order

1. `BookSignatureScan` (worst algorithmic shape — O(n²))
2. `FullScan`'s two passes (confirmed prod incident, now has progress reporting but still
   fully serial)
3. `AcoustIDScan`
4. `internal/server/acoustid_backfill.go` (delete/redirect to the already-parallel plugin
   sibling rather than fix in place)
5. `embed_scan.go` sync path + `embedding_backfill.go`'s startup loops
6. Everything else in the High/Medium tables, roughly in scale order

## Related

- CHANGELOG.md, 2026-07-04/05 entries: gold-label rebuild (#1800), `DeleteBook`
  orphaned-embedding fix (#1802), retroactive orphan cleanup (#1803), calibration
  observability (#1804), `FullScan` phase-2 progress/ETA (#1805) — the same-night work that
  motivated this audit.
- See CLAUDE.md's "Coding Standards" section for the new project-wide concurrency
  guideline added alongside this audit.
