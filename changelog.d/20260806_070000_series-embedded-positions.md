<!-- file: changelog.d/20260806_070000_series-embedded-positions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a9d17c4-8e3b-46f2-9071-c3d84be5f2a1 -->
<!-- last-edited: 2026-08-06 -->

### Added

- **`maintenance.series-denumber` now reads positions embedded in a series name, not just trailing
  ones.** A series name should name the series, but many carry the book's position instead, which
  splits one series into as many one-book series as it has volumes. The op previously handled only
  trailing positions (`Discworld 05`); it now also reads keyword-embedded
  (`Evil Genius: Book 4: Becoming the Apex Supervillain`), bracketed (`Dragon Born [04]`),
  mid-colon (`Station 64: The Doll Dungeon`) and leading-bare (`08. Battle for the Abyss`) shapes.
  A whole-library census found 769 distinct names in these shapes.

  Each shape carries a **confidence**, because the same string shape means opposite things in real
  data: `86—EIGHTY-SIX` is a genuine series title covering 17 books, while `08. Battle for the
  Abyss` is a genuine Horus Heresy position. Only a keyword-vouched position applies unattended; a
  bracketed one requires `{"applyMedium": true}`; a bare number is **only ever reported** and cannot
  be applied at any setting. Names offering two candidate positions
  (`The Demon Wars Saga [07] Immortalis [02]`) are refused rather than guessed at.

- **`reportPath` parameter on `maintenance.series-denumber`.** Writes every candidate — including
  the tiers that will never be applied — as TSV before any row is touched. A merge creates and
  deletes series rows, so there is no transaction to abort; replaying this file is the only rollback.
  Applying without it now logs a warning.

### Fixed

- **`IsJunkSeriesBase` missed artefacts stranded at the front of a name.** The guard only checked
  suffixes, because the parser only stripped suffixes. Stripping a leading number strands the
  punctuation at the front instead (`. Battle for the Abyss`), where none of those checks could see
  it. Also added bundle words (`Publisher's Pack`, `Omnibus`, `Box Set`) — a pack number is not a
  series position — and now rejects single-character bases. This guard stopped 285 bad merges in an
  earlier production dry run, and the new shapes give it more ways to be reached.
