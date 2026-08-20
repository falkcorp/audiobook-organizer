<!-- file: changelog.d/20260820_073000_unified_review_workspace.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b8e2f04-7c31-4a96-b0d5-1e7a3c9f4628 -->
<!-- last-edited: 2026-08-20 -->

### Added

#### Every review lane can now explain how it reached its verdict

The dedup lane's score-breakdown panel has been promoted to a shared
`EvidencePanel` used by all three review lanes, and the metadata scorer now
records the derivation of the number it ships rather than only the number.

The panel does **not** render the three lanes the same way, because their scores
are not the same kind of thing. Evidence is a discriminated union over how the
number was actually computed, and each kind gets the encoding its arithmetic
supports:

- **dedup** is a weighted sum of calibrated signals, so it keeps the stacked
  share bar. That bar asserts "these parts sum to the whole", which is true here.
- **metadata** is `(base × factors) + terms`, and can be replaced outright by an
  LLM rerank or a direct ASIN match. A multiplicative factor has no share of a
  total, so it renders as an ordered waterfall — a replay of the real pipeline.
  Feeding it to the share bar would have produced segments summing to nothing
  meaningful, which is worse than no bar at all because it still looks complete.
- **review queue** reaches its recommendations by rules over observed counts
  rather than arithmetic on them, so it renders named facts and no bar. Drawing
  one would be inventing a computation that does not exist.

The metadata backend gained `score_breakdown` on every candidate: the whole
chain is recorded, not just the base scorer, including the author/narrator/
series/duration multipliers and the wholesale substitution performed by rerank
and direct-ASIN matches. "The breakdown explains the score" is enforced as a
property — replaying the recorded steps must reproduce the shipped score — and
is asserted over a 160-combination cross product plus the real search path.

### Fixed

#### The evidence consistency check could not detect the failure it existed for

`Math.abs(recomposed - score) > 1e-6` is `false` when `recomposed` is `NaN`,
because `NaN` is unordered against every value. A breakdown that could not be
replayed at all was therefore reported as a *verified* derivation and rendered
as a panel of empty rows — confidently blank, which is the worst available
outcome for a feature whose entire job is showing how a verdict was reached.

`NaN` arrives by a realistic route: the frontend's `MetadataScoreStep` is a
hand-written mirror of Go's `ScoreStep`, and a renamed JSON tag makes every
`operand` arrive `undefined`. That seam is now covered by a fixture the Go test
emits from the real search path and the frontend suite consumes, so drift breaks
one side or the other instead of reaching a reviewer.

Writing that test surfaced a second defect at the same seam: rendering a drifted
payload threw on `undefined.toFixed()`, unmounting the entire review screen over
one malformed row. Worse, `NaN%` is an invalid CSS length, so the browser
discarded the width and the unparseable step drew as a **full** bar — reading as
the strongest signal on screen. Unreadable steps now render as an em-dash at
zero width with the incompleteness explained.

#### Sort state, lock state, and icon-only buttons were invisible to assistive tech

Upgrading MUI surfaced that a number of controls had no accessible name at all,
and that two pieces of state were carried *only* by which icon glyph rendered:

- the library's sortable column headers now expose `aria-sort`;
- the metadata lock toggle now exposes `aria-pressed`;
- the book-card overflow menu, the file-browser path controls, the breadcrumb
  root, the search clear button and the canonical-name edit button now have real
  accessible names.

This was found because the end-to-end suite had been reaching these controls
through `data-testid` attributes that MUI stamps on its own icons. That is an
internal debug affordance, not public API, and MUI 9 emits it only in
development builds — so every such selector silently matched nothing against the
production bundle the suite actually tests. The selectors now use accessible
names and ARIA state, which is both what a screen reader consumes and what
cannot vanish in a dependency bump.

### Changed

- The E2E workflow can regenerate visual-regression goldens on demand
  (`update_snapshots`) and uploads failure artifacts. Goldens are per-platform,
  so the Linux ones cannot be produced on a developer's macOS machine; without
  this there was no supported way to update them at all.
