<!-- file: docs/agent-tasks/dedup-label-quality/TASK-05-composite-calibration-op.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9889f55d-e2a8-4b45-83b9-95da789e6297 -->
<!-- last-edited: 2026-07-10 -->

# TASK-05 — Composite (noisy-OR) calibration op: report + gated apply (INIT-1 T5) [⚠ review-critical]

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval. For THIS op that means: dry-run report is the default and only autonomous mode; `{"apply":true}` may only ever be sent by a human operator after a real AskUserQuestion decision.
**File-ownership:** none owned by another initiative — new file + `internal/plugins/dedup/plugin.go` registration only. Do NOT touch `internal/dedup/engine.go`, `internal/database/embedding_store.go` (INIT-2-owned), or `internal/dedup/unified/compose.go` (the formula is REUSED, not modified).

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus/strong-class · calibration-math + op-plumbing subagent · **Why:** sweep math over a composite scorer plus the package's only config-apply path — a wrong recommendation silently degrades prod dedup; highest wrong-answer cost in the package · **Depends on:** TASK-03 (uses `dataset.DedupeByPair`) — ⛔ **DISPATCH HOLD:** as of 2026-07-10 TASK-03 is NOT merged, so this brief is not yet executable. Before dispatching, the coordinator must confirm `grep -rn "func DedupeByPair" internal/dedup/dataset/` hits against origin/main; 0 hits = hold this brief, do not dispatch.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-label-quality-composite-calibration-op" -b agent/dedup-label-quality-composite-calibration-op origin/main
cd "$REPO/.worktrees/dedup-label-quality-composite-calibration-op"
git rebase origin/main
# ⛔ DEPENDENCY GATE — this grep targets code CREATED BY TASK-03; the file
# internal/dedup/dataset/pair_dedupe.go does not exist until TASK-03 merges,
# hence the recursive directory form (a file-path grep would error "No such file").
# 0 hits means TASK-03 is unmerged: STOP IMMEDIATELY and report exactly
# "BLOCKED: TASK-03 (dataset.DedupeByPair) not merged". This is a sequencing
# stop, NOT anchor drift — do not guess, and do not reimplement DedupeByPair.
grep -rn "func DedupeByPair" internal/dedup/dataset/
```

## Goal

Add op `dedup.calibrate-composite` that tunes the noisy-OR composite scorer — the band thresholds AND per-signal confidence bounds in `unified.ScoreConfig` — against the pair-deduped gold-label set, instead of the single embedding cosine the existing `dedup.calibrate-embedding-thresholds` sweeps. Rationale (2026-07-08 findings): ~47% of true_dup pairs score below cosine 0.98 — a recall tail that survives the label re-mine — so no single embedding cut-point can serve the high-confidence tier; the composite is the right calibration surface. (The Jul-8 0.582 precision floor is NOT this op's justification — it was measured on the contaminated label set and may lift after TASK-07's re-mine; TASK-07 re-runs the single-cosine op first so this op's added value is judged against the clean baseline.) The op REUSES `unified.ComposeScore` to replay stored per-pair signal breakdowns under candidate configs. Dry-run (report-only) by default; `{"apply":true}` writes recommended values to the `dedup.signals.*` config surface. MIRROR the existing calibration op's structure — same `sdk.OperationDef` shape, same registration spot, same dry-run discipline as `dedup.rebuild-gold-labels`' `Apply bool` param.

## Background (verify before editing)

- Formula to reuse: `ComposeScore(signals []Signal, suppressors []string, cfg ScoreConfig, pair [2]string) UnifiedDedupScore` and `bandFor(score, cfg)` in `internal/dedup/unified/compose.go` (`FormulaVersion = "noisy-or-v1"`). Config: `type ScoreConfig` with `Signals map[string]KindConfig` + `BandCertainMin`(97)/`BandHighMin`(90)/`BandMediumMin`(75)/`BandReviewMin`(60) and `DefaultScoreConfig()` in `internal/dedup/unified/config.go`.
- Template op to mirror: `calibrateEmbeddingThresholdsDef` + `runCalibrateEmbeddingThresholds` + `collectCalibrationPairs` + `sweepThreshold` in `internal/plugins/dedup/calibrate_embedding_thresholds.go` (def/params/reporter/concurrency-key pattern; it sweeps ONLY a single cosine grid — that is precisely the gap this op fills). Registration: the def list in `internal/plugins/dedup/plugin.go` (see the `calibrateEmbeddingThresholdsDef()` / `rebuildGoldLabelsDef()` lines).
- Input rows: `LabeledExample.ScoreBreakdown` (raw JSON snapshot of `UnifiedDedupScore`, includes the signal set) + `Label`. Historically many rows have a nil breakdown (Experiment 0: 100% empty; post-Jul-7 full-scan rows carry scores) — the op MUST count and skip nil-breakdown rows and fail closed on thin coverage.
- Apply surface: recommended values are written to the config keys under `dedup.signals.*` (the `mapstructure` surface of `ScoreConfig`; defaults at `internal/config/config.go:868-874`) via the existing config update service (`internal/config/update_service.go` — follow an existing caller's pattern for persisting a config change; do NOT invent a new persistence path). **Persistence semantics (pinned):** `UpdateConfig` persists via `SaveConfigToDatabase` (`internal/config/persistence.go:1409`) → the `config_blob` row in the PebbleDB settings store, reloaded at startup by `LoadConfigFromDatabase` before file values fill gaps — an applied value SURVIVES `make deploy`/restart. There is NO feature flag: rollback = a second operator-gated config-apply restoring the previous values echoed in the op report. An applied config affects FUTURE scoring only; already-emitted candidates keep their stored scores/bands until re-scored. Add a test or verification step asserting the applied `dedup.signals.*` values round-trip through save + reload.
- Concurrency (CLAUDE.md mandate): the sweep loop (config variants × ~2.5k pairs) is a whole-library-scale loop — shard the OUTER variant loop with `errgroup.Group` + `SetLimit(runtime.NumCPU())`; `ComposeScore` is pure, so workers share nothing but the read-only pair slice. Add ctx cancellation checks per variant. Add a `-race` test.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'FormulaVersion\|func ComposeScore\|func bandFor' internal/dedup/unified/compose.go
  grep -n 'sweepGridLo\|sweepGridHi\|sweepGridStep' internal/plugins/dedup/calibrate_embedding_thresholds.go
  grep -n "type ScoreConfig struct\|func DefaultScoreConfig" internal/dedup/unified/config.go       # reuse target, 2 hits
  grep -n "calibrateEmbeddingThresholdsDef\|rebuildGoldLabelsDef" internal/plugins/dedup/plugin.go  # registration spot, >=2 hits
  grep -n "func (p \*Plugin) calibrateEmbeddingThresholdsDef\|func (p \*Plugin) runCalibrateEmbeddingThresholds" internal/plugins/dedup/calibrate_embedding_thresholds.go   # mirror source, 2 hits
  grep -rn "func DedupeByPair" internal/dedup/dataset/                                             # ⛔ DEPENDENCY GATE (TASK-03), 1 hit once merged
  grep -n "Apply bool" internal/plugins/dedup/rebuild_gold_labels.go                                # apply-param pattern, 1 hit
  ```
  If any grep returns 0 hits, STOP and report — do not guess. Exception semantics for the ⛔ DEPENDENCY GATE line: 0 hits there means TASK-03 is unmerged — report "BLOCKED: TASK-03 unmerged" (a sequencing stop), not anchor drift. All other lines resolve against origin/main today; 0 hits on any of THOSE is drift.

