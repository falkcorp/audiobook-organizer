### Fixed

#### Undo renames a series back; rows the revert always refuses are never offered as undoable

Series renames are now undoable. `MergeSeries` and the series-normalize rename
pass (`dedup.series-normalize`, `maintenance.series-normalize`) record a
series-scoped `series_rename` row carrying the new `OperationChange.SeriesID`
field, and `POST /operations/:id/revert` renames the series back. It refuses
(Failed, left unmarked) when the series is gone, has been renamed again since,
or its old name now belongs to another series under the same author, and on any
store error. The write goes through the new `RenameSeriesIf` store method,
which repeats those checks under the series name-index lock, so a series
created or renamed between the check and the write is refused rather than
duplicated. The book-scoped `metadata_update` `series_name` rows written before
this change carry no series id and stay record-only. Renames made through
`PUT /series/:id/name` and `PATCH /series/:id` still record no change row.

The preflight runs the same checks, including the book lookup. A row whose book
was hard-deleted, or could not be read, is refused by the revert every time, so
it is now listed under `book_missing` or `check_failed` instead of
`book_deleted`, which keeps only soft-deleted books (the revert restores
those). The refused groups are `book_missing`, `series_deleted`,
`series_renamed_since`, `series_name_taken` and `check_failed`. The Activity
Log confirmation names them as rows that will be refused, never as undoable, so
an operation made only of such rows (for example every series-phantom-repair
run) offers no Undo.

A revert whose book no longer exists now fails that row instead of panicking,
and a file move loads its book before moving the file back.

The unused `undo.RunUndoOperation` walk, its server wrapper, and the
`deluge.NotifyDelugeAfterUndo` hook only it called are deleted; nothing outside
tests called them. The revert endpoint does not notify Deluge when it moves a
file back.
