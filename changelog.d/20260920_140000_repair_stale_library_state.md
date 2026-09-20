- New `maintenance.repair-library-state` op. Audiobookshelf lists only books that
  are both primary and `library_state == "organized"`, so a book that was organized
  and later had its state stamped back to the scanner's creation default is invisible
  in the app while its files sit exactly where they belong. A prod census on
  2026-09-20 found ~11,757 such books — 93% of them already in the canonical tree.
  The op writes the one stale column and **moves no files**, gated on
  `organized_file_hash` as evidence the book really was organized. It defaults to a
  dry run, never touches `organized_source` rows or iTunes paths, and repairs the
  scanner-derived `suspicious` state only when explicitly asked.
