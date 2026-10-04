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

Book ids containing `:` are never indexed (they would make index keys
collide), and `GetBookChanges` always uses the full scan for them, as for an
empty id. `PruneOperationChanges` now commits every 5,000 rows instead of one
batch for the whole prune, and re-reads each row under a journal lock before
deleting it, so a row rewritten under the same id while a prune runs is no
longer deleted from a stale view.

**Deploy note.** The index for existing rows is built by a one-time startup
backfill (`opchange-index-backfill`, started after memdb warmup, next to
`book-atpath-backfill`). It streams the journal in 5,000-row chunks, each
committed with its resume cursor, so it is safe while the server is live and
resumes where it stopped after a restart. Measured at 0.92 s for 300k rows;
expect roughly 3 to 10 s for about 1M rows.

**Readers trust the index only after this boot verifies it.** On every boot,
after the backfill, a read-only pass checks every journal row against the
index. Only when it finds no row without an entry and no unmarked undecodable
row does `GetBookChanges` start reading the index; until then (and if the
check fails) it keeps the full scan, so no caller ever sees a partial index.
If the check finds gaps, it logs at ERROR
(`opchange-index-ensure: index does not cover the journal`) and rebuilds the
index, then trusts it. Look for `opchange-index-ensure: index verified` in
the log.

**Rollback.** A build from before this change does not maintain the index,
and the next boot's verify finds and repairs what it wrote, so no manual step
is required. `maintenance.opchange-book-index-rebuild` is the manual handle:
with `{}` it is a read-only preview that reports journal rows with no index
entry; with `{"dry_run": false}` it clears trust and the done-marker (reads
fall back to the full scan at once), rebuilds from the first row, and trusts
the index again on success. It reports progress per 5,000-row chunk.
