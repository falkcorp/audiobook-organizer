<!-- file: docs/specs/2026-07-10-metadata-matching-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 035d8eaa-938a-4bc2-bcbf-c5aba0468132 -->
<!-- last-edited: 2026-07-10 -->

# Metadata Matching Pipeline — Design Spec

**Status:** Approved — ready for implementation planning (INIT-3 gate: PLAN → EXECUTE AUTONOMOUSLY)
**Scope:** Go backend (`internal/metafetch/`, `internal/config/`, `internal/server/metadata_ops.go`, `internal/metadata/`, `internal/matcher/`, `internal/plugins/metafetch/` calibration op) + React Settings UI (`web/src/`). Follow-ups explicitly deferred to Non-goals.
**Parent task:** INIT-3 — Metadata matching pipeline (`.claude/notes/2026-07-10-remaining-work-master-plan.md`)

---

## Motivation

The metadata matching pipeline (search external providers → score candidates → cache → apply)
works, but has four grounded problems (all anchors grep-verified at HEAD `fce58498`, 2026-07-10):

1. **Nothing is tunable.** Every scoring weight is a code literal: transcription boosts
   ×2.0/×1.4/×1.6/×1.4 (`internal/metafetch/service_scoring.go:303,306,321,325` inside
   `transcriptionBoost:292`), compilation penalty ×0.15 (`service_scoring.go:119`), rich-metadata
   bonuses +0.05×4 capped at 0.15 (`service_scoring.go:133-148`), F1 floor 0.35
   (`service_scoring.go:355`), duration tiers (`service_scoring.go:172,215`), series-name boost
   ×1.4 (`service_search.go:368`) and series-number ×2.0/×0.5 (`service_search.go:512,514`).
   `MetadataScoringConfig` (`internal/config/config.go:219-227`) exposes only 7
   embedding/LLM/backup knobs. Retuning anything means a code deploy.
2. **Two conflicting duration scorers.** `durationScoreMultiplier` (`service_scoring.go:172`,
   absolute-delta buckets, multiplicative ×1.30..×0.50) and `computeDurationScore`
   (`service_scoring.go:215`, ratio buckets, additive +20..−20) assess the same question with
   different bucket systems and can disagree: a 100-minute delta on a 40-hour book is ×0.75
   ("likely different edition") from the first but ratio 0.04 → +20 ("essentially the same
   edition") from the second. Both are live: multiplier at `service_search.go:386,455` and
   `service_scoring.go:428`; additive at `service_search.go:413,473`.
3. **The bulk fetch is single-threaded.** `runBulkMetadataFetchForBookIDs`
   (`internal/server/metadata_ops.go:439`) iterates `for i, w := range work` (`:532`) with a
   nested sequential provider chain (`:562`) — a whole-library network-bound loop with no worker
   pool, the exact shape CLAUDE.md's concurrency mandate forbids (cf. the 2026-07-05 3-hour
   single-core dedup incident).
