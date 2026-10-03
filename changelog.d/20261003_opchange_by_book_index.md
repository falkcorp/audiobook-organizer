### Changed

#### Book history lookups read a by-book index instead of the whole operation journal

`GetBookChanges` used to scan and JSON-decode every `opchange:` row in the
operation journal to find one book's rows: 331 ms per call at 300k rows, and
prod's journal is larger. It runs once per retired book on every resume of a
retire-into apply (`resumeHandOff`, `owedHandOff`, `emptiedRowOn`), which drove
11 s/book applies on 2026-10-03, and in the version-group fixers, undo/revert
paths, the book changelog API and others.

A new secondary index, `opchange_by_book:<bookID>:<opID>:<changeID>`, is
written in the same Pebble batch as every journal write
(`CreateOperationChange` including rewrites by id that move a row to another
book, `MarkOperationChangesReverted`, and `PruneOperationChanges`). Reads walk
one book's entries and point-get each row from one snapshot, returning the
same rows in the same order as the scan. Benchmark at 300k rows on disk:
402 ms per call for the scan, 0.13 ms through the index.
`MarkOperationChangesReverted` also commits all of its marks in one batch now.

**Deploy note.** The index for existing rows is built by a one-time startup
backfill (`opchange-index-backfill`, started after memdb warmup, next to
`book-atpath-backfill`). It streams the journal in 5,000-row chunks, each
committed with its resume cursor, so it is safe while the server is live and
resumes where it stopped after a restart. Until it sets its done-marker
(`system:backfill:opchange_by_book_index_v1_done`) every `GetBookChanges`
keeps using the full scan, so no caller ever sees a partial index. Measured
at 0.92 s for 300k rows; expect roughly 3 to 10 s for about 1M rows. Look for
`opchange-index-backfill: complete` in the log.

**Rollback.** A build from before this change does not maintain the index.
After any rollback followed by a roll-forward, run
`maintenance.opchange-book-index-rebuild`, which clears the done-marker (reads
fall back to the full scan at once) and rebuilds from the first row.
