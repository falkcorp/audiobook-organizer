### Fixed

The `backfill-file-hashes` maintenance op no longer retries, and warns about,
every file that is gone from disk on every run. A prod preview found about 515k
rows with no hash, and every sampled failure was a file missing on the server
rather than a read error. When the hash attempt fails with not-found (ENOENT or
ENOTDIR) the op now sets the row's existing `Missing` flag, and later runs skip
rows flagged missing after one `stat` that confirms the file is still gone. A
file that reappears is hashed and its flag cleared. Read errors of any other
kind are still retried every run and never set the flag, and no `book_file` row
is ever deleted. The flags are written per book through a new narrow store
capability, `database.BookFileBatchModifier` (kept off `Store`), so a book's
aggregates are recomputed once per book rather than once per file; each row is
still re-checked under its own lock before it is flagged. The op reports `hashed`, `skipped_known_missing`,
`newly_missing`, `read_errors` and `write_errors` in its result, and a dry run
reports what an apply would do without writing the flag. Only the first 50
newly-missing paths of a run are written to the operation log; all are counted.
