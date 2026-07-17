<!-- file: docs/specs/fable5-spec-unified-dedup-pipeline.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b3d6f2a-4c8e-4d1b-a7f5-2e6c8a0b4d9f -->

# SPEC 1: Unified Identification & Deduplication Pipeline

Goal: one composite, explainable "is this a duplicate?" answer per candidate pair, at
~98% correct identification, replacing five siloed surfaces. This spec is grounded in a
code-level audit of the current pipeline; **several cheat-sheet assumptions were wrong and
are corrected below** — the design accounts for the code as it actually is.

## 1. Current State Analysis (verified against code)

Signals and their real state:

| Signal | Reality | Citation |
|---|---|---|
| File hash exact | Working. `Engine.checkExactFileHash`, similarity hardcoded 1.0, layer `"exact"` | `internal/dedup/engine.go:286-358` |
| Metadata source hash | Working but **exact-match only** (same external record applied), sim 0.99, layer `"metadata_hash"`. There is **no fuzzy title matcher** in the engine | `engine.go:419-449` |
| Folder/path duplicates | **Stub — returns nil**. `GetFolderDuplicates` and `GetDuplicateBooksByMetadata(0.85)` do not exist in PebbleStore beyond stubs | `internal/database/pebble_store.go:1826-1834` |
| Embedding cosine | Working; chromem in-RAM ANN hydrated from **PebbleDB** (`emb:v:*` — NOT SQLite); thresholds book 0.85/0.95, author 0.80/0.92; layer `"embedding"` | `engine.go:107-110,812-928`; `internal/database/embedding_store.go` |
| AcoustID tier-1 exact | Working, O(1) via `book_file_acoustid:` index | `engine.go:44-45` |
| AcoustID tier-2 fuzzy | Disabled (`ACOUSTID_FUZZY_ENABLED=1`), O(N) too slow even via memdb (PR #1194) | `engine.go:30-46` |
| LSH | `Subprints()` implemented (`LSHBandCount=64`, `LSHSubprintBytes=8`, `LSHMinBandHits=2`, `minFramesForLSH=480`); **`fpidx:` Pebble index not built; nothing wired** | `internal/fingerprint/lsh.go:27-49,69-147` |
| Duration | Implemented as a *gate to layer-exact* (±2% + Levenshtein≤6 title → sim 1.0 + `dedup:duration-match` tag), not a composable signal | `engine.go:543,578-691` |
| LLM review | Working; verdict/reason stored on candidate; layer precedence `exact > llm > embedding` on upsert | `embedding_store.go:455-464,591-598` |
| Candidate store | `DedupCandidate` in PebbleDB: `dedup:r:<id16>`, pair index `dedup:p:<type>:<aID>:<bID>`, `dedup:seq` counter. `Similarity *float64`, no clamp, no breakdown, no provenance | `embedding_store.go:79-91,351-374` |
| UI | 9 tabs in `BookDedup.tsx` (Book/Embedding/AI Review/Acoustic/Advanced Scan/Author/Series/Reconcile/Split); `FingerprintVisualsColumn.tsx` reads legacy seg0–6 for display only, not wired to dedup | `web/src/pages/BookDedup.tsx`; agent audit |
| Known data issue | ~14K stale 100% candidates from pre-whole-file fingerprints; no provenance/invalidation path (HIGH-5 in findings) | `engine.go:1521-1649` |

Negative-evidence (guards, not bonuses) that already exist and must be preserved: same
version-group drop, distinct-series-volume drop, same-parent-directory drop
(`engine.go:886-914,453-536`). There is **no Audible bonus or composite scoring anywhere**
— the "bonus stacking >100%" of project lore exists only in scattered intent, not code.

## 2. Architecture Design

One new package `internal/dedup/unified/` consumed by the existing `Engine`:

```
                    ┌────────────────────────────┐
 signal collectors  │  SignalSet (per pair)      │   scorer            persistence
 ─ exact hash    ──▶│  []Signal{Kind, Raw,       │──▶ ComposeScore() ──▶ DedupCandidate
 ─ acoustid exact──▶│    Confidence, Evidence}   │       │              + ScoreBreakdown
 ─ acoustid LSH  ──▶│                            │       ▼              (new JSON field)
 ─ isbn/asin     ──▶│  collected lazily, cached  │   UnifiedDedupScore
 ─ embedding     ──▶│  per scan run              │
 ─ metadata fuzzy──▶│                            │
 ─ duration      ──▶│  (supporting only)         │
 ─ folder path   ──▶│  (supporting only)         │
                    └────────────────────────────┘
```

- Collectors are pure: given a pair (or a probe book), emit zero or more `Signal`s.
  Existing engine checks are refactored *into* collectors; their guard logic (version
  group, series volume, same-dir) becomes a shared `PairEligibility` pre-filter that runs
  before any collector.
- The scorer is deterministic and synchronous — no I/O — so it is trivially unit-testable
  and re-runnable over stored signal sets (audit requirement).
- LLM review stays a *post-scorer* refinement for the REVIEW band only (unchanged ops).

### Scan operation rationalization (current 7 ops → 4 phases)

| Phase | Op(s) | Schedule |
|---|---|---|
| 1. Index build | `embed-scan`/`embed-async` (embeddings), fingerprint rescan (acoustid plugin), **new: lsh-index-build** | automatic, nightly + on-import incremental |
| 2. Candidate generation | `full-scan` (renamed semantics: run all collectors, write unified scores), `book-signature-scan`, `split-book-scan` | automatic nightly; on-demand from UI |
| 3. Refinement | `llm-review` (REVIEW band only) | on-demand / budgeted batch |
| 4. Hygiene | `purge-stale` (extended with provenance purge, HIGH-5) | automatic, before each phase-2 run |

`embed-scan` vs `embed-async` should collapse to one op with an `async` flag (they differ
only in execution mode — agent audit). Recommended scan order is the phase order above;
phase 2 depends on phase 1 indices and must not start while a phase-1 op holds the same
concurrency key.

## 3. Data Model

```go
// internal/dedup/unified/score.go

type SignalKind string
const (
    SigExactFile     SignalKind = "exact_file"      // certainty 1.00
    SigExactAcoustID SignalKind = "exact_acoustid"  // 0.99
    SigISBNASIN      SignalKind = "isbn_asin"       // 0.98
    SigLSHAcoustID   SignalKind = "lsh_acoustid"    // 0.90–0.97 (hamming-scaled)
    SigEmbedHigh     SignalKind = "embedding_high"  // 0.88–0.95 (cos ≥ .95)
    SigMetaSrcHash   SignalKind = "metadata_hash"   // 0.97 (same external record)
    SigMetaFuzzy     SignalKind = "metadata_fuzzy"  // 0.70–0.85 (NEW collector)
    SigEmbedMedium   SignalKind = "embedding_med"   // 0.65–0.80 (.85 ≤ cos < .95)
    SigDuration      SignalKind = "duration"        // supporting only
    SigFolderPath    SignalKind = "folder_path"     // supporting only
)

type Signal struct {
    Kind       SignalKind `json:"kind"`
    Raw        float64    `json:"raw"`        // e.g. cosine 0.961, hamming 0.93, |Δdur| 0.4%
    Confidence float64    `json:"confidence"` // calibrated per-kind, 0..1
    Evidence   string     `json:"evidence"`   // human-readable: "whole-file hash 9af3… both sides"
    FPVersion  string     `json:"fp_version,omitempty"` // provenance (e.g. "wholefile-v1")
}

type UnifiedDedupScore struct {
    Pair        [2]string `json:"pair"`        // canonical aID < bID
    Score       float64   `json:"score"`       // 0..100, capped (see §7)
    Band        string    `json:"band"`        // CERTAIN | HIGH | MEDIUM | REVIEW
    Signals     []Signal  `json:"signals"`     // full breakdown, always persisted
    Suppressors []string  `json:"suppressors"` // fired negative guards, e.g. "series_volume_differs"
    Formula     string    `json:"formula"`     // version tag, e.g. "noisy-or-v1" — re-score detection
    ComputedAt  time.Time `json:"computed_at"`
}
```

`DedupCandidate` gains: `ScoreBreakdown *UnifiedDedupScore` (JSON), `Band string`,
`FormulaVersion string`. `Layer` and `Similarity` remain for backward compatibility
(Similarity = Score/100). Keys unchanged (`dedup:r:` / `dedup:p:`) — no key migration.

## 4. Scoring formula (decision: **normalize to 0–100, cap at 100**)

Rationale for choosing capped over >100% stacking: every consumer (band thresholds, UI
bars, sorting, the 98%-accuracy target itself) wants a bounded scale; bonus semantics are
preserved *inside* the breakdown where they are explainable. The lore that >100% is
load-bearing found no support in code — nothing today produces >1.0 except by accident.

Noisy-OR over independent primary signals, then bounded supporting boosts, then
suppressor multipliers:

```
P_dup = 1 - Π over primary signals s (1 - Confidence(s))
score = 100 * P_dup
for each supporting signal (duration ±2%, folder_path):
    score += boost(kind)            # duration: +4, folder: +3 (config)
    # supporting signals NEVER create a candidate alone (standalone use forbidden)
for each suppressor (series-volume differs, version-group same, same-dir multi-file):
    candidate dropped entirely (existing behavior preserved — not a score penalty)
score = min(score, 100)
band:  CERTAIN ≥ 97 → auto-merge eligible
       HIGH    90–96.99 → suggest-merge
       MEDIUM  75–89.99 → review queue
       REVIEW  60–74.99 → LLM phase / manual
       below 60 → not persisted
```

Per-kind confidence calibration lives in `config.yaml` under `dedup.signals.<kind>`
(`base`, `scale`, thresholds) — nothing hardcoded; defaults from §3 table. Noisy-OR makes
multiple independent mid-strength signals compound (embedding 0.90 + metadata fuzzy 0.80 →
0.98 — exactly the brief's "high in combination" requirement for weak signals) while one
strong signal suffices alone. The 98% target is then a calibration exercise: the breakdown
+ formula version stored per candidate lets us re-score the entire corpus offline when
constants change (no re-collection), and measure accuracy against the merged/dismissed
history already in the candidate store.

Worked examples:
- exact_file only: 1−(1−1.0) → 100 → CERTAIN.
- embedding 0.93 cos (conf 0.78) + duration match: 78 + 4 = 82 → MEDIUM.
- lsh_acoustid hamming 0.95 (conf 0.94) + metadata_fuzzy 0.80 (conf 0.78): 1−(0.06·0.22)=0.9868 → 98.7 → CERTAIN.

## 5. LSH implementation (the missing Step 3)

PebbleDB secondary index, written at fingerprint-store time:

```
key   fpidx:<subprint-hex16>:<bookfile_id>     (subprint = 8-byte band sample → 16 hex chars)
value <book_id> (string)                        — avoids a BookFile read on probe
```

- **Build**: new plugin op `dedup.lsh-index-build` iterates BookFiles with whole-file
  fingerprints, calls `fingerprint.Subprints(raw)` (exists), writes ≤64 keys per file in
  batches of 1,000 with progress reporting. Versioned completion flag
  `lsh_index_v1_done` (PebbleDB k:v, matching the established backfill-flag pattern).
- **Update**: hook the BookFile fingerprint write path (single chokepoint in PebbleStore
  `UpdateBookFile`/fingerprint setter): delete old `fpidx:` keys for the file (old
  subprints recomputed from previous fingerprint or tracked via a per-file
  `fpidx_member:<bookfile_id>` → list key), write new ones, same batch.
- **Delete**: BookFile delete path removes its `fpidx:` keys via the member list.
- **Probe** (collector `SigLSHAcoustID`): compute probe subprints → 64 point lookups →
  count band hits per candidate file → candidates with ≥`LSHMinBandHits` (2) proceed to
  full `WholeFileSimilarity` Hamming comparison (existing function) → emit signal with
  Raw = hamming similarity, Confidence scaled 0.90–0.97 over hamming 0.85–1.0.
  Cost: O(64 point-reads + small candidate set) vs today's disabled O(N) scan.
- Replaces `ACOUSTID_FUZZY_ENABLED` O(N) path; env var retired (default off → removed).
- Estimated index size: ~308K files × 64 keys × ~60B ≈ 1.2GB max — acceptable; if not,
  band count can drop to 32 via config (constant already centralized in `lsh.go`).

## 6. API changes

Extend, don't fork (the handlers live in `internal/server/handlers/dedup/`):

- `GET /api/v1/dedup/candidates` — response items gain `band`, `score`,
  `score_breakdown` (omitted unless `?include_breakdown=1`), `formula_version`. Existing
  filters keep working; add `band=` filter.
- `GET /api/v1/dedup/candidates/:id/breakdown` — full `UnifiedDedupScore` + both books'
  comparison payload (covers, metadata, file info, audio-sample URLs) for the side-by-side
  panel. One round-trip for the detail view.
- `POST /api/v1/dedup/scan` — unchanged trigger; op now runs the unified phase-2 pipeline.
- `POST /api/v1/dedup/lsh-index` — triggers `dedup.lsh-index-build`.
- `POST /api/v1/dedup/rescore` — re-runs ComposeScore over stored signal sets (no
  collection) after config changes; returns counts per band delta.
- Deprecate (UI stops calling; endpoints stay one release): `/dedup/scan-acoustid`'s
  separate surface, `/audiobooks/:id/compare-acoustid` remains for the BookDetail column.

## 7. UI design

Replace the Books/Advanced-Scan/Acoustic triplet with one **Unified tab** (Author/Series/
Reconcile/Split/AI-Review tabs unchanged):

```
web/src/components/dedup/
  UnifiedDedupTab.tsx          — table: pair, Score chip (0–100 + band color), badge row
    ├── ScoreBadgeRow.tsx      — "Hash ✓" "AcoustID ✓" "ISBN ✓" "Embed 94%" "LSH 96%" chips
    ├── BandFilterBar.tsx      — CERTAIN/HIGH/MEDIUM/REVIEW counts (from /dedup/stats) + filters
    ├── CandidateCompareDrawer.tsx — side-by-side: cover art, metadata diff table
    │     ├── ScoreBreakdownPanel.tsx — each signal: kind, raw, confidence, evidence line,
    │     │                              contribution to noisy-OR (rendered as stacked bar)
    │     ├── FileInfoCompare.tsx     — formats, sizes, durations (±% highlighted)
    │     └── AudioSamplePair.tsx     — existing audio preview endpoints
    └── BulkActionBar.tsx      — per band: auto-merge (CERTAIN) / suggest (HIGH) /
                                  review (MEDIUM) / skip — wraps existing merge/dismiss/cluster APIs
```

Action mapping: CERTAIN → "Auto-merge all" (existing bulk-merge endpoint, scoped
`band=CERTAIN`); HIGH → per-row one-click merge; MEDIUM → drawer-first; REVIEW → "Send to
LLM review". `FingerprintVisualsColumn` in BookDetail gains a "compare in Dedup" link that
deep-links to the unified tab filtered to that book — closing the "fingerprints not
connected to dedup" gap.

## 8. Migration plan

1. **Schema-additive only** — new fields on `DedupCandidate` JSON; old rows lack
   `Band`/`ScoreBreakdown` and are treated as `formula_version=""`.
2. **Provenance purge (fixes the 14K false positives, HIGH-5):** migration op
   `dedup.purge-legacy-fp-candidates`: delete/mark-stale every candidate with
   `Layer ∈ {exact, embedding}` whose similarity came from acoustid segments
   (identifiable: created before whole-file cutover date — stored `CreatedAt` — AND
   sim == 1.0 AND no file-hash match recomputable today). Each examined candidate gets
   re-scored by phase-2 collectors before deletion is final (no data loss: dismissed rows
   are marked, not erased). Versioned flag `dedup_fp_purge_v1_done`.
3. **Backfill scores:** phase-2 `full-scan` run populates `Band`/`ScoreBreakdown` for all
   surviving pending candidates (re-collection, not translation — old `Similarity` is not
   trusted as input).
4. **Rollback:** new fields are ignorable by old code; the UI tab ships behind a
   feature flag (`web` config) defaulting on only after backfill op reports complete —
   satisfying the repo rule "verify data backfill before enabling flags in production".
