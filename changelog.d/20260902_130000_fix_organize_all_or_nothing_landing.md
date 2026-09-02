### Fixed

#### A multi-file book is organized whole or not at all; in-place moves are recorded as such (#3051)

Follow-up to #3046, whose two review passes landed after it merged.

A directory landing is all-or-nothing. A multi-file book whose files only partly
landed in a target directory used to be promoted to a version row whose `FilePath`
was that directory — a directory it shared with the book that won the other files —
and the next `ReOrganizeInPlace` renamed the whole directory, carrying the other book's
audio under this book's row. Now any file that does not land (unsafe destination,
occupant not proven ours, source vanished before a scan flagged it missing, lost race)
fails the book: every file this call created is removed and the error names the count
and reasons. The partial-landing reporting #3046 introduced (`Landing.Skipped`,
`Stats.Partial`, `organize_partial`, `skipped_files`) is deleted; there is no partial
outcome left to report.

`Landing.InPlace` records which branch `OrganizeOneBook` took. The HTTP handler used to
re-derive it from a `RootDir` snapshot taken at startup, so after a runtime `root_dir`
change it could create a second row at the path an in-place move had just produced.
The handler's `rootDir` field is gone.

Resuming a stranded `RenameFiles` temp now requires the temp's size to match the
`book_file` row's recorded size (`FileRenameEntry.ExpectedSize`); a row with no size,
a size mismatch, or a legacy fixed-name `.tmp-rename` sitting beside a still-present
source is refused with the paths named rather than published under the row.
`moveExclusive` routes symlink sources (the `symlink` strategy) to the rename path
instead of rejecting them.

