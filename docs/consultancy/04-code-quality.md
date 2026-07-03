<!-- file: docs/consultancy/04-code-quality.md -->
<!-- version: 1.0.0 -->
<!-- guid: e35c64b1-5c2f-408e-8b86-1e480f05c015 -->
<!-- last-edited: 2026-07-02 -->

# Consultancy Evaluation — Code Quality & Bugs (2026-07-02)

Evaluation run by a read-only multi-agent workflow (bug-hunt agent + code-review/counter-argument agent, followed by adversarial verification and an advisor boundary review). All findings are cited as `file:line` against the repo state on 2026-07-02.

## Executive Summary

Two specialist reports converge on three systemic problems and a set of concrete bugs, several of them live in production right now.

**1. Embedding scoring is silently returning zero candidates during the live bge-m3 cutover (BUG-1 / QUAL-4).** `EmbeddingScorer.queryVector` returns the cached book vector without checking its `Model` field. Books not yet re-embedded hold OpenAI vectors (1536/3072-dim) while candidate vectors come fresh from Ollama at 1024-dim; `CosineSimilarity` returns 0 on length mismatch, every candidate falls below `EmbeddingMinScore` (0.82), and because the scorer returned a nil error there is no F1 fallback. Metadata search silently returns zero results for every not-yet-re-embedded book. The code-review agent independently found two interacting gaps in the same tier: the 0.82 threshold is applied to a post-penalty/pre-boost composite rather than raw similarity, and degenerate all-zero scorer success never triggers F1 fallback.

**2. The memdb-projection write-back footgun is fixed for BookFile but NOT for Book (QUAL-2, CTR-1, CTR-2).** `stripBookForMemdb` clears Description/VersionNotes/BookSigV1* for the in-memory projection; `GetAllBooks` returns stripped rows on the production `UseMemDB` path; `UpdateBook` marshals the caller's struct verbatim with no preserve-on-empty guard (unlike `UpsertBookFile`/`BatchUpsertBookFiles`, which explicitly guard the equivalent fields after the #1552 fingerprint-wipe fix). `reconcile.AssignOrphanVGs` demonstrably reads stripped books and writes them back via `UpdateBook`, wiping Description and dedup book-signatures. The counter-argument findings generalize this: full-column-replacement `UpdateBook` puts an unenforceable merge contract on 149 call sites (CTR-1), and reusing the same `*Book`/`*BookFile` type for two incompatible representations is the root cause of the whole footgun class (CTR-2). Verification tempered severity from critical to high: `UpdateBook`'s CoW `book_ver:` snapshot captures the full pre-wipe row, so damage is recoverable.

**3. A mass mechanical printf→slog conversion corrupted observability repo-wide (BUG-4 / QUAL-1).** Dozens of log calls have duplicate attr keys, literal `"value0"` strings logged as both key and value (25 sites), %-verbs left in messages with the args dropped entirely (`plugin/registry.go:34` loses the duplicate plugin ID outright), and bare positional args producing `!BADKEY` — exactly on the merge/quarantine/metafetch/fingerprint paths where prod debugging happens, directly hampering diagnosis of BUG-1.

Additional confirmed defects: the MATCH-6 TOCTOU in transcription auto-match apply (BUG-3 / QUAL-3), the persisting PEBBLE-CLOSED shutdown race despite the mitigation (BUG-2), retry loops that burn full backoff cycles on permanent errors including the currently-live OpenAI `insufficient_quota` (BUG-5 / QUAL-6), a cover-presence hard filter that can discard the top-scoring candidate (BUG-6 / QUAL-5), unclamped multiplicative score stacking that makes thresholds tier-incomparable (CTR-3), init()-registered global plugin state (CTR-4), and a pagination-race guard that fixed one instance rather than the class (QUAL-7).

Positive findings: the latent `UpsertBookFile` fingerprint-wipe is now guarded (`pebble_store.go:9974-9985`) with pebble-direct existing-row lookups, and the BookFile batch path carries the same guard.

## Advisor Verification