4. **The metadata cache has a TOCTOU window.** Cache entries are keyed only by book ID
   (`internal/metafetch/cache.go:41-52` → `GetMetadataCache(bookID)`); the apply-time guard in
   `internal/server/server_maintenance_deps.go:382-393` explicitly papers over it ("Because the
   metadata cache is shared and keyed only by book ID, it can be refreshed between the gate and
   this call"). The `SourceHash` field (`internal/database/iface_metadata.go:31-34`) already
   captures the search inputs but is "Diagnostic only in v1" — never checked.

Plus two smaller grounded gaps: author/series name→ID resolution was never implemented
(`internal/metadata/enhanced.go:254,256` TODOs), and the fuzzy matcher is purely
lexical/edit-distance (`internal/matcher/fuzzy.go:19-121` — Levenshtein + prefix/
substring heuristics, no token-set robustness against word reordering).

**Correction recorded during adversarial review:** the master-plan bullet "metadata-history never
implemented" (`enhanced.go:667` TODO) is true only of a DEAD legacy stub. A complete
metadata-change-history subsystem already exists and is wired end-to-end in production:
`MetadataChangeRecord` (`internal/database/store.go:872`), PebbleStore impl
(`internal/database/pebble_store_metadata.go:68,85,117` — `RecordMetadataChange` /
`GetMetadataChangeHistory` / `GetBookChangeHistory`), hand-written MockStore twins
(`mock_store.go:124-126,478-492`), writes emitted from every apply/writeback path
(`internal/metafetch/service_writeback.go:905`, `service_apply.go:188`, `service.go:291`), live
routes (`GET /audiobooks/:id/metadata-history[/:field]` + undo,
`internal/server/wire_audiobooks_routes.go:55-57`), and a SHIPPING frontend
(`web/src/components/MetadataHistory.tsx`, wired into `bookdetail/BookDetailDialogs.tsx:491`).
INIT-3 therefore builds NO new history storage — TASK-06 only retires the orphaned stub
(`enhanced.go`'s `MetadataHistory` type / `RecordMetadataChange` free func / `GetMetadataHistory`,
`:651-668`, referenced by nothing outside `enhanced.go` + its own test) and implements the
author/series resolution. See Decision 6.

**Correction recorded during planning:** the master-plan bullet "`embedding_client.go:375` batch
path incomplete" was re-verified this session: `internal/ai/embedding_client.go:375` is a
`TODO(#TASK-12-followup)` about routing `embedBatchRaw` through `DoWithRetry` (retry-loop
consolidation). The batch path itself is complete and functional. `internal/ai/` is outside
INIT-3's scope — no task is cut for it.

**Goal:** make match scoring tunable and internally consistent, parallelize the bulk fetch within
provider rate limits, and close the cache TOCTOU window — with **zero behavior change until an
operator tunes something**.

## Goals

- Every scoring weight readable from `MetadataScoringConfig`, defaulting to today's exact literals.
- One canonical duration-closeness assessment feeding both the multiplier and the breakdown score.
- Bounded-concurrency bulk metadata fetch that respects per-provider rate limits and preserves
  resume semantics.
- Author/series name→ID resolution; the dead legacy metadata-history stub in `enhanced.go`
  retired in favor of the EXISTING `MetadataChangeRecord` subsystem (REUSE — no new store surface).
- Apply paths verify the cache entry's `SourceHash` against the book's current identity.
- (Optional, P3) token-set fuzzy scoring layered on the existing Levenshtein matcher.

## Non-goals (v1)

- Retuning any default weight — extraction ships value-identical; tuning is an operator action later.
- Parallelizing the per-book provider chain itself — the chain stays priority-ordered with
  early-exit (semantics + circuit-breaker + rate-limit reasons; see Decision 4).
- The `internal/ai/embedding_client.go` retry-loop consolidation TODO — out of scope (wrong package).
- Embedding/LLM rerank changes (`RerankTopK`, `service_scoring.go:599`) — config already exists.
- Any change to the existing metadata-history subsystem (store methods, routes,
  `MetadataHistory.tsx` UI) — it already ships end-to-end (see Motivation correction); INIT-3 only
  removes the dead legacy stub that shadowed it.
- Making the per-provider concurrency cap or the duration-tier STRUCTURE (edges/count)
  operator-tunable — fixed in code (reviewed; see C4 and Data model).
- Auto-applying calibration recommendations — the harness reports; an operator tunes via Settings.

## Decisions (locked during design)

1. **Config extraction is value-identical.** Every extracted knob's default (the nil/zero-value
   resolver fallback per C2's per-knob semantics AND the two viper population sites,
   `config.go:1087` and `:1512`) equals today's literal. Losing alternative: "extract and improve while there" — rejected; the INIT-3 gate
   mandates zero behavior change until an operator tunes.
2. **Duration unification keeps both output shapes.** One canonical ratio-based tier table drives
   both a multiplicative factor and the additive `DurationScore` breakdown, so they cannot
   disagree; call-site signatures are preserved (both function names remain as thin derivations).
   Losing alternative: deleting `computeDurationScore` and changing `MetadataCandidate` — rejected,
   larger blast radius into API/UI breakdown fields.
3. **Fixtures before the swap.** TASK-01 lands a golden-fixture test capturing BOTH functions'
   current outputs across a (bookDur, candDur) grid in the SAME commit that unifies them, with the
   intended deltas enumerated in the fixture diff — per the initiative's special constraint.
