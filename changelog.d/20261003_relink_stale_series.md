### Added

- **Repairs fixer `maintenance.relink-stale-series`:** finds books whose `series_id` is empty but whose stored row still carries the series object, the state older series-clear bugs left. Rows are classed `relink` (the series row by that id exists: apply sets `series_id` back, review risk), `name-match` (the id is gone, one same-name series with a compatible author: held, candidate id in the evidence) and `orphan` (held). Rows whose series a user cleared or overrode (a `series_name` lock or a history row) are held, and a Doctor Who / Big Finish / Torchwood series name is held too, since the framework guard reads the series name through `series_id`. The plan's per-class counts are the census to take before #3698 makes the store drop the object.

### Fixed

- **Repairs `Writer` history for `series_id`:** rows now carry the previous/new series refs, so "undo last apply" can revert a `series_id` write made through the Repairs lane (it reported the field failed before).
