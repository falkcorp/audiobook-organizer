<!-- file: docs/agent-tasks/metadata-matching/TASK-02-extract-scoring-config.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e90f6a5-2c4f-4390-9a8e-809bac1cf0eb -->
<!-- last-edited: 2026-07-10 -->

# TASK-02 — Extract hardcoded scoring literals into MetadataScoringConfig (INIT-3-T1 backend)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** shares `internal/metafetch/service_scoring.go` (+ its test file) with TASK-01 — this task runs in wave 2, ONLY after TASK-01's PR has merged to origin/main. Verify with the idempotency grep from TASK-01 (`grep -n "durationTiers" internal/metafetch/service_scoring.go` must hit) before starting.

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Sonnet-class · code-refactor subagent · **Why:** ~14 literals across 3 files with a hard zero-behavior-change equivalence requirement · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-extract-scoring-config" -b agent/metadata-matching-extract-scoring-config origin/main
cd "$REPO/.worktrees/metadata-matching-extract-scoring-config"
git rebase origin/main
# TASK-01 must already be merged (DEPENDENCY GATE — this grep is EXPECTED to fail
# until TASK-01's PR lands on origin/main; as of 2026-07-10 it has NOT landed):
grep -n "durationTiers" internal/metafetch/service_scoring.go || { echo "STOP: TASK-01 not merged yet"; exit 1; }
```

**Coordinator dispatch gate:** do NOT dispatch this brief until TASK-01 has actually merged.
Verify before assigning:

```bash
grep -n "durationTiers" /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/internal/metafetch/service_scoring.go
# zero hits = TASK-01 not merged = HOLD this brief; do not treat the zero hits as anchor drift.
```

## Goal

Move every hardcoded scoring weight in `internal/metafetch/` into `MetadataScoringConfig`
(`internal/config/config.go`) so scoring becomes operator-tunable — with **every default exactly
equal to today's literal** so output scores are bit-identical until someone edits config. REUSE
the existing `MetadataScoringConfig` struct and the existing two viper population sites; do NOT
create a new config struct or a new config file section.

## Background (verify before editing)

- `MetadataScoringConfig` today has exactly 7 embedding/LLM/backup fields — no scoring weights.
- **The new fields, inlined verbatim (NORMATIVE — this block is the struct shape; the spec file
  `docs/specs/2026-07-10-metadata-matching-design.md` it was copied from lives in the planning
  repo and is NOT present in your execution worktree, so do not go looking for it).** Append
  these to `MetadataScoringConfig` after the 7 existing fields; mapstructure keys mirror json:

  ```go
  // --- new: transcription boosts (defaults 2.0 / 1.4 / 1.6 / 1.4) ---
  TranscriptionTitleExactBoost  float64 `json:"transcription_title_exact_boost"  mapstructure:"transcription_title_exact_boost"`
  TranscriptionTitleSubstrBoost float64 `json:"transcription_title_substr_boost" mapstructure:"transcription_title_substr_boost"`
  TranscriptionAuthorBoost      float64 `json:"transcription_author_boost"       mapstructure:"transcription_author_boost"`
  TranscriptionNarratorBoost    float64 `json:"transcription_narrator_boost"     mapstructure:"transcription_narrator_boost"`

  // --- new: base-score adjustments (defaults 0.15 / 0.05 / 0.15 / 0.35).
  // POINTER knobs: 0 is a legitimate operator value for CompilationPenalty,
  // RichMetadataBonusCap, and F1MinScore, so "unset" is nil, NOT 0. ---
  CompilationPenalty     *float64 `json:"compilation_penalty"       mapstructure:"compilation_penalty"`
  RichMetadataFieldBonus float64  `json:"rich_metadata_field_bonus" mapstructure:"rich_metadata_field_bonus"`
  RichMetadataBonusCap   *float64 `json:"rich_metadata_bonus_cap"   mapstructure:"rich_metadata_bonus_cap"`
  F1MinScore             *float64 `json:"f1_min_score"              mapstructure:"f1_min_score"`

  // --- new: series boosts (defaults 1.4 / 2.0 / 0.5) ---
  SeriesNameMatchBoost     float64 `json:"series_name_match_boost"     mapstructure:"series_name_match_boost"`
  SeriesNumberExactBoost   float64 `json:"series_number_exact_boost"   mapstructure:"series_number_exact_boost"`
  SeriesNumberWrongPenalty float64 `json:"series_number_wrong_penalty" mapstructure:"series_number_wrong_penalty"`

  // --- new: duration tier VALUES (defaults = the multiplier/score columns of
  // TASK-01's merged durationTiers table — copy them from
  // internal/metafetch/service_scoring.go at execution time). Tier
  // STRUCTURE (edges + count) stays fixed in code. ---
  DurationTierMultipliers []float64 `json:"duration_tier_multipliers" mapstructure:"duration_tier_multipliers"`
  DurationTierScores      []float64 `json:"duration_tier_scores"      mapstructure:"duration_tier_scores"`

  // --- new: bulk-fetch concurrency (default 4; consumed by TASK-05) ---
  BulkFetchWorkers int `json:"bulk_fetch_workers" mapstructure:"bulk_fetch_workers"`
  ```

  Every field above that is NOT called out as `*float64` is a plain `float64` (or the stated
  `[]float64`/`int`) — only `CompilationPenalty`, `RichMetadataBonusCap`, and `F1MinScore` are
  pointers. Do not rename, retype, or re-tag anything.
- Literals to extract (all inside `internal/metafetch/`):
  - transcription boosts ×2.0 (title exact), ×1.4 (title substring), ×1.6 (author), ×1.4
    (narrator) in `transcriptionBoost` (`service_scoring.go` ~292, literals at ~303/306/321/325)
  - compilation penalty `score *= 0.15` in `ApplyNonBaseAdjustments` (~119)
  - rich-metadata bonus +0.05 per field, cap 0.15 (~133-148)
  - `const f1MinScore = 0.35` (~355)
  - duration tier VALUES from TASK-01 (`durationTiers` multiplier/score columns become
    `DurationTierMultipliers`/`DurationTierScores`; the tier EDGES/count stay FIXED in code — the
    structure is TASK-01's output, not an operator dial (reviewed); do NOT add a
    `DurationTierEdges` config field)
  - series-name match `score *= 1.4` (`service_search.go` ~368)
  - series-number `c.Score *= 2.0` / `c.Score *= 0.5` (`service_search.go` ~512/514)
- There are TWO viper population sites for `MetadataScoring` in `config.go` (~1087 and ~1512) —
  BOTH must populate the new fields, or one config path silently zeroes them.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'score \*= [0-9.]*' internal/metafetch/service_scoring.go
  grep -n 'Rich-metadata bonus' internal/metafetch/service_scoring.go
  grep -n 'f1MinScore = 0.35' internal/metafetch/service_scoring.go
  grep -n 'func (mfs \*Service) BuildSourceChain' internal/metafetch/service_search.go
  grep -n 'c.Score \*= 2.0\|c.Score \*= 0.5' internal/metafetch/service_search.go
  grep -n 'score \*= 1.4' internal/metafetch/service_search.go
  grep -n 'type MetadataScoringConfig struct' internal/config/config.go
  grep -n 'MetadataScoring: MetadataScoringConfig{' internal/config/config.go   # expect 2 hits
  grep -n 'durationTiers' internal/metafetch/service_scoring.go   # TASK-01 output — DEPENDENCY GATE, not a drift anchor
  ```
  Zero hits on any of these = STOP and report drift — EXCEPT the `durationTiers` line, which is
  the TASK-01 dependency gate: zero hits there means TASK-01 has not merged yet; return the brief
  to the coordinator as BLOCKED-on-TASK-01, do not report it as drift.