4. **Parallelize the OUTER book loop only.** Workers = min(4, NumCPU) by default (network-bound —
   smaller than NumCPU per CLAUDE.md), each worker running the sequential provider chain for its
   book. Per-provider concurrency is additionally capped by a per-source semaphore (default 2) so
   N workers cannot stampede one provider; the existing `ProtectedSource` circuit breaker
   (`internal/metadata/circuitbreaker.go:138`, threshold 5 / 30s cooldown) and Hardcover's
   60-rpm limiter (`internal/metadata/hardcover.go:61`) stay in place beneath it. Losing
   alternative: fanning out the source chain per book — rejected (breaks priority early-exit,
   multiplies provider load ~6×).
5. **TOCTOU fix makes the existing `SourceHash` load-bearing.** Apply paths recompute
   `hashSearchInputs` (`internal/metafetch/cache.go:121`) from the book's CURRENT fields and
   reject on mismatch, layered on top of the existing identity re-check in
   `ApplyTranscriptionCandidate`. Losing alternative: re-keying the cache as
   `bookID+hash` — rejected; orphans every existing cache row and complicates enumeration.
6. **History is REUSE, not build.** The metadata-history half of T4 was descoped after review
   verified the `MetadataChangeRecord` subsystem already ships end-to-end (see Motivation
   correction). No new store methods, no mock twins, no mockery regen; `enhanced.go`'s dead stub
   is retired (deleted, or delegated to the existing `store.GetBookChangeHistory` if deletion
   turns up a hidden consumer). TASK-06's author/series ID changes record through the EXISTING
   `RecordMetadataChange`. Losing alternative: the originally-drafted `MetadataHistoryEntry` ULID
   keyspace + `SaveMetadataHistory`/`GetMetadataHistoryForBook` methods — rejected as a wholesale
   rebuild of a wired subsystem (violates the master plan's REUSE mandate and would orphan the
   existing routes and UI).
7. **Fuzzy upgrade is additive and optional (P3).** New token-set scorer alongside
   `LevenshteinDistance`; `ScoreMatch` adopts it only with fixture-locked before/after evidence.
   Callers affected: `internal/matcher/matcher.go`, `internal/scanner/scanner.go`,
   `internal/itunes/service/path_repair_resolver.go`.

## Data model

