<!-- file: docs/audits/2026-09-02-dedup-review-matching-path-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f8a2c19-6d4e-4b7a-9e12-c5d08f1a7b36 -->
<!-- last-edited: 2026-09-02 -->

# Dedup and review surfaces: matching-path and "why" audit

Read-only investigation at HEAD `d2fcef16a` (2026-09-01), performed by four read-only census agents
and consolidated by hand. Written up on 2026-09-02; no repo files were modified by the audit itself.
Owner's ask: "go through our deduplication and review pages and make sure all of them go
through the same matching path and we can see why they got their rating like in metadata."

Method: three read-only census agents (core scorers, peripheral scorers, `/review`
workspace) plus one (`/dedup` page). Every headline claim below was re-read by me at HEAD
before inclusion; agent-only claims are marked "(agent, not re-read)". All paths are relative
to the repo root.

---

## 0. Executive summary

**Short answer: no, they do not go through the same matching path, and only the `/review`
workspace shows "why". There is exactly one composite book-pair scorer
(`unified.ComposeScore`), but five other verdict systems rate book pairs independently,
several TS components invent ratings of their own, and the configured thresholds do not
reach the live scorer at all.**

Findings that change how everything else reads:

| # | Finding | Evidence |
|---|---|---|
| S1 | **Configured band thresholds are inert for live scoring.** `Engine.SetScoreConfig` has zero callers; `getScoreConfig()` always returns `unified.DefaultScoreConfig()`. `unified.LoadScoreConfig()` (the only reader of DB/viper overrides) is called from one place: the `dedup.calibrate-composite` op. So the Settings UI band inputs, `dedup.signals.*` in config, `registry_wire.go:212/225`, and the calibrator's own `apply` all write values nothing live reads. Not a numeric divergence today because all three sources agree at 97/90/75/60 — but memory records a recommended 85.5/96 as "GATED-PENDING apply"; had it been applied, it would have done nothing. | `internal/dedup/engine.go:288-300`; `grep -rn "SetScoreConfig("` = definition only; `internal/plugins/dedup/calibrate_composite.go:467` sole `LoadScoreConfig` caller; `internal/dedup/unified/config.go:66-72` literal defaults, `:152-172` `bandOverride` read only by `LoadScoreConfig`; `internal/server/registry_wire.go:212,225` |
| S2 | **Legacy exact-layer rows can never acquire a breakdown.** The store's precedence rule protects `Layer=="exact"` rows from any overwrite, so the unified pass's write (which carries `ScoreBreakdown`/`Band`/`FormulaVersion`) is silently discarded for every pair first emitted by `upsertExactCandidate(..., "exact", 1.0)`. These rows show `similarity=1.0`, no band, no signals, no "why" — forever. `Rescore` then skips them because they have no breakdown. | `internal/database/embedding_store.go:714-731`; `internal/dedup/engine.go:946-968` (unified upsert), `:1745-1752` (legacy literal has no breakdown fields), `:3279-3282` (Rescore skip) |
| S3 | **Suppressors are never persisted.** Every live `ComposeScore` call passes `nil`; the scan deletes a suppressed pair instead of scoring it. The auto-resolve veto on `Suppressors` is vacuous (documented in code). The `/review` UI has no suppressor concept and the TS type omits the field. | `internal/dedup/engine.go:948`; `internal/dedup/auto_resolve.go:222-231`; `web/src/services/api.ts:5200-5206` |
| S4 | **`Similarity` carries two incompatible scales.** Pre-unified rows hold cosine/hash (0-1); unified rows hold `Score/100`. The LLM review window (0.80-0.92, hardcoded) and the `/dedup` Embedding/Acoustic tabs render both as if they were one number. | `internal/dedup/engine.go:956,963` vs `:2303-2309`, `:165-166`, `:3626-3638`; `web/src/components/dedup/DedupEmbeddingTab.tsx:1482`, `DedupAcousticTab.tsx:1239` |
| S5 | **Settings band inputs are on the wrong scale** (0-1 with step 0.01) while Go bands are 0-100. Combined with S1 this is a double defect: the control is both mis-scaled and disconnected. | `web/src/components/settings/DedupSettingsSection.tsx:~195-202`; `internal/config/config.go:1878-1881`; `internal/dedup/unified/config.go:313-321` Validate |
| S6 | **A dead threshold parameter.** `findDuplicateAuthorsInternal(authors, threshold, ...)` never reads `threshold`; seven callers pass 0.85/0.9/0.90, all inert. | `internal/dedup/author.go:1159` (only other hit is a comment at `:1403`) |
| S7 | **Engine constants are hand-copied** into two other packages with no compile-time link: `minPlausibleAudioBytes` (engine.go:1680 = dataset/rules.go:38 = maintenance/dedup_triage.go:114, "copied ... to avoid an import cycle"), `partVsWholeDurationRatioMax` (engine.go:50 = rules.go:33). | as listed |
| S8 | **The metadata lane is the only surface that meets the bar** ("see why they got their rating"): a recorded waterfall of every applied step, replayed client-side and flagged if it does not recompose. The dupes lane is close (signals + confidences, no bar). Nothing on `/dedup` shows a derivation. | `internal/metafetch/score_breakdown.go:45-65`; `web/src/components/review/evidence/EvidencePanel.tsx:304-415`, `:329-342` |

---

## 1. Surface table

Legend: **Breakdown exposed?** = does the HTTP response carry per-component evidence.
**UI shows why?** = does the rendered row/panel show that evidence. Y / N / partial.

### 1a. `/review` workspace (`web/src/App.tsx:386-389` -> `ReviewWorkspace`)