## Step-by-step

1. **Equivalence fixtures first.** Extend `internal/metafetch/service_scoring_test.go` with
   `TestScoringConfigDefaultsMatchLegacyLiterals`: capture current outputs of
   `transcriptionBoost`, `ApplyNonBaseAdjustments`, and the duration functions over
   representative inputs BEFORE any extraction; commit separately.
2. Add the new fields to `MetadataScoringConfig` in `internal/config/config.go` exactly as the
   inline struct block in Background defines (names, types, json + mapstructure tags — copy it
   verbatim). Do not rename or reorder the 7 existing fields.
3. At BOTH viper population sites (grep above, 2 hits), add `viper.SetDefault(...)` + population
   for each new key with the legacy value (e.g. `metadata_scoring.transcription_title_exact_boost`
   → 2.0).
4. In `internal/metafetch/`, add ONE unexported fail-open resolver (e.g.
   `func scoringKnobs() resolvedScoringKnobs`) that reads `config.AppConfig.MetadataScoring` with
   PER-KNOB unset semantics (spec C2, normative — reviewed; a blanket zero-as-unset rule made 0
   unreachable for knobs where 0 is legitimate):
   - Multiplicative boosts (four transcription, three series, `RichMetadataFieldBonus`): plain
     `float64`; zero/missing → legacy literal, NEVER "multiply by zero".
   - `F1MinScore` / `CompilationPenalty` / `RichMetadataBonusCap`: `*float64` in the config
     struct; nil → legacy literal, but an EXPLICIT 0 is honored (0 is a real operator value —
     e.g. `F1MinScore=0` disables the reject floor).
   - Duration tier value arrays: length must equal the built-in table; mismatch → whole built-in
     table (fail-open, logged).
   Replace each literal with a resolver read. Do not change any function signature. Keep BOTH
   mechanisms (viper.SetDefault covers YAML/env loads; the nil/zero-gated resolver covers
   zero-value structs built without viper, e.g. in tests) — this is deliberate, not redundancy.
