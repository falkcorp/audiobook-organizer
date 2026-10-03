### Added

- **Repairs fixer `maintenance.relink-stale-series`:** finds books whose `series_id` is empty but whose stored row still carries the series object. Almost every series clear ever made leaves that state, because the store kept the old object when the id went nil. The plan summary is the census taken before #3698 makes the store drop the object. Each class is a count:
  - `relink-no-history`: the only applicable class. The series row by that id exists, its name and author agree, and no series history clears or contradicts it. Risk is review.
  - `held-cleared-by-history`: the newest series history row, compared by time across `series`, `series_id` and `series_name`, cleared the series, or a `series_name` lock did. The skip kind `skipped_cleared_by_<source>` groups these rows by who cleared them, and the source is in Evidence.
  - `held-name-mismatch`: the series row's name or author disagrees with the object, or newer history names another series.
  - `name-match`: held, with the candidate id in Evidence.
  - `orphan`: held.
  - `error`: held.
- Doctor Who / Big Finish / Torchwood series names are held too.

### Fixed

- **Repairs `Writer` history for `series_id`:** rows now carry the previous and new series refs. "Undo last apply" can now revert a `series_id` write made through the Repairs lane; it reported the field failed before.