```go
// internal/config/config.go — MetadataScoringConfig grows tuning knobs.
// EVERY default equals today's hardcoded literal (Decision 1). Existing 7
// fields are unchanged; new fields appended. mapstructure keys mirror json.
type MetadataScoringConfig struct {
	// --- existing fields (unchanged) ---
	EmbeddingEnabled   bool    `json:"embedding_enabled"    mapstructure:"embedding_enabled"`
	EmbeddingMinScore  float64 `json:"embedding_min_score"  mapstructure:"embedding_min_score"`
	EmbeddingBestMatch float64 `json:"embedding_best_match" mapstructure:"embedding_best_match"`
	LLMEnabled         bool    `json:"llm_enabled"          mapstructure:"llm_enabled"`
	LLMRerankEpsilon   float64 `json:"llm_rerank_epsilon"   mapstructure:"llm_rerank_epsilon"`
	LLMRerankTopK      int     `json:"llm_rerank_top_k"     mapstructure:"llm_rerank_top_k"`
	WriteBackupBefore  bool    `json:"write_backup_before"  mapstructure:"write_backup_before"`

	// --- new: transcription boosts (defaults 2.0 / 1.4 / 1.6 / 1.4) ---
	TranscriptionTitleExactBoost  float64 `json:"transcription_title_exact_boost"  mapstructure:"transcription_title_exact_boost"`
	TranscriptionTitleSubstrBoost float64 `json:"transcription_title_substr_boost" mapstructure:"transcription_title_substr_boost"`
	TranscriptionAuthorBoost      float64 `json:"transcription_author_boost"       mapstructure:"transcription_author_boost"`
	TranscriptionNarratorBoost    float64 `json:"transcription_narrator_boost"     mapstructure:"transcription_narrator_boost"`

	// --- new: base-score adjustments (defaults 0.15 / 0.05 / 0.15 / 0.35).
	// POINTER knobs: 0 is a legitimate operator value for CompilationPenalty,
	// RichMetadataBonusCap, and F1MinScore (zero the penalty/cap; disable the
	// reject floor), so "unset" is nil, NOT 0 — the fail-open resolver
	// substitutes the legacy literal only on nil (reviewed; see C2). ---
	CompilationPenalty     *float64 `json:"compilation_penalty"       mapstructure:"compilation_penalty"`
	RichMetadataFieldBonus float64  `json:"rich_metadata_field_bonus" mapstructure:"rich_metadata_field_bonus"`
	RichMetadataBonusCap   *float64 `json:"rich_metadata_bonus_cap"   mapstructure:"rich_metadata_bonus_cap"`
	F1MinScore             *float64 `json:"f1_min_score"              mapstructure:"f1_min_score"`

	// --- new: series boosts (defaults 1.4 / 2.0 / 0.5) ---
	SeriesNameMatchBoost   float64 `json:"series_name_match_boost"   mapstructure:"series_name_match_boost"`
	SeriesNumberExactBoost float64 `json:"series_number_exact_boost" mapstructure:"series_number_exact_boost"`
	SeriesNumberWrongPenalty float64 `json:"series_number_wrong_penalty" mapstructure:"series_number_wrong_penalty"`

	// --- new: duration tier VALUES (see Component C1; defaults = TASK-01's
	// unified table). The tier STRUCTURE (delta-ratio edges + tier count) is
	// fixed in code — it is the OUTPUT of TASK-01's unification, not an
	// operator dial; exposing edges invited mismatched-length/non-monotonic
	// corruption of all duration scoring (reviewed). Each array's length
	// MUST equal the built-in table; any mismatch → the whole built-in
	// table is used (fail-open, logged).
	DurationTierMultipliers []float64 `json:"duration_tier_multipliers" mapstructure:"duration_tier_multipliers"`
	DurationTierScores      []float64 `json:"duration_tier_scores"      mapstructure:"duration_tier_scores"`

	// --- new: bulk-fetch concurrency (default 4; TASK-05). The per-provider
	// in-flight cap is a FIXED internal constant (2), deliberately not
	// config — no per-deployment need identified, and the ProtectedSource
	// breaker + Hardcover limiter sit beneath it (reviewed). ---
	BulkFetchWorkers int `json:"bulk_fetch_workers" mapstructure:"bulk_fetch_workers"`
}
```

### Persistence

- Metadata history: REUSE the existing `MetadataChangeRecord` store surface
  (`RecordMetadataChange` / `GetBookChangeHistory` / `GetMetadataChangeHistory`,
  `internal/database/pebble_store_metadata.go`). No new type, keyspace, store methods, or mocks
  (Decision 6). TASK-06's author/series resolution records one `MetadataChangeRecord` per ID
  change (old → new AuthorID/SeriesID) so a wrong link is auditable and reversible after a code
  revert (see C6 and Rollback).
- `MetadataCandidateCache.SourceHash` (existing, `iface_metadata.go:31-34`) — becomes load-bearing
  at apply time (Component C5). No schema change; rows lacking a hash fail-open with a warn log
  (legacy rows predate the field).

## Components

### C1. Unified duration scoring (`internal/metafetch/service_scoring.go`) — TASK-01

One canonical tier table (delta-ratio based, `|candDur−bookDur|/bookDur`) with two derivations
preserving today's exact signatures:

```go
func durationScoreMultiplier(bookDurationSec, candidateDurationSec int) float64 // ×-factor
func computeDurationScore(bookDurationSec, candidateDurationSec int) float64    // additive points
```

Zero/negative durations on either side remain "unknown" → multiplier 1.0 / score 0 (non-
disqualifying), exactly as today. Golden fixtures capture pre-unification outputs of BOTH
functions over a grid; the unification commit updates fixtures with every changed cell enumerated
and justified in the test file. Call sites (`service_search.go:386,413,455,473`,
`service_scoring.go:428`) are untouched.

### C2. Config extraction (`internal/config/config.go` + `internal/metafetch/`) — TASK-02

