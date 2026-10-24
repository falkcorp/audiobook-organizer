Added the retired-version guards on purge, trash, restore and auto-promote, the
fourth of nine stacked changes for retired book versions. A retired version is
one a live book keeps out of its own file list, and until now a version with no
rows left in it could still be trashed, restored or purged, and a holder whose
only files were retired versions looked empty to the purge. The version
lifecycle (trash, restore, purge now, the nightly cleanup) refuses a retired
version with its own error, the version routes answer 409 for it, and
auto-promote never picks one. Purging trashed books, deleting a trashed book
for good and discarding a book's progress now count a holder's retired versions
as files it still owns: the purge reports them under the new
`holds_retired_versions` reason (`skipped_holds_retired_versions` and its id
list in the result), the delete and discard routes answer with the
`HOLDS_RETIRED_VERSIONS` code, and the delete checks before it writes a hash
block so a refused delete leaves no block behind. Restoring a trashed holder
keeps reading only its live files to decide whether it is organized, so a
retired version stored outside the library folder does not demote it to
imported; its retired versions come back untouched. The AcoustID scan's two
retired checks now have unit tests. Purging a trashed version
(the Purge now route and the nightly cleanup) no longer removes a file that any
other book_file row, retired ones included, still names, and it stops without
deleting anything when it cannot tell who names a path. Restoring a trashed book
that is the shell of a retired version still holding its files is refused with
a 409 pointing to "Restore as separate book", instead of bringing back a book
with no files; the version restore route also answers 409 (not 400) for a
retired version. Nothing retires a row yet, so running behavior does not change.
