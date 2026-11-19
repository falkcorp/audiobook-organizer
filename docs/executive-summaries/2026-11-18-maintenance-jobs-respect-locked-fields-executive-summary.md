<!-- file: docs/executive-summaries/2026-11-18-maintenance-jobs-respect-locked-fields-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5b3c0b30-3329-45c7-8ab5-ff4e0d328316 -->
<!-- last-edited: 2026-11-19 -->

# Cleanup jobs no longer undo what you locked

**Date:** 18 November 2026. The effect on a real library is not measured: the
checks below ran against test libraries, and no cleanup job has been run against
the real library with this change.

## Executive Summary

- You can lock a book's title, author, narrator or description so that no
  automatic process changes it. Most of the app respects that lock. A group of
  background cleanup jobs did not look at it: when one of them ran, it could
  quietly put back its own idea of a locked field, and your edit was gone with
  nothing to tell you.
- The jobs fixed here are: cleanup of one-book series and duplicate series,
  the "read by" narrator fix, the author/narrator swap fix, the fetch of
  missing authors, the author split, the iTunes regroup (which could rewrite a
  title), the audit that restores a lost description, and the three author
  repairs that move a book from one author to another (the rewrite of a book's
  main author, the fix for a title that was filed as an author, and the merge of
  two authors). The same repair also deletes a leftover author row after moving
  books off it; that last step now leaves a locked book alone too, and keeps the
  row while a locked book still names it.
- Each of them now checks the lock for the book it is about to change, and
  leaves a locked book exactly as it was. Where a job changes a book in one save,
  the check is made inside that save, under the book's own write lock. The lock
  itself is saved under a different lock than the book, so a lock saved in the
  same instant as a job's save can still lose: the gap is one save wide, not
  gone, and a follow-up item tracks closing it. The job's summary says how many
  books it left alone and why, so a run that skipped a locked book is visible
  instead of looking like it did nothing. The count includes books held by a
  repair's own lock, not only locks a person set.
- If the lock information cannot be read, the job leaves the book alone. An
  unreadable lock is never treated as "not locked".
- Two cases matter beyond the plain skip. When a cleanup job was about to remove
  a duplicate series or a duplicate author, and a locked book still belongs to
  it, the duplicate is kept and the job reports it, rather than deleting
  something a book still points at. Moving a book from one author to another used
  to be two saves (its credits, then its main author). The title-filed-as-author
  fix and the leftover-row clean-up now do both in a single save, so those two
  never leave a book half moved (credited to the new author while still pointing
  at the old one), and nothing has to be put back if a lock turns up. The author
  split still saves in two steps: a lock saved between them leaves that book's
  credits split while its main author is unchanged, the run counts the book as
  held rather than as an error, and a later run finishes it once the lock is
  lifted.
- Merging two authors is all or nothing for each author. If any one of the
  author's books has its author locked, none of that author's books move, the
  job reports the author as held (not as failed), and the author stays. If a
  lock is saved part-way through, the books already moved stay moved, the job
  counts them, refreshes the author lists, and keeps the author for the next run.
- Two other jobs were checked and need no change: the filing of fragments into
  one book already respected the title lock (a test now proves it), and the
  intro transcription jobs only write the transcript and what was heard in it,
  which no lock covers (a test now fails if that ever stops being true).

## What this does not do

- The same kind of unguarded write exists in other places that this change
  deliberately leaves alone and that stay on the to-do list: the author and
  series rename tools in the server, the scheduler's extra jobs, the title
  backfill and title repair jobs, the series de-numbering job, the metadata
  refresh handler, the entity handler, and the duplicate-merge helpers in the
  server. The author-id repair job, and the other places that use the shared
  step that moves a book's credits (the Authors page merge and the AI author
  reassignment), still move books without a lock check. The Authors page merge is
  a person's own action, so it is deliberately not blocked by a lock; the other
  two are on the to-do list. The author-duplicate-merge job, the title-as-author
  fix and the author merge do check, as described above.
- A separate to-do item stays open too: the cleanup jobs are not yet known to
  the system that stops two conflicting jobs from running at once, so a
  cleanup job can still run beside a series operation. The list of what each
  job writes is now worked out in a test, which fails if a job is missing one, but
  connecting it changes how every cleanup job queues, so it needs its own
  review.

## Why it mattered

A locked field is a promise to the person who set it. Breaking it silently is a
form of data loss: the old value is gone, the job reports success, and nobody
learns that their correction was undone until they notice the book is wrong
again.