Every literal listed in Motivation §1 becomes a read from `config.AppConfig.MetadataScoring`,
with a package-level `scoringDefaults()` guard whose fail-open semantics are PER-KNOB (reviewed —
a blanket zero-as-unset rule made 0 unreachable for knobs where 0 is a legitimate value):

- **Multiplicative boosts** (four transcription boosts, three series boosts,
  `RichMetadataFieldBonus`): plain `float64`; zero/unset resolves to the legacy literal (a zero
  multiplier is nonsensical and only ever means "missing key").
- **Knobs where 0 is a legitimate operator value** (`F1MinScore` — disable the reject floor;
  `CompilationPenalty`; `RichMetadataBonusCap`): `*float64`; the resolver substitutes the legacy
  literal ONLY on nil. An explicit 0 is honored, so the UI-persisted value always equals the
  effective value and the calibration harness may legitimately recommend 0.
- **Duration tier value arrays**: length must equal the built-in table; any mismatch → the whole
  built-in table (fail-open, logged).

Two mechanisms coexist deliberately (reviewed — not redundant): `viper.SetDefault` at both
population sites (`config.go:1087`, `:1512` — grep `MetadataScoring: MetadataScoringConfig{`,
2 hits) covers YAML/env-loaded configs, so a missing key never zeroes a boost; the read-time
resolver covers zero-value `MetadataScoringConfig` structs constructed WITHOUT viper (unit tests,
embedded defaults). Because the resolver is nil-gated for the pointer knobs, it can never make 0
unreachable.

### C3. Settings UI (`web/src/components/settings/MetadataScoringSection.tsx`) — TASK-03

Extend the EXISTING section (file exists, 119 lines) + `web/src/services/api.ts:836`
(`MetadataScoringConfig` TS type) + `web/src/hooks/useSettingsHandlers.ts` (`metadata_scoring`
case at `:800`, section list at `:679`). Grouped numeric inputs with "reset to default" per group;
defaults rendered from a constants map that mirrors the Go literals. Serialization rules mirror
C2's per-knob semantics: for the pointer knobs (`f1_min_score`, `compilation_penalty`,
`rich_metadata_bonus_cap`) an EMPTY input is sent as absent/null (backend → legacy default) and an
explicit 0 is sent as 0 (honored); for the multiplicative boosts empty may be absent or 0. The
duration tier inputs expose VALUES only (two fixed-length lists); the tier edges are not editable
(fixed in code — see Data model).

### C4. Parallel bulk fetch (`internal/server/metadata_ops.go`) — TASK-05

`runBulkMetadataFetchForBookIDs` (`:439`) and `runBulkMetadataFetchAll` (`:55`) replace the
serial `for i, w := range work` with `errgroup.Group` + `SetLimit(workers)` (workers =
`BulkFetchWorkers`, default 4). This is the CLAUDE.md-sanctioned equivalent of the
`registry.RunItems` sibling (`internal/plugins/acoustid/backfill.go:118`); `RunItems` itself is
NOT used here — the loop's `OperationResult` resume rows and custom every-50 progress cadence
don't fit its reporter contract, and specifying both was reviewed as contradictory instructions to
the executor. Per-source `chan struct{}` semaphores (FIXED internal constant, 2 in-flight per
provider — deliberately not config, reviewed) wrap each provider call. Shared state hardened:
`found`/`notFound` become atomics like `completed` already is; `store.CreateOperationResult` rows
keep resume semantics (skip-if-done map built before dispatch, read-only afterwards). Progress
reporting cadence unchanged (every 50 / final). `-race` test required.

**Wave-1 tunability disclosure (reviewed):** TASK-05 lands in Wave 1 with a hardcoded worker
constant 4 (`// TODO(INIT-3-T1)` marker); `BulkFetchWorkers` only becomes runtime-tunable when
TASK-02 (Wave 2) + TASK-03 (Wave 3) land. Until then the sole mitigation for a misbehaving
fan-out is revert-PR — accepted because the per-provider semaphore, the `ProtectedSource`
circuit breaker, and Hardcover's 60-rpm limiter all bound provider impact beneath the pool.

### C5. TOCTOU cache validation (`internal/metafetch/cache.go` + apply seams) — TASK-07

New helper:

```go
// ValidateCachedIdentity recomputes hashSearchInputs from the book's current
// fields and compares to entry.SourceHash. Empty stored hash (legacy row):
// fail-open with slog warn. Mismatch: return ErrStaleMetadataCache.
func (mfs *Service) ValidateCachedIdentity(entry *MetadataCandidateCache, bookID, query, author, narrator, series string) error
```

`ApplyTranscriptionCandidate` (`internal/server/server_maintenance_deps.go:393`) calls it before
the existing slot-0 identity re-check (both guards kept — the hash catches input drift, the
identity check catches candidate-order drift). Fail-closed on mismatch: error out; caller already
treats non-nil error as "skip + log".

### C6. Author/series resolution + legacy-stub retirement (`internal/metadata/enhanced.go`) — TASK-06

Resolve the two `:254/:256` TODOs using EXISTING store methods — `GetAuthorByName`
(`internal/database/pebble_store_authors.go:93`) / `CreateAuthor` (`:113`), `GetSeriesByName`
(`pebble_store_series.go:91`) / `CreateSeries` (`:116`) — lookup-then-create, then set
`book.AuthorID`/`book.SeriesID`. Hydrate the full row before `UpdateBook` (memdb-slim footgun).
`BatchUpdateMetadata`'s `database.BookStore` parameter lacks the author/series/history methods —
widen it (compose the existing `AuthorStore`/`SeriesStore` interfaces, or accept `database.Store`)
and update its one handler call site (`internal/server/handlers/metadata/handler.go:278` +
`interfaces.go`).

**Store-error policy — fail-open on ID resolution (reviewed):** if any of the four
lookup/create calls errors mid-apply, log it, leave the ID unset, and STILL persist the other
applied fields — a store/provider hiccup never aborts the whole metadata apply. The update itself
stays fail-closed on `UpdateBook` errors exactly as today. (Per-path choice: empty name → skip,
no change; lookup miss → create; store ERROR → fail-open as above.)

Each successful ID change also records a `MetadataChangeRecord` (old → new AuthorID/SeriesID) via
the EXISTING `RecordMetadataChange`, making a mis-resolution auditable and reversible.

The metadata-history half of the original T4 is descoped to stub retirement (Decision 6): delete
`enhanced.go`'s dead `MetadataHistory` type, `RecordMetadataChange` free func, and
`GetMetadataHistory` (`:651-668` — no callers outside the file + its own test), or delegate
`GetMetadataHistory` to the existing `store.GetBookChangeHistory` if deletion turns up a hidden
consumer. No new store surface, no mock changes, no mockery regen.

### C7. Scoring calibration harness (`internal/plugins/metafetch/`) — TASK-04