## Step-by-step

1. Create `internal/plugins/dedup/calibrate_composite.go` (4-line Go header). Define params:
   ```go
   type calibrateCompositeParams struct {
       TargetPrecisionCertain float64 `json:"target_precision_certain"` // default 0.98
       TargetPrecisionHigh    float64 `json:"target_precision_high"`    // default 0.90
       MinScoredPairs         int     `json:"min_scored_pairs"`         // default 500 per class
       Apply                  bool    `json:"apply"`                    // default false — REPORT ONLY
   }
   ```
2. `calibrateCompositeDef()` — mirror `calibrateEmbeddingThresholdsDef` field-for-field: `ID: "dedup.calibrate-composite"`, matching `ConcurrencyKey`, description stating "dry-run by default; apply=true writes dedup.signals.* — operator-gated". Register it in `plugin.go`'s def list directly below `calibrateEmbeddingThresholdsDef()`.
3. `runCalibrateComposite` — pipeline:
   a. Load labeled examples (both classes), `dataset.DedupeByPair`, then partition by label; drop `unsure`/unlabeled.
   b. Parse each row's `ScoreBreakdown` into `unified.UnifiedDedupScore` to recover its `Signal` set; count + skip rows with nil/unparseable breakdowns (`skipped_no_breakdown`).
   c. **Fail-closed coverage floor:** if scored `true_dup` or `not_dup` pairs `< MinScoredPairs` → report status `insufficient-coverage` with the counts and RETURN without recommending anything.
   d. Baseline: score every pair with the CURRENT `ScoreConfig` (loaded the same way production scoring loads it; fall back to `DefaultScoreConfig()`); report per-band precision/recall.
   e. Coordinate-wise sweep (locked design — NOT full grid): round 1 sweeps each band threshold over a ±10 grid (step 0.5) holding others fixed, keeping the value meeting the target precision with max recall; round 2 sweeps each signal kind's `MinConfidence`/`MaxConfidence` (±0.05, step 0.01) the same way; 2 rounds max. Shard the outer variant loop with `errgroup` + `SetLimit(runtime.NumCPU())`; check `ctx.Err()` per variant.
   f. Report: baseline vs recommended config (full JSON of both), per-band precision/recall/n for each, `rows_in`/`pairs_out`/`skipped_no_breakdown`, and target-met flags per band. When no candidate meets a target: `target-not-met` for that band, recommend nothing for it (same stop-branch discipline as the embedding op).
   g. Apply path: ONLY when `Apply && every recommended band met its target` — persist via the config update service, and echo the PREVIOUS values in the report (they are the rollback record). Log loudly. Never apply partial/target-not-met recommendations.
