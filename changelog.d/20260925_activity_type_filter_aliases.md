### Fixed

- `GET /api/v1/activity?type=`: a type that names an operation now matches the op's canonical ID and every former (pre-rename) ID, so a renamed op's activity history is no longer split between the old and new spellings. `GET /operations/timeline?def_id=` already did this. The match is done in the stores (`ActivityFilter.TypeAliases`: SQL `IN`, one Pebble filter-index range per spelling), so paging and `total` stay exact. A former ID used here counts in `audiobook_organizer_operation_deprecated_def_id_total{entry="activity_filter"}`.
- The live fpcalc decode tests now synthesise a 330 s clip, longer than both production analysis windows, so a dropped or changed `-length` changes the frame count instead of passing. `fpcalcHead`'s argument vector is pinned by a unit test that runs in CI.
- The review-lane benchmark's `/review/count` stub and the vitest global fetch mock now serve the snake_case `by_kind` / `import_paths` keys.
