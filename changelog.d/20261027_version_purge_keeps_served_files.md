The trash purge (`versions.PurgeVersion`, used by the nightly
`maintenance.trash-cleanup` op and by `POST /api/v1/audiobooks/:id/versions/:vid/purge-now`)
never deletes a file a live book still serves. Trashing the ACTIVE version only
flips its status, so its files stay where the book plays them; 14 days later the
purge deleted them and left the book pointing at a missing file.

A file of the version is now kept when the version's book is not itself trashed
and the file is the book's `FilePath`, lies inside it when it is a folder (a
multi-file book's `FilePath` is its containing folder; the rule is the exported
`database.BookOwnFolder`, and anything under a `.versions/` slot is excluded), or
is named by a `book_file` row of the book's active version or of no version.
Another live (non-trashed) book counts the same way: its `FilePath`, the folder
it names, or a row of its active or no version, because deluge and import folders
are shared. Paths are compared after resolving symlinks, and two names for one
file (hard links) count as the same file. If any file of the version is served,
nothing is deleted, the version stays in the trash, one WARN per version is
logged (book id, version id, path count, "book still serves this file") and
`*versions.BookServesFilesError` is returned; purge-now answers 409. If the book,
its versions or rows, or another book's rows cannot be read the purge fails
closed and deletes nothing.

`CleanupTrashedVersions` now returns a `CleanupResult` (purged count plus the kept
versions); the op logs one WARN per kept version to its own log and its summary
reads "purged N versions, kept M versions whose files the book still serves".

Unchanged: a book that is itself in the trash (soft-deleted or library state
"deleted") is not guarded, so its versions purge as before.
