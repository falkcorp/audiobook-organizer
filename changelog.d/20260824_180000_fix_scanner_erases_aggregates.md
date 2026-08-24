### Fixed

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

This is worth recording as a pattern rather than an incident. The batch fix was correct,
and on the most common path it would still have appeared to do nothing, because something
downstream quietly undid it. Shipping it without checking the callers would have counted
as a success.

#### Failed total recalculations after a bulk write are now reported once, loudly

When a bulk write finished and some books' totals could not be recalculated, each failure
produced one line among however many the run emitted — in a job touching 175,000 rows, a
run where every recalculation failed and one where none did were distinguishable only by
searching the log, and both reported success. Bulk writes now also emit a single summary
naming how many books were affected and a sample of them.

The note in the code claiming a maintenance job would clean up any misses has been
removed, because that job cannot currently run a second time — its documented override is
accepted but never read. That is recorded as a separate defect; in the meantime the
failure is at least visible.
