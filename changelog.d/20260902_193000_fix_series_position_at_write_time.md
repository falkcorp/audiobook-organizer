### Fixed

- The book's position in a series is no longer written into the series *name*.
  Series named `Discworld 05` or `Nameless Sovereign #5` are now stored as
  `Discworld` and `Nameless Sovereign`, with the number moved into the book's
  `series_sequence` rather than deleted. This runs
  on all four write paths (metadata apply, library scan, iTunes import, and the
  series-normalize maintenance pass), so new contamination stops at the source.
  A number that cannot be attributed with confidence -- an embedded or leading
  one with no `book`/`vol`/`#` keyword vouching for it, such as `86—EIGHTY-SIX`
  -- is flagged for review instead of stripped, because stripping it produces
  garbage. A number in brackets (`Dragon Born [04]`, `The Hollows (7)`) is a
  third case: the brackets come out of the name, but the number is deliberately
  *not* written into `series_sequence`. In this library roughly 180 of the 198
  bracketed rows measured turned out to be fragments of a single split-up book
  rather than series positions, so the number is far more likely to be wrong
  than right -- and an empty position is visible and fixable, while a wrong one
  is not. Every strip is logged with the book id, the original and cleaned
  name, the extracted position, and which rule matched.
