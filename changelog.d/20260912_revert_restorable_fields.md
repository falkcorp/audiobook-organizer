### Fixed

#### Undo restores `series_id` changes and series renames; `version_group_id` stays record-only

`POST /operations/:id/revert` and its preflight now restore `metadata_update`
rows on `series_id` (series dedup, `MergeSeries`). The old value is written back
as a typed `*int`, and an empty old value clears the series to nil. A row is
refused (Failed, left unmarked) when its old series no longer exists, because
writing it back would leave a dangling series reference. That includes every
series-phantom-repair row, whose old value is a dangling id by definition.

Series renames are now undoable. `MergeSeries` records a series-scoped
`series_rename` row carrying the new `OperationChange.SeriesID` field, and the
revert renames the series back. It refuses when the series is gone, has been
renamed again since, or its old name now belongs to another series under the
same author, and on any store error. The book-scoped `series_name` rows written
before this change carry no series id and stay record-only.

The preflight runs the same checks. It lists refusals under `series_deleted`,
`series_renamed_since`, `series_name_taken` and `series_check_failed`, and the
Activity Log confirmation names them as rows that will be refused, never as
undoable, so an operation made only of such rows offers no Undo.

A revert whose book no longer exists now fails that row instead of panicking,
and a file move loads its book before moving the file back.

The unused `undo.RunUndoOperation` walk and its server wrapper are deleted.
Nothing outside tests called it, and it restored `series_id` without these
checks.

`version_group_id` stays record-only: the organizer records an empty old value
even when the book was already in the group, and the same write also demotes
the book and creates the organized copy.
