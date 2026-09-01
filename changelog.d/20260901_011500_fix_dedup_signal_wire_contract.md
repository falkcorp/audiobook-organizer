### Fixed

#### The duplicates lane's evidence panel rendered blank because the TypeScript type described a payload the backend has never sent

`DedupSignal` in `web/src/services/api.ts` declared `{kind, value, weight, evidence, primary}`.
The Go struct it models — `models.Signal`, `internal/models/dedup_score.go` — has the
JSON tags `{kind, raw, confidence, evidence, fp_version}`, and there is no custom
`MarshalJSON`. Only `kind` and `evidence` ever lined up. Everything else arrived
`undefined`, with three separate visible consequences:

- every signal row showed `—` instead of a number, because it read `value`;
- every segment of the stacked share bar rendered at `0%`, because it divided by
  `weight` and `barPercent` clamps the resulting `NaN` to zero;
- the signal chips on a duplicates row **never rendered at all**, because
  `primarySignals` filtered on `s.primary` and so always returned an empty array.

Nothing caught it because every fixture hand-wrote the fictional shape behind an
`as unknown as` cast — both unit suites and the Playwright mock. The e2e test was
asserting against a payload production does not produce. Those casts are gone and
the fixtures are now real `DedupSignal`s, so the compiler holds the line; the one
remaining cast fabricates a deliberately malformed payload and says so.

**The stacked share bar is removed from this lane rather than repaired.** The
comment justifying it claimed the dedup score "genuinely IS a weighted sum". It is
not. `ComposeScore` (`internal/dedup/unified/compose.go`) computes

    100 * (1 - PROD(1 - confidence_i)) + SUM(boost_j)    capped at 100

— a noisy-OR product over the primary signals plus bounded additive boosts from the
two supporting ones. There are no weights in it anywhere. A share bar asserts that
its parts sum to the whole, which is the exact reasoning `docs/evidence-panel-audit.md`
used to reject a bar for the metadata lane. The lane now renders one confidence row
per signal with no bar, showing the calibrated `confidence` as the headline number
and `raw` beside it — that order matters, because `models.Signal` states that
`ComposeScore` reads `confidence` while `raw` is kept for auditing.

The evidence union member was renamed `weighted` → `confidence` to match, since a
type named for an arithmetic the data does not have is the same defect in slower
form. The false justification appeared in five places (`types.ts`, `adapters.ts`,
`lanes/dupes.ts`, and two test comments); all five now state the real formula.

`primarySignals` derives the primary/supporting split from the signal kind instead,
via a new `isPrimaryKind` — the supporting kinds are exactly `duration` and
`folder_path`, per `isSupportingKind` in `internal/dedup/unified/score.go`. This is a
**duplicated rule, and a stopgap**: the wire format does not serialize the
classification at all, so the frontend cannot read the answer. A follow-up should add
`primary` to `models.Signal` and collapse this list into reading it. An unknown kind
is treated as primary, so a new collector appears in the UI rather than vanishing.

Also consolidated the kind→label map, which existed in three copies that had already
drifted — a row chip read "exact file" where the panel read "Exact file hash". There
is now one map, and the longer labels won.
