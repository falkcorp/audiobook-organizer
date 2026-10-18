- [ ] **01-P81E-TAGSTORE** After 01-T1 merges, remove the whole author and series
      tag store surface: about 18 methods across `AuthorTagReader`,
      `AuthorTagWriter`, `SeriesTagReader` and `SeriesTagWriter` in
      `internal/database/iface_tags.go`, their PebbleStore implementations and
      mocks. 01-P81e retired the HTTP routes and 01-T1 deletes the
      `EnsureSingleton*Tag` callers, so no production reader or writer is left.