The advisor's P4 boundary assessment (independent spot-check of the raw findings):

> Findings verify cleanly — I spot-checked BUG-1 (embedding_scorer.go:92-95 returns cached vector modelless; CosineSimilarity embedding_store.go:1214 returns 0 on dim mismatch), QUAL-2 (GetAllBooks memdb path pebble_store.go:1392, strip at memdb_strip.go:34-40, write-back reconcile.go:1149), BUG-3 (server_maintenance_deps.go:378-393 ignores identity params, applies cache slot 0), and BUG-4 slog sites (plugin/registry.go:34 drops the arg; embedding_client.go literal "value0"). One overreach: QUAL-2 "critical" is tempered by UpdateBook's CoW snapshot (pebble_store.go:2688-2698 writes old row to book_ver: before overwrite) — wipes are recoverable, so severity is high, not critical. The two reports duplicate ~5 findings (BUG-3=QUAL-3, BUG-4=QUAL-1, BUG-5=QUAL-6, BUG-6=QUAL-5, BUG-1≈QUAL-4); treat as one set. No fabricated citations found.

Corrections applied in this report:

- **QUAL-2 downgraded critical → high** inline (CoW `book_ver:` snapshots make wipes recoverable; trigger is a manual admin op, not automatic).
- **QUAL-1/BUG-4 held at medium** per the verification verdict (real and widespread, but impact is confined to log quality/diagnostics, not behavior or data).
- **Duplicates consolidated**: each deduplicated finding is presented once below with both IDs noted.

Advisor engagement context worth keeping in mind when reading severities: PebbleDB is the only production store; the system is mid-cutover (OpenAI quota-dead, Ollama primary, bge-m3 re-embed in flight via `dedup.embed-scan`); prod is live with ~50K books, so all of these are production-affecting.

## Adversarial Verification Verdicts

An independent verification pass re-derived the top findings end-to-end from source:

