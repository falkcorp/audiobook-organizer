<!-- file: docs/agent-tasks/metadata-matching/TASK-04-scoring-calibration-harness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 86cd8415-e3a2-425c-b37a-de2d59d50df0 -->
<!-- last-edited: 2026-07-10 -->

# TASK-04 — Read-only metadata-scoring calibration harness op (INIT-3-T1 harness)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** none (new files in `internal/plugins/metafetch/`; the registration edit is one blank-import line in `internal/plugins/plugins.go` — resolved by review, mirroring how `internal/plugins/dedup` registers; not shared with the only wavemate TASK-03, which touches `web/src/` exclusively).

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · op-plugin subagent · **Why:** new op mirroring an existing sibling's structure, but the replay/sweep logic needs real judgment · **Depends on:** TASK-02

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-scoring-calibration-harness" -b agent/metadata-matching-scoring-calibration-harness origin/main
cd "$REPO/.worktrees/metadata-matching-scoring-calibration-harness"
git rebase origin/main
# TASK-02 must already be merged (DEPENDENCY GATE — this grep is EXPECTED to fail
# until TASK-02's PR lands on origin/main; as of 2026-07-10 it has NOT landed):
grep -n "TranscriptionTitleExactBoost" internal/config/config.go || { echo "STOP: TASK-02 not merged yet"; exit 1; }
```

**Coordinator dispatch gate:** do NOT dispatch this brief until TASK-02 has actually merged.
Verify before assigning:

```bash
grep -n "TranscriptionTitleExactBoost" /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/internal/config/config.go
# zero hits = TASK-02 not merged = HOLD this brief; do not treat the zero hits as anchor drift.
```

## Goal

Add a READ-ONLY calibration op `metafetch.calibrate-scoring` that replays persisted metadata
candidate caches against books whose metadata was actually applied, sweeps the TASK-02 knobs, and
REPORTS which knob values would rank the applied candidate first — analogous to the existing
`dedup.calibrate-embedding-thresholds` harness. It NEVER writes config, never mutates books or
caches. MIRROR the sibling op's file structure, doc-comment discipline, params/report shape, and
registration mechanism — do not invent a new op framework.

## Background (verify before editing)

- The sibling to mirror: `internal/plugins/dedup/calibrate_embedding_thresholds.go` — a read-only
  sdk-plugin op with an extensive "why this op exists / READ-ONLY / deferred owner-gated follow-up"
  package comment, params via JSON (`{"def_id":"dedup.calibrate-embedding-thresholds","params":{...}}`),
  and a sweep+report body. Registration mechanism (resolved by review): the dedup package
  self-registers via `internal/plugins/dedup/register.go` (`serviceregistry.Register` in `init()` +
  a `PostInit` that registers op-defs against the container's opregistry), and the package is
  blank-imported in `internal/plugins/plugins.go:16`. Mirror EXACTLY this: new
  `internal/plugins/metafetch/register.go` + one blank-import line in `internal/plugins/plugins.go`.
- Ground truth for "correct match": books whose `MetadataSourceHash` is set (stamped at metadata
  apply time — `internal/metafetch/service_apply.go` sets `book.MetadataSourceHash`) AND that
  still have a `MetadataCandidateCache` row — read it via the Service accessor
  `GetCachedCandidates(bookID)` (`internal/metafetch/cache.go`, the anchor grep below; it wraps
  the store's underlying row read — there is no Service method named `GetMetadataCache`), where
  one cached candidate corresponds to the applied source/identity.
- **Circularity caveat (reviewed, MUST appear in the report output):** most applied candidates
  were CHOSEN by the current scorer, so top-1 accuracy over the full set is biased toward
  re-deriving today's weights. Segment results by apply origin where determinable
  (manual/override applies vs auto-applies) and present the manual segment as the primary
  non-circular signal; if no manual segment is determinable, say so in the report and flag the
  whole sweep as circular-biased.
- Knobs to sweep (read from `config.AppConfig.MetadataScoring`, added by TASK-02 — exact field
  names, so no spec lookup is needed once TASK-02 has merged): `TranscriptionTitleExactBoost` /
  `TranscriptionTitleSubstrBoost` / `TranscriptionAuthorBoost` / `TranscriptionNarratorBoost`
  (`float64`, defaults 2.0/1.4/1.6/1.4); `CompilationPenalty` (`*float64`, default 0.15);
  `RichMetadataFieldBonus` (`float64`, default 0.05); `RichMetadataBonusCap` (`*float64`, default
  0.15); `F1MinScore` (`*float64`, default 0.35); `SeriesNameMatchBoost` /
  `SeriesNumberExactBoost` / `SeriesNumberWrongPenalty` (`float64`, defaults 1.4/2.0/0.5);
  `DurationTierMultipliers` / `DurationTierScores` (`[]float64`, defaults = TASK-01's
  `durationTiers` table). Sweep a small grid around the defaults (e.g. ±50% in 4 steps per knob, one knob at a
  time — one-factor-at-a-time is fine for v1; say so in the report). Sweep points for the pointer
  knobs (`F1MinScore`/`CompilationPenalty`/`RichMetadataBonusCap`) MAY include 0 — 0 is a
  reachable operator value (spec C2), so a 0 recommendation is applicable, not clamped.
- CLAUDE.md concurrency mandate: the replay loop iterates a whole-library-scale collection →
  bounded worker pool required. Copy-from source for the pool shape:
  `internal/plugins/acoustid/backfill.go` (`registry.RunItems`).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "READ-ONLY calibration harness" internal/plugins/dedup/calibrate_embedding_thresholds.go
  grep -n "serviceregistry.Register" internal/plugins/dedup/register.go   # sibling registration, ≥1 hit
  grep -n "plugins/dedup" internal/plugins/plugins.go                     # sibling blank import, 1 hit
  # ^ mirror EXACTLY this mechanism: new register.go + blank import in plugins.go
  grep -n "registry.RunItems" internal/plugins/acoustid/backfill.go   # pool copy-from, ≥1 hit
  grep -n "SourceHash" internal/metafetch/service_apply.go            # apply-time hash stamping
  grep -n "func (mfs \*Service) GetCachedCandidates" internal/metafetch/cache.go
  grep -n "TranscriptionTitleExactBoost" internal/config/config.go    # TASK-02 knobs present — DEPENDENCY GATE, not a drift anchor
  ```
  Zero hits on any of these = STOP and report drift — EXCEPT the `TranscriptionTitleExactBoost`
  line, which is the TASK-02 dependency gate: zero hits there means TASK-02 has not merged yet;
  return the brief to the coordinator as BLOCKED-on-TASK-02, do not report it as drift.

