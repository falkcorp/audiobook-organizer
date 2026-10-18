- [ ] **01-P81E-TAGSTORE** After 01-T1 merges, remove the whole author and series
      tag store surface: 16 methods (8 reader, 8 writer) across `AuthorTagReader`,
      `AuthorTagWriter`, `SeriesTagReader` and `SeriesTagWriter` in
      `internal/database/iface_tags.go`, plus their PebbleStore implementations
      and mocks. 01-P81e retired the HTTP routes. Ten of the methods have no
      non-test caller today: `GetAuthorTags`, `ListAllAuthorTags`,
      `GetAuthorsByTag`, `RemoveAuthorTag`, `SetAuthorTags` and the five series
      equivalents. The other six are only called from the `EnsureSingleton*Tag`
      helpers in `tag_helpers.go`, which 01-T1 deletes.
