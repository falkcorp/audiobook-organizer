- [ ] **LIBRARY-READ-STATUS-ALTERNATION-EMPTY** The Library list's `filters=` JSON
      route answers `200` with `count: 0` for a read_status value written as an
      alternation: `[{"field":"read_status","value":"(finished|in_progress)"}]`
      and `"finished|in_progress"` both return an empty list (probed 2026-10-18
      with the `TestLibraryListDateFilters` setup), the "0 reads as none exist"
      failure the handler already guards against for other fields. The cause is
      `internal/audiobooks/filter_compiled.go`: the `read_status` case only checks
      `IsPlainLiteral`, which accepts `(a|b)`, and `matchesPerUserFilter` then
      compares the whole string to one status with EqualFold. `last_played` with
      an alternation is already a 400 ("is not a single value"). Found while
      auditing the consumers of `PerUserFilters` in 07-F5a (#4071); that PR fixed
      the search-DSL path (Bleve, playlists, dynamic collections), which builds one
      OR-group filter, but the Library `filters=` route builds one `FieldFilter` per
      term and has no alternation. Fix properly: either split the alternation into
      an OR-group in `FieldFilter`-to-`PerUserFilter` conversion
      (`internal/audiobooks/filter_date.go`) so it agrees with the DSL path, or
      refuse a value that is not a single known status with a 400 that names the
      valid statuses.
