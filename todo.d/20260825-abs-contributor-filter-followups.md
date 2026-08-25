- [ ] **Check whether `/filterdata`'s SERIES list has the same zero-book
      problem the author list had.** `LibraryFilterData` now sources authors and
      narrators from `contributorIndex`, so both are restricted to contributors
      on a visible book. Series is still built from `GetAllSeries()` — every
      series row in the store. In production 4,975 of 12,854 authors (38.7%)
      had no visible book; nobody has measured the equivalent figure for the
      14,625 series rows.

      It was left alone deliberately: series has no entry in the contributor
      index, so moving it needs its own build path and its own measurement
      rather than a drive-by. Measure first — if the fraction is small the fix
      may not be worth a second full-library pass.

- [ ] **Make `/filterdata` stop walking the whole book keyspace twice to read
      two fields.** `GetDistinctGenres` and `GetDistinctLanguages`
      (`internal/database/pebble_store.go`) each iterate every `book:*` key and
      `json.Unmarshal` the full row to read ONE field — `Genre` and `Language`
      respectively. `publishedDecades` then scans another 5,000 rows.

      Measured against production 2026-08-25: `/filterdata` took 7.17s and
      6.57s on two consecutive calls. The endpoint is now cached, so this is no
      longer on every page load, but the cold rebuild still pays all three
      passes. At minimum the two distinct-value scans should share one pass;
      better would be a projection that does not unmarshal the whole row.