| # | Surface | Page/component | API fn | Endpoint | Handler | Scorer | Breakdown exposed? | UI shows why? |
|---|---|---|---|---|---|---|---|---|
| R1 | Metadata: CompactRow score chip | `web/src/components/review/spine/CompareSpine.tsx:415`, chip `:480-489` | `getCachedReviewResults` `web/src/services/api.ts:3831` | GET `/api/v1/audiobooks/metadata/cache/review?limit=0&offset=0` | `internal/server/handlers/metadata_cache.go:162` (route `wire_library_routes.go:66`) | `ApplyNonBaseAdjustmentsWithBreakdown` `internal/metafetch/service_scoring.go:133`; search loop `service_search.go:517-619` | **Y** inline (`MetadataCandidate.ScoreBreakdown` `service.go:205`) | **N** (chip only) |
| R2 | Metadata: CompactRow expanded | `CompareSpine.tsx:765` -> `EvidenceSection :93` -> `EvidencePanel :99` | same | same | same | same | Y | **Y** waterfall (`EvidencePanel.tsx:304`) |
| R3 | Metadata: TwoColumnCard | `CompareSpine.tsx:772`, evidence `:1046` | same | same | same | same | Y | **Y** (always visible) |
| R4 | Metadata: AutoCard | `CompareSpine.tsx:1221` -> TwoColumnCard `:1233` | same | same | same | same | Y | **Y** (inherited) |
| R5 | Metadata: GroupedCard (multi-book group) | `CompareSpine.tsx:178`, chip `:354-358` | same | same | same | same | Y arrives | **N** — no `EvidenceSection`/`EvidencePanel` call in lines 178-415 (verified) |
| R6 | Metadata: QueueRail row | `web/src/components/review/QueueRail.tsx:552-556` | same | same | same | same | Y arrives | **N** (`scoreColor` chip) |
| R7 | Metadata: manual search dialog | `web/src/components/audiobooks/MetadataSearchDialog.tsx:700` (opened from `MetadataPanel.tsx:171`) | `searchMetadataForBook` `api.ts:3130` | POST `/api/v1/audiobooks/:id/search-metadata` | `wire_metadata_routes.go:30` | `service_search.go:619` | **Y** | **N** — no `EvidencePanel` import and no `score_breakdown` reference in the file (verified) |
| R8 | Dupes: DupesSpine row | `web/src/components/review/spine/DupesSpine.tsx:324` band, `:329` score, `:332` layer, `:341` signal chips | `getDedupCandidates` `api.ts:5245` | GET `/api/v1/dedup/candidates?include_breakdown=true&include_books=true` | `internal/server/handlers/dedup/handler.go:141` (route `wire_dedup_routes.go:26`) | `unified.ComposeScore` via `runUnifiedScoringForBook` `engine.go:948` | **Y** gated on `include_breakdown` (`handler.go:202`, default false); lane sets it `useDupesLane.ts:316` | **partial** — primary-signal chips (`signalLabels.ts:96`) |
| R9 | Dupes: DupesSpine evidence panel | `DupesSpine.tsx:427`, gated `twoColumn \|\| expanded` `:425`, "Why?" `:357` | same | same | same | same | Y | **Y** confidence rows (`EvidencePanel.tsx:135`) |
| R10 | Dupes: CandidateCompareDrawer | `web/src/components/dedup/CandidateCompareDrawer.tsx:575`, fetch on open `:309` | `getDedupCandidateBreakdown` `api.ts:6209` | GET `/api/v1/dedup/candidates/:id/breakdown` | `handler.go:364-381` | same | **Y** always | **Y** |
| R11 | Regroup: accordion chip | `web/src/components/review/spine/RegroupSpine.tsx:715-721` | `getReviewItems` `api.ts:6437` | GET `/api/v1/review/items?status=pending&limit=500` | `internal/server/handlers/review/handler.go:265` | `recommendAction`/`classifyGroup` `internal/itunes/service/fs_regroup_shape.go:333/:832` | **partial** — no score; facts ride inside `Payload string` (`internal/database/review_store.go:68`) | **partial** (recommendation label) |
| R12 | Regroup: RecommendationPanel | `RegroupSpine.tsx:210`, `EvidencePanel :254` | same | same | same | `RecommendationEvidence` `fs_regroup_shape.go:229-255`, written `internal/plugins/maintenance/regroup_shattered_ai.go:101,:432-434` | partial | **Y** fact chips (`EvidencePanel.tsx:234`) |
| R13 | ReviewBanner | `web/src/components/ReviewBanner.tsx:22,35` | `getReviewCount` `api.ts:6425` | GET `/api/v1/review/count` | `review/handler.go:231` | — | N/A | N |

No legacy `/review` page exists: `ReviewQueue.tsx` / `MetadataReviewDialog` were deleted in
Phase 7 (`App.tsx:46-50`). Stale copy: `ReviewWorkspace.tsx:347,354,361` disable three Queue
commands with "The review-queue lane is not ported yet — use the Review page." (verified) —
that page no longer exists and the regroup lane is ported.

### 1b. `/dedup` page (`web/src/pages/BookDedup.tsx:27-37`, 9 tabs; pair candidates moved to `/review` per comment `:59-64`)

