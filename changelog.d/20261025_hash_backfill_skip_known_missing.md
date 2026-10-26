### Fixed

The `backfill-file-hashes` maintenance op no longer warns about every file that
is gone from disk, once per file, on every run. A prod preview found about 515k
rows with no hash, and every sampled failure was a file missing on the server
rather than a read error.

A hash attempt that fails with not-found (ENOENT or ENOTDIR) is now its own
counted outcome, `not_found`, instead of a warning. Nothing is written to the
database for such a file: no new column, no flag on the row, and `Missing` is
untouched. Only the first 50 not-found paths go to the operation log. Every row
the run reaches that has no hash is written to a per-row TSV report at
`{root_dir}/.reports/backfill-file-hashes-<op id>.tsv` (bucket, file_id, book_id,
path, reason; the buckets are `hashed`, `not-found`, `not-found-outside-root`,
`read-error`, `write-error` and `no-path`; a run with no operation id gets a
timestamped name, so reports are never overwritten), dry runs included. Any
other failure, such as permission denied or an I/O error, stays a `read_errors`
outcome and is retried every run. A not-found outside the library root, or with
no library root configured, cannot be checked against the root's mount, so it is
counted apart as `unverified_outside_root`. Rows with an empty path are counted
as `skipped_no_path`. The result line reports `hashed`, `not_found`,
`unverified_outside_root`, `read_errors`, `write_errors` and `skipped_no_path`
and names the report; a dry run reports what an apply would do without storing
any hash (it still writes the report and its resume checkpoint).

A run that is cancelled and then resumed, or interrupted at any point other than
the one described below, now ends with exactly the report and totals an
uninterrupted run would have. Each chunk's rows are held in memory and written to the report only
immediately before that chunk's checkpoint is saved, so no row past the
checkpoint reaches the file, and a chunk cut off half way stores, counts and
reports nothing. On resume the op reads the existing report, skips the rows it
already lists, and starts its counters from it, so the final summary counts the
whole run. If the earlier report is missing, or no root is configured, the
summary says its counts cover only the resumed part. A report that cannot be
read back (a bad header, a line that is not a row) stops the resume with an
error instead of guessing. If closing the report fails after the last chunk, the
op fails, keeps its checkpoint and saves no "completed" summary.

One window is not closed. A chunk's hashes are stored, then its report rows are
written, then its checkpoint is saved. If the whole process is killed (not
cancelled) after some of a chunk's hashes are stored and before its report rows
are flushed, those files keep their stored hash, so the resumed run skips them
and neither counts nor lists them: `hashed` and the report come up short by up to
one chunk (512 files). The database itself is correct. A dry run stores nothing
and is not affected.

When a library root is configured, the op refuses to run before hashing
anything, a dry run included, if any of these holds:

- the root cannot be read;
- the root is not an absolute path;
- the root holds nothing but dot-prefixed entries, which is how an unmounted
  share looks (an unmounted share answers not-found for every path);
- `{root_dir}/.reports` cannot be created or written.

The mount check is coarse. It rejects an empty or dot-only directory and
nothing more: it does not prove the share is healthy, and a root that holds any
non-dot entry passes. A run with no root configured works as before, without a
report.

### Changed

A new `internal/libroot` package holds the library-root checks the op shares with
the consolidation-leftovers fixer: whether the root is mounted, and where a
report is written. Its mount check no longer counts dot-prefixed entries (the
`.reports` directory, the wav cache) as library content, so writing a report can
never make a bare mountpoint look mounted. The consolidation-leftovers fixer now
also refuses a root that holds only dot-prefixed entries, which it used to
accept.
