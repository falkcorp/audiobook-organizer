<!-- file: docs/agent-tasks/dedup-label-quality/TASK-03-label-pair-dedupe.md -->
<!-- version: 1.0.0 -->
<!-- guid: 82c2d17d-6ecd-41af-af5b-903d746076cd -->
<!-- last-edited: 2026-07-10 -->

# TASK-03 — Collapse labeled examples to one per book-pair at consumption (INIT-1 T3)

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval.
**File-ownership:** `ListLabeledExamples` is implemented in `internal/database/dedup_label.go` (method on EmbeddingStore) — NOT in `embedding_store.go` — so this task does NOT collide with INIT-2's embedding_store.go index work and needs no serialization. Do NOT edit `internal/database/dedup_label.go`, `internal/database/embedding_store.go`, or `internal/dedup/engine.go` in this task. Shares `internal/server/handlers/dedup/label_review.go` with TASK-04 — TASK-04 runs in a later wave; you own the file for this wave.

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · pure-function + integration subagent · **Why:** a pure dedupe helper with a precise preference order plus two consumer integrations; needs care with tie-breaks, not architecture · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-label-quality-label-pair-dedupe" -b agent/dedup-label-quality-label-pair-dedupe origin/main
cd "$REPO/.worktrees/dedup-label-quality-label-pair-dedupe"
git rebase origin/main
```

## Goal

Stop calibration and export from double-counting labels: the label store keys rows by `candidateID`, and one book-pair produces multiple candidates across layers, so the 2026-07-08 export held 6,926 rows for only 2,564 unique pairs (~2.7×). Add a pure helper `DedupeByPair` in `internal/dedup/dataset` (NEW file — the store itself is untouched, per the locked consumption-time decision) and wire it into the two consumers: `collectCalibrationPairs` in the embedding-calibration op and `ExportLabeledExamples` in the label-review handler. REUSE `database.LabeledExample` as-is; do NOT re-key the `dedup:label:` keyspace and do NOT modify `ListLabeledExamples`.

## Background (verify before editing)

- Store layout: `dedup:label:<candidateID 16-hex>` → `LabeledExample` JSON (`internal/database/dedup_label.go`, prefix const near the top). `LabeledExample` carries `CandidateID int64`, `EntityAID`/`EntityBID string`, `Label` (`true_dup|not_dup|unsure`, `""` = unlabeled), `LabelSource` (`rule|itunes_attr|human|llm_judge`), `DecidedAt` (RFC3339 string).
- Consumer 1: `collectCalibrationPairs` in `internal/plugins/dedup/calibrate_embedding_thresholds.go` loads `true_dup` and `not_dup` lists via `es.ListLabeledExamples` and scores each row — duplicated pairs are double-counted in precision math.
- Consumer 2: `ExportLabeledExamples` in `internal/server/handlers/dedup/label_review.go` streams rows to JSONL — the export shows the same 2.7× duplication.
- Preference order (locked): `human > llm_judge > itunes_attr > rule`; ties → latest `DecidedAt` (RFC3339 strings compare correctly lexicographically; empty string loses); still tied → highest `CandidateID`. Unlabeled rows (`Label == ""`) never displace a labeled row for the same pair, but a pair with ONLY unlabeled rows keeps one of them.
- Canonical pair key: sort the two entity IDs lexicographically and join (e.g. `min + "|" + max`) so A/B ordering never splits a pair.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func (h \*Handler) ExportLabeledExamples' internal/server/handlers/dedup/label_review.go && grep -n 'func (s \*EmbeddingStore) ListLabeledExamples' internal/database/dedup_label.go
  grep -n 'dedupLabelPfx = "dedup:label:"' internal/database/dedup_label.go
  grep -n "func collectCalibrationPairs" internal/plugins/dedup/calibrate_embedding_thresholds.go   # edit target, 1 hit
  grep -n 'sweepGridLo\|sweepGridHi\|sweepGridStep' internal/plugins/dedup/calibrate_embedding_thresholds.go
  ```
  If any grep returns 0 hits, STOP and report — do not guess.

## Step-by-step

