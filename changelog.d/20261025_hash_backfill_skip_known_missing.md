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
`read-error`, `write-error`, `busy` and `no-path`; a run with no operation id
gets a timestamped name, so reports are never overwritten), dry runs included.
The path and reason columns are lossy: tabs, newlines and carriage returns in
them are replaced by spaces so every row stays one line, so a path with such a
character does not round-trip exactly. The `file_id` column is the key and is
never altered. Any other failure, such as permission denied or an I/O error,
stays a `read_errors` outcome and is retried every run. A not-found outside the
library root, or with no library root configured, cannot be checked against the
root's mount, so it is counted apart as `unverified_outside_root`. Rows with an
empty path are counted as `skipped_no_path`. The result line reports `hashed`,
`not_found`, `unverified_outside_root`, `read_errors`, `write_errors`, `busy` and
`skipped_no_path` and names the report; a dry run reports what an apply would do
without storing any hash (it still writes the report).

A hash is stored only if the row still has no hash and still points at the file
that was hashed (same path, size and scan mtime), and the check and the write
happen together through `ModifyBookFile`. A row that was repointed, or whose file
changed, between the hash and the store gets nothing stored: it is counted as
`busy`, listed in the report with the reason "row changed since the file was
hashed, retried next run", and retried by the next run. A row another writer
hashed in the meantime keeps that hash and is counted as `hashed` with the reason
"hash already stored". A write that fails with the store's changed-since error
(`database.ErrBookFileChangedSince`) is also `busy`, listed with the reason "row
was busy, retried next run", and is no longer counted as a write error. The same
conditional store is used when a chunk journal is replayed after a restart.
`TODO(#4169)`: once the store gains compare-and-swap and a
`database.ErrBookFileConflict`, map that error to `busy` too. `ModifyBookFile`
recomputes the book's aggregates once per stored hash, which costs more than the
plain hash write did; on the prod library few rows get a hash (most files are
missing), so the extra work is small.

A run that is cancelled, killed or restarted part way (for example by a server
restart) and then resumed now ends with exactly the report and totals an
uninterrupted run would have. There is no checkpoint index to resume from any
more: a run always starts at the first row, skips rows that already have a hash
and rows its report already lists, and starts its counters from the report, so
the final summary counts the whole operation. A report named after an operation
id is never truncated, whatever the saved state says, so a kill before the first
checkpoint, a failed checkpoint save, an unreadable saved state, or a re-queue
of an operation that had already finished can no longer wipe the earlier rows.
Rows that were retired between attempts no longer shift the resume point, because
nothing is resumed by position. Each chunk's rows are held in memory and written
to the report only after the chunk is journaled and its hashes stored, so a
chunk cut off half way before that point stores, counts and reports nothing.
If the saved state shows the operation started before but no earlier report is
found, the summary says its counts cover only the work done in this run; if the
saved state cannot be read and no report is found it says the counts may cover
only part of the operation. Failures to read or save that state are logged as
warnings instead of being discarded. A report that cannot be read back (a bad
header, a line that is not a row, an unknown bucket) stops the resume with an
error instead of guessing; a last row that lacks its newline, or a report with
CRLF line endings, is accepted. If closing the report fails after the last
chunk, the op fails and saves no "completed" summary.

Each chunk is also journaled before anything in it is stored: its outcomes are
written to the operation's own saved state in the database (the
`opstate:<op id>:params` key, synced to disk), the hashes are stored, the report
rows are written and fsynced, and only then is the journal deleted. If the
process is killed anywhere in that sequence, the next run finds the journal and
replays it: rows the report already lists are skipped, the rest are stored
(conditionally), counted and listed, and a hash that is already stored is not
stored (or computed) again. So a kill between storing a chunk's hashes and
writing its report rows loses nothing. A journal that cannot be read, belongs to
another operation, or holds an entry that cannot be a row stops the resume with
an error; a failed journal write stops the run before any hash is stored. That
key is the shape the retention job's saved-state sweep ties to the operation, so
a journal an operation never came back for is dropped with it, and
`ClearState` removes it with the checkpoint; there are no journal files to
clean up.

The report is treated as untrusted input. It lives in `{root_dir}/.reports`
inside the library share, which other users may be able to write, so on resume
its content only ever decides which rows to skip and what to count: nothing read
from it is hashed, stored or used as a path, and a forged line cannot cause a
hash write. Each line's bucket must be a known one and its file and book IDs must
be 1 to 64 characters of letters, digits, `_` and `-` (not ULID-only, because the
store accepts caller-supplied IDs); anything else stops the resume with an error.
The report is opened without following symlinks (`O_NOFOLLOW`), and the open
descriptor is checked before anything is read: it must be a regular file owned by
the user the server runs as and no larger than 512 MiB (about 4x the expected
size), lines are read through a 64 KiB bounded reader, and a report that breaks
any of this stops the resume with an error (a fresh report is vetted before it is
emptied). A new report is created `0640`. A forged line can still make the job
skip a row it would have hashed, never write one.

The default report directory, `{root_dir}/.reports`, is vetted by `libroot` before
any op writes to it: it is created `0750` when absent, and when it exists it must
be a real directory (an `Lstat` check, so a planted symlink is refused and nothing
is written through it) and owned by the user the server runs as. If it is owned by
that user but writable by group or others, it is tightened to `0750` and a warning
is logged with the mode it had; it is refused only when it is owned by someone
else or the `chmod` fails. The check is `Lstat` plus `O_NOFOLLOW` on the file, not an
`openat` walk, so a swap between the directory check and the file open is still
possible in principle. **Deploy impact:** a `.reports` directory that already
exists group-writable (`0775`, which the earlier version of the helper created) is
changed to `0750` the first time any op that writes its report to the default
directory runs (mark-missing-files, recover-missing-files, merge-same-path-dupes,
metadata-cache-reap, the repoint ops, book-shape-report, dedupe-book-file-rows, and
this one), not just this op; nothing has to be done by hand before deploying, and
group members who relied on writing there lose that access. A report directory
behind an explicit report path is not vetted.

The op's description now says what the op really does: it names the
`unverified_outside_root` and `busy` outcomes, the conditional store, and the
four refusals below.

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
report is written (the default report directory is now created `0750`, not
`0775`, and an existing one is refused if it is a symlink or owned by someone else,
and tightened to `0750` if it is group- or world-writable, for every op that uses
it). Its mount check no longer counts dot-prefixed entries (the
`.reports` directory, the wav cache) as library content, so writing a report can
never make a bare mountpoint look mounted. The consolidation-leftovers fixer now
also refuses a root that holds only dot-prefixed entries, which it used to
accept.
