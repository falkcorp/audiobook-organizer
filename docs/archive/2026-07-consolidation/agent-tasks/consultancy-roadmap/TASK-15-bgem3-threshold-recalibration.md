<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-15-bgem3-threshold-recalibration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 90b16f4c-0985-40c7-9893-63ba0b98f2b5 -->
<!-- last-edited: 2026-07-03 -->

# TASK-15 — bge-m3 embedding-threshold recalibration + candidate regeneration (consultancy-roadmap)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus · go-backend subagent · **Depends on:** TASK-13

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-15-bgem3-threshold-recalibration" -b agent/cr-15-bgem3-threshold-recalibration origin/main
cd "$REPO/.worktrees/cr-15-bgem3-threshold-recalibration"
git rebase origin/main
```

## Goal

Fix DEDUP-2 / DEDUP-3: the Layer-2 (embedding) similarity thresholds and
confidence ramps are hard-coded numbers calibrated for OpenAI
`text-embedding-3-large` (3072-dim, cosine distribution X). The corpus is
mid-cutover to a local Ollama `bge-m3` model (1024-dim), which has a different
cosine distribution — the OpenAI-era thresholds have never been validated
against it. If bge-m3's true-duplicate cosines cluster lower than 0.95, the
high-confidence tier silently stops firing post-cutover with no error, just
missing candidates (recall collapse).

This task does **three** things, in order:

1. Make the book-embedding thresholds **per-embedding-model** and
   config/DB-tunable (a map keyed by model name), defaulting to the current
   hard-coded values for the legacy OpenAI model key so behavior is
   byte-for-byte unchanged until an operator opts a new model in.
2. Build a **calibration harness** (a new dry-run-only op) that scores the
   existing labeled gold dataset (`true_dup`/`not_dup` examples already in the
   dedup dataset store) using whatever embeddings are actually stored for
   `bge-m3`, sweeps candidate thresholds, and emits a report recommending
   thresholds that hit a target precision (≥0.98 for the "high" band) for the
   bge-m3 model — it does **not** write the recommendation into config.
3. Document (not implement) the follow-up step: after the re-embed finishes
   and an operator has reviewed/applied the harness's recommended thresholds,
   a `FullScan` must be run to regenerate embedding-layer candidates under the
   new thresholds — this run is owner-gated and out of scope for this task's
   acceptance criteria.

**Do not** change `AutoMergeEnabled` behavior, touch Layer 1 (exact) or Layer 3
(LLM) logic, or attempt to trigger a production re-embed or `FullScan`
yourself.

## Background (verify before editing — line numbers drift)

- `internal/dedup/engine.go` — `Engine.BookHighThreshold` / `BookLowThreshold`
  fields (currently plain `float64`, no model dimension) are set as constants
  in `NewEngine`:
  ```bash
  grep -n "BookHighThreshold\|BookLowThreshold" internal/dedup/engine.go
  ```
  As of this writing (verify — do not trust the line numbers in this brief),
  `NewEngine` sets `BookHighThreshold: 0.95, BookLowThreshold: 0.85` around
  line 194-195, and these are copied into `EmbeddingCollectorConfig` in
  `PostInit`/collector wiring around line 507-508
  (`embCfg.HighThreshold = de.BookHighThreshold`).
- `internal/dedup/collectors_embedding.go` — `embedHighConfidence` and
  `embedMediumConfidence` (verify with
  `grep -n "func embedHighConfidence\|func embedMediumConfidence\|func DefaultEmbeddingCollectorConfig" internal/dedup/collectors_embedding.go`)
  hard-code the same 0.95/0.85 cut points and interpolate confidence within
  each band. These must become a function of the *active* threshold pair, not
  new hard-coded literals, so a future model swap only requires new threshold
  values, not a code change.
- `internal/config/config.go` — `DedupConfig` (verify with
  `grep -n "type DedupConfig struct" -A 20 internal/config/config.go`) has
  flat `BookHighThreshold`/`BookLowThreshold` fields with viper bindings
  (`dedup.book_high_threshold` / `dedup.book_low_threshold`) and a runtime
  PUT-persisted path in `internal/config/persistence.go` (verify with
  `grep -n "BookHighThreshold\|BookLowThreshold" internal/config/persistence.go`).
  There is currently **no per-model** threshold concept anywhere in config.
- `internal/database/embedding_store.go` — `Embedding.Model` field (verify
  with `grep -n "type Embedding struct" -A 10 internal/database/embedding_store.go`)
  already tags every stored vector with the producing model name (e.g.
  `text-embedding-3-large` or the configured Ollama model id such as
  `bge-m3`). `CosineSimilarity(a, b []float32) float32` (verify with
  `grep -n "func CosineSimilarity" internal/database/embedding_store.go`)
  returns 0 on dimension mismatch (DEDUP-3's mechanism) — the harness must
  only compare same-model, same-dimension pairs, never mix models.
- `internal/database/dedup_label.go` — `LabeledExample` (verify with
  `grep -n "type LabeledExample struct" -A 25 internal/database/dedup_label.go`)
  has `EntityAID`, `EntityBID`, and `Label` (`true_dup`/`not_dup`/`unsure`)
  fields. `(*EmbeddingStore).ListLabeledExamples(f LabeledExampleFilter)`
  (verify with `grep -n "func.*ListLabeledExamples" internal/database/dedup_label.go`)
  is the existing accessor — filter by `Label: "true_dup"` and a second pass
  with `Label: "not_dup"` to get both classes. This dataset is populated by
  the `dedup.dataset-backfill` and `dedup.mine-gold-labels` ops (already
  shipped — do not build dataset population, only consume it).
- `internal/plugins/dedup/reembed_embeddings.go` — already-shipped op
  `dedup.reembed-embeddings` documents the cutover sequence (Layer 2 OFF
  during re-embed, restart, re-embed, restart, re-enable). Its package doc
  comment (verify with `sed -n '1,45p' internal/plugins/dedup/reembed_embeddings.go`)
  is the authoritative source for "after re-embed, run FullScan" — this task
  does not modify that file, only reads it for context.
- `internal/plugins/dedup/dataset_backfill.go` is the closest existing
  precedent for a dry-run-only op that reads the labeled dataset and reports
  counts without mutating dedup state — mirror its shape (params struct with
  an `Apply`-style flag if you add one, `sdk.OperationDef`, `Run` function,
  `reporter.Logger()` for structured logging) for the new calibration op.
  Registration precedent: `internal/plugins/dedup/plugin.go` lists every op
  def in a slice (verify with
  `grep -n "p\..*Def()," internal/plugins/dedup/plugin.go`); add the new op
  there following the existing one-per-line-with-comment style.

## Step-by-step

### Part A — Per-model threshold plumbing (config + engine)

1. In `internal/config/config.go`, extend `DedupConfig` with a new field,
   e.g. `EmbeddingThresholdsByModel map[string]EmbeddingModelThresholds`
   (define `EmbeddingModelThresholds{ High, Low float64 }` alongside it).
   Keep the existing flat `BookHighThreshold`/`BookLowThreshold` fields
   as-is — they become the **default fallback** entry used when the active
   embedding model has no entry in the map (this guarantees zero behavior
   change for the current OpenAI-era config and for any model not yet
   calibrated).
2. Add a small resolver, e.g.
   `func (c DedupConfig) ThresholdsForModel(model string) (high, low float64)`,
   that looks up `model` in `EmbeddingThresholdsByModel`; if absent, returns
   `c.BookHighThreshold, c.BookLowThreshold`. Add a unit test for both branches
   (present in map / absent-falls-back).
3. In `internal/dedup/engine.go`, thread the active embedding model name
   through to threshold selection. The engine already knows the model via
   `de.embedClient` (verify the accessor —
   `grep -n "func (c \*ai.EmbeddingClient) Model\|embedClient.Model()" internal/dedup/*.go internal/ai/*.go`).
   Where `de.BookHighThreshold`/`de.BookLowThreshold` are currently read
   directly (the `embCfg.HighThreshold = de.BookHighThreshold` site and any
   other read sites found via
   `grep -n "de.BookHighThreshold\|de.BookLowThreshold" internal/dedup/*.go`),
   replace with a call to `config.AppConfig.Dedup.ThresholdsForModel(de.embedClient.Model())`
   (guard for `de.embedClient == nil`, falling back to the existing
   `de.BookHighThreshold`/`de.BookLowThreshold` fields unchanged — those
   fields remain the test-injection seam, do not remove them).
4. Update `internal/dedup/collectors_embedding.go`'s `embedHighConfidence`/
   `embedMediumConfidence` so the band cut-points (`0.95`, `0.85`) are
   **parameters derived from the resolved thresholds**, not hard-coded
   literals — e.g. change signatures to `embedHighConfidence(cos float32, high, low float64) float64`
   (or thread an `EmbeddingCollectorConfig` through) so a non-default
   threshold pair reshapes the confidence ramp consistently instead of
   silently using stale 0.95/0.85 cut-points while similarity comparisons use
   the new thresholds. Update all call sites and existing tests in
   `internal/dedup/collectors_embedding_test.go` (verify filename with
   `ls internal/dedup/*embedding*test*`).
5. Persist the new config plumbing per the existing runtime-config pattern in
   `internal/config/persistence.go` (verify the read/merge pattern around the
   existing `BookHighThreshold`/`BookLowThreshold` handling before adding the
   map — a nested map may need explicit JSON marshal/unmarshal handling
   distinct from the flat float fields; follow whatever pattern the file
   already uses for structured sub-objects, or add a narrowly-scoped
   `map[string]json.RawMessage`-free approach if the existing persistence
   layer doesn't support nested maps — do not invent a new persistence
   mechanism, extend the existing one).

### Part B — Calibration harness (new dry-run-only op)

6. Create `internal/plugins/dedup/calibrate_embedding_thresholds.go` modeled
   on `dataset_backfill.go`'s structure:
   - New op ID: `dedup.calibrate-embedding-thresholds`.
   - Params: `{"model": "bge-m3", "target_precision": 0.98}` (both optional;
     default `model` to the currently-configured embed client model via
     `de.embedClient.Model()`, default `target_precision` to `0.98`).
   - Logic: call `ListLabeledExamples` twice (`Label: "true_dup"`,
     `Label: "not_dup"`); for each labeled pair, fetch both entities'
     embeddings via `embeddingStore.Get("book", entityAID)` /
     `Get("book", entityBID")`; **skip the pair** (count it as skipped, do
     not error) if either embedding is missing, or either embedding's
     `.Model` field does not equal the target `model` param, or the two
     vectors have different lengths. For surviving pairs compute
     `database.CosineSimilarity(vecA, vecB)` and bucket by label.
   - Sweep a fixed grid of candidate high-band cut-points (e.g. every 0.01
     from 0.80 to 0.99) and, for each, compute precision = true_dup pairs at
     or above the cut-point ÷ (true_dup + not_dup pairs at or above the
     cut-point). Pick the **lowest** cut-point whose precision is ≥
     `target_precision` (maximizes recall subject to the precision floor);
     if no cut-point reaches the target, report that explicitly (do not pick
     a value silently).
   - Do the same sweep for a low/medium-band cut-point using a lower default
     target (document the choice — e.g. reuse the same target_precision
     parameter, or add a second `target_precision_low` param defaulting to a
     looser value; pick one and state the rationale in a doc comment).
   - Emit the result via `reporter` (structured log fields: `model`,
     `sample_true_dup`, `sample_not_dup`, `skipped_dimension_mismatch`,
     `skipped_missing_embedding`, `recommended_high_threshold`,
     `recommended_high_precision`, `recommended_low_threshold`,
     `recommended_low_precision`, or `no_threshold_met_target: true` when
     applicable) and also return a JSON summary as the op's result payload
     (follow whatever result-payload convention `dataset_backfill.go` or
     `mine_gold_labels.go` uses — verify with
     `grep -n "reporter.Result\|return.*nil.*//.*result\|OpResult" internal/plugins/dedup/dataset_backfill.go internal/plugins/dedup/mine_gold_labels.go`).
   - This op is **read-only** — it must never call `UpsertCandidate`,
     `SetScoreConfig`, or write to `config.AppConfig`. It only reads the
     labeled dataset and embedding store and reports.
