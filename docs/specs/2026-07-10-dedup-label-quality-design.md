<!-- file: docs/specs/2026-07-10-dedup-label-quality-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 567c170c-43c5-4d3b-8074-e2cf4afedae0 -->
<!-- last-edited: 2026-07-10 -->

# Dedup Label-Quality & Training/Refinement Loop — Design Spec

**Status:** Draft
**Scope:** Go backend (`internal/dedup/dataset`, `internal/database/dedup_label.go`, `internal/database/duration_sanity.go`, `internal/plugins/dedup`, `internal/scheduler`, `internal/config`, `internal/server/handlers/dedup`) + one React page (`web/src/pages/DedupLabels.tsx`). One deferred `internal/dedup/engine.go` constant touch (INIT-2 owns that file structurally).
**Parent task:** INIT-1 (`.claude/notes/2026-07-10-remaining-work-master-plan.md` §INIT-1)

**Gate (verbatim, applies to every task in this package):** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval.

---

## Motivation

The 2026-07-08 calibration run (`.claude/notes/2026-07-08-dedup-calibration-findings.md`, op `01KX13KD3MDT63JBKM5044XZPV`) proved the dedup precision floor is unreachable — best precision **0.582 @ cosine 0.98** against a 0.90 low target — and root-caused it to **contaminated `not_dup` gold labels**, not thresholds or the bge-m3 model:

- **100% of `not_dup` labels are `source=rule`; zero are human.** No operator decision is contradicted by fixing the mining.
- The op's highest-cosine `not_dup` diagnostic pairs were hand-verified against live prod: **all real duplicates mislabeled `not_dup`** — Way of the Wolf ×3 (ASIN B002V8MAAM, same file path, durations 21,171 vs **20,810,840** — a 1000× ms/sec unit mismatch), Foundation and Empire (B003FCV4O6, 34,535 vs **57,989,869**), Alcatraz vs the Evil Librarians (B005GGGC3M, same path).
- Two mining rules produce the contamination (`internal/dedup/dataset/rules.go`):
  `partVsWhole` (rules.go:106, `const partVsWholeRatioMax = 0.5` at rules.go:17) misfires on ms/sec-corrupted durations (ratio ≈ 0.001 → "part vs whole"); `missingFile` (rules.go:62) labels `not_dup` purely because files are unresolvable — absence of files ≠ not a duplicate.
- The label store also has **~2.7× row duplication** (6,926 labeled rows → 2,564 unique book-pairs) because rows key on `candidateID` (`internal/database/dedup_label.go:16-17`), and one book-pair yields multiple candidates across layers — calibration double-counts.
- Separately, ~47% of `true_dup` pairs score below cosine 0.98 — a genuine recall tail that a single embedding threshold can never fix; the **noisy-OR composite** (`internal/dedup/unified/compose.go:47`, `FormulaVersion="noisy-or-v1"` at compose.go:12) is the right surface to calibrate, and today no op tunes it — `dedup.calibrate-embedding-thresholds` sweeps only a single cosine cut-point (`internal/plugins/dedup/calibrate_embedding_thresholds.go:64-67`).

**Goal:** fix the gold labels at the mining source, dedupe them per book-pair, grow human label volume cheaply, calibrate the composite scorer on the clean set, and wire a (disabled-by-default) scheduled refinement loop — so the dedup models improve as the app is used.

## Goals

- `not_dup` rule labels can no longer be emitted for pairs sharing ASIN / version-group / identical file path (T1).
- "No resolvable files" downgrades to `unsure` instead of `not_dup` (T1).
- Duration units are normalized at the mining boundary, PER FILE before summing (T2). The write boundary is NOT new work: the store chokepoints already repair+warn ms-scale durations (shipped CONS-18, `normalizeBookFileDuration`) — T2 verifies that by grep/test and must not weaken or duplicate it.
- One label per book-pair reaches calibration and export — no double-counting (T3).
- Suspicious rule labels (high-evidence `not_dup`) surface in the Gold Labels UI for one-click human override, reusing the existing `OverrideDedupLabel` endpoint (T4).
- A calibration op tunes the noisy-OR composite (signal confidences + band thresholds in `internal/dedup/unified/config.go`), read-only report + gated apply (T5).
- A scheduled refinement chain (re-mine dry-run → composite calibration report → drift log) exists, built-in-DISABLED (T6).
- After T1–T3 land: prod re-mine + recalibration confirms the precision floor is reachable (T7, operator-driven).
- Engine/dataset part-vs-whole ratio constants align (0.6 vs 0.5 mismatch, `internal/dedup/engine.go:1528` / const at engine.go:107) — deferred behind INIT-2's engine.go waves (T8).