| # | Surface | Tab file | API fn | Endpoint | Handler | Scorer | Breakdown exposed? | UI shows why? |
|---|---|---|---|---|---|---|---|---|
| D1 | Version Groups | `web/src/components/dedup/DedupBookTab.tsx:62` | `getBookDuplicates` `api.ts:1632` | GET `/audiobooks/duplicates` | `internal/server/handlers/duplicates/handler.go:131` | none (grouping) — `response_types.go:55-66` | N | N |
| D2 | Duplicate Scan | `DedupAdvancedScanTab.tsx:56` | `getBookDedupScanResults` `api.ts:1709` | GET `/audiobooks/duplicates/scan-results` | `duplicates/handler.go:163`; op `internal/server/duplicates_ops.go:50` | `internal/dedup/book_dedup.go:165-175` tiers + `applyTranscriptionMetadataTiebreaker :190` | N — `BookDupGroup{Confidence "high"/"medium"/"low", Reason}` `book_dedup.go:31-36` | **partial** (chip `:277`, reason string `:290`) |
| D3 | Authors | `DedupAuthorTab.tsx:309` | `getAuthorDuplicates` `api.ts:1525` | GET `/authors/duplicates` | `duplicates/handler.go:413`; op `duplicates_ops.go:278` -> `dedup.FindDuplicateAuthors(authors, 0.9, ...)` `:357` (inert, S6) | `areAuthorsDuplicate` `internal/dedup/author.go:632` -> bool | N — `AuthorDedupGroup` has no score/reason | N |
| D4 | Series | `DedupSeriesTab.tsx:122` | `getSeriesDuplicates` `api.ts:2843` | (op) `duplicates/handler.go:433`; `duplicates_ops.go:388` | `internal/dedup/series_dedup.go:215 ("exact") / :259 ("subseries")` | N — `match_type` only (`series_dedup.go:45`) | **partial** (subseries chip `:445-447`) |
| D5 | AI Review | `DedupAIReviewTab.tsx:79` | `getAIScanResults` | GET `/ai/scans/:id/results` (`wire_media_routes.go:52`) | LLM | `ScanSuggestion.Confidence` string `internal/aiscan/ai_scan_store.go:73`, `Reason :72` | N | **partial** (chip `:360`, reason `:386`) |
| D6 | Reconcile | `DedupReconcileTab.tsx:51` | `getReconcilePreview` `api.ts:4765` | GET `/operations/reconcile/preview` | — | `internal/reconcile/reconcile.go:~396-402` hash high 1.0, `:~418-424` original_hash high 0.95, filename low tiers | N — `ReconcileMatch{match_type, confidence, score}` `:69-77` | **partial** (`score` never rendered; dead `case 'medium'` `:149-150`) |
| D7 | Embedding | `DedupEmbeddingTab.tsx:246-252` | `getDedupCandidates` | GET `/dedup/candidates` (no `include_breakdown`, verified) | `dedup/handler.go:141` | ComposeScore (when row has one; see S2) | **partial** — endpoint can carry it, tab does not ask | **N** (layer chip `:1470-1473`, `maxSimilarity` `:1482`; TS-invented `LAYER_RANK` `:109`) |
| D8 | Acoustic table | `DedupAcousticTab.tsx:579-583` | same | same (no `include_breakdown`, verified) | same | same | partial | **N** (`simPct` `:1239`, `>=0.9` `:1241`; TS-invented `metadataQuality :523-537`, `qualityChip :540-545`, "Recommended keep" `:1166-1169`) |
| D9 | Acoustic compare dialog | `DedupAcousticTab.tsx:224` | `compareAcoustID` `api.ts:5682` | POST `/audiobooks/:id/compare-acoustid` | `dedup/handler.go:1787`, segment ratio `:1838-1854` | independent segment-hash ratio | per-segment table `:465-472` | partial — a *different* score from the same pair's candidate row |
| D10 | Split Books | `DedupSplitBookTab.tsx:183` | `getSplitBookCandidates` `api.ts:5553` | GET `/dedup/split-book-candidates` | — | `internal/dedup/split_book_detector.go:337/:407` boolean qualifier | N — `SequentialPattern` string + `Shape` (`:72,:75`) | partial (pattern text) |
| D11 | Labels (`/dedup/labels`) | `web/src/pages/DedupLabels.tsx:509-521` | raw `apiFetch` | GET `/dedup/labels` | `internal/server/handlers/dedup/label_review.go:25` | stored `LabeledExample.Score`/`ScoreBreakdown` (`internal/database/dedup_label.go:59-60`) | **Y on the wire** — but local TS `interface LabeledExample` `DedupLabels.tsx:57-69` omits both fields | **N** |
| D12 | Labels: Suspicious | `DedupLabels.tsx:194-266` | — | GET `/dedup/labels/suspicious` | `label_review.go:123`, `suspicionReasons :83-111` | rule list (band in CERTAIN/HIGH `:99`, sim>=0.95 `:103`, ratio<0.01 `:107`) | reasons list | **Y** ("Why suspicious" chips `:201,:255-263`) |
| D13 | Settings: dedup section | `web/src/components/settings/DedupSettingsSection.tsx` (mounted `Settings.tsx:44,857`) | — | config | `config.NewUpdateService` -> `registry_wire.go:212` | writes `bandOverride` — read by nothing live (S1) | — | wrong scale (S5) |

Rows D7/D8 are the only `/dedup` surfaces that *could* show the ComposeScore derivation
today with no Go change: add `include_breakdown: true` and mount `EvidencePanel`.

---

## 2. Distinct scorer / verdict implementations

Grouped by relationship to the shared core. "Core?" = calls `unified.ComposeScore` (Y),
reads its output without recomposing (reads), or is independent (N).

### 2a. The shared core

| Fn | file:line | Inputs | Thresholds (source) | Output | Tests |
|---|---|---|---|---|---|
| `ComposeScore` | `internal/dedup/unified/compose.go:47-98` | `[]Signal`, suppressors, `ScoreConfig`, pair | noisy-OR over primary `Confidence` verbatim (`:67`), boosts `duration 4.0`/`folder_path 3.0` (`unified/config.go:139,146`), cap 100 (`compose.go:80-82`), bands via `bandFor :100-113` from cfg — **cfg is always `DefaultScoreConfig` in prod (S1)**. Per-kind Min/Max confidence bounds are dead for scoring (`internal/config/config.go:237-242`). | `UnifiedDedupScore{Score, Band, Signals, Suppressors, Formula "noisy-or-v1", ComputedAt}` — components preserved | `unified/compose_test.go` |
| `collectPairSignals` | `internal/dedup/rescore.go:68` | book, candidate ID, per-book batches | filters Evidence strings by embedded book IDs (`engine.go:1073-1091`, load-bearing coupling); embedding tiering inline `rescore.go:95,104` | `[]Signal` | **no test names it** (indirect via `rescore_test.go`) |
| `runUnifiedScoringForBook` | `internal/dedup/engine.go:769` | book | `getScoreConfig()` `:807`, `resolvedBookThresholds()` `:812` (`dedup.book_high/low_threshold` 0.95/0.85, `config.go:1787-1788`), collector defaults `:815-817` all hardcoded; **`Band==""` -> bare `continue` `:951-953`** | persists `ScoreBreakdown/Band/FormulaVersion` + collapses `Similarity = Score/100` `:956-967` — **discarded for protected rows (S2)** | `engine_fullscan_score_parallel_test.go` |
| `ScorePairsForBook` | `rescore.go:185`, ComposeScore at `:276` | injected work list | same configs `:210-217`; bypasses band gate (`:274`) | `[]RescorePairResult` with full breakdown, no persistence | `rescore_test.go`, `handlers/dedup/label_capture_test.go` |
| `Engine.Rescore` | `engine.go:3245`, ComposeScore `:3284` | stored breakdowns | only call site passing real suppressors (always empty, S3) | `UpdateCandidateScore` `:3294` — **does not touch `Similarity`**, so Band and Similarity drift | `rescore_test.go` |

