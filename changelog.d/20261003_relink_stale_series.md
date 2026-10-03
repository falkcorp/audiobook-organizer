### Added

- **Repairs fixer `maintenance.relink-stale-series`:** finds books whose `series_id` is empty but whose stored row still carries the series object. Almost every series clear ever made leaves that state, because the store kept the old object when the id went nil. The plan summary is the census taken before #3698 makes the store drop the object. Each class is a count:
  - `relink`: the only applicable class. The series row by that id exists, its name and author agree, and the newest series history agrees or there is none. Evidence says which. Risk is review.
  - `held-cleared-by-history`: the newest series history row, compared by time across `series`, `series_id` and `series_name`, cleared the series, or a `series_name` lock did. The skip kind `skipped_cleared_by_<bucket>` groups these rows by kind of writer: manual, batch, undo, operation_revert, fixer, metadata_apply, other or field_lock. The full source is in Evidence.
  - `held-name-mismatch`: the series row's name or author disagrees with the object, or newer history names another series.
  - `name-match`, `orphan` and `error`: all held.
- Doctor Who / Big Finish / Torchwood series names are held too.
- Replan reads the target series row fresh, and Apply re-checks its name and author.

### Fixed

- **Repairs `Writer` history for `series_id`:** rows now carry the previous and new series refs. "Undo last apply" can now revert a `series_id` write made through the Repairs lane.
- **Operation revert records history:** every book column a revert restores now gets a history row. The source is `operation_revert`, the operation id is the batch id, and the change type is undo. Before, a reverted series link left the newest history saying "set to X".
