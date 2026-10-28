The trash purge (`versions.PurgeVersion`, used by the nightly
`maintenance.trash-cleanup` op and by `POST /api/v1/audiobooks/:id/versions/:vid/purge-now`)
never deletes a file the version's own live book still serves. Trashing the
ACTIVE version only flips its status, so its files stay where the book plays
them (`book.FilePath` and its `book_file` rows); 14 days later the purge
deleted them and left the book pointing at a missing file. Now, when the book
is not itself trashed and the version's file is the book's `FilePath` or is
named by a `book_file` row of the book's active version (or of no version),
the purge deletes nothing, leaves the version in the trash, logs one WARN per
version (book id, version id, path count, "book still serves this file") and
returns `*versions.BookServesFilesError`. Purge-now answers 409 in that case.
`CleanupTrashedVersions` now returns a `CleanupResult` (purged count plus the
kept versions) and the op's summary line reads "purged N versions, kept M
versions whose files the book still serves". If the book, its versions or its
rows cannot be read the purge fails closed and deletes nothing.
