### Fixed

#### Batch file writes no longer leave a book's total duration and size stale

Every way of changing a book's files recomputed that book's total duration and
file size afterwards — except one. `BatchUpsertBookFiles`, the path used by
whole-library backfills, never did. The rows it wrote were correct; only the
parent book's summed fields were left at whatever a previous single-file write or
the original import had set.

Nothing reported this. The write succeeded, the files were right, and the totals
were quietly wrong. A backfill that corrected the duration of every file in the
library could therefore finish successfully and leave every book still displaying
the old total — which also makes the correction look like it did nothing on
re-read, and invites running it again.

The batch path now recomputes each affected book exactly once, after the write
commits — the same shape `DeleteBookFilesByIDs` already used. Once per book, not
once per row: the recompute re-reads the book's entire file set, so doing it per
row is what turns an N-file write into an N² read.

Two details that are easy to get wrong, and are now pinned by tests:

- **The recompute follows the row, not the request.** A batch upsert matches an
  existing file by iTunes ID or by path and adopts that row's owner, so a file
  submitted under one book can land on another. The book that gets recomputed is
  the one actually written.
- **Deleting every file does not zero a book's duration.** A deliberate
  partial-data rule keeps the last known good value rather than letting a
  temporarily-missing file destroy it. A comment on the bulk-delete path claimed
  the opposite; it has been corrected.
