- [ ] **SERIES-MEMBERSHIP-CORE-AND-MOVEIN-MASK: two leftovers from the residual
      series-membership hoist (#3377).**
      1. `executeSeriesNormalizeCore` (`internal/server/duplicates_helpers.go`) still
         calls `GetBooksBySeriesIDCore` once per action to build the organize
         worklist. That is a full `book:` Pebble scan per series once memdb is tainted.
         There is no bulk Core capability, and deriving Core from AllVersions would
         re-implement the primary-version filter. Fix: add `GetBooksBySeriesIDsCore`
         (MemStore, PebbleStore, MockStore) plus a parity test against the per-series
         getter, like #3374's `GetBooksBySeriesIDsAllVersions`.
      2. The series-denumber apply loop reads `refCounts` once at run start. When an
         earlier plan moves books INTO a later plan's source series (a reachable case:
         the IntoName sort is byte-wise but `GetSeriesByName` is case-insensitive, e.g.
         "Alpha 05 07" before "alpha 05"), those moved-in books raise `len(books)`, so
         they can mask a trashed reference in `refCounts[FromID] <= len(books)`. The
         source then gets deleted under the trashed row. The old per-series read had
         this gap too, so it was not introduced by the hoist. Fix: track moved-in
         counts per series and compare `refCounts[FromID] + movedIn[FromID]`.
