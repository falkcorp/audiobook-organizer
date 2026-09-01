### Changed

- **`buildMetadataProvenance` is one function again.** It existed as two
  byte-identical 79-line copies, in `internal/audiobooks` and `internal/server`,
  differing only in which spelling of the field-state type they named. The server
  copy had zero production callers — its only caller was its own test, the same
  shape as the dead `isInitialToken` removed earlier. Both are now
  `metafetch.BuildMetadataProvenance`, alongside the `MetadataFieldState` that was
  already canonical there. The move is proven behaviour-preserving by a
  differential over six cases captured before and after, byte-identical.

  The 5-line `nonEmpty` helper the function depends on was itself duplicated the
  same two ways; it is canonical in `metafetch` now, with both packages holding a
  one-line alias so none of its 52 call sites changed.
