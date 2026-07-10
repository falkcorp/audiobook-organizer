<!-- file: docs/agent-tasks/metadata-matching/TASK-03-scoring-settings-ui.md -->
<!-- version: 1.0.0 -->
<!-- guid: 04bc1077-94ae-472f-bb7a-e677a0eef8c6 -->
<!-- last-edited: 2026-07-10 -->

# TASK-03 — Settings UI for the new metadata scoring knobs (INIT-3-T1 frontend)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** none (web/src files touched by no other task in this workstream). Requires TASK-02 merged — the Go json field names are the contract.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · frontend subagent · **Why:** extends an existing typed settings section; needs React + TS-type care but no novel design · **Depends on:** TASK-02

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-scoring-settings-ui" -b agent/metadata-matching-scoring-settings-ui origin/main
cd "$REPO/.worktrees/metadata-matching-scoring-settings-ui"
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

Expose the scoring knobs added by TASK-02 in the EXISTING Settings surface so an operator can tune
them: extend the `MetadataScoringConfig` TypeScript type, the existing
`MetadataScoringSection.tsx` component, and the existing `metadata_scoring` save wiring. REUSE the
existing section/component/save-path patterns exactly — do NOT create a new settings page, new
API endpoint, or new save mechanism.

## Background (verify before editing)

- The settings section ALREADY exists: `web/src/components/settings/MetadataScoringSection.tsx`
  (~119 lines) with a sibling `.test.tsx`. Mirror its existing input/label/help-text pattern for
  the new fields.
- The TS type `MetadataScoringConfig` lives in `web/src/services/api.ts`; the Settings page loads
  it in `web/src/pages/Settings.tsx` (`if (config.metadata_scoring) setMetadataScoringConfig(...)`)
  and saves via `web/src/hooks/useSettingsHandlers.ts` (the `'metadata_scoring'` case).
- Field names MUST equal the Go json tags from TASK-02 (snake_case, e.g.
  `transcription_title_exact_boost`). Read them from `internal/config/config.go` — that file is
  the source of truth, not this brief.
- **`SCORING_DEFAULTS` values, inlined (the Go legacy literals TASK-02 preserves — no external
  spec lookup needed):**

  | json key | default |
  |---|---|
  | `transcription_title_exact_boost` | 2.0 |
  | `transcription_title_substr_boost` | 1.4 |
  | `transcription_author_boost` | 1.6 |
  | `transcription_narrator_boost` | 1.4 |
  | `compilation_penalty` | 0.15 |
  | `rich_metadata_field_bonus` | 0.05 |
  | `rich_metadata_bonus_cap` | 0.15 |
  | `f1_min_score` | 0.35 |
  | `series_name_match_boost` | 1.4 |
  | `series_number_exact_boost` | 2.0 |
  | `series_number_wrong_penalty` | 0.5 |
  | `bulk_fetch_workers` | 4 |
  | `duration_tier_multipliers` / `duration_tier_scores` | copy the multiplier/score columns of the merged `durationTiers` table: `grep -n 'durationTiers' internal/metafetch/service_scoring.go` (TASK-01 output — present in your worktree because TASK-02, which this task gates on, depends on TASK-01) |

- To cross-check defaults against Go: `grep -n 'MetadataScoring: MetadataScoringConfig{'
  internal/config/config.go` hits TWICE (~1087 and ~1512). The ~1087 block is the viper
  population site (`viper.Get*` calls — NOT literal values); the ~1512 block is the
  literal-defaults block (hardcoded numbers) and is the authoritative place to read default
  values. You normally do not need either — the table above already carries the values.