1. Create `internal/dedup/dataset/pair_dedupe.go` (4-line Go header) with exactly these two exported functions:
   ```go
   // PairKey returns the canonical identity of a labeled example's book pair:
   // the two entity IDs sorted lexicographically, joined with "|".
   func PairKey(ex database.LabeledExample) string

   // DedupeByPair collapses examples to ONE per canonical pair.
   // Preference: human > llm_judge > itunes_attr > rule; ties by latest
   // DecidedAt (string compare), then highest CandidateID. Labeled rows
   // always beat unlabeled (Label == "") rows. Order of the result follows
   // first-seen pair order (stable for tests).
   func DedupeByPair(examples []database.LabeledExample) []database.LabeledExample
   ```
   Implement source rank with a small map; treat an unknown `LabelSource` as rank of `rule` (lowest) — unknown is non-disqualifying but never preferred.
2. `internal/plugins/dedup/calibrate_embedding_thresholds.go` — in `collectCalibrationPairs`, apply `dataset.DedupeByPair` to the combined true_dup+not_dup rows BEFORE scoring (dedupe across both lists together, so a pair holding both a rule not_dup and a human true_dup resolves to the human row rather than appearing in both classes). Add two report fields/log lines: `rows_in` (pre-dedup count) and `pairs_out` (post-dedup) so the collapse is observable in the op report.
3. `internal/server/handlers/dedup/label_review.go` — in `ExportLabeledExamples`, apply `dataset.DedupeByPair(items)` after the list call; support a query param `raw=true` that skips dedup (debugging escape hatch), defaulting to deduped. Do not change `ListDedupLabels` or `OverrideDedupLabel`.
4. Purely additive elsewhere: no signature changes to store methods; no edits to `dedup_label.go`; imports formatted by the formatter only.
5. Tests — `internal/dedup/dataset/pair_dedupe_test.go` (NEW): `TestDedupeByPairPrefersHuman` (rule + human rows same pair → human survives), `TestDedupeByPairCrossClass` (rule not_dup + human true_dup same pair → single human true_dup row), `TestDedupeByPairLatestDecidedAt`, `TestDedupeByPairHighestCandidateID`, `TestDedupeByPairKeepsSingletons` (unique pairs pass through unchanged — anti-over-suppression: dedupe must not drop distinct pairs), `TestDedupeByPairUnlabeledNeverDisplaces`, `TestPairKeyOrderInsensitive` (A,B and B,A give one key). Also extend the calibration op's test file to assert `rows_in >= pairs_out` appears in the report.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./internal/dedup/dataset/... ./internal/plugins/dedup/... ./internal/server/handlers/dedup/... -race
go test ./... -short
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "func DedupeByPair" internal/dedup/dataset/pair_dedupe.go` hits
- [ ] `grep -n "DedupeByPair" internal/plugins/dedup/calibrate_embedding_thresholds.go` hits (calibration consumes deduped pairs)
- [ ] `grep -n "DedupeByPair" internal/server/handlers/dedup/label_review.go` hits (export deduped by default, `raw=true` escape hatch present: `grep -n '"raw"' internal/server/handlers/dedup/label_review.go`)
- [ ] `go test ./internal/dedup/dataset/ -run TestDedupeByPair -v` — all cases pass, including `TestDedupeByPairKeepsSingletons` (anti-over-suppression: distinct pairs are never dropped)
- [ ] cross-class resolution asserted: a pair with rule not_dup + human true_dup contributes exactly ONE row, the human one (`TestDedupeByPairCrossClass`)
- [ ] Tests green (`go test ./... -short`); `make ci` green; vet clean
- [ ] File headers bumped on every changed file

## Commit message

```
fix(dedup): dedupe labeled examples to one per book-pair at calibration/export (INIT-1 T3)

The dedup:label store keys by candidateID, so multi-layer pairs exported ~2.7x
duplicate rows (6,926 rows -> 2,564 pairs on 2026-07-08) and calibration
double-counted them. Adds dataset.DedupeByPair (human > llm_judge >
itunes_attr > rule, latest DecidedAt, highest CandidateID) and applies it in
collectCalibrationPairs and ExportLabeledExamples.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-label-quality-label-pair-dedupe
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "func DedupeByPair" internal/dedup/dataset/pair_dedupe.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the label store and `ListLabeledExamples` are untouched, so pre-existing (duplicated) consumption behavior returns exactly.