4. Purely additive elsewhere: `plugin.go` gets one def line; no changes to `compose.go`, `config.go` defaults, or the embedding op.
5. Tests — `internal/plugins/dedup/calibrate_composite_test.go` (NEW), synthetic in-memory label sets:
   - `TestCalibrateCompositeInsufficientCoverage` — below-floor input → `insufficient-coverage`, no recommendation.
   - `TestCalibrateCompositeDryRunWritesNothing` — default params, well-separated synthetic set → recommendations in report, config store untouched.
   - `TestCalibrateCompositeFindsSeparation` — construct pairs where a band shift from 90→92 achieves the target → recommendation contains it.
   - `TestCalibrateCompositeTargetNotMet` — inseparable classes → `target-not-met`, nothing recommended (anti-over-suppression for the recommender: it must not force a bad threshold).
   - `TestCalibrateCompositeSweepParallel` — run the sweep under `-race` (the errgroup pool is exercised).
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./internal/plugins/dedup/... -race
go test ./... -short
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -rn '"dedup.calibrate-composite"' internal/plugins/dedup/` hits in both the def and `plugin.go`'s registration list
- [ ] `grep -n "insufficient-coverage" internal/plugins/dedup/calibrate_composite.go` hits (fail-closed floor present)
- [ ] `grep -n "SetLimit(runtime.NumCPU())" internal/plugins/dedup/calibrate_composite.go` hits (bounded sweep pool)
- [ ] `go test ./internal/plugins/dedup/ -run TestCalibrateComposite -race -v` — all five tests pass, including `TestCalibrateCompositeTargetNotMet` (the recommender never forces a threshold that misses target) and `TestCalibrateCompositeDryRunWritesNothing`
- [ ] nil-breakdown rows are skipped + counted, never scored as zero (asserted in the coverage test)
- [ ] apply path unreachable without `{"apply":true}` AND all targets met (assert via `TestCalibrateCompositeDryRunWritesNothing` + a target-not-met-with-apply case)
- [ ] `go test ./... -short` green; `make ci` green
- [ ] File headers bumped on every changed file

## Commit message

```
feat(dedup): dedup.calibrate-composite op — tune noisy-OR bands + signal confidences (INIT-1 T5)

The existing calibration sweeps only a single embedding cosine cut-point;
~47% of true_dup pairs score below cosine 0.98, so the composite scorer is
the right calibration surface. Replays stored ScoreBreakdown signals through
unified.ComposeScore under coordinate-wise config variants against the
pair-deduped gold set. Dry-run by default; apply=true is operator-gated and
refuses partial/target-not-met recommendations.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-label-quality-composite-calibration-op
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -rn "dedup.calibrate-composite" internal/plugins/dedup/calibrate_composite.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit (the op is additive; dry-run default writes nothing). If a config apply has ALREADY been operator-approved and executed: the values persist in the PebbleDB settings store (`config_blob` via `SaveConfigToDatabase`) and survive redeploy, and there is no feature flag — the revert is a SECOND operator-gated config-apply restoring the previous `dedup.signals.*` values echoed in that op run's report (they are recorded there for exactly this purpose). Reverting the code PR alone does NOT undo an applied config.
