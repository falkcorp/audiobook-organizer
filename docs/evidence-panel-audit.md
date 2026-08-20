<!-- file: docs/evidence-panel-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e8b2f65-9a13-47d0-8c52-6b0f1d94a3e7 -->
<!-- last-edited: 2026-08-20 -->

# Evidence panel audit — can each lane explain itself?

PLAN.md Phase 5 promotes `ScoreBreakdownPanel` to a shared `EvidencePanel` across all
three lanes, and flags one risk against it:

> Needs a backend audit to confirm they are retained rather than collapsed into a scalar
> before serialization — **if they are discarded, exposing them is a backend change and
> gets its own step.**
>
> Risk: this is the one place the unification might require backend work rather than pure
> frontend consolidation. Audit before committing to a lane-complete date.

This is that audit. **The risk is real for one lane of the three.**

## Result

| Lane | Per-signal evidence | Reaches the browser | Work required |
|---|---|---|---|
| **Dupes** | Yes — `DedupScoreBreakdown` | Yes | None |
| **Regroup** | Yes — `RecommendationEvidence` | Yes, but undeclared in TS | Frontend adapter only |
| **Metadata** | **No — collapsed to a scalar** | Only fragments | **Backend change** |

## Dupes — already correct

`DedupScoreBreakdown` carries `signals[]`, each with `kind`, `value`, `weight`, `evidence`
and `primary`, plus `formula` and `skipped_reason`. This is the shape the other two lanes
should be measured against, and it is why the dedup lane can already answer "why did it
conclude that".

## Regroup — the data is there, the frontend just can't see it

`RecommendationEvidence` (`internal/itunes/service/fs_regroup_shape.go:229`) is fully
serialized and embedded in the group payload as `json:"recommendationEvidence"`
(`internal/plugins/maintenance/regroup_shattered_ai.go:101`). It carries real numbers:
`members`, `durationsKnown`, `bookLengthMembers`, `medianKnownSec`, `longestKnownSec`,
`distinctStems`, `numberedMembers`, `structure`.

It is **not declared anywhere in `web/src/services/api.ts`**, so nothing in the frontend
can consume it today. That is an adapter and a type declaration — no backend work.

Note the shape difference: this is a set of named scalars, not a weighted `signals[]`.
The adapter maps facts to rows; there are no weights to render, so the contribution bar
does not apply to this lane and the panel must degrade to the per-signal rows.

## Metadata — the signals are computed, then thrown away

This is the lane the plan warned about, and it is confirmed.

`ScoreOneResult` (`internal/metafetch/service_scoring.go:636`) returns a single
`float64`. Everything feeding it is a local intermediate that is never retained:

- **F1 base** — `computeF1Base`, the title/author fuzzy match. The dominant signal.
- **Compilation penalty** — multiplicative, `isCompilation(r.Title)`.
- **Length penalty** — multiplicative, when the result title is >1.5× the search length.
- **Rich-metadata bonus** — additive, +bonus each for description / cover / narrator /
  ISBN, capped at `RichMetadataBonusCap`. Four distinct signals summed into one number
  before anything can observe them individually.
- **Transcription boost** — `transcriptionBoost` applies a multiplier.

By the time a candidate is serialized, all of the above exists only as its effect on
`score`. There is no `signals[]` to adapt.

### What does survive

Three components are retained on the struct (`internal/metafetch/service.go:190-230`):

- `DurationScore float64` — `json:"duration_score,omitempty"`, documented as "the additive
  score component from the duration signal", with published bands (<5% → +20, <10% → +15,
  <20% → +10, >50% → −10, >100% → −20).
- `DurationMismatch bool` — `json:"duration_mismatch,omitempty"`.
- `TranscriptionBoosted bool` — `json:"transcription_boosted,omitempty"`, a flag with no
  magnitude.

**Separate finding:** `duration_score`, `duration_mismatch` and `category_tags` are all
serialized by the backend but are **not declared on the TypeScript `MetadataCandidate`
interface** (`web/src/services/api.ts:2895`). One genuine per-signal contribution already
crosses the wire today and nothing reads it.

## What this means for Phase 5

The metadata lane cannot show a truthful weighted breakdown without a backend change.
Two options, and the difference matters:

1. **Do it properly.** Have the scorer return a breakdown alongside the score — the
   components are already computed and named, so this is retention and plumbing rather
   than new analysis. The risk is that `ScoreOneResult` is load-bearing and pinned by
   golden fixtures (`service_scoring_test.go`) whose acceptance criteria state that "zero
   additive-score cells may change", so the total must stay bit-for-bit identical while
   the breakdown is added beside it.

2. **Ship a partial panel** from `duration_score`, `transcription_boosted`, provider and
   ratings. Cheap, but it would render a contribution bar that omits the *dominant*
   signal (F1 base), which is worse than showing nothing: a breakdown that looks complete
   and isn't will mislead exactly when it matters.

**Recommendation: option 1, as its own step before the metadata lane is wired.** The
explainability panel is the feature most worth getting right — it is the one the user
singled out — and a partial version undermines the reason for promoting it in the first
place.

The dupes and regroup lanes are unblocked and can proceed independently.