Collectors (all return `[]Signal`, nothing collapsed; all confidences hardcoded):
`CollectExactFileHash` 1.0 (`collectors_exact.go:82,100`), `CollectISBNASIN` 0.98 (`:157,195`),
`CollectMetaSrcHash` 0.97 (`:283,306`), `CollectExactAcoustID` 0.99 (`collectors_acoustid.go:90,148`),
`CollectLSHAcoustID` 0.90-0.97 interp (`:237`, defaults `:202-209`), `CollectDuration` conf 0
(`collectors_metadata.go:144,232`; tolerances `engine.go:1500,1508,1518`; **no test names it**),
`CollectMetaFuzzy` 0.70-0.85 interp over `0.70*title + 0.30*author` (`:404`, `:294-300`, `:381`),
embedding tiers 0.88-0.95 / 0.65-0.80 (`collectors_embedding.go:94-95,114-115`; `CollectEmbedding`
`:141` is **dead** — the live copy is inlined in `collectPairSignals`).

### 2b. Legacy engine emitters that write `Similarity` with no breakdown (independent, same pairs)

| Fn | file:line | Writes | Core? | Tests |
|---|---|---|---|---|
| `upsertExactCandidate` | `engine.go:1711`, literal `:1745-1752` | `Layer` + caller-supplied `Similarity`: 1.0 at `:1226,1291,1332,1488,1620`; 0.99 at `:1368` ("metadata_hash") | **N** — and its `"exact"` rows are immune to correction (S2) | 7 test files |
| `findSimilarBooks` | `engine.go:2186`, literal `:2304-2311` | raw cosine as `Similarity` `:2303`, top-K 20 hardcoded `:2222,2244` | N | `engine_primary_gate_test.go` |
| `AcoustIDScan` emit | `engine.go:4287-4295` | raw `WholeFileSimilarity` >= 0.80 (`fingerprint/fpcalc.go:78`) | N — never runs the unified pass | `engine_acoustid_test.go` |
| `BookSignatureScan` emit | `engine.go:4487-4502` | masked sig sim >= 0.80, overlap >= 512 (`:4534`) | N — `"book_signature"` has **no `SignalKind`** (`unified/score.go:30-70`), so it can never enter ComposeScore | `engine_booksig_parallel_test.go` |
| `bestLayerFromSignals`/`layerNameForKind` | `engine.go:1100/:1133` | collapses signals to one of `exact`/`acoustid`/`embedding` `:1136-1144` | collapse only | `layerNameForKind`: **no test** |

### 2c. Verdicts that read `ScoreBreakdown` but do not recompose (can disagree with the band)

| Fn | file:line | Rule | Output | Tests |
|---|---|---|---|---|
| `autoResolveEligible` | `internal/dedup/auto_resolve.go:213` | stored `Band==CERTAIN` `:214`; breakdown non-nil `:219`; suppressors (vacuous, `:222-231`); **live** `PairEligibility` `:236`; `hasPlausibleAudio` `:243`; `identifiersConflict` `:246`; `>=2` distinct primary kinds from allow-list `:36-41` at `:255-262` OR stored `true_dup` label whose reason contains `"whole-book signatures match"` `:267-271` | `(bool, reason string)` — reasons `:215,220,223,237,244,247,261,270,273` | `auto_resolve_test.go` |
| `ApplyVerdicts` (LLM) | `engine.go:3734` | `duplicate` + `high` -> auto-merge if `dedup.llm_auto_merge_high_confidence` (`:3761,3770`) — **never reads Band** | `LLMVerdict/LLMReason` | `engine_test.go` |
| `listAmbiguousCandidates` | `engine.go:3626` | `Layer=="embedding"`, `Similarity` in [0.80, 0.92] hardcoded (`:165-166`) — mixed scale (S4) | LLM work list | **no test** |
| `ClassifyCandidate` (triage) | `internal/plugins/maintenance/dedup_triage.go:124` | reads `ScoreBreakdown.Signals` `:196-277`; own stricter title rule (`:151-153`); copied constant `:112-114` (S7); fragment ratio `<0.05` `:259` | `(TriageClass, reason)`, reasons kept for 5 samples/class (`:70,77`), feeds purge (`internal/server/server_maintenance_deps.go:437,441,488`) | `dedup_triage_test.go` |
| `dataset.Classify` | `internal/dedup/dataset/rules.go:50` | hardcoded 0.95 (`:28`), 0.5 (`:33`), 256 KiB (`:38`) — two are copies (S7) | `(label, reason, fires)` -> `LabeledExample.LabelReason` (`engine.go:1012-1017`) | `rules_test.go` |
| `classifyStaleCandidate` | `internal/dedup/drain_stale.go:304` | six predicates `:310-327` on raw fields, not band | `(reason, bool)`, aggregated counts | **no direct test** |

### 2d. Independent book-pair scorers on other scales (the `/dedup` page's rating systems)

| Fn | file:line | Thresholds | Output | Core? | Tests |
|---|---|---|---|---|---|
| `book_dedup.go` tiers | `internal/dedup/book_dedup.go:165-175` (`addGroups` high/medium/low), `applyTranscriptionMetadataTiebreaker :190`, `metadataPairSimilarity :254` | `metadataDuplicateThreshold 0.85`, `BorderlineFloor 0.80`, `Ceiling 0.88` hardcoded `:25-27` | `BookDupGroup{Confidence "high"/"medium"/"low", Reason}` `:31-36` | **N** — reuses `metaTitleAuthorSimilarity` (`:257`) with primary titles only; never bands | `book_dedup_test.go`; `metadataPairSimilarity`/`transcriptionAgreement` **no test** |
| Reconcile matches | `internal/reconcile/reconcile.go:~396-402, ~418-424, :433+` | literals: hash `high 1.0`, original_hash `high 0.95`, filename `low` 0.5/0.3 (agent, not re-read); `medium` never produced | `ReconcileMatch{match_type, confidence, score}` `:69-77` | N | `reconcile_hash_test.go` |
| AcoustID compare | `internal/server/handlers/dedup/handler.go:1787`, ratio `:1838-1854` | segment-hash ratio | per-segment table | N | — |
| `merge.CollisionCandidate`/`QuickHash` | `internal/merge/collision.go:15,32` | first 1 MiB SHA-256 | `match_type` label only | N | **no dedicated test** |
| `calibrate_embedding_thresholds.sweepThreshold` | `internal/plugins/dedup/calibrate_embedding_thresholds.go:245` | raw cosine grid 0.80-0.99 (`:66-68`) | report only (`:115`) | N — bypasses the confidence mapping | has test |

