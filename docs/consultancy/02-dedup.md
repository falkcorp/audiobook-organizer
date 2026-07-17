<!-- file: docs/consultancy/02-dedup.md -->
<!-- version: 1.1.0 -->
<!-- guid: eb002776-8824-40c5-915b-67f4f146e852 -->
<!-- last-edited: 2026-07-17 -->

# Consultancy Evaluation — Dedup Engine & Strategy (2026-07-02)

This report is one dimension of a software-consultancy evaluation of the audiobook-organizer repository, produced by a read-only multi-agent workflow (an expert strategy agent — findings `DEDUP-*` — and a Go specialist agent — findings `DEDUPC-*`), cross-checked by an advisor pass. All findings are cited as `file:line` against the repository state on 2026-07-02.

## Executive Summary

The layered dedup engine is well-guarded at its exact-emission chokepoint: the primary-version gate, identifier-conflict gate, boilerplate-title, short-duration, and part-vs-whole gates all funnel through `upsertExactCandidate` (`internal/dedup/engine.go:1327-1368`), and the dataset catchers are conservative and honest. The dominant problem is not engine logic but **backlog + calibration debt**.

**Strategic verdict on the fingerprint question:** fingerprint coverage (~8,387 unfingerprinted books) is a **real but second-order lever**. It powers only the *positive* oracle — `wholeBookSignatureMatch` at similarity ≥0.95 → `true_dup` (`internal/dedup/dataset/rules.go:53-58`) — which is what gates auto-resolution. It cannot resurrect the pulled AcoustID veto (#1737): masked-similarity ~0.50 is the physical noise floor of uncorrelated chromaprints, so the negative direction stays useless at any coverage (`internal/dedup/engine.go:1351-1359`).

The first-order levers, in priority order:

1. **Drain the ~383,902 stale pre-fix exact candidates** (`TODO.md:188`) via re-detection/purge, now that the CONS-16/17 fixes and the duration-backfill have landed. Needs zero fingerprints — only the gated dry-run + user greenlight.
2. **Recalibrate and re-scan the embedding layer for bge-m3** — thresholds (0.95/0.85) and confidence maps were calibrated on text-embedding-3-large, and nothing regenerates embedding candidates after the in-flight re-embed.
3. **Duration coverage** (ffprobe-class scan) is ~100× cheaper than fpcalc and unlocks the `not_dup` catchers on the Duration=0-with-files residual that is unlabeled by design.
4. **Then** fingerprint the 8,387 books to enable positive-oracle auto-resolve.

Do (1)–(3) before spending GPU-days on (4).

On the code side, the Go specialist confirmed the `upsertExactCandidate` chokepoint and that the #1738 model-aware re-embed fix is correct for the book sync path (`prepBookEmbed` + `embed_model_cutover_test.go`). Main defects found:

1. The model-aware skip was **not** applied to `EmbedAuthor` or `EmbedBooksAsync`, so author embeddings never migrate off text-embedding-3-large — author dedup Layer 2 silently degrades to mixed 3072/1024-dim comparisons after the cutover (DEDUPC-1, the highest-severity code finding).
2. `FullScan` omits `checkExactMetadataSourceHash`, so rescans never surface metadata-source-hash dups (DEDUPC-2).
3. `AcoustIDScan` emits via `upsertCandidateWithLiveLabel` and, while it does replicate the boilerplate and identifier-conflict gates, it misses the primary-version and short-duration/part-vs-whole gates (DEDUPC-3, scope corrected by the advisor pass — see below).
4. The identifier-conflict gate vetoes even byte-identical file-hash pairs (DEDUPC-5).
5. Unified rescoring rewrites `Layer` to the strongest signal, silently removing pairs from `RunLLMReview`'s `layer=="embedding"` filter (DEDUPC-4).
6. A 2–10% duration-delta dead zone produces neither candidate nor tag (DEDUPC-6).

A full **auto-resolution design** is reproduced as its own section below. It builds on the existing `ComposeScore` bands (CERTAIN ≥97 / HIGH ≥90 / MEDIUM ≥75), `dataset.Classify` catchers, and the `ApplyVerdicts` auto-merge + tag + `CleanupCandidatesAfterMerge` machinery; Tier-2 is explicitly gated on fingerprinting the ~8,387 unfingerprinted books.

**Cross-link to the storage workstream (STOR-1):** a `BookSigV1` wipe kills the positive oracle (`wholeBookSignatureMatch`), `BookSignatureScan`, and `ReevaluateAcoustIDConflicts` simultaneously — silently, since all three skip (never error) on missing data. DEDUP-6 and DEDUPC-7 report this same cross-link from two angles and are consolidated below.

### Advisor verification

The advisor pass verified the findings as largely accurate and well-cited:

- **DEDUPC-1 confirmed**: `engine.go:2244` and `:2308` check only `TextHash`, with no `embeddingModelMatches` guard like `:2094`. Note that `EmbedBooksAsync` is the OpenAI Batch API path and is likely inert under Ollama, so **`EmbedAuthor` is the live exposure**.
- Hardcoded thresholds at `engine.go:194-203` confirmed; `rules.go:53-58` positive oracle confirmed.
- `CosineSimilarity` returning 0 on dimension mismatch (`embedding_store.go:1214`) confirms DEDUP-3's silent-degradation mechanism.
- **DEDUPC-3 overreach corrected**: the original claim that the AcoustID emit path "bypasses the exact-chokepoint gates" overstates. The emit closure actually **replicates** the boilerplate gate (`engine.go:3353`) and the identifier-conflict gate (`:3360`), plus same-directory suppression. Only the **primary-version** and **short-duration/part-vs-whole** gates are missing. The finding text below reflects this correction.
- **Redundancy noted**: DEDUP-6 and DEDUPC-7 are the same STOR-1 cross-link; they are presented as a consolidated section below.

## Findings Table

| ID | Severity | Impact | Effort | Title |
|----|----------|--------|--------|-------|
| DEDUP-1 | high | high | medium | Fingerprint coverage only feeds the positive oracle; the ~384K stale-candidate drain is the higher-yield, zero-fingerprint lever |
| DEDUP-2 | high | high | medium | Embedding thresholds and confidence maps are calibrated for text-embedding-3-large; no recalibration or candidate regeneration exists for the bge-m3 cutover |
| DEDUPC-1 | high | high | low | Model-aware re-embed skip missing in EmbedAuthor and EmbedBooksAsync — author embeddings stranded on OpenAI model after bge-m3 cutover |
| DEDUP-3 | medium | medium | low | Layer 2 silently degraded during the re-embed window; the documented cutover sequence (Layer 2 OFF) was not followed by the in-flight run |
| DEDUP-4 | medium | high | low | Duration coverage is the cheap complementary lever: the Duration=0-with-files residual is unlabeled by design and blocks the not_dup catchers |
| DEDUP-5 | medium | medium | low | ReevaluateAcoustIDConflicts and dataset-backfill load up to 1M candidates and cache full unstripped Book signatures in memory |
| DEDUP-6 / DEDUPC-7 | medium | high | low | (Consolidated) BookSigV1 dependency concentration: a STOR-1-style signature wipe silently disables the positive oracle, BookSignatureScan, and the conflict purge |
| DEDUPC-2 | medium | medium | low | FullScan omits checkExactMetadataSourceHash — rescans never surface metadata-source-hash duplicates |
| DEDUPC-3 | medium | medium | medium | AcoustIDScan emit path misses the primary-version and short-duration/part-vs-whole gates (scope corrected by advisor) |
| DEDUPC-4 | medium | medium | medium | Unified rescoring rewrites Layer to strongest signal, silently removing pairs from RunLLMReview's layer="embedding" filter |
| DEDUPC-5 | medium | medium | low | Identifier-conflict gate vetoes byte-identical file-hash matches; auto-merge path inconsistently bypasses the same gate |
| DEDUPC-6 | low | low | low | Duration-match dead zone: 2–10% delta pairs with similar titles get neither candidate nor tag |

## DEDUP-1 — Fingerprint coverage only feeds the positive oracle; the ~384K stale-candidate drain is the higher-yield, zero-fingerprint lever

**Severity:** high · **Impact:** high · **Effort:** medium

### Detail

The pulled veto (#1737) is coverage-**independent** dead: `BookSignatureSimilarityMasked` ~0.50 is the noise floor for uncorrelated audio, so no threshold separates dups from non-dups even at 100% coverage — `engine.go:1351-1359` documents this in-code. What fingerprints DO unlock is `wholeBookSignatureMatch` (similarity ≥0.95 → `true_dup`, `rules.go:53-58`), the only strong positive oracle for auto-resolution.

Meanwhile `TODO.md:188` records ~383,902 stale exact candidates computed before the CONS-16/17 duration-ms/title-leak fixes, with the duration-backfill already applied (17,684 files / 1,210 books ms→s). The drain is blocked only on the dry-run + user-OK gate (`TODO.md:170`), not on fingerprints. That drain shrinks the backlog by an order of magnitude more than any fingerprinting campaign.

### Recommendation

Sequence: (1) run maintenance re-scan + `dedup.quarantine-chapter-artifacts` dry-run post-backfill, present the list, get the greenlight, drain; (2) only then size the fingerprint campaign — target the ~8,387 books, framed explicitly as enabling positive-oracle auto-confirm (`true_dup`), not the veto.

### Citations

- `internal/dedup/dataset/rules.go:53-58`
- `internal/dedup/engine.go:1351-1359`
- `TODO.md:170`
- `TODO.md:188`
- `docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:63-66`

## DEDUP-2 — Embedding thresholds and confidence maps are calibrated for text-embedding-3-large; no recalibration or candidate regeneration exists for the bge-m3 cutover

**Severity:** high · **Impact:** high · **Effort:** medium

### Detail

`BookHighThreshold=0.95` / `BookLowThreshold=0.85` (`engine.go:194-197`) and the `SigEmbedHigh`/`SigEmbedMedium` confidence interpolations (`collectors_embedding.go:84-118`) encode the cosine distribution of OpenAI text-embedding-3-large. bge-m3 has a different similarity distribution; if its dup-pair cosines sit lower, `SigEmbedHigh` (≥0.95) may almost never fire and Layer 2 recall silently collapses post-cutover — with no error, just missing candidates.

Additionally, candidate generation (`findSimilarBooks`) runs only in `CheckBook`/`FullScan`; the in-flight embed-scan only writes vectors, so existing embedding candidates still carry OpenAI-era similarities and no bge-m3 candidates exist until a `FullScan` runs. `DedupCandidate` rows carry no model tag, so old- and new-model similarities are indistinguishable in the store.

### Recommendation

After the re-embed completes: sample bge-m3 cosines for known `true_dup` pairs (the rule-labeled dataset exists for exactly this), adjust High/Low thresholds and the confidence ramps, then run a `FullScan` to regenerate embedding candidates. Consider tagging candidates with the producing embedding model.

### Citations

- `internal/dedup/engine.go:194-203`
- `internal/dedup/collectors_embedding.go:70-75`
- `internal/dedup/collectors_embedding.go:84-118`
- `internal/plugins/dedup/reembed_embeddings.go:30-38`

## DEDUP-3 — Layer 2 silently degraded during the re-embed window; the documented cutover sequence (Layer 2 OFF) was not followed by the in-flight run

**Severity:** medium · **Impact:** medium · **Effort:** low

### Detail

`CosineSimilarity` returns 0 on dimension mismatch (`embedding_store.go:1213-1216`), so during the ~29K-book re-embed a fresh 1024-dim query scores 0 against every not-yet-re-embedded 3072-dim row — dedup-on-import `CheckBook` calls during this window find only already-cutover books and miss the rest permanently (nothing re-runs `findSimilarBooks` for them afterwards). `reembed_embeddings.go:30-38` documents the safe sequence (`dedup_embeddings_enabled:false` during re-embed, restart, re-enable), but the status doc shows the live run went via `dedup.embed-scan` with no mention of disabling Layer 2.

### Recommendation

Accept the transient gap but schedule a `FullScan` after re-embed completion (also required by DEDUP-2) so window-imported books get embedding candidates. For future cutovers, make embed-scan/reembed ops warn or auto-disable Layer 2 when a model mismatch is detected mid-corpus.

### Citations

- `internal/database/embedding_store.go:1213-1216`
- `internal/plugins/dedup/reembed_embeddings.go:29-38`
- `docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:55-58`

## DEDUP-4 — Duration coverage is the cheap complementary lever: the Duration=0-with-files residual is unlabeled by design and blocks the not_dup catchers

**Severity:** medium · **Impact:** high · **Effort:** low

### Detail

`partVsWhole` deliberately does not fire when either side has `TotalDurationSec==0` (`rules.go:101-111`), and `dataset_backfill.go:18-23` documents that the dominant residual class (stub/unscanned pairs with one side duration=0) escapes all catchers. `hasPlausibleAudio` (`engine.go:1650-1661`) also passes zero-duration books through on `FileSize` alone.

Every one of these paths is unblocked by knowing durations — an ffprobe-class metadata scan, which is roughly two orders of magnitude cheaper per file than fpcalc fingerprinting (no full decode needed for duration in most containers). The duration-backfill already applied fixed ms→s corruption on existing values; books with NO duration still need a probe pass.

### Recommendation

Before mass fingerprinting, run a duration-probe backfill over books with files but Duration≤0, then re-run `dedup.dataset-backfill` (dry-run first). This converts a chunk of the unjudgeable residual into rule-labeled `not_dup` at near-zero GPU/CPU cost.

### Citations

- `internal/dedup/dataset/rules.go:101-111`
- `internal/plugins/dedup/dataset_backfill.go:18-23`
- `internal/dedup/engine.go:1650-1661`

## DEDUP-5 — ReevaluateAcoustIDConflicts and dataset-backfill load up to 1M candidates and cache full unstripped Book signatures in memory

**Severity:** medium · **Impact:** medium · **Effort:** low

### Detail

Both ops call `ListCandidates` with Limit 1,000,000 (`engine.go:1559-1563`; `dataset_backfill.go:98-105`). With ~384K pending candidates in prod, that materializes the whole set, and `ReevaluateAcoustIDConflicts` additionally builds an unbounded per-book cache map (`engine.go:1573`) whose entries hold `BookSigV1` strings (~22KB each per `memdb_strip.go`'s own math) fetched via pebble-direct `GetBookByID`. Worst case (most candidate books fingerprinted, ~50K distinct books) that cache alone is on the order of a gigabyte, in the same process that previously hit a 69GB warmup incident. Not a correctness bug, but a prod-memory foot-gun on the exact host where these ops will be run to execute the DEDUP-1 drain.

### Recommendation

Page `ListCandidates` in both ops (cursor/batch like `checkExactISBNScan`'s 500-row pattern) and store only (sig, mask) truncated to what `BookSignatureSimilarityMasked` needs, or bound the cache with an LRU. Cheap change; do it before running the drain at full backlog size.

### Citations

- `internal/dedup/engine.go:1559-1563`
- `internal/dedup/engine.go:1569-1592`
- `internal/plugins/dedup/dataset_backfill.go:98-105`

## DEDUP-6 / DEDUPC-7 — (Consolidated) BookSigV1 dependency concentration: a STOR-1-style signature wipe silently disables the positive oracle, BookSignatureScan, and the conflict purge

**Severity:** medium · **Impact:** high · **Effort:** low

Both agents independently flagged this cross-link to the storage workstream; the advisor confirmed they are the same finding and directed consolidation.

### Detail

`stripBookForMemdb` clears `BookSigV1`, `BookSigV1Mask`, `BookSigSegments`, `BookSigBuiltAt`, `BookSigCoveragePct` (`memdb_strip.go:36-40`). The dedup engine itself defends correctly — `bookSignature()` re-fetches by ID from pebble-direct `GetBookByID` when the passed struct lacks the sig (`engine.go:1500-1521`) — but any write path elsewhere that round-trips a memdb-sourced Book through a full-replace update (the documented `UpdateBook` full-column-replacement footgun, same class as the `AcoustIDFingerprint` wipe fixed in #1552) erases the signature permanently.

Three independent mechanisms all read `Book.BookSigV1`/`BookSigV1Mask`:

1. `dataset.wholeBookSignatureMatch` — the ONLY `true_dup` rule catcher (`rules.go:54`);
2. `BookSignatureScan`'s O(N²) pairwise layer (`engine.go:3555`);
3. `ReevaluateAcoustIDConflicts` / `acoustIDSignaturesConflict` (`engine.go:1490`, `:1610`).

All are conservative on missing data (skip, never veto), so a STOR-1-style `BookSigV1` wipe fails **silently**: labeled `true_dup` examples stop being generated, the `book_signature` layer emits zero pairs, and the conflict purge reports everything as Skipped. Since `book_sig_v1` is the sole input to the `true_dup` positive oracle and Tier-1/Tier-2 auto-resolution (design below) leans on the signature oracle, a wipe would silently freeze the auto-merge pipeline with no error anywhere — and would waste the planned 8,387-book fingerprint campaign.

### Recommendation

The root fix belongs to the storage workstream (STOR-1), but the dedup team should add cheap invariants:

- A periodic count of books with `BookSigBuiltAt` set but `BookSigV1` empty (should be ~0), alerting on regression before/during the fingerprint campaign.
- A coverage counter (books with non-empty `BookSigV1` / total primaries) exposed via the dedup status endpoint, alerting/logging when coverage drops between runs.
- Make the auto-resolution op refuse to run Tier-2 when coverage regressed since its last run.

### Citations

- `internal/database/memdb_strip.go:29-47`
- `internal/dedup/engine.go:1500-1521`
- `internal/dedup/engine.go:3546-3558`
- `internal/dedup/engine.go:1604-1614`
- `internal/dedup/dataset/rules.go:53-58`

## DEDUPC-1 — Model-aware re-embed skip missing in EmbedAuthor and EmbedBooksAsync — author embeddings stranded on OpenAI model after bge-m3 cutover

**Severity:** high · **Impact:** high · **Effort:** low

### Detail

PR #1738 added `embeddingModelMatches` to `prepBookEmbed`'s cached-skip (`engine.go:2094`), forcing book re-embeds on a backend switch. But `EmbedAuthor` (`engine.go:2244`) and `EmbedBooksAsync` (`engine.go:2308`) still skip solely on `existing.TextHash == hash`. After the OpenAI→Ollama cutover, all ~8.8K author embeddings remain 3072-dim text-embedding-3-large vectors forever (text never changes for an unchanged author name), while any newly-created author embeds are 1024-dim bge-m3. `CheckAuthor`'s `FindSimilar` then compares mismatched-dimension vectors (linear-scan cosine yields garbage/zero; HNSW mixed-dim triggers the panics #1741 now recovers as errors), so author dedup Layer 2 silently returns nothing.

`EmbedBooksAsync` is the OpenAI Batch path — currently unused and likely inert under Ollama (advisor: `EmbedAuthor` is the live exposure) — but it would silently no-op the re-embed if anyone runs `dedup.embed-async`.

### Recommendation

Add `&& de.embeddingModelMatches(existing.Model)` to the cached-skip in `EmbedAuthor` and `EmbedBooksAsync`, mirroring `prepBookEmbed`. Extend `embed_model_cutover_test.go` with an author-path case. Then trigger an author re-embed pass after the book re-embed completes.

### Citations

- `internal/dedup/engine.go:2243-2249`
- `internal/dedup/engine.go:2306-2310`
- `internal/dedup/engine.go:2094`
- `internal/dedup/engine.go:2110-2115`

## DEDUPC-2 — FullScan omits checkExactMetadataSourceHash — rescans never surface metadata-source-hash duplicates

**Severity:** medium · **Impact:** medium · **Effort:** low

### Detail

`CheckBook` (the import-event path) runs five Layer-1 checks including `checkExactMetadataSourceHash` (`engine.go:413`). `FullScan`'s Layer-1 block (`engine.go:2443-2454`) runs only `checkExactFileHash`, `checkExactISBN`, `checkExactTitle`, and `checkDurationMatch`. `FullScan`'s own doc comment says it exists precisely because "Layer 1 used to only run on ingest... Running it inside FullScan populates the bucket" — but the 0.99-similarity `metadata_hash` layer (two books applied from the exact same external record, deliberately not gated by `hasPlausibleAudio`) is never populated by a rescan. Any pair whose shared `MetadataSourceHash` was set after import, or that predates the check, is permanently invisible.

### Recommendation

Add `de.checkExactMetadataSourceHash(&book)` to `FullScan`'s Layer-1 block alongside the other four checks.

### Citations

- `internal/dedup/engine.go:2443-2454`
- `internal/dedup/engine.go:409-415`
- `internal/dedup/engine.go:1024-1042`

## DEDUPC-3 — AcoustIDScan emit path misses the primary-version and short-duration/part-vs-whole gates (scope corrected by advisor)

**Severity:** medium · **Impact:** medium · **Effort:** medium

### Detail

> **Advisor correction applied:** the original finding claimed the AcoustID emit path "bypasses the exact-chokepoint gates." That overstates. The emit closure **does replicate** the boilerplate gate (`engine.go:3353`) and the identifier-conflict gate (`engine.go:3360`), plus same-directory suppression. What it misses are the **primary-version**, **short-duration**, and **part-vs-whole** gates.

`upsertExactCandidate` centralizes the gates (non-primary version, identifier conflict, boilerplate title, short duration, part-vs-whole) with the explicit comment "ALL exact emitters route through here". But `AcoustIDScan`'s emit closure (`engine.go:3377`) calls `upsertCandidateWithLiveLabel` directly. It checks boilerplate + identifier conflict but NOT `isNonPrimaryVersion`, `hasKnownShortDuration`, or `isPartVsWholeMismatch`. The query loop iterates primaries only (`getAllBooks`), yet the OTHER side comes from file-level indexes — `LookupAcoustIDCandidates` (line 3447) and `GetBookFileByAcoustID` (line 3489) — which include non-primary books' files. Also, the candidate-side file is never checked with `knownShortFingerprintFile` (only the query side at line 3440), unlike `collectors_acoustid.go` which guards both sides (lines 139, 317). Resulting primary-vs-nonprimary acoustid pairs sit in the queue until the next `PurgeStaleCandidates` run; `PairEligibility` does not catch cross-group non-primaries either.

### Recommendation

In `emit()`, resolve both books (`bookForIdentifierGate` already does) and apply `isNonPrimaryVersion` + `hasKnownShortDuration` + `isPartVsWholeMismatch` — or route acoustid emissions through a shared gate helper extracted from `upsertExactCandidate`. Add `knownShortFingerprintFile(cand)` in the LSH and exact-hit branches.

### Citations

- `internal/dedup/engine.go:3352-3387`
- `internal/dedup/engine.go:3446-3464`
- `internal/dedup/engine.go:3489-3495`
- `internal/dedup/engine.go:1327-1350`
- `internal/dedup/collectors_acoustid.go:139`

## DEDUPC-4 — Unified rescoring rewrites Layer to strongest signal, silently removing pairs from RunLLMReview's layer="embedding" filter

**Severity:** medium · **Impact:** medium · **Effort:** medium

### Detail

`runUnifiedScoringForBook` re-upserts each scored pair with `Layer=bestLayerFromSignals` (`engine.go:671-681`) — e.g. an embedding-seeded pair that also has an ISBN signal becomes `layer="exact"`. `listAmbiguousCandidates` (`engine.go:2903-2910`) filters `Layer=="embedding"` only. So the Layer-3 LLM review permanently loses exactly the multi-signal ambiguous pairs the unified pipeline was built to score; only pure-embedding pairs (which unified scoring leaves at `layer="embedding"`) remain reviewable.

Compounding: the default review model is `"gpt-5-mini"` via the OpenAI parser (`engine.go:2887-2890`), which is quota-dead until the LLM backend-mode toggle ships — Layer 3 is currently inert either way, but this filter bug will persist after the local-LLM cutover.

### Recommendation

Select LLM-review candidates by Band (MEDIUM/REVIEW from `ScoreBreakdown`) rather than the legacy Layer string, or include all non-llm layers within the similarity window. Wire the review model through the coming backend-mode toggle to qwen2.5:7b-instruct.

### Citations

- `internal/dedup/engine.go:669-685`
- `internal/dedup/engine.go:785-813`
- `internal/dedup/engine.go:2900-2913`

## DEDUPC-5 — Identifier-conflict gate vetoes byte-identical file-hash matches; auto-merge path inconsistently bypasses the same gate

**Severity:** medium · **Impact:** medium · **Effort:** low

### Detail

`checkExactFileHash` → `handleFileHashMatch`: with `AutoMergeEnabled` + same normalized title/author, `MergeBooks` fires with NO identifier-conflict check (`engine.go:886-893`). Without auto-merge, the pair goes to `upsertExactCandidate`, where `identifiersConflict` (`engine.go:1331`) silently drops it. A byte-identical file (same SHA) with a mis-tagged ASIN/ISBN on one side — common after bad metadata applies — is therefore either merged instantly or never surfaced at all, depending on a config flag. Physical hash identity is strictly stronger evidence than a metadata identifier mismatch; the mismatch is itself a signal the metadata is wrong.

### Recommendation

For `layer=exact` candidates originating from a file-hash match, bypass (or downgrade to a warning annotation) the `identifiersConflict` veto — e.g. pass an `origin` hint into `upsertExactCandidate`. Keep the veto for title/ISBN-seeded emitters where it protects against name collisions.

### Citations

- `internal/dedup/engine.go:874-897`
- `internal/dedup/engine.go:1327-1338`

## DEDUPC-6 — Duration-match dead zone: 2–10% delta pairs with similar titles get neither candidate nor tag

**Severity:** low · **Impact:** low · **Effort:** low

### Detail

`checkDurationMatch` emits a candidate at pct ≤ 2% and an abridged tag at pct ≥ 10%; the code's own comment says "normal transcoding noise ends around 2-5%" (`engine.go:1275`). A real duplicate re-encoded with different silence trimming or VBR at 3–5% delta, whose title Levenshtein is 3–6 (too far for `checkExactTitle`'s <3), falls in the (2%, 10%) gap and produces nothing — no candidate, no tag. These pairs can still be caught by embedding cosine, but only if both sides are embedded and above 0.85.

### Recommendation

Either widen `durationMatchTolerance` to ~0.05 with a lower similarity value (e.g. 0.9 instead of 1.0) so the unified score reflects the weaker evidence, or emit a `SigDuration` supporting signal for the 2–10% band instead of dropping it entirely.

### Citations

- `internal/dedup/engine.go:1141-1149`
- `internal/dedup/engine.go:1253-1283`

## Steelman: "Fingerprint coverage is the real lever"

Strongest case FOR the current "fingerprint coverage is the real lever" position: it is the only path to trustworthy AUTO-resolution. Every other signal in the engine is metadata-derived and was the source of the 380K-mislabel disaster; whole-book audio signatures are the one physical oracle that cannot be fooled by title leaks or ms/s corruption, and `rules.go` already wires sim≥0.95 as an auto `true_dup` label. With 65% of candidates having an unfingerprinted side, no amount of threshold tuning makes the oracle usable at scale — coverage is the binding constraint, exactly as the status doc concludes.

**Verdict:** the position is directionally correct but incomplete — coverage unlocks the **positive oracle only** (the veto stays dead at any coverage), and the stale-candidate drain plus bge-m3 recalibration plus duration probing are cheaper and should land first (see DEDUP-1 through DEDUP-4).

## Design: Auto-Resolution — Confidence-Tiered Pipeline on the Existing Unified Score

All machinery exists: `ComposeScore` bands (CERTAIN ≥97 / HIGH ≥90 / MEDIUM ≥75 / REVIEW ≥60, DB-tunable via `SetBandThresholds`), `dataset.Classify` catchers, `mergeService.MergeBooks` + `CleanupCandidatesAfterMerge`, and the `ApplyVerdicts` auto-merge pattern (merge → status="merged" → provenance tag). Add one op, `dedup.auto-resolve`, dry-run by default like Rescore/dataset-backfill.

### Tier 0 — auto-dismiss (live today)

Run `dedup.dataset-backfill apply=true`: `Classify`'s `not_dup` catchers (missingFile, implausibleAudio, partVsWhole<0.5) dismiss rule-negatives. Plus `PurgeStaleCandidates` each pass. Known residual: Duration=0-with-files pairs stay unlabeled by design.

### Tier 1 — auto-merge CERTAIN (live today, small volume)

Merge when ALL hold:

- Band == CERTAIN;
- ≥2 independent primary signal kinds from {SigExactFile, SigExactAcoustID, SigISBNASIN, SigMetaSrcHash} OR dataset `wholeBookSignatureMatch` (sig sim ≥0.95);
- empty Suppressors;
- `hasPlausibleAudio` both sides;
- no `identifiersConflict`.

Execute exactly like `ApplyVerdicts`' high-confidence path: `MergeBooks(pair, "" auto-primary)`, `UpdateCandidateStatus("merged")`, tag survivor `dedup:merge-survivor:auto-certain`, `CleanupCandidatesAfterMerge`.

### Tier 2 — auto-merge HIGH (90–97). Unblocked by fingerprint coverage

Requires positive audio corroboration: SigExactAcoustID/SigLSHAcoustID signal or `book_sig_v1` similarity ≥0.95 with ≥512-word overlap. Gate: fingerprint the ~8,387 unfingerprinted books first (65% of pairs are unjudgeable today; masked-sim 0.50 is the noise floor — corroboration must be positive evidence, never a veto). Refuse to run if `BookSigV1` coverage regressed (DEDUPC-7).

### Tier 3 — LLM triage (MEDIUM 75–90)

Local qwen2.5:7b-instruct once the backend-mode toggle ships; reuse `RunLLMReview`/`ApplyVerdicts` with `LLMAutoMergeHighConfidence`, but select by Band not `layer=="embedding"` (DEDUPC-4). Verdicts land in review, not auto-merge, for the first N runs.

### Safety rails

1. Dry-run default returning `AcoustIDConflictSample`-style capped samples per tier.
2. First apply run capped (`max_merges=200`) with a mandatory human sampling audit of ~30 merges before uncapping.
3. Reversibility: `MergeBooks` builds version groups — losers become non-primary members, not deletions; log every auto-merge to a journal key (`dedup:automerge:<ts>`), and provide an unmerge op that re-promotes a member.
4. Global kill switch config flag; band thresholds already runtime-tunable.

### Backlog drain (~12.5K pending)

1. `Rescore(apply=true)` to re-band everything under the current formula after CONS-16/17/18 forward fixes;
2. dataset-backfill apply → Tier-0 dismissals;
3. Tier-1 capped pass, audit, uncap;
4. fingerprint 8,387 books → `BookSignatureScan` → Tier-2 pass (largest expected drain);
5. residual MEDIUM → Tier-3 LLM / unified review tab.
