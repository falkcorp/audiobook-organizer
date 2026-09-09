### Fixed

- `GET /api/v1/audiobooks/quarantined` no longer reports `total: 0` alongside a
  non-empty list of books when the count fails. The handler discarded the error
  from `CountQuarantinedBooks` (`total, _ :=`), producing a response a caller
  could not distinguish from "there are no quarantined books"; the list scan
  immediately above it already failed loudly for the same class of error. The
  endpoint now returns 500 when the count fails, and the behaviour is pinned by
  a test that fails if the error is discarded again.

### Changed

- Corrected a false comment on `CountQuarantinedBooks`, which claimed the scan
  counted "without deserializing the full book object" while doing a plain
  `json.Unmarshal` into a full `Book` — the claim had been wrong since the
  file's first commit. Both quarantine store methods now document their real
  cost: each walks the entire `book:*` keyspace, measured at ~4.2s apiece on
  production (~8.5s for the endpoint, to find 7 quarantined books), and there is
  no quarantine index. Documented rather than optimized on purpose: the endpoint
  has no frontend caller, so the note records the cost and the trigger for
  revisiting it instead of adding an index nothing currently needs.
