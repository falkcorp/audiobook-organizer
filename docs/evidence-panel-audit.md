<!-- file: docs/evidence-panel-audit.md -->
<!-- version: 1.2.0 -->
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
| **Regroup** | Yes — `RecommendationEvidence` | Yes — declared + adapted already | Reuse existing adapter |
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

**Correction (v1.2.0):** the first version of this audit said the type was "not declared
anywhere in `web/src/services/api.ts`, so nothing in the frontend can consume it today."
The first half is true and the second half is false, and acting on it would have meant
rebuilding something that already works. `RecommendationEvidence` **is** declared — in
`web/src/lib/reviewPayload.ts:21`, not in `services/api.ts` — and `evidenceFacts()`
(same file) already adapts it into labelled, tooltipped chips with a `warn` flag for the
known-runtime gap. `ReviewQueue.tsx:799` renders them. It is covered by tests.

Regroup's remaining work is therefore **smaller** than stated: not "declare a type and
write an adapter" but "reuse the adapter that exists from the shared panel". Searching
one file and generalising from its absence is what produced the wrong call.

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

## Addendum — the panel has three evidence *kinds*, not one

The recommendation above ("retain the components and plumb them through") is correct
but underspecified, and the missing part changes the JSON contract. Written before
checking how `ScoreBreakdownPanel` actually draws its bar:

```ts
const totalWeight = signals.reduce((sum, s) => sum + Math.max(0, s.weight), 0);
share = Math.max(0, s.weight) / totalWeight;   // width of the segment
```

The bar is a **share of total weight**. That is truthful for dedup, whose score is a
weighted sum. It is meaningless for metadata, whose pipeline is

```
score = (base × compilationPenalty × lengthPenalty) + richMetadataBonus
```

A *multiplicative* factor has no share of a total. To force one you would have to pick
a decomposition (log-space, or delta-from-counterfactual), and neither maps onto
`weight`. This is the same defect the audit already rejected in option 2, reached from
the other direction: option 2 renders a bar that **omits** the dominant signal; a
weight-shaped metadata breakdown renders a bar whose segments **sum to nothing
meaningful**. The second is worse, because it looks complete.

So the three lanes need three renderings, discriminated by the shape of the evidence:

| Lane | Arithmetic | Rendering |
|---|---|---|
| Dupes | weighted sum | stacked share bar (today's panel, unchanged) |
| Regroup | named facts, no weights, no score | fact rows, no bar |
| Metadata | `(base × factors) + terms` | **waterfall** — running total after each step |

A waterfall is the honest visual for a mixed pipeline: each row shows the value going
in, the operation, and the running total coming out. It also gives the decomposition a
**property to test rather than an observation to hope for** — replaying the steps must
reproduce the shipped score exactly:

```
recompose(breakdown) == ScoreOneResult(r, searchWords)
```

Passing the golden fixtures only proves the *tested* cases still agree; that property
proves the breakdown is a decomposition of the number actually shipped, for any input.
If it cannot be written, what the backend is returning is annotations, not a breakdown —
still worth shipping, but then it must not drive a contribution bar.

### Revised order of work

1. **Define the evidence union** (frontend) — a discriminated type over the three kinds.
2. **Dupes + regroup adapters** (frontend only, no Go, no fixture risk). Regroup is what
   forces the no-bar mode to exist.
3. **Metadata backend** — design the Go struct against a panel that already handles
   non-weighted evidence, so the JSON contract is written once.

Committing the Go struct before step 2 risks a second backend change plus an adapter
rewrite, which is why the order matters more than it looks.
