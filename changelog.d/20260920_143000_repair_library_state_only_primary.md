- `maintenance.repair-library-state` now defaults to primary versions only. The
  first prod dry run reported 15,055 repairable rows where the primary-only census
  had predicted 5,857: the extra ~9,198 are non-primary. Repairing those has no
  visible effect — Audiobookshelf requires a row to be *both* primary and organized —
  and it would have silently emptied `maintenance.repoint-version-primary`'s
  candidate pool, which selects non-primary group members whose state is exactly
  `imported`. Pass `only_primary: false` to widen it deliberately.
