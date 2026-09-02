### Fixed

- The book's position in a series is no longer written into the series *name*.
  Series named `Discworld 05`, `Nameless Sovereign #5` or `Dragon Born [04]` are
  now stored as `Discworld`, `Nameless Sovereign` and `Dragon Born`, with the
  number moved into the book's `series_sequence` rather than deleted. This runs
  on all four write paths (metadata apply, library scan, iTunes import, and the
  series-normalize maintenance pass), so new contamination stops at the source.
  A number that cannot be attributed with confidence -- an embedded or leading
  one with no `book`/`vol`/`#` keyword vouching for it, such as `86—EIGHTY-SIX`
  -- is flagged for review instead of stripped, because stripping it produces
  garbage. Every strip is logged with the book id, the original and cleaned
  name, the extracted position, and which rule matched.
