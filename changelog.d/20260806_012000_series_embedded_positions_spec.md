<!-- file: changelog.d/20260806_012000_series_embedded_positions_spec.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d3e7182-4c05-46ba-b71f-e0328a6c95d4 -->
<!-- last-edited: 2026-08-06 -->

### Added

- **Design + implementation plan for series names that carry a book position in
  an embedded (non-trailing) shape** — owner item 4. Documentation only.

  Whole-population measurement on production: of 12,201 distinct series names
  covering 32,119 books, **982 books across 769 names** carry a position the
  current parser cannot see, in four shapes — `<Series> N: <Title>` (343 books),
  `<Series> (N)`/`[N]` (307), `N - <Title>` (271), and
  `<Series> Book N: <Title>` (61).

  The spec records why this is riskier than the trailing case already handled:
  a leading or embedded number is frequently part of the real title.
  `86—EIGHTY-SIX` (17 books) and `5-Minute Sherlock` are genuine series names,
  while `08. Battle for the Abyss` and `11. Fallen Angels` are genuine positions
  — identical shape, opposite meaning. `The Demon Wars Saga [07] Immortalis [02]`
  carries two candidate numbers and a naive last-match takes the wrong one.

  Design answer is confidence tiers rather than one regex: keyword-introduced
  positions auto-apply, single-bracket positions auto-apply, bare leading numbers
  emit a review hold, and multi-number names are refused outright. The
  `IsJunkSeriesBase` guard — which stopped 285 bad merges in the earlier dry run
  — is extended in the same commit as the parser, never after.
