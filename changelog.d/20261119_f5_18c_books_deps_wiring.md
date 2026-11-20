### Added

- maintenance: the books module's own dependency values for the converted maintenance groups 2, 3, 4 and 6 (the boot passes and index repairs; the activity, temp-file and credits backfills; the book-file, transcribe, dedup-triage and index kinds; the merged-user-state, iTunes heal and activity compaction kinds), and for the maintenance jobs' store, built dark (F5-18c). Each store answers the reads from the library store and resolves the optional capabilities through the decorator chain; every write is one gated `internal/books/writer` call stamped with the method's whole row from the shared table, so a preview, a fenced run or a write outside the declared effects refuses it before the store is reached, and a read-only run, which has no writer, refuses every write. A missing dependency leaves the dependent field unset, so each group's own refusal (a nil stats sink, no queue, no library root) fires instead of a later nil dereference. The jobs store has no `Unwrap` and no embedded store; its raw key writes take their row from the key's family and an unclassified key is refused. Nothing calls any of it yet, so behaviour is unchanged.

### Changed

- books: the metadata-fetch gated store embeds the inner store through an unexported alias, so a caller can no longer reach it as a field, and the no-hand-back test now checks fields as well as methods.
