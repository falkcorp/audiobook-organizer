### Added

- **Repairs: "Series named after the author" (`maintenance.author-named-series`).**
  Lists every book linked to a series whose name is just its author's name
  (a "Brandon Sanderson" series holding Sanderson's books), one row per book,
  judged by the same rule the folder parse uses since #3775
  (`foldernames.JudgeSeriesBooks`, now exported): junk only when every book in
  the series is by that same-named author and the author has books outside it.
  Real series that share an author's name, series whose author row is the junk
  side, co-authored books, locked series and books with applied or "no match"
  metadata are listed held with the reason; Doctor Who / Big Finish /
  Torchwood and iTunes-owned books are skipped by the framework guards.
  Applying approved row ids removes the series link and position through the
  Writer (history rows after the write, then op-journal rows), refuses a row
  whose link moved since the plan, and enqueues one forced metadata candidate
  fetch for the changed books (fetch only). Undo with "undo last apply" or the
  apply operation's revert. Series rows left empty are kept (the revert needs
  them); the plan logs how many.
- Repairs framework: an optional `AfterApplier` hook runs once after a write
  run with the applied books; its result is in the apply result's
  `follow_up` / `follow_up_error`.

### Fixed

- The operation revert restores `series_sequence` along with `series_id`.