## Step-by-step

1. Study the sibling file end-to-end (package comment, params struct, report struct, progress
   reporting, registration). Create `internal/plugins/metafetch/calibrate_scoring.go` with the
   same skeleton: op ID `metafetch.calibrate-scoring`, params `{sample_limit int, sweep_steps int}`
   with sane defaults, a package comment carrying the same READ-ONLY / never-applies discipline.
2. Build the evaluation set: enumerate books with non-empty `MetadataSourceHash` and a cache row;
   skip (and COUNT, never fail) books with missing/empty caches or unparseable candidates. Edge
   semantics: a book with no identifiable "applied candidate" among the cached ones is SKIPPED and
   counted in the report as `unmatchable`, not treated as a ranking failure.
3. Replay: for each book, re-rank its cached candidates under the current knobs and under each
   sweep point; record whether the applied candidate ranks first (top-1 accuracy) and its rank.
   Run the per-book loop through a bounded pool (mirror `registry.RunItems` options from the
   acoustid sibling; CPU-bound → NumCPU limit).
4. Report (JSON in the op result, like the sibling): current-knob top-1 accuracy, per-knob sweep
   table, skip counts by reason, sample size, apply-origin segmentation (manual vs auto) with the
   circularity caveat from Background stated verbatim. NO writes: the op must not call any store
   mutation.
5. Register the op: create `internal/plugins/metafetch/register.go` mirroring
   `internal/plugins/dedup/register.go` (`serviceregistry.Register` in `init()` + `PostInit`
   op-def registration) and add the blank import of the new package to
   `internal/plugins/plugins.go` (bump its header).
6. Tests in `calibrate_scoring_test.go`: fixture books+caches → known ranks; skip-counting for
   empty cache; report shape; a `-race` run of the pooled loop.
7. Anti-over-suppression: N/A (read-only reporting; no filter/guard/veto/skip/dedupe path added to
   any production matching flow).
8. Bump headers on every touched file; new files get fresh 4-line headers with new guids.

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green.
go test ./internal/plugins/metafetch/ -race -v
```

## Acceptance criteria

- [ ] `grep -rn "metafetch.calibrate-scoring" internal/ --include=*.go` hits (op + registration)
- [ ] `grep -n "Upsert\|UpdateBook\|Delete" internal/plugins/metafetch/calibrate_scoring.go` returns 0 hits (read-only proven mechanically)
- [ ] Pooled loop present: `grep -n "RunItems\|SetLimit" internal/plugins/metafetch/calibrate_scoring.go` hits
- [ ] Tests cover: known-rank fixtures, `unmatchable` skip counting, report shape; `-race` green
- [ ] Report carries the circularity caveat + apply-origin segmentation (manual vs auto), asserted in the report-shape test
- [ ] `grep -n "plugins/metafetch" internal/plugins/plugins.go` hits (blank import registered)
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci`, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(metafetch): read-only scoring calibration harness op (INIT-3-T1)

metafetch.calibrate-scoring replays persisted candidate caches against
applied metadata (MetadataSourceHash ground truth), sweeps the TASK-02
scoring knobs, and reports top-1 accuracy per sweep point. Mirrors the
dedup.calibrate-embedding-thresholds READ-ONLY discipline: reports only,
never writes config or data. Pooled per-book replay per the concurrency
mandate.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/metadata-matching-scoring-calibration-harness
gh pr create --fill
gh pr merge <number> --rebase
```

(When running under a coordinated sweep, STOP after commit — the coordinator owns push/PR/merge.)

## Idempotency / Rollback

If `grep -rn "metafetch.calibrate-scoring" internal/ --include=*.go` hits, this task is already
applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the op
is read-only so no data, config, or scoring behavior is affected by adding or removing it.
