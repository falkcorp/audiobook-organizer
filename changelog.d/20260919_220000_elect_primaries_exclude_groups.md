### Added

- `POST /api/v1/operations/elect-missing-primaries` now takes an
  `exclude_groups` parameter naming version groups the election must leave
  alone. The 2026-09-19 census found 15 groups whose "versions" are in fact
  the chapter files of one book, where electing a primary crowns a chapter as
  the book; with no way to hold those back, the whole 1,140-group repair was
  blocked on 15 rows. Excluded groups are still counted in the flagged totals
  and are reported by id (`excluded_applied`), alongside ids that named a
  group needing no repair (`excluded_not_candidate`) and ids that matched
  nothing at all (`excluded_unmatched`, also logged) — so an operator can tell
  a list that protected something from one that quietly protected nothing.
  `dry_run` still defaults to true and the write path is otherwise unchanged.