### 2e. Non-book-pair domains (different unit of analysis; cannot share ComposeScore as-is)

Authors: `areAuthorsDuplicate` `author.go:632` / `Precomputed :1525` -> **bool**, JW floors 0.95/0.85/0.92/0.80/0.60 hardcoded (`:664,680,721,812,1409,1437`), no reason string in `AuthorDedupGroup`.
Series: `ScanSeriesDuplicates` `series_dedup.go:148` — normalized-name equality + subseries pattern, `match_type` only.
Split books: `sequentialRun :337` coverage >= 0.70 (`:361`), gap <= 2 (`:365`), cluster >= 3 — boolean, `SequentialPattern` text preserved.
Regroup: `classifyGroup :832` (11-rule cascade `:980-1082`) and `recommendAction :333` (4 gates `:340-395`) — independent by design (`:326-329`); `bookLengthSec 90*60` `:178`, `flatMultitrackMin 4` `:482`; `Confident bool` collapsed to `"high"/"review"` at `regroup_shattered_ai.go:408-411`.
Missing-file repoint: `planMissingFileRepoint` `internal/plugins/maintenance/missing_file_repoint.go:233` — no score; four refuse-gates (`:300-403`), every row gets `Bucket`+`Reason` to TSV (`:82-85,:497`). No `/review` surface for it exists (only `regroup.*` kinds are produced: `regroup_shattered_ai.go:263,340`).

### 2f. Ratings invented in TypeScript (no Go counterpart)

| What | file:line |
|---|---|
| `LAYER_RANK` ordering | `web/src/components/dedup/DedupEmbeddingTab.tsx:109` |
| `metadataQuality` / `qualityChip` (>=6/>=3) / "Recommended keep" | `DedupAcousticTab.tsx:523-545, :1166-1169, :1213, :1231` |
| overall colour `>=0.85/>=0.6` | `DedupAcousticTab.tsx:437-445`; `spine/rowState.ts:120-124` re-inlined at `CompareSpine.tsx:357,484-487,730-733,953-956` |
| `SUPPORTING_KINDS`/`isPrimaryKind` — TS copy of Go `isSupportingKind` (`unified/score.go:102`) | `web/src/components/review/evidence/signalLabels.ts:52-64` (flagged in-code as a stopgap) |
| Band tooltip strings "Score >= 97 / 90-97 / 75-90 / 60-75" baked | `web/src/components/review/BandFilterBar.tsx:22-37` (zero non-test render sites) |
| Three inconsistent high/medium/low colour maps | `DedupAdvancedScanTab.tsx:119-130` (high -> error) vs `DedupReconcileTab.tsx:145-154` and `DedupAIReviewTab.tsx:363-369` (high -> success) |
| Band -> colour disagreement within one row | `DupesSpine.tsx:100-105` (CERTAIN -> success) vs `EvidencePanel.tsx:97-108` (CERTAIN -> error) — verified; a CERTAIN pair is green on the row and red in the panel beneath it |
| `ScoreBadgeRow` conflates 0-100 composite and 0-1 cosine into one `NN%` | `web/src/components/review/ScoreBadgeRow.tsx:31-36` |
| Strict-preset confidence 190, default 85, filter `score*100 >= threshold` | `useMetadataLane.ts:100,104,717` |

---

## 3. The bar: what the metadata lane exposes as "why"

Go: `internal/metafetch/score_breakdown.go:45-65`
`ScoreStep{ID, Label, Op ("base"|"multiply"|"add"|"replace"), Operand, Running, Detail, Capped}`,
`ScoreBreakdown{Score, Steps}`; carried as `MetadataCandidate.ScoreBreakdown` (`service.go:205`).
The recorder **owns** the score (`:110-113`) so a factor cannot be applied without being recorded —
with one honest limitation: identity steps (`factor == 1` `:130-132`, `term == 0` `:141-143`) are
applied but not recorded, so "duration not used" and "duration exactly neutral" are indistinguishable.

Step vocabulary (id -> operand source): `base` (tier label `:208-217`); `compilation` x cfg 0.15
(`service_scoring.go:145`); `length` (`:156`); `rich_metadata` + capped (`:193`, only step that sets
`Capped`); `author` x **1.5/0.7/0.75 hardcoded** (`service_search.go:540,543,547` — verified);
`narrator_match` x **1.3** (`:557`); `series` x cfg `SeriesNameMatchBoost` 1.4 (`:567`);
`narrator_present` x **1.15/0.85** (`:575,578`); `transcription` (`:586`); `duration` (`:591`);
`asin_match` replace 1.0 (`:667`); `llm_rerank` replace (`score_breakdown.go:261`).
Config knobs resolve through `scoringKnobs()` (`service_scoring.go:332`) with a documented
fail-open asymmetry (`:307-331`): `*float64` fields honour an explicit 0, plain `float64` treat 0
as unset.

TS render: `EvidencePanel.tsx:304-415` `WaterfallView` — one zebra row per step, operand formatted
by op (`:267-279`), tone green/red/neutral (`:286-302`), 72 px bar scaled to peak (`:314-318`),
running total, `detail` as tooltip. **Consistency check** `:329-330`: recomposes via
`recomposeWaterfall` (`types.ts:183`) and shows a "breakdown incomplete" chip (`:338-342`) with
`incompleteReason` (`types.ts:240-252`) if it does not replay to the score. Acceptance test on the
Go side: `TestSearchPath_BreakdownExplainsEveryCandidateScore`
(`internal/metafetch/service_scoring_breakdown_test.go:205`) asserts the property over every
returned candidate. Epsilon mismatch: panel `1e-6` vs `types.ts:217` default `1e-9`.

