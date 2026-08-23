- [ ] **BOOK-ID-RANGE: measure whether any `book:` key has a non-digit first byte, then
      widen or document the `book:0`..`book:;` bound.** ~20 sites scan book records with
      that hand-written range, which admits only `'0'`-`'9'` and `':'` as the first byte
      after the colon. All four ID-minting sites produce ULIDs (leading `0`-`7`), so it
      holds today — but `CreateBook` only mints when `book.ID == ""`, so importers and
      restore paths can supply their own, and `pebble_store.go` describes the same
      keyspace as "below any UUID character (0-9, a-f, '-')", which a UUID-leading id
      would fall outside in BOTH directions. A row outside the range is invisible to the
      legacy-AuthorID pass of the unfiltered author ref scan, losing that reference.
      This is a measurement task first: prefix-scan `book:` on the live library for keys
      whose first byte after the colon is not a digit. If any exist, widen both bounds to
      `book:`..`prefixUpperBound("book:")` — the existing `strings.Count(key, ":") != 1`
      filter already excludes secondary indexes over the wider range. Raised in review on
      #2787 and explicitly left unmeasured rather than guessed.
