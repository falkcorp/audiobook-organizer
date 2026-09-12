### Fixed

#### Undo restores `series_id` changes and series renames; `version_group_id` stays record-only

`POST /operations/:id/revert` and its preflight now restore `metadata_update`
rows on `series_id` (series dedup, `MergeSeries`, series-phantom-repair). The
old value is written back as a typed `*int`, and an empty old value clears the
series to nil. A row is refused (Failed, left unmarked) when its old series no
longer exists, because writing it back would leave a dangling series reference.

Series renames are now undoable. `MergeSeries` records a series-scoped
`series_rename` row carrying the new `OperationChange.SeriesID` field, and the
revert renames the series back. It refuses when the series is gone, has been
renamed again since, or its old name now belongs to another series under the
same author, and on any store error. The preflight runs the same checks and
reports refusals in the `series_deleted`, `series_renamed_since` and
`series_name_taken` conflict groups. The book-scoped `series_name` rows written
before this change carry no series id and stay record-only.

`version_group_id` stays record-only: the organizer records an empty old value
even when the book was already in the group, and the same write also demotes
the book and creates the organized copy.