**Computed on every metadata path** (three set sites: `service_search.go:619,702`,
`score_breakdown.go:266`; survives the cache round-trip `cache.go:143-151` ->
`metadata_cache.go:305-306`). **Rendered on only R2-R4.**

So "the bar" = (a) every applied component with its operand and running total, (b) a client-side
replay that flags disagreement between scorer and recorder, (c) a Go test asserting the property.
The dupes lane meets (a) partially (signals + confidences, no boosts/thresholds shown), lacks (b)
(the TS `recompose` exists in `types.ts:23-36` but the panel does not flag mismatch), and has (c)
only for `ComposeScore` itself.

---

## 4. Divergences with concrete example pairs

All pairs are constructed from the code paths above; confidence interpolations are approximate
(linear per `collectors_metadata.go:305` / `collectors_embedding.go:91,112`).

**P1 — same file hash, first seen by the legacy path.**
`handleFileHashMatch` -> `upsertExactCandidate(a,b,"exact",1.0)` (`engine.go:1226`): row =
`{layer:"exact", similarity:1.0}`, no breakdown. Full scan later composes `exact_file` conf 1.0 ->
100 CERTAIN and writes it — **discarded** by `embedding_store.go:716`. Result in `/review` dupes
lane: no band chip, no signal chips, `EvidencePanel` renders `dedupEvidence(undefined)` ->
empty-state text; `Band` filter excludes it entirely; `autoResolveEligible` rejects it as "no score
breakdown (legacy row)". The identical pair first seen by the unified pass shows CERTAIN 100 with an
"Exact file hash" row. Same evidence, two presentations, and only the second is auto-resolvable.

**P2 — same title+author (sim ~0.90), no hash, different folders.**
`/dedup` Duplicate Scan (`book_dedup.go:170-175`): `metadataPairSimilarity` 0.90 >= 0.85 -> group
confidence **"low"**, reason "Similar title and author". `/review` dupes lane: `metadata_fuzzy`
conf ~0.82 -> score **82 MEDIUM**. `dataset.Classify` may add a `not_dup` label if durations imply
part-vs-whole (`rules.go:144`). Three verdict vocabularies for one pair.

**P3 — same original hash (file since re-encoded).**
Reconcile tab: `original_hash`, **"high", 0.95** (`reconcile.go:~424`) on a 0-1 scale; unified:
`exact_file` would not fire (current hash differs) — the pair may have *no* candidate row at all.
Where both exist, "high 0.95" vs "CERTAIN 100" are different scales and vocabularies for the same
claim.

**P4 — duration-only match (within 2%), nothing else.**
`checkDurationMatch` emits `Layer "exact", Similarity 1.0` (`engine.go:1620`). Unified pass:
`duration` is supporting (conf 0, boost +4) -> score 4.0 -> band `""` -> bare `continue`
(`engine.go:951-953`); nothing written, nothing deleted. The `/dedup` Acoustic/Embedding tables
render this row at **100%** (`simPct`, `maxSimilarity`), while the scorer rated it 4/100 and walked
away. `/review` dupes lane never shows it (no band).

**P5 — embedding cosine 0.93.**
`findSimilarBooks` writes `Similarity 0.93, Layer embedding`. Unified pass: 0.93 is between low
0.85 and high 0.95 -> `embedding_med` conf ~0.77 -> **77 MEDIUM**; `Similarity` overwritten to 0.77.
Embedding tab now shows "77%" as if it were the cosine. LLM window (0.80-0.92 on `Similarity`):
pre-unified 0.93 is above it, post-unified 0.77 is below it — never reviewed either way.
`calibrate-embedding-thresholds` sweeps the raw 0.93. Four readings of one number.

**P6 — LLM says "duplicate / high" on a MEDIUM pair.**
`ApplyVerdicts` auto-merges if `dedup.llm_auto_merge_high_confidence` (`engine.go:3761-3797`)
without reading `Band`. Conversely a CERTAIN pair can be set `not_duplicate` by the LLM. The band
and the verdict are two authorities with no tie-break rule.

**P7 — the calibrator's recommended bands.**
`dedup.calibrate-composite --apply` writes `band_certain_min 96 / band_high_min 85.5` via
`ApplyUpdates` (`calibrate_composite.go:648-670`) -> `registry_wire.go:212 SetBandThresholds` ->
`bandOverride` -> read only by `LoadScoreConfig` -> read only by the calibrator itself. Live
scoring keeps 97/90. The calibrator would then re-read its own recommendation as the "baseline"
and report all targets met. (Prod state not checked — read-only task; verify with the config
endpoint before relying on this.)

**P8 — Settings UI.**
User types `0.97` into "Certain min" (`DedupSettingsSection.tsx` htmlInput max 1). Go `Validate`
requires certain > high > medium > review (`unified/config.go:313-321`); with high still 90 it
fails; if all four are entered on 0-1 they pass and every score >= 1 would be CERTAIN — except
nothing live reads them (S1). Both a scale bug and a disconnected control.

**P9 — same series-volume pair (`series_volume_differs`).**
`PairEligibility` returns the suppressor (`eligibility.go:86,96`); the scan **deletes** the
candidate (`engine.go:921`) and counts the reason into a log line (`:916-918,972-977`). The `/review`
UI can never show "this pair was suppressed because volumes differ" — the reason is discarded before
persistence. The `Suppressors` field exists on the wire and is always `[]`.

**P10 — author pair "J. R. R. Tolkien" / "JRR Tolkien".**
Grouped by `areAuthorsDuplicate` (bool). `/dedup` Authors tab shows the group with no score and no
reason; every caller's `threshold` argument is ignored (S6). No surface can say why.

---

## 5. Recommended unification design

### 5.1 Principle

Two different problems are tangled in the owner's sentence:

1. **"Same matching path"** — achievable for **book pairs** (ComposeScore already exists and
   already owns the derivation). Not achievable literally for authors, series, regroup folders and
   repoints — different units of analysis, different evidence. For those the target is "same
   explanation shape", not "same scorer".
2. **"See why they got their rating like in metadata"** — achievable everywhere by giving every
   verdict-producing Go type one explanation envelope and rendering it through the existing
   `EvidencePanel`.

### 5.2 `MatchExplanation` (Go, `internal/models/match_explanation.go`, ~120 lines)