Read-only op `metafetch.calibrate-scoring`, mirroring the shape and doc-comment discipline of
`internal/plugins/dedup/calibrate_embedding_thresholds.go` ("READ-ONLY calibration harness …
NEVER writes the recommendation into config"): replay persisted `MetadataCandidateCache` entries
for books whose metadata was subsequently applied (`MetadataSourceHash` set), sweep knob values,
and report which settings would have ranked the applied candidate first. Reports only; the
operator tunes via C3.

**Ground-truth caveat (reviewed):** most applied candidates were CHOSEN by the current scorer, so
top-1 accuracy over the full set is biased toward re-deriving today's weights (circularity). The
op segments results by apply origin where determinable — manual/override applies vs auto-applies —
and reports the manual segment as the primary (non-circular) signal; if no manual segment is
determinable, the report says so and flags the whole sweep as circular-biased. The report states
this caveat verbatim in its output. Sweep points for the pointer knobs may legitimately include 0
(reachable per C2). The harness is master-plan-mandated (INIT-3 T1) but gates nothing in this
plan; its value grows as operator tuning and manual applies accumulate.

### C8. Token-set fuzzy (optional, `internal/matcher/fuzzy.go`) — TASK-08

Additive `TokenSetRatio(a, b string) float64` (order-insensitive token comparison);
`ScoreMatch` blends it in only with fixture-locked before/after tables proving known-good matches
don't regress. P3 — skippable without impact on any other task.

## Migration / integration

- C2 is a mechanical literal→config-read swap. Before: `score *= 2.0` → After:
  `score *= mfs.scoringCfg().TranscriptionTitleExactBoost` (resolver applies legacy default when
  unset). No call-site signature changes.
- C4 preserves the op registry contract (`RegisterBulkMetadataFetchOp`, `metadata_ops.go:379`),
  op IDs, params, and resume rows — only the loop's execution strategy changes.
- C5/C6/C7/C8 are additive; no existing caller changes behavior without them being invoked.

## Milestones

- **M1 — Consistency + concurrency + guards (Wave 1).** Duration unification (fixture-locked),
  parallel bulk fetch, TOCTOU validation, author/series resolution + stub retirement, optional
  fuzzy. NOT behavior-free (reviewed — the earlier blanket "no scoring-weight behavior changes"
  claim was wrong): Wave 1 carries exactly THREE intentional, unflagged runtime behavior changes,
  each with a per-change revert-PR justification (see Rollback):
  1. **TASK-01** changes the duration MULTIPLIER output for some grid cells on the live matching
     path (`service_search.go:386,455`) — every changed cell enumerated and justified in the
     golden fixtures; the additive `DurationScore` breakdown is unchanged.
  2. **TASK-06** makes the batch metadata-update path START writing `book.AuthorID`/`SeriesID`
     and creating author/series rows — the documented intent of the TODOs; every ID change is
     recorded as a `MetadataChangeRecord` (old → new), so a revert stops future writes and the
     recorded rows make already-written links auditable/reversible.
  3. **TASK-07** makes `ApplyTranscriptionCandidate` REFUSE applies on a proven `SourceHash`
     mismatch — fail-closed only on mismatch, fail-open for legacy empty-hash rows; a refusal
     never mutates data, so a revert restores exactly today's apply behavior.
  Everything else in Wave 1 is additive or contract-preserving (parallel fetch keeps the op
  contract; the token-set blend is raise-only). None of the three is flag-gated: each mutation is
  either enumerated (1), audit-trailed (2), or non-mutating (3), so revert-PR is a sufficient
  inverse — a default-off toggle would add config surface without improving reversibility.
- **M2 — Config extraction (Wave 2).** All literals become knobs, value-identical defaults.
  Additive in effect: output scores are bit-identical until an operator edits config.
- **M3 — Operator surface (Wave 3).** Settings UI + calibration harness. This is the ONE
  user-visible capability change, and it is inert until used: defaults ship as today's literals,
  the harness is read-only. No feature flag needed — the "flag" is the operator not touching the
  new knobs.

Each milestone is independently shippable: M1's three behavior changes are enumerated above,
M2 is value-identical by construction, and M3 is inert-by-default.

## Files modified

| File | Change |
|---|---|
| `internal/metafetch/service_scoring.go` | C1 unified tier table; C2 literal→config reads |
| `internal/metafetch/service_scoring_test.go` | C1 golden fixtures; C2 default-equivalence tests |
| `internal/metafetch/service_search.go` | C2 series-boost literal→config reads |
| `internal/config/config.go` | C2 new `MetadataScoringConfig` fields + defaults (2 viper sites) |
| `internal/server/metadata_ops.go` | C4 bounded worker pool + per-provider semaphores |
| `internal/metafetch/cache.go` | C5 `ValidateCachedIdentity` + `ErrStaleMetadataCache` |
| `internal/server/server_maintenance_deps.go` | C5 hash check in `ApplyTranscriptionCandidate` |
| `internal/metadata/enhanced.go` (+test) | C6 author/series resolution + legacy history-stub retirement |
| `internal/server/handlers/metadata/handler.go` + `interfaces.go` | C6 widened store param at the `BatchUpdateMetadata` call site |
| `internal/plugins/metafetch/calibrate_scoring.go` + `register.go` | C7 NEW read-only calibration op + in-package registration (mirrors `internal/plugins/dedup/register.go`) |
| `internal/plugins/plugins.go` | C7 one blank-import line for the new plugin package |
| `web/src/components/settings/MetadataScoringSection.tsx` (+test) | C3 new knob inputs |
| `web/src/services/api.ts`, `web/src/hooks/useSettingsHandlers.ts` | C3 TS type + save wiring |
| `internal/matcher/fuzzy.go` (+test) | C8 optional token-set scorer |

## Testing

| Test | Asserts |
|---|---|
| `TestDurationScoringGolden` | pre/post-unification grid; every changed cell enumerated; zero-duration → 1.0/0 (unknown, non-disqualifying) |
| `TestScoringConfigDefaultsMatchLegacyLiterals` | zero-value/unset config → scores bit-identical to pre-extraction fixtures |
| `TestScoringConfigExplicitZeroHonored` | explicit 0 for `F1MinScore`/`CompilationPenalty`/`RichMetadataBonusCap` (pointer knobs) takes effect — 0 is reachable, nil → legacy default |
| `TestBulkFetchParallelResume` (`-race`) | pool honors worker cap; done-map skip preserved; counters exact under concurrency; ctx cancel stops workers |
| `TestPerProviderSemaphore` | ≤N in-flight calls per source under a 4-worker pool |
| `TestValidateCachedIdentity` | mismatch → `ErrStaleMetadataCache`; empty legacy hash → fail-open warn; match → nil |
| `TestApplyTranscriptionCandidateStaleCache` | mutated book identity between gate and apply → apply refused |
| `TestAuthorSeriesResolution` | existing name → ID reused; unknown name → created once; empty name → skip (never clears); store ERROR → fail-open (ID unset, other fields still applied); each ID change records a `MetadataChangeRecord` (old → new) |
| `TestLegacyHistoryStubRetired` | `enhanced.go` no longer exports the dead `MetadataHistory` stub trio (or `GetMetadataHistory` delegates to `store.GetBookChangeHistory` if a consumer was found) |
| `TestTokenSetRatioNoRegression` | fixture table of known-good matches keeps passing scores (anti-over-suppression) |

## Rollback

**Initiative gate (verbatim):** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config
extraction (T1) MUST default to today's literal values — zero behavior change until an operator
tunes them.

- M2/M3 are inert-by-default: reverting = deleting a config knob's value (behavior returns to the
  legacy literal via the fail-open default resolver) or reverting the PR. No flag needed.
