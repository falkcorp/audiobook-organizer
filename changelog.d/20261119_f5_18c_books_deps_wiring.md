### Added

- maintenance: the books module's own dependency values for the group-2 maintenance kinds (the boot passes, the book-at-path and book-hash index repairs, the AcoustID index repair, the chapters backfill, the folder book-file build and the book-signature migration), built dark (F5-18c). The store answers the reads from the library store and resolves the optional capabilities through the decorator chain; every write is one gated `internal/books/writer` call stamped with the method's whole row from the shared table, so a preview, a fenced run or a write outside the declared effects refuses it before the store is reached; a read-only run, which has no writer, refuses every write. The report path is the confining resolver, not a pass-through. Nothing calls them yet, so behaviour is unchanged.

### Changed

- books: the metadata-fetch gated store embeds the inner store through an unexported alias, so a caller can no longer reach it as a field, and the no-hand-back test now checks fields as well as methods.
