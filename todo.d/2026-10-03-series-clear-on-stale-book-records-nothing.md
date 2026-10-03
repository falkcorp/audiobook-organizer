- [ ] **SERIES-CLEAR-STALE-NO-TRACE** A top-level `series_name: ""` edit of a
      book whose `series_id` is already nil (but whose stored row still has the
      embedded `Series` object, so GET shows a series) records no history and
      no lock. `SeriesID` goes nil to nil, so `RecordBookEditHistory` sees no
      change, `Series` is not a tracked column, and the field extractor writes
      no lock for `""`. Verified 2026-10-03 against a real Pebble store. The
      web BookDetail dialog is NOT affected: a dirty series field also sends
      `overrides.series_name = {value: "", locked: true}`, which records an
      override history row and a lock. Any client that sends only the
      top-level key (API, batch) leaves no trace. `maintenance.relink-stale-series`
      then cannot tell that clear from a lost link (its relink rows are risk
      review for this reason). PR #3698 is reworking this path in
      `service_mutation.go`. Done means: a series clear always records a
      history row (field `series`, new value `""`) and a `series_name` lock,
      even when `SeriesID` was already nil, with a test on a stale book.