```go
// MatchExplanation is the one wire shape every rating surface carries.
type MatchExplanation struct {
    Formula    string        `json:"formula"`      // "noisy-or-v1" | "metadata-waterfall-v1" | "book-dedup-tiers-v1" | "reconcile-hash-v1" | "regroup-facts-v1" | "author-jw-v1" | "series-name-v1" | "split-book-run-v1"
    Scale      string        `json:"scale"`        // "percent" (0-100) | "unit" (0-1) | "categorical"
    Score      *float64      `json:"score,omitempty"`
    Verdict    string        `json:"verdict"`      // band | tier | match_type | recommended action
    Components []Component   `json:"components"`   // one row per signal / step / fact
    Thresholds []Threshold   `json:"thresholds"`   // every cut-point that was applied, with provenance
    Suppressors []Suppressor `json:"suppressors"`  // reasons the pair was vetoed / down-ranked
    Steps      []Step        `json:"steps,omitempty"` // optional ordered waterfall (metadata lane)
    ComputedAt time.Time     `json:"computed_at"`
}
type Component struct {
    Kind, Label string; Raw, Confidence float64; Boost float64
    Primary bool          // authoritative, not re-derived in TS
    Evidence string; Applied bool // Applied=false for identity/skipped steps (fixes the waterfall blind spot)
}
type Threshold struct { Name string; Value float64; Source string } // Source: "config:dedup.signals.band_certain_min" | "hardcoded:internal/dedup/book_dedup.go:25" | "db-override"
type Suppressor struct { ID, Reason string }
```

Adapters (pure functions, one per producer, each ~20-40 lines, each with a golden test):
- `unified.UnifiedDedupScore` -> `MatchExplanation` (`internal/dedup/unified/explain.go`) — adds
  `Primary` from `isSupportingKind`, `Thresholds` from the cfg actually used, `Suppressors` from
  `PairEligibility` (see 5.4).
- `metafetch.ScoreBreakdown` -> (`internal/metafetch/explain.go`) — `Steps` populated; also emit
  identity steps with `Applied:false` (recorder change, ~15 lines in `score_breakdown.go:128-144`).
- `dedup.BookDupGroup` -> (`internal/dedup/book_dedup_explain.go`) — tier + the three hardcoded
  thresholds with `Source:"hardcoded:..."` until 5.5 retires them.
- `reconcile.ReconcileMatch`, `dedup.SeriesDupGroup`, `dedup.AuthorDedupGroup` (needs a
  `Reason`/JW value added to the group — `author.go` returns bool today; ~40 lines to thread the
  computed JW and the floor that admitted it), `dedup.SplitBookCandidate`,
  `itunesservice.RecommendationEvidence` (+ `recommendAction` reason), AcoustID compare result.

TS: `web/src/services/api.ts` gains `MatchExplanation` (~40 lines) and **deletes** the phantom
`skipped_reason`, adds `suppressors`, `primary`, and `layer:'acoustid'|'book_signature'`. The
existing `EvidencePanel` three-kind union stays; add one adapter `explanationEvidence(MatchExplanation)`
in `evidence/adapters.ts` (~60 lines) that picks `waterfall` when `steps` is non-empty, `confidence`
when `scale=="percent"` with components, `facts` otherwise. `signalLabels.ts:52-64` TS copy of the
supporting-kind rule becomes dead and is removed.

### 5.3 Tier 0 — defects to fix regardless of design (each is a one-PR item)

| Fix | Files | Est. |
|---|---|---|
| Make configured bands live: `getScoreConfig()` should return `LoadScoreConfig()` (cached, invalidated by `SetBandThresholds`) or `PostInit` should call `SetScoreConfig` | `internal/dedup/engine.go:288-300`, `internal/dedup/lifecycle.go:76`, test asserting a DB override changes a band | ~30 lines + test |
| Settings scale 0-100 | `web/src/components/settings/DedupSettingsSection.tsx:~195-202` | ~10 lines |
| Persist suppressors: either score suppressed pairs with `Suppressors` set (and a band-forcing rule) instead of deleting, or persist the reason on a tombstone. Owner decision (D3). | `internal/dedup/engine.go:914-921,948` | ~40 lines + test |
| Decide the `Layer=="exact"` protection vs unified writes (D2) | `internal/database/embedding_store.go:714-731` + test asserting unified score lands over an exact row | ~15 lines + test |
| Remove the dead `threshold` param or make it live | `internal/dedup/author.go:1159` + 7 callers | ~20 lines |
| Export the copied constants from one package | `engine.go:50,1680`, `dataset/rules.go:33,38`, `maintenance/dedup_triage.go:114` | ~10 lines (import-cycle check needed for maintenance) |
| Add `primary` to `models.Signal`; add `suppressors` to TS; drop `skipped_reason` | `internal/models/dedup_score.go:18-40`, `api.ts:5191-5206`, `adapters.ts` | ~15 lines |

### 5.4 Tier 1 — show what already exists (no Go change)

| Surface | Change | Est. |
|---|---|---|
| D7 Embedding, D8 Acoustic | `include_breakdown: true` at `DedupEmbeddingTab.tsx:246-252`, `DedupAcousticTab.tsx:579-583`; mount `EvidencePanel` in the expanded row; replace `maxSimilarity`/`simPct` with band + score when a breakdown exists | ~60 lines each |
| D11 Labels | add `score`, `score_breakdown` to `interface LabeledExample` `DedupLabels.tsx:57-69`; add a column + drawer | ~40 lines |
| R5 GroupedCard, R6 QueueRail, R7 MetadataSearchDialog | mount `EvidenceSection`/`EvidencePanel` with the breakdown already in hand | ~20 lines each |
| Band colours | one `bandColor` in `evidence/` used by `DupesSpine.tsx:100-105` and `EvidencePanel.tsx:97-108`; one `scoreColor` (`rowState.ts:120`) used by the four `CompareSpine` sites | ~20 lines |
| Stale copy | `ReviewWorkspace.tsx:347,354,361` | 3 lines |

### 5.5 Tier 2 — one matching path for book pairs (design work; owner gate)

- Retire `book_dedup.go` tiers as a scorer: express hash groups as `exact_file`, same-folder as the
  `folder_path` boost, title+author as `metadata_fuzzy`, transcription tiebreak as a new primary
  kind; Duplicate Scan then lists ComposeScore bands. `book_dedup.go` keeps only the grouping.