- Edge semantics (per-knob — spec C2, reviewed): for plain multiplicative knobs an empty/unset
  numeric input may be sent as ABSENT or 0 (backend treats 0 as "use legacy default"). For the
  POINTER knobs (`f1_min_score`, `compilation_penalty`, `rich_metadata_bonus_cap`) **0 is a real
  operator value**: send an EMPTY input as absent/null (never 0), and send an explicit 0 as 0 —
  the backend honors it. Never coerce empty → NaN.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -rn "metadata_scoring" web/src | head -20
  # expect hits in: pages/Settings.tsx (~611), hooks/useSettingsHandlers.ts (~494, ~679, ~800),
  # services/api.ts (~836)
  grep -n "MetadataScoringSection" web/src/components/settings/MetadataScoringSection.tsx
  grep -n 'MetadataScoring: MetadataScoringConfig{' internal/config/config.go   # expect 2 hits (~1087 viper site, ~1512 literal-defaults site — see Background)
  grep -n "TranscriptionTitleExactBoost" internal/config/config.go   # TASK-02 output, source of truth for field list — DEPENDENCY GATE, not a drift anchor
  ```
  Zero hits on any of these = STOP and report drift — EXCEPT the `TranscriptionTitleExactBoost`
  line, which is the TASK-02 dependency gate: zero hits there means TASK-02 has not merged yet;
  return the brief to the coordinator as BLOCKED-on-TASK-02, do not report it as drift.

## Step-by-step

1. Read the merged `MetadataScoringConfig` struct in `internal/config/config.go` and copy the
   exact json tag names for every NEW field (transcription boosts, compilation penalty,
   rich-metadata bonus/cap, F1 floor, series boosts, duration tier VALUE arrays —
   multipliers/scores only, edges are fixed in code and not editable — and bulk-fetch workers;
   there is no per-provider-concurrency field, it is a fixed backend constant).
2. Extend the `MetadataScoringConfig` interface in `web/src/services/api.ts` with those fields,
   all optional (`?: number` / `?: number[]`), matching snake_case names exactly.
3. Extend `web/src/components/settings/MetadataScoringSection.tsx`, mirroring its existing input
   pattern: group the new fields (Transcription boosts / Score adjustments / Series /
   Duration tiers / Bulk fetch), each group with a "Reset to defaults" affordance driven by a
   local `SCORING_DEFAULTS` constants map whose values equal the Go legacy literals (the exact
   values are inlined in the Background table above — use those; the duration tier arrays are
   copied from the merged `durationTiers` table per that table's last row). Render the two
   duration tier VALUE arrays
   (multipliers/scores) read-only-with-edit-toggle or as two aligned numeric lists — the tier
   edges are fixed in code and are NOT rendered as inputs. Keep it simple; no new dependencies.
4. Extend the `'metadata_scoring'` case / payload assembly in
   `web/src/hooks/useSettingsHandlers.ts` so the new fields round-trip on save. Do not touch other
   settings sections.
5. Extend `MetadataScoringSection.test.tsx`: render with a full config, edit one field per group,
   assert the save payload contains the edited keys with numeric values, and assert an empty input
   does not produce `NaN` in the payload. For the three pointer knobs additionally assert: empty
   input → key ABSENT/null in the payload (not 0), and an explicit 0 → `0` in the payload (0 is a
   real value the backend honors).
6. Purely additive: do not modify existing embedding/LLM fields, other sections, or the save
   transport.
7. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added).
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids
   (TSX/TS headers use `//` comment style — mirror the headers already present in each file).

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green. make ci also runs the frontend tests.
cd web && npx vitest run src/components/settings/MetadataScoringSection.test.tsx
```

## Acceptance criteria

- [ ] `grep -n "transcription_title_exact_boost" web/src/services/api.ts` hits
- [ ] `grep -c "transcription_" web/src/components/settings/MetadataScoringSection.tsx` ≥ 4 (all four boost inputs rendered)
- [ ] Component test proves save payload round-trips the new keys and empty input ≠ NaN
- [ ] Pointer-knob semantics tested: empty → absent/null in payload; explicit 0 → 0 (spec C2)
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci`).
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(web): settings UI for metadata scoring knobs (INIT-3-T1)

Exposes the TASK-02 scoring config (transcription boosts, adjustments,
series boosts, duration tiers, bulk-fetch concurrency) in the existing
MetadataScoringSection with per-group reset-to-default. Field names mirror
the Go json tags; empty inputs stay absent so the backend fail-open
defaults apply.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/metadata-matching-scoring-settings-ui
gh pr create --fill
gh pr merge <number> --rebase
```

(When running under a coordinated sweep, STOP after commit — the coordinator owns push/PR/merge.)

## Idempotency / Rollback

If `grep -n "transcription_title_exact_boost" web/src/services/api.ts` hits, this task is already
applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the
backend ignores the absent fields (fail-open defaults), so no config data or scoring behavior is
affected.