## Non-goals (v1)

- Re-architecting human capture — `recordHumanLabel` (`internal/server/handlers/dedup/label_capture.go:71`) and its call sites are REUSED as-is. — deferred (nothing needed).
- Rebuilding the composite scorer — `ComposeScore` is reused; only its config values become calibratable. — deferred.
- LLM-as-judge auto-labeling (dataset design spec #3) — deferred.
- Fine-tuning a model on the labels (dataset design spec #4) — deferred until labels accrue post-T7.
- The full Workflow System (INIT-6 WF-2..6) — T6 stays a minimal `ScheduledTaskConfig` entry that WF-3 will later subsume.
- Destructive label-store schema migration — pair-dedup happens at read/consumption time; `dedup:label:<candidateID>` keyspace is unchanged.
- Quantifying total contamination across the whole `not_dup` set (blocked on missing ASIN for file-unresolvable pairs) — T7 measures the outcome instead.

## Decisions (locked during design)

1. **Fix labels at the SOURCE** (mining rules + duration normalization), not by tuning thresholds — the Jul-8 findings' stop-branch conclusion. (Losing alternative: keep sweeping cosine thresholds on the contaminated set — the 0.582 @ cos 0.98 floor is a symptom of the labels, not the model.) **Caveat on that 0.582 figure:** it was measured on the CONTAMINATED label set and does not survive this package's own fixes — after the T7 re-mine the existing single-cosine op may well reach its target, so 0.582 is NOT a justification for building T5. T5 stands on the independent leg alone: the ~47% true_dup recall tail below cosine 0.98, which re-mining does not fix (no single cosine cut can serve the high-confidence tier). T7 deliberately re-runs the single-cosine op first (runbook step 4) so the composite's added value is measured against the clean-set baseline, not asserted from stale numbers.
2. **REUSE human capture + composite scorer.** No new label-capture path; T4's one-click override calls the existing `POST /dedup/labels/:id/override` route (`internal/server/wire_dedup_routes.go:44` → `OverrideDedupLabel` at `internal/server/handlers/dedup/label_review.go:103`, stamps `label_source=human` at label_review.go:138). (Losing alternative: a new review endpoint — duplicate surface.)
3. **Pair-dedup at consumption, not in the store.** A pure helper in `internal/dedup/dataset` collapses rows to one per canonical pair (sorted `EntityAID`/`EntityBID`), preference `human > llm_judge > itunes_attr > rule`, tie-break latest `DecidedAt` then highest `CandidateID`. Calibration and export call it. (Losing alternative: re-key the PebbleDB keyspace — destructive, breaks `rebuild-gold-labels` passthrough of human rows at `internal/plugins/dedup/rebuild_gold_labels.go:214-215`.)
4. **`missingFile` → `unsure` unconditionally.** File absence is evidence-free for dup-ness. (Losing alternative: keep `not_dup` with guards — still wrong ground truth.)
5. **`partVsWhole` guarded, not removed.** A real part-vs-whole pair with sane durations IS `not_dup`; only shared-ASIN / shared-version-group / identical-path pairs are exempted (→ `unsure`), and durations are unit-normalized first (T2). (Losing alternative: drop the rule — loses the only cheap negative miner.)
6. **Duration normalization reuses the shipped heuristic where it ALREADY lives:** `database.DurationLooksLikeMillis` — exported, in `internal/database/duration_sanity.go:36` (CONS-18); `duration_backfill.go:33` is only a thin wrapper delegating to it. NO new package, NO "move" — never a second parallel heuristic with its own constants. **The write boundary is already repaired, not warn-only:** `normalizeBookFileDuration` (`duration_sanity.go:61`) `/1000`-repairs AND `slog.Warn`s at all three BookFile write chokepoints (`pebble_store_bookfiles.go:192`/`:785`/`:849`, shipped CONS-18), and the heuristic double-checks that the corrected value lands inside the 4 kbps–3 Mbps plausibility band before flagging (duration_sanity.go:46-52), so the genuine-low-bitrate edge case is already handled. T2 therefore makes NO store-write edits: it must not weaken the shipped repair to warn-only (a regression) and must not add a redundant warn beside it — T2 VERIFIES the chokepoints by grep + existing tests. T2's only genuinely-new work is READ-time normalization of historical per-file durations in the dataset builder (C2), because rows written before CONS-18 still hold ms-scale values that the mining boundary consumes.
7. **Composite calibration is coordinate-wise sweep, not full grid** — sweep band thresholds and per-kind confidence bounds one dimension at a time over the pair-deduped gold set; full grid over `Signals × bands` is combinatorial. Refuses to recommend when scored-pair coverage is below a floor (fail-closed).
8. **The scheduled loop NEVER applies on a schedule.** T6 runs dry-run/report steps only; every apply remains an operator AskUserQuestion decision per the gate. Built-in-disabled (`viper` default `enabled=false`), aligned with — and awaiting — INIT-6 WF-3.
9. **File-ownership partition with INIT-2 (verbatim):** INIT-2 OWNS all structural edits to `internal/dedup/engine.go`. INIT-1's single engine.go touch (align `isPartVsWholeMismatch` ratio 0.6 at engine.go:1528/const :107 with the dataset rule's 0.5) lands AFTER INIT-2's engine.go waves merge, rebased on top — never a concurrent wave on engine.go. `ListLabeledExamples` is implemented in `internal/database/dedup_label.go:139` (method on `EmbeddingStore`) — NOT in `embedding_store.go` — so INIT-1 T3 does NOT collide with INIT-2's embedding_store.go index work and needs no serialization. **This CORRECTS the master-plan §INIT-1 locked-scope premise** (which placed the `ListLabeledExamples` dedup in `embedding_store.go` and serialized it behind INIT-2) — the verified anchor shows that premise was factually wrong; surface this so the owner amends the master plan. Residual checks: INIT-2's touched set (engine.go, embedding_store.go index work, `pebble_store.go` stub getters, collectors/handler files) does NOT include `internal/database/dedup_label.go`, so T1's `BookFeatures` edit there is collision-free; INIT-2 also does not touch `internal/plugins/dedup/plugin.go`, and T5's op registration there is a single append-style def-list line, trivially rebase-resolved even if that changes.

## Data model

```go
// internal/database/dedup_label.go — BookFeatures gains two identity fields
// (T1). Snapshotted at capture time by the dataset builder; empty string means
// "unknown", which is NON-DISQUALIFYING for every rule that reads them.
type BookFeatures struct {
	// ... existing fields unchanged (Title, Author, PrimaryPath,
	// TotalDurationSec, FileCount, HasCover, FilesExist, RecordingIDs,
	// ITunesPIDPresent, WholeBookSigPresent, FileSizeBytes) ...

	// ASIN is the book's Amazon/Audible ID ("" when the book has none).
	// Source: database.Book.ASIN (*string, nil → "").
	ASIN string `json:"asin,omitempty"`
	// VersionGroupID links books that are versions of the same work
	// ("" when ungrouped). Source: database.Book.VersionGroupID (*string).
	VersionGroupID string `json:"version_group_id,omitempty"`
}
```

```go
// internal/dedup/dataset/pair_dedupe.go (NEW, T3) — pure, no DB access.
// PairKey returns the canonical sorted pair identity for a labeled example.
func PairKey(ex database.LabeledExample) string

// DedupeByPair collapses examples to ONE per canonical book-pair.
// Preference: human > llm_judge > itunes_attr > rule; ties broken by latest
// DecidedAt (RFC3339 string compare), then highest CandidateID.
// Unlabeled rows (Label == "") never displace labeled rows for the same pair.
func DedupeByPair(examples []database.LabeledExample) []database.LabeledExample
```

```go
// internal/database/duration_sanity.go (T2) — ONE new exported helper beside
// the already-shipped, already-exported DurationLooksLikeMillis (:36).
// NO new package, no move: the heuristic's single home stays duration_sanity.go.
//
// NormalizeDurationSec returns durationSec/1000 when DurationLooksLikeMillis
// fires, else durationSec unchanged. fileSizeBytes <= 0 or durationSec <= 0 →
// returned unchanged (unknown ≠ corrupt). Pure read-time twin of the store's
// normalizeBookFileDuration write-chokepoint repair (:61), which stays untouched.
func NormalizeDurationSec(fileSizeBytes int64, durationSec int) int
```

### Persistence

- `dedup:label:<candidateID 16-hex>` → `LabeledExample` JSON — **unchanged keyspace** (dedup_label.go:16-17); only the embedded `BookFeatures` gains the two optional fields (old rows unmarshal with `""`).
- Config keys (T5 apply / T6): `dedup.signals.*` (existing `ScoreConfig` mapstructure surface in `internal/dedup/unified/config.go`), new `scheduled.label_refinement.{enabled,interval,on_startup}` (defaults: `false`, `10080`, `false`).

## Components

### C1. Mining-rule guards (`internal/dedup/dataset/rules.go`, T1)

`missingFile` (rules.go:62 — verify with the anchor grep; rules.go:106 is `partVsWhole`) returns `("unsure", "side X has no resolvable files", true)` instead of `not_dup`. `partVsWhole` (rules.go:106) keeps `partVsWholeRatioMax = 0.5` (rules.go:17) but first checks a new exported `SharesIdentity(ex)` helper (exported because C4's suspicious-label predicate reuses it — one implementation, two consumers): `A.ASIN != "" && A.ASIN == B.ASIN`, or `A.VersionGroupID != "" && ==`, or identical cleaned `PrimaryPath` — any hit → `("unsure", "duration ratio X but pair shares <identity> — suspected unit corruption", true)`. Empty identity fields on either side are non-disqualifying: the rule proceeds to the normal ratio check. **Fail-open toward `unsure`** — a wrong `unsure` costs a review; a wrong `not_dup` poisons calibration.

### C2. Duration normalization at the mining boundary (`internal/database/duration_sanity.go`, `internal/dedup/dataset/builder.go`, T2)

`buildFeatures` (builder.go:129 — called from `BuildExample` at builder.go:72) SUMS per-file durations into `TotalDurationSec` (builder.go:169; the `fl.Duration` int-seconds fallback branch — fpcalc-measured `AcoustIDFingerprintDurationSec` is trusted as-is). Normalization is applied **PER FILE, before summing** — `DurationLooksLikeMillis` is a per-file implied-bitrate test (this file's size vs this file's duration), so applying it to the summed aggregate against a book-level size is wrong: a book with one ms-corrupted file among clean seconds-files produces a muddied sum the bitrate test can under- or over-correct. T2 adds `database.NormalizeDurationSec` (data model above) and calls it on each `fl.Duration` (with that file's `fl.FileSize`) inside the summing loop, so `durationRatio` (builder.go:174) never sees a 1000× mismatch. A fixture with a MIXED clean+corrupted file set is mandatory (per-file, not aggregate, behavior proven). The BookFile write chokepoints need NO new code — `normalizeBookFileDuration` already repairs+warns there (Decision 6; `pebble_store_bookfiles.go:192`/`:785`/`:849`): T2 verifies by grep and keeps existing store tests green, touching neither `pebble_store_bookfiles.go` nor `pebble_store.go` (INIT-2's stub-getter file). Upstream iTunes importer already converts ms→sec via `trackDurationSeconds` (`internal/itunes/service/importer.go`, CONS-16, PR #1523/#1524) — likewise verify-by-grep, don't re-fix.

### C3. Pair-dedup helper (`internal/dedup/dataset/pair_dedupe.go`, T3)

As in the data model. Consumers: `collectCalibrationPairs` (`internal/plugins/dedup/calibrate_embedding_thresholds.go`) and `ExportLabeledExamples` (`internal/server/handlers/dedup/label_review.go:162`, list call at label_review.go:177). Both report `rows_in` / `pairs_out` so the ~2.7× collapse is observable.

### C4. Suspicious-label review queue (`internal/server/handlers/dedup/label_review.go`, `web/src/pages/DedupLabels.tsx`, T4)

New handler `ListSuspiciousDedupLabels` (route `GET /dedup/labels/suspicious`, wired next to wire_dedup_routes.go:44's override route). Suspicion predicate over `ListLabeledExamples` output (in-handler Go filter; the set is ~7k rows):
`Label=="not_dup" && LabelSource=="rule" &&` any of: (a) `dataset.SharesIdentity(ex)` — the SAME exported helper T1's `partVsWhole` guard uses (same ASIN / version-group / identical path); REUSE it, do not re-implement identity checks in the handler; (b) `Band ∈ {CERTAIN, HIGH}`; (c) `Similarity != nil && *Similarity >= 0.95`; (d) identical Title with `0 < DurationRatio < 0.01` (the ms/sec signature). NOTE: arm (a) is TRANSITIONAL — after T7 re-mines with T1's guard in place, identity-sharing pairs are emitted as `unsure`, not rule `not_dup`, so (a) only surfaces the historical pre-re-mine backlog and then goes quiet; the queue's durable post-re-mine value comes from (b)/(c)/(d). UI: a "Suspicious" tab on the existing Gold Labels page with a one-click "Mark true_dup" / "Mark not_dup (confirm)" that POSTs the existing override route — the cheap human-volume lever.

### C5. Composite calibration op (`internal/plugins/dedup/calibrate_composite.go` NEW, T5)

Op ID `dedup.calibrate-composite`, `sdk.OperationDef` mirroring `calibrateEmbeddingThresholdsDef` (`internal/plugins/dedup/calibrate_embedding_thresholds.go:109`-area; registered in `internal/plugins/dedup/plugin.go` beside it). Loads labeled examples → `dataset.DedupeByPair` → parses each pair's `ScoreBreakdown` signals → replays `unified.ComposeScore` (compose.go:47) under candidate `ScoreConfig` variants. Coordinate-wise sweep: band thresholds (`BandCertainMin`/`BandHighMin`/`BandMediumMin`/`BandReviewMin`) then per-kind `MinConfidence`/`MaxConfidence`. Targets: precision ≥ 0.98 for CERTAIN, ≥ 0.90 for HIGH (params-overridable). **Fail-closed:** rows lacking `ScoreBreakdown` are skipped + counted; if scored coverage `< min_scored_pairs` (default 500 per class) → report `insufficient-coverage`, recommend nothing. Sweep work sharded with `errgroup.Group` + `SetLimit(runtime.NumCPU())` over the outer variant loop (CLAUDE.md concurrency mandate). Dry-run default; `{"apply":true}` writes recommended `dedup.signals.*` values via the config update service — apply is operator-gated per the package gate. **Apply persistence (pinned):** `internal/config/update_service.go` `UpdateConfig` persists via `SaveConfigToDatabase` (`internal/config/persistence.go:1409`) → the `config_blob` row in the PebbleDB settings store, reloaded at startup by `LoadConfigFromDatabase` before file values fill gaps — so an applied value SURVIVES `make deploy`/restart and is not silently reverted by a redeployed config file. There is NO feature flag: rollback = a second operator-gated config-apply restoring the previous values echoed in the op report. An applied config affects FUTURE scoring only — already-emitted candidates keep their stored scores/bands until re-scored (e.g. by `dedup.full-scan`); no retroactive re-banding happens on apply.

### C6. Scheduled refinement loop (`internal/scheduler/tasks.go`, `internal/config/config.go`, T6)

New `ScheduledTasksConfig.LabelRefinement ScheduledTaskConfig` + viper defaults (`scheduled.label_refinement.enabled` **false**). One `TaskDefinition` registered in `registerAllTasks` (mirror the `dedup_refresh` entry's `IsEnabled`/`Interval`/`RunOnStart` closure shape): chain `dedup.rebuild-gold-labels` (dry-run) → `dedup.calibrate-composite` (dry-run) → log a drift summary (label-bucket deltas + recommended-vs-current config diff). No apply parameter exists on the scheduled path at all. Minimal by design — INIT-6 WF-3 will subsume these keys into its `Workflow` object.

### C7. Engine ratio alignment (`internal/dedup/engine.go`, T8 — deferred wave)

`partVsWholeDurationRatioMax` 0.6 → 0.5 so the engine veto (`isPartVsWholeMismatch`, engine.go:1528) agrees with `dataset/rules.go`'s `partVsWholeRatioMax` (rules.go:17). Narrows the veto slightly (fewer pairs suppressed). Lands only after INIT-2's engine.go waves merge, rebased on top (Decision 9).

## Migration / integration

- Old `LabeledExample` JSON rows unmarshal cleanly (new fields `omitempty`, zero-value `""`). No backfill of the new fields is required for T1's guards to be safe: empty identity → guard falls through, same behavior as today.
- After T1+T2 merge, historical rule labels are stale by construction — that is exactly what `dedup.rebuild-gold-labels` re-derives (human rows preserved via the `case "human"` passthrough at rebuild_gold_labels.go:214-215). T7 runs it on prod: dry-run → AskUserQuestion → apply.
- `calibrate-embedding-thresholds` keeps its current single-cosine behavior; it just consumes pair-deduped input after T3. The composite op is additive alongside it.
- Consumer switch shape (T3), Before: `items, err := es.ListLabeledExamples(filter)` → After: `items, err := es.ListLabeledExamples(filter)` + `items = dataset.DedupeByPair(items)` (import `internal/dedup/dataset`).

## Milestones

- **M1 — Clean mining (T1, T2).** Rules guarded; durations normalized per-file at the mining boundary; shipped write-chokepoint repair (CONS-18) verified intact. Additive/guard changes only; no existing candidate or label row is mutated by code merge alone.
- **M2 — Clean consumption (T3) + human volume (T4).** Pair-dedup at calibration/export; suspicious-label queue + one-click override. Additive read surfaces.
- **M3 — Composite calibration (T5) + scheduled loop (T6).** New op (dry-run default) + scheduler entry gated by `scheduled.label_refinement.enabled` (default **off**); validate on prod data via dry-runs before any enable.
- **M4 — Prod re-mine + recalibrate (T7).** The ONE prod-data-mutating milestone. Gated by dry-run → AskUserQuestion at every apply step.
- **M5 — Engine alignment (T8).** Behavior-changing constant (veto narrows); lands post-INIT-2, covered by engine tests + a full-scan re-verify in T7's shadow.

Each milestone is independently shippable; M1–M3 are additive until M4's gated applies.

## Files modified

| File | Change |
|---|---|
| `internal/dedup/dataset/rules.go` | T1: `missingFile` → `unsure`; `partVsWhole` identity guard (exported `SharesIdentity`, reused by T4) |
| `internal/dedup/dataset/rules_test.go` | T1: regression tests incl. Jul-8 verified-mislabel fixtures (B002V8MAAM, B003FCV4O6, B005GGGC3M) |
| `internal/database/dedup_label.go` | T1: `BookFeatures` + `ASIN`, `VersionGroupID` |
| `internal/dedup/dataset/builder.go` | T1: populate identity fields; T2: per-file duration normalization in `buildFeatures` (before summing) |
| `internal/dedup/dataset/builder_test.go` | T1/T2: feature + normalization tests (incl. mixed clean+corrupted file set) |
| `internal/database/duration_sanity.go` (+ `duration_sanity_test.go`) | T2: NEW exported `NormalizeDurationSec` beside the shipped `DurationLooksLikeMillis`; no other change |
| `internal/dedup/dataset/pair_dedupe.go` (+test) | T3 NEW: `PairKey`, `DedupeByPair` |
| `internal/plugins/dedup/calibrate_embedding_thresholds.go` | T3: pair-dedup input + `rows_in`/`pairs_out` reporting |
| `internal/server/handlers/dedup/label_review.go` | T3: export pair-dedup; T4: `ListSuspiciousDedupLabels` |
| `internal/server/wire_dedup_routes.go` | T4: `GET /dedup/labels/suspicious` |
| `web/src/pages/DedupLabels.tsx` (+test) | T4: Suspicious tab + one-click override |
| `internal/plugins/dedup/calibrate_composite.go` (+test) | T5 NEW: composite calibration op |
| `internal/plugins/dedup/plugin.go` | T5: register the op |
| `internal/config/config.go` | T6: `LabelRefinement` scheduled-task config + defaults (off) |
| `internal/scheduler/tasks.go` | T6: register `label_refinement` task (dry-run chain only) |
| `internal/dedup/engine.go` | T8 (post-INIT-2): `partVsWholeDurationRatioMax` 0.6 → 0.5 |

T2 verify-only (NO edits): `internal/database/pebble_store_bookfiles.go` (shipped CONS-18 chokepoint repair at :192/:785/:849 — verify by grep + existing tests), `internal/plugins/maintenance/duration_backfill.go` (already delegates to `database.DurationLooksLikeMillis` at :33), `internal/itunes/service/importer.go` (CONS-16 `trackDurationSeconds`).

## Testing

| Test | Asserts |
|---|---|
| `TestPartVsWholeSharedASINGoesUnsure` | Way-of-the-Wolf-shaped fixture (same ASIN, ratio 0.001) → `unsure`, never `not_dup` |
| `TestPartVsWholeSharedPathGoesUnsure` | identical `PrimaryPath`, ratio 0.5 → `unsure` (Galactic Center / Great Sky River shape) |
| `TestPartVsWholeGenuineStillNotDup` | disjoint identity, sane durations, ratio 0.3 → still `not_dup` (anti-over-suppression) |
| `TestMissingFileGoesUnsure` | `FilesExist=false` side → `unsure` with the files reason |
| `TestNormalizeDurationSecMillisDetected` / `...UnknownUnchanged` | ms-scale value ÷1000; `fileSize<=0` or `dur<=0` unchanged |
| `TestBuildExampleNormalizesDurationRatio` | ms-vs-sec pair yields ratio ≈ 1.0 after normalization |
| `TestBuildFeaturesMixedCorruptFiles` | one ms-corrupted file among clean seconds-files → correct per-file-normalized sum (an aggregate-level normalization would misjudge this fixture) |
| `TestDedupeByPairPrefersHuman` / `...LatestDecidedAt` / `...KeepsSingletons` | preference order; unlabeled never displaces labeled; unique pairs untouched |
| `TestSuspiciousPredicate*` | each suspicion arm fires; a clean rule `not_dup` is NOT flagged (anti-over-suppression) |
| `TestCalibrateCompositeInsufficientCoverage` | `< min_scored_pairs` scored → no recommendation, `insufficient-coverage` status |
| `TestCalibrateCompositeDryRunWritesNothing` | default run leaves config untouched |
| `TestLabelRefinementDisabledByDefault` | task registered, `IsEnabled()==false` with default config |
| `TestIsPartVsWholeMismatchAlignedRatio` | T8: engine veto boundary at 0.5 |

All suites run under the full `go test ./... -short` (store-getter/full-suite discipline) and `-race` where goroutines are involved (T5 sweep pool).

## Rollback

**Gate (verbatim):** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval.

- T1/T2/T3/T8 are forward-only code — revert the PR; no data is touched by merge alone. T2 touches no store write path (the chokepoint repair is prior shipped CONS-18 behavior, outside this package), so reverting T2 only restores the old read-time mining behavior — zero stored bytes change.
- The label re-mine (T7) is dry-run-gated and preserves `human` rows (rebuild_gold_labels.go:214-215 passthrough). **Its apply is NON-ATOMIC and NOT self-healing:** it deletes ALL rule/auto_high_conf rows in one call (`DeleteLabeledExamplesBySource`, rebuild_gold_labels.go:164) and re-inserts fresh rows in a separate per-row loop (:171-178). If interrupted between the delete and completing the writes, the lost rows CANNOT be regenerated by re-running rebuild (`computeRebuildDiff` derives Fresh only from surviving `existing` rows). Real partial-failure recovery: re-run the mining ops that re-derive labels from candidate state — `dedup.dataset-backfill` (rule rows) and `dedup.mine-gold-labels` (auto_high_conf rows) — then rebuild. The JSONL export is NOT a restore path (no import op exists); human rows are safe regardless via the passthrough. A bad-but-COMPLETE re-mine (logic error) is corrected by re-running after a rule fix.
- No threshold/config value is written unless a calibration explicitly recommends it AND the operator approves the apply (T5); reverting an applied config = restore previous `dedup.signals.*` values recorded in the op report.
- T6 stays dormant until `scheduled.label_refinement.enabled=true` is set by the owner; disable to roll back instantly. T4's queue is read-only; its override writes are individual, human-initiated, and individually re-overridable.

## Open questions (resolved — recorded for the plan)

1. ~~Fix the store keyspace to be pair-keyed?~~ → No — consumption-time dedup (Decision 3); keyspace unchanged.
2. ~~Should the write invariant auto-correct ms values?~~ → It ALREADY does — shipped CONS-18 `normalizeBookFileDuration` repairs+warns at all three chokepoints, double-checking the corrected value lands in the plausible-bitrate band; T2 verifies it and adds nothing there (Decision 6). The draft's warn-only premise contradicted shipped code and is withdrawn.
3. ~~Full-grid composite sweep?~~ → No — coordinate-wise with a coverage floor (Decision 7).
4. ~~Can the scheduled loop apply the re-mine?~~ → Never — dry-run/report only; applies are operator AskUserQuestion decisions (Decision 8).
5. ~~Where does the engine 0.6/0.5 alignment land?~~ → T8, after INIT-2's engine.go waves, rebased on top (Decision 9).
