### Fixed

#### Undo restores `series_id` changes; `version_group_id` and `series_name` stay record-only with a stated reason

`POST /operations/:id/revert` and its preflight now restore `metadata_update`
rows on `series_id`, written by series dedup (`DedupSeries`, `MergeSeries`) and
`maintenance.series-phantom-repair`. The old value is written back as a typed
`*int`, and an empty old value clears the series to nil instead of writing 0.
Before this, `RunUndoOperation` already restored these rows while the revert
endpoint called them record-only. The old string-only reflection writer would
also have panicked on an `*int` field if one had been added to its map.

A `series_id` row is refused (counted Failed, left unmarked) when its old series
no longer exists. Series dedup deletes the merged-from series in the same
operation, and a phantom-repair old value is a dangling id by definition, so
writing either back would create a dangling series reference. The preflight
runs the same check and reports these rows in a new `series_deleted` conflict
bucket, which the Activity Log confirmation counts as conflicts.

`MergeSeries` now takes the before-image for its `series_id` row from the
hydrated book it overwrites, not the index copy, matching `DedupSeries`.

`version_group_id` stays record-only: the organizer version-copy path always
records an empty old value, even when the book was already in that group, and
the same write also demotes the book and creates the organized copy, none of
which a field write can reverse. `series_name` stays record-only because it
renames a series entity and the row does not record which series.
