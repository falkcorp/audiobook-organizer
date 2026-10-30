### Fixed

- A daily digest in the activity feed no longer states totals that include the rows of Repairs runs the caller
  may not read. The digest's `counts`, `original_count`, `tag_counts` and summary used to cover every row of the
  day while only its items were redacted, so they said how much a hidden run did. `CompactByDay` now stores each
  operation's share of those numbers, and a read subtracts the share of every operation the caller may not read.
  A digest compacted before this change has no shares: its totals are left out for a caller who could be hidden
  something, with `aggregates_withheld: true`, and the Activity page shows a "counts hidden" chip instead.

### Added

- Digests now carry exact `tag_counts` (the `action:` and `source:` tags) from compaction, where before only the
  recompaction maintenance op wrote them, and then only from a sample of at most 500 items.
