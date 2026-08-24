### Fixed

#### Batch file writes no longer leave a book's total duration and size stale

Adding, updating and deleting a book's files each recomputed that book's total
duration and file size afterwards. `BatchUpsertBookFiles` — the path used by
whole-library backfills, the scanner and the iTunes importer — did not. The rows it
wrote were correct; only the parent book's summed fields were left at whatever a
previous single-file write or the original import had set.

Nothing reported this. The write succeeded, the files were right, and the totals
were quietly wrong. A backfill that corrected every file it touched could therefore
finish successfully and leave those books still displaying the old totals — which
also makes the correction look like it did nothing on re-read, and invites running
it again.

One caller had already hit this and worked around it locally: the duration backfill
runs its own second pass, with a note explaining that the batch path would not do it.
So the gap was known in one place and never fixed where it lived, leaving every other
caller with the bug.

The batch path now recomputes each affected book once **per call**, after the write
commits — the same shape `DeleteBookFilesByIDs` already used. Once per book rather
than once per row is the point: the recompute re-reads the book's entire file set, so
doing it per row is what turns an N-file write into an N² read.

Per call, not per job — a caller that flushes in chunks and does not group its rows
by book will still recompute a book once per chunk that book appears in. That is
bounded and vastly better than per row, but it is not a promise of exactly one
recompute per book across a whole backfill.

#### The scanner was erasing the totals it had just computed

Fixing the batch path exposed that it did not help the highest-volume caller. When the
scanner creates a book's files it loads the book once at the start, writes the files,
and then — for books whose path points at a file rather than a folder, i.e. single-file
audiobooks — writes that original copy back to normalise the path. That copy still has
the totals from before the write.

`UpdateBook` preserves a field on nil for nine fields; duration and file size are not
among them, so the nils in the stale copy were written straight through, discarding what
the recompute had just stored. Every single-file audiobook the scanner imported had its
totals computed and then erased inside one function.

The scanner now re-reads the book before that final write. If the re-read fails it falls
back to the old behaviour and says so in the log, rather than losing the values silently.

Two details that are easy to get wrong, and are now pinned by tests:

- **The recompute follows the row, not the request.** A batch upsert matches an
  existing file by iTunes ID or by path and adopts that row's owner, so a file
  submitted under one book can land on another. The book that gets recomputed is
  the one actually written.
- **Deleting every file does not zero a book's duration.** A deliberate
  partial-data rule keeps the last known good value rather than letting a
  temporarily-missing file destroy it. A comment on the bulk-delete path claimed
  the opposite; it has been corrected.