| Finding | Verdict | Severity after verification | Notes |
| --- | --- | --- | --- |
| BUG-1 | Real | High | Confirmed end-to-end: modelless cached-vector fast path → CosineSimilarity=0 on 1536-vs-1024 dim mismatch → nil error means no F1 fallback → +0.15 max bonus < 0.82 MinScore drops all candidates. Gated on `MetadataScoring.EmbeddingEnabled` (prod state unconfirmed), but ~12K skipped non-primary versions keep stale vectors permanently, so the exposure window is not just the cutover. |
| QUAL-1 (= BUG-4) | Real | Medium (downgraded from high) | All ten citations verified by direct read. Real, widespread, on live code paths — but impact is confined to log quality/diagnostics, not behavior or data. |
| QUAL-2 | Real | High (downgraded from critical) | Verified end-to-end (strip → memdb GetAllBooks → unguarded UpdateBook full replace; AssignOrphanVGs provably wipes fields, reachable via POST /operations/assign-orphan-vgs). Downgraded because CoW `book_ver:` snapshots retain old data and the trigger is a manual admin op, not automatic. |
| CTR-1 | Real | High | UpdateBook confirmed full-replacement with no field preservation; stripped and canonical Books are type-indistinguishable; a concrete prod-reachable violation exists (AssignOrphanVGs); CoW then snapshots degraded rows. Quarantine citation is safe (direct Pebble scan), but the architectural footgun plus demonstrated instance stands. |
| CTR-2 | Real | High | All citations verified: type-indistinguishable projections, silent representation switch on UseMemDB, manual "keep both in sync" guard on the BookFile side, zero Book-field preservation in UpdateBook today. The class already caused real data loss (PR #1552 fingerprint wipe), patched per-instance. |

## Findings Table

Duplicated findings across the two reports are merged (BUG-3=QUAL-3, BUG-4=QUAL-1, BUG-5=QUAL-6, BUG-6=QUAL-5, BUG-1≈QUAL-4). Severities reflect post-verification values.

| ID | Severity | Impact | Effort | Title |
| --- | --- | --- | --- | --- |
| BUG-1 / QUAL-4 | High (verified) | High | Low | Stale-model cached query vector zeroes all embedding scores; min-score filter drops every candidate with no F1 fallback; MinScore also applied at the wrong point in the pipeline |
| QUAL-2 | High (downgraded from critical) | High | Low | UpdateBook full-replacement lacks memdb preserve guard; AssignOrphanVGs provably wipes Description/BookSigV1 via memdb round-trip |
| CTR-1 | High (verified) | High | Medium | Counter: full-column-replacement UpdateBook is the wrong write primitive for a 149-call-site codebase |
| CTR-2 | High (verified) | High | High | Counter: memdb field-stripping reuses the same *Book/*BookFile type for two incompatible representations |
| BUG-2 | Medium | Medium | Medium | Registry.Shutdown can return with store-touching goroutines still live (PEBBLE-CLOSED variant persists despite fix) |
| BUG-3 / QUAL-3 | Medium | Medium | Low | MATCH-6 TOCTOU confirmed: ApplyTranscriptionCandidate applies whatever is cache slot 0 at apply time, not the gated candidate |
| BUG-4 / QUAL-1 | Medium (downgraded from high) | Medium | Low–Medium | Repo-wide corrupted slog conversions: dropped values, literal "value0" pairs, bare values as keys, duplicate attr keys |
| BUG-5 / QUAL-6 | Medium | Medium | Low | No permanent-error classification in either retry implementation; 4xx/insufficient_quota retried with full backoff |
| CTR-3 | Medium | Medium | Medium | Counter: unclamped multiplicative score stacking makes thresholds tier-incomparable |
| CTR-4 | Medium | Medium | Medium | Counter: init()-registered global plugin registry trades explicit wiring for import-order coupling and untestable global state |
| BUG-6 / QUAL-5 | Low | Low–Medium | Low | Cover filter hard-drops cover-less candidates regardless of score, before sort and LLM rerank, including direct ASIN matches |
| QUAL-7 | Low | Low | Medium | Fetch-race guard is instance-level (useLibraryQuery), not a shared abstraction — the pagination-race class is not structurally fixed |

## Findings

### BUG-1 / QUAL-4 — Stale-model cached query vector zeroes all embedding scores; min-score filter drops every candidate with no F1 fallback (High, verified)

**Detail (BUG-1):** `EmbeddingScorer.queryVector` (embedding_scorer.go:93-97) returns the EmbeddingStore-cached book vector whenever one exists, without checking the record's `Model` field (which exists — embedding_store.go:95) against the client's current model. During the live bge-m3 cutover, books not yet re-embedded hold OpenAI vectors (1536/3072-dim); candidate vectors come fresh from Ollama at 1024-dim. `CosineSimilarity` returns 0 on length mismatch (embedding_store.go:1214-1216), so every candidate scores 0; `ApplyNonBaseAdjustments` adds at most +0.15 bonus, below `EmbeddingMinScore=0.82` (config.go:1351), so service_search.go:333-337 drops all candidates. Because `Score()` returned nil error, `ScoreBaseCandidates` never falls back to F1 (service_scoring.go:492-495). Even same-dim cross-model vectors give meaningless similarities. Result: metadata search silently returns zero candidates for every un-re-embedded book, right now in prod.

**Detail (QUAL-4, same tier):** Two interacting gaps. (1) Ordering: the 0.82 `EmbeddingMinScore` (config.go:1351) is applied at service_search.go:334 to the score AFTER `ApplyNonBaseAdjustments` penalties (compilation/length) but BEFORE author (1.5x), narrator (1.3x), series (1.4x) boosts at :340-370. A 0.9-cosine candidate hit by a compilation penalty drops below 0.82 and is discarded even though boosts would have carried it far above threshold — the threshold semantically targets raw embedding similarity but is compared against a partially-adjusted composite. (2) Fallback asymmetry: `ScoreBaseCandidates` falls back to F1 only on scorer error or length mismatch (service_scoring.go:492-499). A scorer that succeeds with all-zero/near-zero vectors (empty embedding text, degraded Ollama model output) returns tier=embedding, every candidate fails `score <= 0.82`, and the search returns zero candidates where F1 would have matched. With Ollama now primary, this failure mode is live.

**Verification note:** gated on `MetadataScoring.EmbeddingEnabled` (prod state unconfirmed); ~12K skipped non-primary versions keep stale vectors permanently, so the exposure window is not just the cutover.

**Recommendation:** In `queryVector`, only use the cached vector when `existing.Model` equals the client's model (or at minimum `len(existing.Vector)` matches the expected dimension); otherwise embed the query live. In `ScoreBaseCandidates`, treat an all-zero score vector as scorer failure and fall through to F1. Apply `EmbeddingMinScore` to the base score (raw cosine) before adjustments, or move the check after boosts.

**Citations:**
- internal/ai/embedding_scorer.go:93-97
- internal/ai/embedding_scorer.go:79-86
- internal/database/embedding_store.go:1213-1216
- internal/metafetch/service_search.go:328-337
- internal/metafetch/service_search.go:330
- internal/metafetch/service_search.go:334
- internal/metafetch/service_search.go:340
- internal/metafetch/service_scoring.go:491
- internal/config/config.go:1351

### QUAL-2 — UpdateBook full-replacement lacks memdb preserve guard; AssignOrphanVGs provably wipes Description/BookSigV1 via memdb round-trip (High, downgraded from critical)

**Detail:** `stripBookForMemdb` clears Description, VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments (memdb_strip.go:33-40). `GetAllBooks` routes through memdb when `UseMemDB` (pebble_store.go:1392-1393) — production config — returning stripped rows. `UpdateBook` (pebble_store.go:2664) marshals the caller's struct verbatim with no preserve-on-empty guard, unlike `UpsertBookFile:9974-9985` and `BatchUpsertBookFiles:10042-10053` which explicitly guard the equivalent BookFile fields. Concrete instance: `reconcile.AssignOrphanVGs` pages the whole library via `store.GetAllBooks` (reconcile.go:1115), sets VG fields, and calls `UpdateBook` (reconcile.go:1149) — every orphan book processed loses its Description and dedup book-signature. With 149 `UpdateBook` call sites, any other memdb-sourced caller repeats this silently.

**Severity adjustment (advisor + verification):** originally reported critical; downgraded to high because the CoW version snapshot (pebble_store.go:2688-2698 writes the old row to `book_ver:` before overwrite) preserves pre-wipe data — wipes are recoverable — and the demonstrated trigger (POST /operations/assign-orphan-vgs) is a manual admin op, not automatic. Note however that nothing automatically restores from snapshots, and per CTR-1 the snapshots themselves can capture progressively degraded rows.

**Recommendation:** Add the same preserve-on-empty guard to `UpdateBook` for Description/VersionNotes/BookSigV1* (mirroring `UpsertBookFile`), or make `UpdateBook` merge from `oldBook` (already fetched at :2666). Audit other GetAllBooks→UpdateBook flows. Check prod for books with populated BookSigBuiltAt but nil BookSigV1 to measure existing damage — and sequence any recovery from `book_ver:` snapshots before any snapshot pruning.

**Citations:**
- internal/database/pebble_store.go:2664
- internal/database/pebble_store.go:2683
- internal/database/pebble_store.go:1391
- internal/database/memdb_strip.go:29
- internal/reconcile/reconcile.go:1115
- internal/reconcile/reconcile.go:1149

### CTR-1 — Counter: full-column-replacement UpdateBook is the wrong write primitive for a 149-call-site codebase (High, verified)

**Detail:** The design puts merge responsibility on every one of 149 callers: each must hold a complete, canonical Book or silently destroy fields. That contract is unenforceable — the type system cannot distinguish a Pebble-canonical Book from a memdb-stripped one, and QUAL-2 shows a caller already violating it in prod-path code. It also composes badly with the CoW versioning at :2693: versions faithfully snapshot progressively-degraded rows, so the "undo" history itself can be full of stripped states. Every new bulk-maintenance job (quarantine, reconcile, VG assignment) re-inherits the footgun. The BookFile side already conceded the point by adding preserve-on-empty guards — the same argument applies a fortiori to Book, whose stripped fields (Description, BookSigV1) are more expensive to regenerate. Verification note: the quarantine citation turns out to be safe (direct Pebble scan), but the architectural footgun plus the demonstrated AssignOrphanVGs instance stands.

**Recommendation:** Either (a) make `UpdateBook` merge-on-empty from `oldBook` for strip-list fields, or (b) introduce `UpdateBookFields(id, patch)` for the common set-two-pointers callers (most of the 149 sites set 1-3 fields) and deprecate whole-row Update outside the importer.

**Citations:**
- internal/database/pebble_store.go:2664
- internal/database/pebble_store.go:2683
- internal/reconcile/reconcile.go:1149
- internal/quarantine/service.go:275

### CTR-2 — Counter: memdb field-stripping reuses the same *Book/*BookFile type for two incompatible representations (High, verified)

**Detail:** The 10GB→2GB RSS win is real, but the mechanism is a projection that is type-indistinguishable from the canonical row, and the store silently switches representation based on a runtime flag (pebble_store.go:1392: the same `GetAllBooks` returns stripped or full depending on `UseMemDB`). Consequences observed: per-write-path guard duplication with an explicit "keep both in sync" comment (pebble_store.go:9973) — a maintenance treadmill; predicate filters that "silently miss against stripped books" (memdb_strip.go:26-28); and QUAL-2, where the guard was simply never written for the Book entity. Every future heavy field added to Book must be added to the strip list AND every write-path guard, with no compiler help. The nil-means-both-"absent"-and-"projected" ambiguity is the root cause of the entire footgun class the team keeps patching instance by instance — the class already caused real data loss once (PR #1552 fingerprint wipe).

**Recommendation:** Introduce a distinct `BookListRow` projection type for memdb (the compiler then prevents passing it to `UpdateBook`), or centralize all Book/BookFile writes through one merge function that always rehydrates strip-list fields from Pebble before marshal.

**Citations:**
- internal/database/memdb_strip.go:29
- internal/database/memdb_strip.go:87
- internal/database/pebble_store.go:1392
- internal/database/pebble_store.go:9969

### BUG-2 — Registry.Shutdown can return with store-touching goroutines still live (PEBBLE-CLOSED variant persists despite fix) (Medium)

**Detail:** The comment at registry.go:838-841 claims Shutdown "guarantees callers can safely close the underlying store immediately after Shutdown returns". Two escape hatches break that guarantee: (1) if the shutdown ctx expires during the drain poll, remaining ops are marked interrupted and Shutdown proceeds while their run goroutines still execute plugin code that writes to the store (registry.go:820-836); (2) `goroutineWG.Wait` is bounded by a hard 2s timeout — "goroutines did not exit within 2s; proceeding" (registry.go:862-865). The in-process op run goroutine itself (worker.go:236-238, `done <- r.safeRun(...)`) is never enrolled in `goroutineWG`; during shutdown its run handle stays registered (worker.go:276-282) so only the ctx-bounded drain poll waits for it. A wedged plugin therefore survives both waits, and the caller's `store.Close()` races it → "pebble: closed" panic. TODO.md:64 confirms the residual race still reproduces under package-wide `-race`.

**Recommendation:** Either make the safe-close guarantee real (block until goroutineWG and all run handles drain, no timeout) or make the failure explicit: have Shutdown return a sentinel "not drained" error and have callers skip/delay `store.Close()` on that path. Enroll the safeRun goroutine in goroutineWG.

**Citations:**
- internal/operations/registry/registry.go:838-841
- internal/operations/registry/registry.go:855-866
- internal/operations/registry/registry.go:820-836
- internal/operations/registry/worker.go:236-238
- internal/operations/registry/worker.go:276-282
- TODO.md:64

### BUG-3 / QUAL-3 — MATCH-6 TOCTOU confirmed: ApplyTranscriptionCandidate applies whatever is cache slot 0 at apply time, not the gated candidate (Medium)

**Detail:** `SearchTranscriptionCandidate` returns `entry.Candidates[0]` from the metadata cache; auto_match_transcribed.go gates on that candidate's score (>= minScore), normalized-title equality, and author containment (lines 143-165). `ApplyTranscriptionCandidate` then discards the passed candTitle/candAuthor (parameters are `_, _` at server_maintenance_deps.go:379) and re-reads `GetCachedCandidates`, applying whatever is `Candidates[0]` at that moment. Any concurrent cache refresh for the same book (a user-triggered metadata search, another maintenance job, a metadata re-fetch op — the cache is shared) between the two calls can reorder or replace candidates, so an ungated candidate gets applied — and may set `MetadataReviewStatus=audio_confirmed` on the wrong metadata. The "auto-match-transcribed: applied" log (auto_match_transcribed.go:181-183) then reports the gated title even though a different one was written — actively misleading during incident triage.

**Recommendation:** Pass identity through: have Apply accept the candidate (or an entry-version/hash token) and verify the cache entry still matches before applying — e.g. compare `Unmarshal(entry.Candidates[0]).Title/Author` against the passed candTitle/candAuthor and abort with a retryable error on mismatch, forcing re-triage. Cheap and fully backward compatible since callers already pass the values.

**Citations:**
- internal/server/server_maintenance_deps.go:354-372
- internal/server/server_maintenance_deps.go:379-397
- internal/server/server_maintenance_deps.go:390
- internal/plugins/maintenance/auto_match_transcribed.go:130-183

### BUG-4 / QUAL-1 — Repo-wide corrupted slog conversions: dropped values, literal "value0" pairs, bare values as keys, duplicate attr keys (Medium, downgraded from high)

**Detail:** The printf→slog migration produced at least four distinct corruption shapes. (a) Data lost entirely: plugin/registry.go:34 logs "plugin %q already registered" with ZERO args — the duplicate plugin ID is unrecoverable from logs. (b) Literal-string values: 25 sites match the `"value0", "value0", "value1", x` pattern (embedding_client.go:215/235/260, quarantine/service.go:269,276, reconcile.go:687,695) — the literal string "value0" is logged as both key and value, the original first datum was deleted by the conversion, and real values get wrong keys (e.g. embedding_client.go:260: `slog.Warn("embedding cache put failed (hash)", "value0", "value0", "value1", hash[:8], ...)`). (c) Structure corrupted / !BADKEY: itunes/service/validate.go:81 passes two positional args after a %q-bearing message (req.From becomes the key, req.To its value); scan_composer_tags.go:196 appends bare `willWrite, w.filePath` after keyed pairs, making the written tag value the attr KEY; file_io_pool.go:338 similar. (d) Duplicate-key cluster ("value","value" / "count","count") across metafetch — service_apply.go:66 has "value" twice, :457 has "id" three times and "value" twice, validate.go:118 has "response" twice, service_scoring.go:498/607/612/642/677, service_search.go:254/265/335/541, service_writeback.go:123, service_fetch.go:141 — breaking structured-log queries and JSON log parsing, and making score/threshold drop reasons unreadable — directly hampering diagnosis of BUG-1.

**Severity note (verification):** real, widespread, on live code paths — but impact is confined to log quality/diagnostics, not behavior or data, so medium not high.

**Recommendation:** One mechanical sweep PR (good /parallel-sweep candidate): grep with `grep -rnE '"value[0-9]*",|%[sdqvf]' internal --include='*.go'` for slog messages still containing %-verbs, non-string bare args after the message, and repeated keys; rewrite each with distinct meaningful keys. Add `go vet` (the slog checker is in vet since Go 1.21) or a sloglint step to `make ci` to prevent recurrence.

**Citations:**
- internal/plugin/registry.go:34
- internal/ai/embedding_client.go:215
- internal/ai/embedding_client.go:235
- internal/ai/embedding_client.go:260
- internal/itunes/service/validate.go:81
- internal/itunes/service/validate.go:118
- internal/maintenance/jobs/scan_composer_tags.go:196
- internal/server/file_io_pool.go:338
- internal/metafetch/service_scoring.go:607
- internal/metafetch/service_search.go:335
- internal/metafetch/service_apply.go:66
- internal/metafetch/service_apply.go:457
- internal/quarantine/service.go:269
- internal/reconcile/reconcile.go:687

### BUG-5 / QUAL-6 — No permanent-error classification in either retry implementation; 4xx/insufficient_quota retried with full backoff (Medium)

**Detail:** `ai.DoWithRetry` (retry.go:26-47) retries any non-nil error identically up to maxAttempts with quadratic backoff — no check for permanent failures (4xx/insufficient_quota/invalid-request). `embedBatchRaw` (embedding_client.go:287-312) is a second, independent inline retry loop (hardcoded 3 attempts, 1s/4s delays) that also retries unconditionally: any error from `Embeddings.New` → `lastErr = ...; continue`. HTTP 401 (bad key), 400 (input too long / oversized batch), and the currently-live OpenAI 429 `insufficient_quota` are all permanent, yet each embed call burns ~5s of sleeps plus 3 doomed API calls before failing. On batch scans (`dedup.embed-scan` iterating tens of thousands of items) a misconfigured/quota-dead backend turns into hours of futile retries instead of failing fast. Two divergent retry mechanisms in one package also mean fixes must land twice.

**Recommendation:** Add an `IsPermanent(err)` classifier consulted by both loops: unwrap the openai SDK's `*openai.Error` and stop immediately on status 400/401/403/404 and on 429 with code `insufficient_quota` (retry only rate-limit 429s and 5xx/network errors). Better, make `embedBatchRaw` use `DoWithRetry` so there is one implementation.

**Citations:**
- internal/ai/retry.go:26-47
- internal/ai/retry.go:40
- internal/ai/embedding_client.go:287-312
- internal/ai/embedding_client.go:309

### CTR-3 — Counter: unclamped multiplicative score stacking makes thresholds tier-incomparable and lets boost products outvote base similarity (Medium)

**Detail:** Scores intentionally exceed 100% (documented project decision), but the cost is now visible: boosts stack multiplicatively (author 1.5 × narrator 1.3 × series 1.4 × narrator-present 1.15 × series-number 2.0 ≈ 6.3x, service_search.go:345-519), so a base score of 0.15 with lucky metadata overlap beats a 0.9 exact match with sparse candidate metadata. Fixed thresholds are applied at inconsistent points on this unbounded scale: `EmbeddingMinScore` at 0.82 pre-boost (:334), ASIN floor of 1.0 (:446), DurationMismatch cutoffs — each calibrated to a different effective range, and F1-tier vs embedding-tier scores pass through identical multipliers despite different base distributions. Every new boost silently re-scales what "high confidence" means for auto-apply consumers of `Candidate.Score`, and there is no place to recalibrate because the scale has no ceiling.

**Recommendation:** Keep the raw composite internally but expose a normalized/calibrated confidence (e.g. logistic over log-score, per-tier) for thresholds and auto-apply gates; alternatively convert boosts to additive log-space weights so individual signals cannot dominate combinatorially.

**Citations:**
- internal/metafetch/service_search.go:334
- internal/metafetch/service_search.go:345
- internal/metafetch/service_search.go:375
- internal/metafetch/service_search.go:519
- internal/metafetch/service_search.go:446

### CTR-4 — Counter: init()-registered global plugin registry trades explicit wiring for import-order coupling and untestable global state (Medium)

**Detail:** `Register` is invoked from package `init()` into a package-level singleton (registry.go:30-39, `Global()` at :43). Argued against: (1) duplicate registration is silently skipped with a Warn that — due to BUG-4/QUAL-1 — currently doesn't even log which plugin (:34), so a refactor that double-imports a plugin ships with no diagnosable signal; (2) the registry's contents depend on which packages the binary happens to import, so an API-only build vs full build can differ in registered ops with no compile-time manifest; (3) it is the same global-state pattern already causing the documented GlobalStore/GlobalQueue test flakiness — tests cannot construct isolated registries, so plugin tests share mutable state across the package's test binary. The convenience benefit (no wiring boilerplate) is small relative to a repo that already has an explicit Deps-injection pattern (internal/itunes/service).

**Recommendation:** Keep init()-registration short-term but make `Register` return an error on duplicate and have a startup assertion log the full registered-plugin manifest; long-term, construct a Registry in main/server wiring (matching the itunes Deps pattern) so tests and alternate builds get explicit, isolated registries.

**Citations:**
- internal/plugin/registry.go:30
- internal/plugin/registry.go:34
- internal/plugin/registry.go:43

### BUG-6 / QUAL-5 — Cover filter hard-drops cover-less candidates regardless of score, before sort and LLM rerank, including direct ASIN matches (Low)

**Detail:** After all score computation (min-score gate, author/narrator multipliers, ASIN candidates), service_search.go:487-497 discards every candidate with empty CoverURL as long as at least one candidate has a cover, then sorts, caps at 50, and reranks (lines 526-539). Cover presence is a hard gate, not a ranking signal — a cover-less exact title+author+series match with score 2.0 is removed while a wrong-book candidate with a cover at 0.83 survives and gets applied. This includes the direct ASIN-lookup candidate appended at :456, which is supposed to be authoritative (score forced to >=1.0 at :446). Sources like OpenLibrary and Audnexus frequently return correct entries without cover URLs, and `ApplyNonBaseAdjustments` already awards a +0.05 cover bonus, so the hard filter double-counts and can invert correctness; the correct-match decision then feeds `ApplyMetadataCandidate` and auto-apply pipelines. Verdict from the bug-hunt agent: PLAUSIBLE — code behavior is verified; whether it misfires in practice depends on source cover coverage.

**Recommendation:** Replace the hard drop with a score demotion (e.g. ×0.8–0.9 for missing cover), or only drop cover-less candidates whose score is below the top cover-less candidate by a margin; at minimum exempt candidates above a high-confidence score threshold and candidates from the ASIN-direct path.

**Citations:**
- internal/metafetch/service_search.go:487-497
- internal/metafetch/service_search.go:526-539
- internal/metafetch/service_search.go:456

### QUAL-7 — Fetch-race guard is instance-level (useLibraryQuery), not a shared abstraction — the pagination-race class is not structurally fixed (Low)

**Detail:** The PR #1706 page-size race fix landed as a hand-rolled `latestRequestIdRef` guard inside `useLibraryQuery` (:76, checked at :145/:185/:199). Other data-loading pages (Authors.tsx:317-332, Series, Works — grep shows zero requestId/AbortController/ignore-flag usage in pages/) use bare async-effect + setState with no staleness guard. Today those are mostly parameterless single fetches, so exposure is low, but the moment any of them gains server-side search/pagination params the same race recurs. There is no shared useAsyncData/query hook and no React Query-style library, so each new fetcher re-decides whether to guard.

**Recommendation:** Extract the requestId/abort pattern from `useLibraryQuery` into a shared hook (or adopt TanStack Query for list endpoints) so the class is fixed once; lint or review-checklist new useEffect+fetch combinations.

**Citations:**
- web/src/hooks/useLibraryQuery.ts:76
- web/src/hooks/useLibraryQuery.ts:145
- web/src/pages/Authors.tsx:317
- web/src/pages/Authors.tsx:330

## Steelman and Design

The specialist reports included empty `steelman` and `design` fields for this phase — no additional content to reproduce. The counter-argument findings (CTR-1 through CTR-4) serve as the design-critique component of this dimension, and each includes an acknowledgment of the design's real benefit (e.g. the 10GB→2GB RSS win from memdb stripping in CTR-2, wiring convenience in CTR-4) before the counter-argument.
