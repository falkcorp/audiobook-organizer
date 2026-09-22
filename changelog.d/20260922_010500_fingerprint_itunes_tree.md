### Changed

- The windowed fingerprint pass no longer excludes the frozen iTunes tree.
  `acoustid.window-backfill` skipped every row under it — roughly 143,766 files,
  19% of the corpus — so none of them could ever contribute an acoustic signal
  to dedup or identification. The exclusion cited the standing "hands off
  iTunes" rule, but that rule is about **mutation**, and this op only reads.
  Every iTunes mutation guard (`config.UnderFrozenITunesTree` in the merge guard
  and the maintenance ops) is untouched, and root registration does not change,
  so no other root-gated operation is affected.
