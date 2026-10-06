### Added

- **Repairs: "Series named after the author" (`maintenance.author-named-series`).**
  Lists books linked to a series whose name is just their author's name (a
  "Brandon Sanderson" series holding Sanderson's books), one row per book,
  judged by the rule the folder parse uses since #3775
  (`foldernames.JudgeSeriesBooks`, now exported). A row is applicable only
  when the author also has a distinct, primary-credited book outside the
  series. Held with the reason: real series sharing an author's name,
  series whose author row is the junk side, numbered series (a pen-name
  house series such as Nick Carter looks like this), co-authored books,
  locked series and books with applied or "no match" metadata. Doctor Who /
  Big Finish / Torchwood and iTunes-owned books are skipped by the framework
  guards. Applying approved row ids marks the series row held, removes the
  series link and position (history rows after the write, then op-journal
  rows), refuses a row whose link moved since the plan, and enqueues one
  unforced metadata candidate fetch for the changed books (fetch only).
  Undo with "undo last apply" or the apply operation's revert.
- Repairs framework: an optional `AfterApplier` hook runs once after a write
  run with the books of its applied and partially applied rows, also after a
  cancel; its result is in the apply result's `follow_up` /
  `follow_up_error`.
- `Series.HeldBy` and `database.SeriesHolder`: a held series row is never
  deleted (`DeleteSeries` refuses it with `ErrSeriesHeld`; the series prune
  passes it over), so an undo can link books back to it. The emptied junk
  rows stay until a later decision releases them.

### Fixed

- Undo never links a book to a series row that no longer exists: "undo last
  apply" and single-field undo refuse it, with the reason in
  `failed_reasons` (the operation revert already refused it).
- A series position is restored only into the series it was numbered in:
  every undo path pairs `series_sequence` with the `series_id` change of the
  same batch or operation, whatever order the rows are walked in. The
  operation revert now restores `series_sequence` (it restored only
  `series_id`).
