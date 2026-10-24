Added the retired-version guards on purge, trash, restore and auto-promote, the
fourth of five stacked changes for retired book versions. A retired version is
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
retired checks now have unit tests. Nothing retires a row yet, so running
behavior does not change.
