### Fixed

The `backfill-file-hashes` maintenance op no longer warns about every file that
is gone from disk, once per file, on every run. A prod preview found about 515k
rows with no hash, and every sampled failure was a file missing on the server
rather than a read error.

A hash attempt that fails with not-found (ENOENT or ENOTDIR) is now its own
counted outcome, `not_found`, instead of a warning. Nothing is written to the
database for such a file: no new column, no flag on the row, and `Missing` is
untouched. Only the first 50 not-found paths go to the operation log; every one
is written to a per-row TSV report at
`{root_dir}/.reports/backfill-file-hashes-<op id>.tsv` (bucket, file_id, book_id,
path, reason; a run with no operation id gets a timestamped name, so reports are
never overwritten), dry runs included. Any other failure, such as permission
denied or an I/O error, stays a `read_errors` outcome and is retried every run.
A not-found outside the library root cannot be checked against the root's mount,
so it is counted apart as `unverified_outside_root`. Rows with an empty path are
counted as `skipped_no_path`. The result line reports `hashed`, `not_found`,
`unverified_outside_root`, `read_errors`, `write_errors` and `skipped_no_path`
and names the report; a dry run reports what an apply would do without storing
any hash.

The op refuses to run when the library root is configured but holds no library
content (an unmounted share answers not-found for every path), and a run with no
root configured works as before, without a report.

### Changed

A new `internal/libroot` package holds the library-root checks the op shares with
the consolidation-leftovers fixer: whether the root is mounted, and where a
report is written. Its mount check no longer counts dot-prefixed entries (the
`.reports` directory, the wav cache) as library content, so writing a report can
never make a bare mountpoint look mounted.
