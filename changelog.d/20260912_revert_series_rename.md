### Fixed

#### Undo renames a series back; always-refused rows are never offered as undoable

Series renames are now undoable. `MergeSeries` records a series-scoped
`series_rename` row carrying the new `OperationChange.SeriesID` field, and
`POST /operations/:id/revert` renames the series back. It refuses (Failed, left
unmarked) when the series is gone, has been renamed again since, or its old
name now belongs to another series under the same author, and on any store
error. The book-scoped `metadata_update` `series_name` rows written before this
change carry no series id and stay record-only.

The preflight runs the same checks. It lists refusals under `series_deleted`,
`series_renamed_since`, `series_name_taken` and `series_check_failed`, and the
Activity Log confirmation names them as rows that will be refused, never as
undoable, so an operation made only of such rows (for example every
series-phantom-repair run) offers no Undo.

A revert whose book no longer exists now fails that row instead of panicking,
and a file move loads its book before moving the file back.

The unused `undo.RunUndoOperation` walk and its server wrapper are deleted.
Nothing outside tests called it, and it restored `series_id` without the
dangling-series check.
