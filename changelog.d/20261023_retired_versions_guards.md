### Added

#### Retired-version guards on purge, trash, restore and auto-promote

The fourth stacked change for retired book versions. A retired version is one a
live book keeps out of its own file list. Until now a version with no rows left
in it could still be trashed, restored or purged, and a trashed book that only
held retired versions was reported as an ordinary book that owns files. The
version lifecycle (trash, restore, purge now, the nightly cleanup) now refuses a
retired version with its own error (`versions.ErrRetiredVersion`), the version
routes answer 409 for it, and auto-promote never picks one.

Purging trashed books now tells a holder of retired versions apart from a book
with files of its own. `database.CheckBookOwnsNoFiles` wraps the new
`database.ErrBookHoldsRetiredVersions` (as well as `ErrBookOwnsFiles`) when every
row that blocks the delete is retired. The purge, its preview and the carry
precheck report such a book under the `holds_retired_versions` reason
(`skipped_holds_retired_versions` and its id list in the result, in the
summary, in the capped operation result and in the decided count), and the
hard delete, discard-progress and purge-now routes answer 409 with the
`HOLDS_RETIRED_VERSIONS` code instead of `OWNS_FILES`. The trash listing's
breakdown still counts such a book under `owns_files`; it has no separate
figure yet.

Restoring a trashed holder keeps reading only its live files to decide whether
it is organized, so a retired version stored outside the library folder does not
demote it to imported, and its retired versions come back untouched. Restoring a
trashed book that is the shell of a retired version still holding its files is
refused with a 409 saying the book stays in the trash and its files are safe,
instead of bringing back a book with no files. The version restore route also
answers 409 (not 400) for a retired version. Purging a trashed version (the
Purge now route and the nightly cleanup) no longer removes a file that any other
book_file row, retired ones included, still names, nor one that any other book
names with its own file path, a trashed (restorable) book included, and it stops
without deleting anything when it cannot tell who names a path or when the store
cannot answer the all-books lookup at all. Nothing retires a row yet, so
running behavior does not change.

The web client knows the new refusal. The purge dialog lists a book that holds
only retired versions as its own kept reason ("hold retired versions of another
book's files") and counts it as decided, and a delete, purge-now or discard
refusal with the `HOLDS_RETIRED_VERSIONS` code shows a message saying the book
holds retired versions of another book's files, which cannot be moved, instead
of the generic error.
