### Fixed

- **Owner-manual guard follow-ups (review of #3754).**
  - `itunes.regroup` no longer aborts the whole nightly run when one book's
    owner-manual read fails. The book is marked "check failed" and only the
    groups holding it are skipped (`manual-check-failed-skipped` in the plan
    summary). An apply then ends with an error naming the count.
  - The regroup snapshot's owner-manual file reader keeps an int32 index into
    the bulk read instead of a full `BookFile` per row. At 300k rows the
    benchmark allocates 15 MB instead of 751 MB.
  - Credited author rows in the snapshot come from one `GetAllAuthors` read,
    with a point-read fallback for tombstoned ids.
  - `applygate.BookManualOnly` refuses a nil files, series, author or tag
    reader (an error, so the caller fails closed). It no longer skips that
    part of the check.
  - `merge-chapter-groups` checks the files its fingerprint was built from.
    It runs the check only after the iTunes guard passes, and describes
    preview groups on a NumCPU worker pool.
  - `author-path-link` re-runs the owner-manual check on fresh reads right
    before it writes.
