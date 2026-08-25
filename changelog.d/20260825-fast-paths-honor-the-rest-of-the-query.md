### Fixed

- The **file errors** and quick-query views (missing covers, in import path, no
  ISBN, duplicates flagged) now respect the search box, filters, and sort you
  have set, instead of silently ignoring them.

  Opening one of these views while a search or filter was active returned every
  book in that category, not just the ones matching what you had asked for. The
  filter chips stayed lit above the results and the total at the top counted the
  unfiltered set, so there was nothing on screen to indicate the narrowing had
  been dropped. Reaching this took no unusual steps: the "file errors" tile on
  the Dashboard links straight into the library, carrying whatever filters were
  already applied.

  Page counts and totals now agree with what is listed, and results in these
  views also show fingerprint coverage, which the file-errors view previously
  left blank.
