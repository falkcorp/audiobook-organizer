- [ ] **`extractSeriesName` truncates an album on the first `,`, `-` or `:`.**
      `internal/itunes/service/importer.go`'s `extractSeriesName` splits the
      album title on the first `,`, `-` or `:` and keeps only the left half, so
      the series name is shortened *before* any normalizer sees it:
      `86—EIGHTY-SIX` is stored as `86—EIGHTY`. Found while moving the series
      position out of the series name (PR for
      `fix/strip-series-position-at-write-time`); left unfixed there because it
      is a separate pre-existing defect on a different function, and pinned by
      `TestITunesImport_ExtractSeriesNameTruncatesOnHyphen` so the short name in
      prod is not misattributed to the de-numbering change. Any fix needs to
      decide what a legitimate hyphen/colon in a series name means — the split
      is presumably there to drop a subtitle — so it wants real album samples
      from the library before changing behaviour.