7. Register the new op in `internal/plugins/dedup/plugin.go`'s op-def slice
   (mirror the existing one-per-line style, e.g. add
   `p.calibrateEmbeddingThresholdsDef(), // DEDUP-2/3: bge-m3 threshold calibration report (dry-run only)`
   near `p.datasetBackfillDef()`).
8. Add `internal/plugins/dedup/calibrate_embedding_thresholds_test.go`
   covering:
   - Synthetic labeled examples + a fake/stub embedding store where
     true_dup pairs cluster at high cosine and not_dup pairs cluster lower —
     assert the harness recommends a threshold between the two clusters and
     reports the expected precision.
   - A case where model/dimension mismatch causes a pair to be skipped
     (skip counters increment, no panic, no false precision inflation).
   - A case where no cut-point reaches `target_precision` — assert the op
     reports `no_threshold_met_target` (or equivalent) rather than fabricating
     a recommendation.

### Part C — Document the deferred regeneration step

9. In the calibration op's doc comment (top of the new file) and in this
   task's PR description, state explicitly: *"After the re-embed
   (`dedup.reembed-embeddings`) completes and an operator has reviewed this
   report and updated `dedup.embedding_thresholds_by_model` (or equivalent)
   accordingly, run `dedup.full-scan` to regenerate embedding-layer
   candidates under the new thresholds. This step is owner-gated and is not
   performed by this task."* Do not add code that auto-triggers `FullScan`.