- C1 (duration unification) is the one intentional micro-behavior change, fixture-enumerated;
  rollback = revert the single PR (fixtures revert with it).
- C4 rollback = revert; the op contract (IDs, params, resume rows) is unchanged either way.
- C5 fails open for legacy rows and fail-closed only on a PROVEN hash mismatch; a refusal never
  mutates data, so rollback = revert restores exactly today's apply behavior.
- C6 is roll-FORWARD for data (reviewed — do not overclaim): revert-PR stops FUTURE author/series
  resolution but does NOT undo already-written `book.AuthorID`/`SeriesID` links or created
  author/series rows. The per-change `MetadataChangeRecord` rows (old → new ID) are the audit
  trail for manual or scripted reversal of a mis-resolution. The stub retirement is pure code
  deletion (revert restores it). C7 is read-only.
- Each task ships as its own worktree/PR (rebase/FF), so any single component reverts cleanly.

## Open questions (resolved — recorded for the plan)

1. ~~Delete one duration function or keep both?~~ → Keep both signatures, one canonical table
   (Decision 2).
2. ~~Parallelize the provider chain per book?~~ → No; outer loop only + per-provider semaphores
   (Decision 4).
3. ~~Re-key the cache or validate the existing hash?~~ → Validate `SourceHash` at apply
   (Decision 5).
4. ~~Is `embedding_client.go:375` in scope?~~ → No; claim corrected (functional batch path,
   retry-consolidation TODO, wrong package). Recorded in Motivation.
5. ~~Split T1 frontend/backend?~~ → Yes: TASK-02 (Go) / TASK-03 (React) / TASK-04 (harness).
6. ~~Build a new metadata-history store?~~ → No; adversarial review verified the
   `MetadataChangeRecord` subsystem already ships end-to-end (store + mocks + routes +
   `MetadataHistory.tsx` UI). TASK-06 descoped to author/series resolution + stub retirement
   (Decision 6, Motivation correction).
7. ~~Zero-as-unset for every knob?~~ → No; `F1MinScore`/`CompilationPenalty`/
   `RichMetadataBonusCap` are `*float64` (nil-gated) so an explicit 0 is a reachable operator
   value (C2).