- Retire `upsertExactCandidate`'s `Similarity=1.0` writes: the Layer-1 checks should emit signals
  into the unified pass rather than candidates. This also removes S2 and S4 at the root.
- Add `SigBookSignature` so `BookSignatureScan` can be composed; give `AcoustIDScan` the same.
- Reconcile: keep as a repair tool but emit `MatchExplanation` with `Source:"hardcoded"` thresholds.
- Give `Similarity` one meaning (drop it, or define it as `Score/100` and migrate legacy rows via a
  backfill op — `breakdown_backfill.go` is the parallel sibling to copy).
Estimate: 4-6 PRs, ~800-1200 lines including tests; the `Similarity` migration is the risky one.

### 5.6 Payload and N+1 at 50-100 items

Measured shapes (from the census):
- Dupes lane: 50 rows/page (`useDupesLane.ts:26,314`), inline breakdown ~10 JSON fields per row
  (one or two signals typical), `include_books` adds two full `Book` objects per row and is the
  heavier half (`api.ts:5216-5219`). **Inline is right at this size.** `MatchExplanation` adds
  ~3 thresholds + `primary` flags: +~10 fields/row. Fine.
- Metadata lane: **fetches the whole library in one request** (`limit=0`, `useMetadataLane.ts:537`;
  5,774 rows in prod per the file's own comment `:389-392`), each with a 5-7-step waterfall
  (~32-44 fields). Filtering/paging is client-side (`:705-807`). The endpoint has a measured
  history of 21.7 s / 35.2 s timeouts (`metadata_cache.go:184-193`) and a dedicated
  `CACHED_REVIEW_TIMEOUT_MS` (`api.ts:3847`). This is the real "quick and responsive" problem, and
  it is not caused by the breakdown — it is caused by `limit=0`. Recommendation: server-side paging
  + filters (band/score/stale/author) on `/audiobooks/metadata/cache/review`, 50-100 per page,
  breakdown inline (it is what the page is for). Keep a lazy `/…/:id/breakdown` only for the
  drawer, mirroring `getDedupCandidateBreakdown`.
- N+1 in TS: `useMetadataLane.ts:1096` fans out one `markNoMatch` per group member — needs a batch
  endpoint. Server-side N+1s were already batched (`metadata_cache.go:184-193,266-280`).
- Rule of thumb for the envelope: inline `MatchExplanation` when the list is paged at <= 100 and
  each explanation is <= ~15 components; lazy per-row when either bound is exceeded (the regroup
  lane's 500-item fetch `useRegroupLane.ts:104` is the candidate for lazy).

---

## 6. What the owner must decide

- **D1 — Make configured bands live (S1)?** Yes/no, and whether to first re-run
  `dedup.calibrate-composite` since its historical "apply" never reached scoring. Rollback is the
  current literal 97/90/75/60.
- **D2 — Should the unified pass overwrite legacy `exact` rows (S2)?** Options: (a) allow overwrite
  when the incoming write carries a `FormulaVersion`; (b) one-shot backfill op that stamps
  breakdowns on protected rows; (c) leave as is and accept the two-presentation P1. (a)+(b) is the
  fix-it-right answer.
- **D3 — Persist suppressors or keep deleting suppressed pairs?** Persisting is what makes "why was
  this NOT a duplicate" answerable in the UI; deleting keeps the candidate table small.
- **D4 — Retire the `/dedup` Embedding and Acoustic candidate tables** (the `/review` dupes lane is
  the port) **or** wire `EvidencePanel` into them (Tier 1)? The `/dedup` page comment says pair
  candidates moved; the tabs still exist.
- **D5 — Fold `book_dedup.go` tiers and reconcile literals into ComposeScore signals (Tier 2), or
  keep them as separate tools with `MatchExplanation` envelopes only?**
- **D6 — Single meaning for `Similarity`**: migrate to `Score/100`, or drop the field from the API
  and derive from the breakdown.
- **D7 — LLM verdict vs band precedence (P6)**: which authority wins, and should the LLM window
  read `Band` instead of `Similarity`.
- **D8 — `primary`/thresholds on the wire** (accept the extra ~10 fields per row) — recommended yes.
- **D9 — Server-side paging for the metadata review list** (the actual responsiveness lever).
- **D10 — Authors: thread a JW score + reason into `AuthorDedupGroup`** so D3 can show why, and fix
  the dead `threshold` (make it live, or delete the parameter).

---

## 7. Memory cross-check (settled facts, not re-litigated)

Read: `project_composite_calibration_blocked_persistence_gap.md` (RESOLVED: calibrator now clears
0.90 via `dedup.rescore-labeled-examples` #1926/#1927; recommended bands 96/85.5 GATED-PENDING),
`project_dedup_calibration_precision_resolved_jul8.md`, `project_dedup_tuning.md`,
`project_review_queue_regroup.md`. Also relevant from MEMORY.md: `dedup.breakdown-backfill` (#1982)
populated pre-T015 candidates; not_dup gold labels were 100% rule-mined; `review_apply_enabled`
default OFF; candidate `Status` is a verdict never overwritten by rescan; `UnifiedDedupTab` retired
into `/review` (`eae9e2f70`). Nothing in memory records S1 (`SetScoreConfig` dead) or S2 (protected
exact rows block breakdowns); both are new. The breakdown-backfill op (#1982) writes via
`UpdateCandidateScore` (`breakdown_backfill.go:491`), which is *not* the protected upsert path — so
it may have stamped some legacy rows; that is consistent with memory and does not contradict S2 for
rows created since.

## 8. Coverage gaps found (tests that do not exist)

`collectPairSignals`, `CollectDuration`, `classifyStaleCandidate`, `metadataPairSimilarity`,
`transcriptionAgreement`, `layerNameForKind`, `resolvedBookThresholds`, `listAmbiguousCandidates`
(all `internal/dedup/`); `internal/plugins/dedup/llm_review.go` (no test file);
`internal/merge/collision.go` `QuickHash`/`CollisionCandidate`; no test asserts whether a unified
score lands over a protected `exact` row (S2); no test catches the dead `threshold` param (S6).

---