10. Bump the file header (version + `last-edited`) on every file you touch,
    per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/config/... -run Threshold -count=1
go test ./internal/dedup/... -count=1
go test ./internal/plugins/dedup/... -count=1
go vet ./internal/config/... ./internal/dedup/... ./internal/plugins/dedup/...
```

## Acceptance criteria

- [ ] `DedupConfig` supports a per-embedding-model threshold map with a
      resolver that falls back to the existing flat `BookHighThreshold`/
      `BookLowThreshold` for any model not present in the map (default
      behavior for the current OpenAI-era config is byte-for-byte unchanged).
- [ ] Engine threshold reads (collector config wiring, any other read sites
      found via the grep in step 3) go through the resolver keyed by the
      active embedding client's model name, not the flat fields directly.
- [ ] `embedHighConfidence`/`embedMediumConfidence` band cut-points derive
      from the resolved thresholds, not hard-coded 0.95/0.85 literals, and
      existing collector tests are updated accordingly and pass.
- [ ] New op `dedup.calibrate-embedding-thresholds` exists, is registered in
      `plugin.go`, is read-only (no candidate/config mutation), consumes only
      the existing labeled dataset + embedding store, and emits a report with
      recommended high/low thresholds (or an explicit "target not met"
      result) plus sample-size and skip counters.
- [ ] New tests cover: correct threshold recommendation on synthetic
      clustered data, correct skip behavior on model/dimension mismatch, and
      correct "no threshold met target" reporting.
- [ ] The deferred `FullScan` regeneration step is documented (doc comment +
      PR description) as owner-gated, and this task's acceptance criteria do
      **not** require it to have run — applying new thresholds to prod config
      or triggering `FullScan` is explicitly out of scope for this PR.
- [ ] `go build ./...`, targeted `go test`, and `go vet` are all clean.
- [ ] File headers bumped on every changed/added file.

## Commit message

```
feat(dedup): per-model embedding thresholds + bge-m3 calibration harness (DEDUP-2/3)