5. Repeat the same edge-semantics statement in the tests: assert that a zero-value
   `MetadataScoringConfig` produces bit-identical scores to the pre-extraction fixtures; that an
   EXPLICIT 0 in each pointer knob takes effect (`TestScoringConfigExplicitZeroHonored`); and that
   a mismatched-length `DurationTierMultipliers`/`DurationTierScores` falls back to the built-in
   table.
6. Purely value-preserving transform: no new scoring paths, no reordering of boosts, no changes to
   `service_search.go` beyond the three literal swaps.
7. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added).
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green.
go test ./internal/metafetch/ ./internal/config/ -v -run 'Scoring|Duration'
```

## Acceptance criteria

- [ ] `grep -n "TranscriptionTitleExactBoost" internal/config/config.go` hits (struct + 2 viper sites → ≥3 hits)
- [ ] `grep -n 'score \*= 2.0' internal/metafetch/service_scoring.go` returns 0 hits; same for `score \*= 0.15` and `c.Score \*= 2.0` in `service_search.go` (literals replaced by config reads)
- [ ] `TestScoringConfigDefaultsMatchLegacyLiterals` green — zero-value config → bit-identical scores (the gate's zero-behavior-change requirement, mechanically proven)
- [ ] Zero/missing MULTIPLICATIVE knob resolves to legacy default, never to "multiply by zero" (explicit test case)
- [ ] `TestScoringConfigExplicitZeroHonored` green — explicit 0 in `F1MinScore`/`CompilationPenalty`/`RichMetadataBonusCap` (pointer knobs) takes effect; nil → legacy default (0 is reachable)
- [ ] `grep -n "DurationTierEdges" internal/config/config.go` returns 0 hits (tier structure stays in code)
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci`, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(config): extract metadata scoring literals into MetadataScoringConfig (INIT-3-T1)

All scoring weights (transcription boosts, compilation penalty, rich-metadata
bonus, F1 floor, duration tiers, series boosts) become tunable config with
defaults equal to today's literals — bit-identical scoring until an operator
tunes them (equivalence fixture-proven). Per-knob fail-open resolver:
missing multiplicative knobs fall back to legacy literals; pointer knobs
(F1 floor, compilation penalty, bonus cap) keep an explicit 0 reachable
(nil = unset). Duration tier structure stays in code; only tier values are
config.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/metadata-matching-extract-scoring-config
gh pr create --fill
gh pr merge <number> --rebase
```

(When running under a coordinated sweep, STOP after commit — the coordinator owns push/PR/merge.)

## Idempotency / Rollback

If `grep -n "TranscriptionTitleExactBoost" internal/config/config.go` hits AND
`grep -n 'score \*= 2.0' internal/metafetch/service_scoring.go` returns 0, the transform is
already done — run the acceptance checks instead of re-applying. Rollback = revert the commit;
scoring returns to literals. Because defaults are value-identical, rollback has zero behavioral
effect unless an operator had already tuned a knob (their tuning is config data, untouched by the
revert).
