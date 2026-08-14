### Fixed

- Book-row copy paths no longer propagate dangling series references (C610):
  the organizer's organized-copy, reconcile's metadata merge, and dedup-books'
  keeper fill now validate the copied `SeriesID` against the series table and
  drop refs whose series is gone (logged, fail-open on store errors). No new
  phantom IDs are minted since `resolveSeriesID` creates-by-name; these copy
  paths were how the existing ~12K dangling refs kept spreading.
