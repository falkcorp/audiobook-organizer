### Fixed

- `franchise.MatchAll`, which runs on every scanned book and dedup
  candidate, now skips each rule's regular expression unless one of that
  rule's literal keywords occurs in the text, and `Detect` no longer runs
  `MatchAll` twice on a title that only a weak rule names. Results are
  unchanged (a test compares them against the unfiltered matcher); the
  matcher is about 50 times faster on a 200-value corpus.