BookHighThreshold/BookLowThreshold and the embedding confidence ramps were
hard-coded for OpenAI text-embedding-3-large's cosine distribution, with no
recalibration path for the bge-m3 cutover. Add a per-embedding-model
threshold map (falls back to the existing flat values for any unlisted
model), thread the active model through threshold resolution, and add a
read-only dedup.calibrate-embedding-thresholds op that sweeps the labeled
gold dataset's bge-m3 cosines and reports precision-targeted threshold
recommendations. Applying the recommendation to prod config and running
FullScan to regenerate candidates remains an owner-gated follow-up.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-15-bgem3-threshold-recalibration
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `DedupConfig` already has a per-model threshold map wired through to both
the engine's threshold reads and `embedHighConfidence`/`embedMediumConfidence`,
and a read-only `dedup.calibrate-embedding-thresholds`-equivalent op already
exists and is registered in `plugin.go`, this task is done — verify with:

```bash
grep -n "EmbeddingThresholdsByModel\|ThresholdsForModel" internal/config/config.go
grep -n "calibrate.*embedding\|calibrate-embedding-thresholds" internal/plugins/dedup/*.go
```

If the consultancy citation is stale (e.g. thresholds have already been made
per-model by another task, or the calibration harness already exists under a
different name), do not duplicate the work — note in the PR description which
existing mechanism already covers DEDUP-2/DEDUP-3 and stop.

Rollback = revert the commit. The change is additive (new config field with
a safe fallback, new op, parameterized confidence functions with the same
default cut-points) — no existing behavior changes for models not present in
the new per-model map, so rollback carries no data-migration risk. The
calibration op never mutates dedup state, so there is nothing to undo on the
data side even without a code revert.
