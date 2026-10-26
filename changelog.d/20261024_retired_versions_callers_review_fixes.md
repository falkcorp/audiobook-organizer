Review fixes for the retired-version callers change. Importing a new book no
longer fails while the in-memory index is still warming up: the retired-content
hash check answers "none" at once when no retired version exists, and otherwise
reads the retired versions straight from the database instead of refusing. A
rescan that reaches a trashed or merged book's old folder (the shell a retired
version was taken from) by its own path or organizer ID is now skipped as known
retired content, and never mints a second row for a file a retired row already
holds. The iTunes and external-ID backfills no longer map a persistent ID to a
retired holder when a live row carries it. Reverting a repoint hands a path's
index entry back to a retired owner (unless a live row has the path), so the
undo journal and the index agree. The collision
resolver in organize and write-back counts a retired version's row as the
occupant of a path and tombstones the losing row instead of repointing it over
the retired row's index entry, the Deluge import refuses a source whose path a
retired row holds (and the write guard's importer says so), the scanner locks
the holder of a retired version whose row is a multi-file book's first segment,
and a rewrite of a retired version's file keeps that row's hashes and codec
fields true. Stale comments about the retired-row checks were corrected, the
single-flight test helper now asserts the call's error, and the AcoustID scan's
retired guards have their own unit tests. Nothing retires a row yet, so running
behavior does not change.
