<!-- file: docs/executive-summaries/2026-09-10-the-safety-checks-that-were-missing-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 5b9d2e47-8c1a-4f63-b2d7-1e6a4c9f0d38 -->
<!-- last-edited: 2026-09-10 -->

# The safety checks that were missing

**Pull requests:** #3181 (merge lock), #3180 (organize check), #3182 (author delete
guard), #3183 (backup verification). All are open and
**held for the owner's review** because they touch paths that move or delete library
data; this summary will be updated with merge commits as they land. Planning package:
#3179 (`docs/agent-tasks/todo-completion-2026-09/`).

## Executive Summary

- A full re-audit of the codebase on 2026-09-10 found a short list of places where the
  app could quietly damage or misreport library data. This is the first batch of fixes
  from that list, chosen because each one is a data-loss risk.
- **Two "merge" features could step on each other.** Merging duplicate books from the
  review screen and the separate "split-book" merge (which glues chapter files back onto
  one book) both rewrite the same records. Three of the four merge paths already took
  turns through one shared lock; the split-book path did not. Two merges touching the
  same book at once could leave it half-deleted, or with files pointing nowhere. It now
  takes the same lock as the others.
- **"Organize" could say a file was fine when it was gone.** When a book's recorded
  location already matched where the organizer wanted to put it, the organizer reported
  success without looking at the disk. A stale record pointing at a deleted or moved file
  was reported as organized. It now checks that the file is actually there and reports an
  error if it is not.
- **The "is this author still used?" check could miss books.** Before deleting an
  author that looks unused, the app counts every book that still credits them. That
  count scanned a slightly-too-narrow slice of the database: it covered every book id the
  app generates itself, but not ids supplied by an importer, migration, or restore. A book
  with one of those ids could credit an author and still not be counted, so the author
  looked unused and was deleted. The scan now covers every book record.
- **"Verify this backup before restoring" did nothing.** The restore screen has a
  "verify" option, on by default. It never verified anything, because the app computed a
  fingerprint when it made a backup and then threw it away. Restoring from a damaged
  backup file looked identical to restoring from a good one. The app now saves that
  fingerprint next to each backup and checks it on restore, refusing to restore a file
  that no longer matches.
- All four fixes come with a test that reproduces the original problem and fails on the
  old code, so the gap cannot silently reopen.
- Each fix also turned up a sibling of the same shape (a second unguarded merge path in
  a maintenance job, and the in-place re-organize step). Those were deliberately left out
  of these changes and filed as tracked tasks so they get their own fix and review.

## 1. Two merge paths that did not take turns

**What it was.** The app has several ways to merge books: the duplicate-review merge,
the multi-file "combine," an iTunes-driven merge, and the split-book merge that
reassembles a book whose chapters were imported as separate books. Each one reads the
book, changes it, and deletes the losers. The first three already coordinate through a
single lock so only one runs at a time on any book. The split-book merge did not, and it
can be started from two places, a background bulk job and a button in the UI, neither of
which knew about the others.

**Why it mattered.** Two merges running on the same book at the same time can interleave
their steps. The known outcome of that pattern, seen once before in this project, is a
book that ends up both "primary" and "deleted," or chapter files orphaned mid-move. It is
a corruption you only notice later, when a book is missing or plays wrong.

**The fix.** The split-book merge now takes the same shared lock as the other three, so
any two merge operations on the library are mutually exclusive. A concurrency test races
it against the review merge and confirms only one runs at a time. A fifth path in an
older maintenance job was found doing the same thing without the lock; it is filed as a
separate task rather than folded into this change.

## 2. The organize step that trusted the record over the disk

**What it was.** When the organizer works out where a book's file should live and finds
the record already says it is there, it treats the job as done. It never looked at the
disk to confirm the file still existed.

**Why it mattered.** Records go stale: a file can be deleted, moved, or replaced after it
was organized, and a database record can be edited by hand. In every one of those cases
the organizer reported "organized" for a file that was not there, hiding exactly the
problem a user would want to know about, and letting downstream steps (the iTunes import
path uses this directly) act on a file that does not exist.

**The fix.** The organizer now checks the file on disk before reporting the no-change
case as a success, and returns a clear error naming the book and the missing path if it
is gone. The batch re-organize step has a similar shortcut that was left unchanged here
and is filed as its own task.

## 3. The author-usage count that scanned too narrow a range

**What it was.** Deleting an author, whether by hand or through the "purge empty
authors" cleanup, is guarded by a count of every book that still credits them. Book
records are stored under keys that begin with the book's id, and the count walked the
range of keys that begins with a digit, because the ids the app mints itself always do.
But the app also accepts ids handed to it by an importer, a migration, or a backup
restore, and those can begin with a letter or an underscore. Those records sat just
outside the scanned range.

**Why it mattered.** A book with such an id that credited an author only through the
older single-author field was not counted. If that was the author's only remaining
credit, the guard reported zero, and the author record, including anything a user had
edited on it, was deleted with no error. The same too-narrow range was also used by the
lookup that merge and delete flows use to find which books to re-link first, so a merge
could skip re-linking those books.

**The fix.** Both scans now walk the full range of book records, with the same
structural filter the other already-fixed scans use to skip the app's secondary indexes.
Two tests create a book with a letter-leading id and confirm it is counted and re-linked.
The one-off check for how many such ids exist in the live library was left for the
owner to run, since it touches production data.

## 4. The backup verification that never ran

**What it was.** When a backup is created, the app computes a SHA-256 fingerprint of the
archive, a short value that changes if even one byte of the file changes. It reported
that fingerprint in the API response and then discarded it. The restore endpoint accepts
a "verify" flag, and the Settings page turns it on by default, but with nothing saved to
compare against, the flag only produced a log line saying verification was "not yet
implemented" and the restore went ahead.

**Why it mattered.** A restore is the moment you most need to know a backup is intact.
A truncated or corrupted archive, which can happen on a full disk or an interrupted copy,
restored with the same success message as a good one. The user saw "verified" behavior
in the UI that did not exist.

**The fix.** Each new backup now gets a small companion file holding its fingerprint,
written atomically so it is either complete or absent. A restore with "verify" on reads
that file, recomputes the fingerprint, and refuses with a clear "does not match" error
before touching anything if they differ. Backups made before this change have no
companion file; verifying one of those returns an explanatory error that tells the user
to restore with verification off or re-create the backup, rather than pretending. The
first version of this fix simply refused every verified restore, which would have broken
the default UI path; it was sent back and replaced with real verification.
