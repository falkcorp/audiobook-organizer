The trash purge (`versions.PurgeVersion`, used by the nightly
`maintenance.trash-cleanup` op and by `POST /api/v1/audiobooks/:id/versions/:vid/purge-now`)
never deletes a file a live book still serves. Trashing the ACTIVE version only
flips its status, so its files stay where the book plays them; 14 days later the
purge deleted them and left the book pointing at a missing file.

While the book is live, only files inside this version's own `.versions/<id>` slot are
ever purged. Trashing a version never moves its files, and the player streams a book's rows with no version
filter (and `Book.FilePath` is often stale), so a file of the version's own book
that is not inside a slot is still where the book plays it and is always kept.
Inside a slot, a file is still kept when the book is not itself trashed and it is
the book's `FilePath` (symlinks resolved, hard links count as the same file) or
lies in the folder `FilePath` names (the exported `database.BookOwnFolder` rule),
or is named by a row of the book's active version or of no version.

Another live (non-trashed) book counts too: its `FilePath`, any ancestor folder
of the file that is a live book's `FilePath` (the walk goes up to the filesystem
root or the configured library root, whichever comes first, and never through a
`.versions/` slot), or any non-retired row of it whose version is not trashed or
purged (alt versions included, because the player streams rows regardless of
version). Deluge and import folders are shared between books.

If any file of the version is served, nothing is deleted, the version stays in
the trash, one WARN per version is logged (book id, version id, path count,
"book still serves this file") and `*versions.BookServesFilesError` is returned;
purge-now answers 409. If the book, its versions or rows, or another book's rows
cannot be read the purge fails closed and deletes nothing.

`CleanupTrashedVersions` now returns a `CleanupResult` (purged count plus the kept
versions); the op logs one WARN per kept version to its own log and its summary
reads "purged N versions, kept M versions whose files the book still serves".

A book that is itself in the trash (soft-deleted or library state "deleted")
serves nothing, so its own `FilePath` and rows no longer protect a file; its
versions purge as before unless ANOTHER live book serves the file, which is still
checked.

Combined with the retired-version purge guards (the claim-then-unlink purge): the
served-file check runs in the read-only pass before the version is claimed, ahead
of the other-owner check. A served file on a trashed version ends the whole purge
with `BookServesFilesError`: nothing is claimed or unlinked and the version stays
in the trash, restorable. On a version already `purging` or `purge_failed` a served
path is kept (reported in `Kept`) and the purge finishes the rest. One
`versions.CleanupResult` carries `Purged`, `Failed`, `Kept` (served versions, each
with its paths and the reason), `KeptFiles`, `NeedsAttention` and `CannotTell`; the
`maintenance.trash-cleanup` result adds `served_kept*` fields and the Activity page
links the count to the versions it names (`set=served_kept`, Trash tab). A file
named only by a trashed book, or by a purged version's lingering row, is kept on
disk and listed under kept files rather than deleted, because the trashed book can
be restored and wants its audio back.
