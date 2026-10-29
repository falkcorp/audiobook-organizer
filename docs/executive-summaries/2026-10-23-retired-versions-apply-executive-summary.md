<!-- file: docs/executive-summaries/2026-10-23-retired-versions-apply-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: b7e34a18-52c9-4f60-9d1b-8a63c0e5f247 -->
<!-- last-edited: 2026-10-29 -->

# The trashed-duplicates clean-up can now actually run, behind an off switch

## Executive Summary

The library has 51,450 books sitting in the trash that were merged into another
copy. Last week's preview sorted them and showed the owner what a clean-up
would do. This work builds the clean-up itself. **It ships switched off.** Until
the owner turns on the setting `retire_versions_apply_enabled`, nothing changes:
the screen still shows only the preview, there is no Apply button, and the
server refuses any attempt to apply.

## What the clean-up does, per trashed book

Of the 51,450 trashed books, 17,602 are ones the clean-up may touch: 15,548 whose
files are their own, and 2,054 that point at a file the kept copy also points at.
For each of those it:

- keeps every file record. Nothing is deleted. The records are re-labelled as an
  "older version" that belongs to the kept copy, so the files stay reachable and
  the trash can later be emptied without losing them;
- moves the book's iTunes and other outside identifiers to the kept copy, so a
  later import finds the kept copy instead of a dead entry;
- marks the trashed book as merged into the kept copy;
- writes a record of every step before taking it, so the work can be traced and,
  later, reversed.

Books the preview left alone (too recent, owner said no, listening progress,
Doctor Who and the other protected series, files under the iTunes folder, or no
clear kept copy) are still left alone.

## Trashed books that were cleaned up are protected from every way out of the trash

The trash is emptied automatically after 30 days. A book whose files were moved
onto another copy has no files of its own any more, so the purge would have
treated it as empty and deleted it, taking with it the only record of where
those older versions came from. Until the purge that keeps a snapshot is built,
the purge, "Purge now", "Discard progress and purge", the hard delete and
"Restore" all refuse such a book and say why (restoring it would only bring
back an empty book). The same refusal now covers the other ways a trashed book
could be brought back or removed: the metadata "revert" button, undoing an
automatic merge, undoing a delete, and undoing a combine. A book that holds
nothing but older versions of other books counts as owning files: the purge
keeps it rather than failing every night, and "Discard progress and purge" and
the live delete refuse it before anyone's listening progress is touched. If the check cannot be read, the book is
left alone. Asking to restore a book that does not exist now says "not found"
rather than a generic server error.

## Why it is safe to run

- **Preview first.** An apply is a dry run unless it is explicitly asked to write.
  The dry run counts what it would do and changes nothing.
- **Checked again at the last moment.** Just before each book, the clean-up looks
  at it afresh. If anything about it changed since the plan was made, it is
  skipped and counted as "changed since plan", never applied on stale facts.
- **Stops and restarts cleanly.** A book that is already done is skipped, so a
  second run applies nothing. If a run is cut off after a book's files moved but
  before its identifiers did, a fresh plan lists just that book as "finish the
  tail" and the next run completes it. A book is only counted as cleaned up if an
  older version actually holds its files, never because an empty version exists.
- **One look at the library per run.** The settings and indexes the clean-up
  needs are read once when the run starts, not for every book and never while
  the merge lock is held. If the switch is flipped off as a run starts, the books
  are reported as "try again later", not as failures.
- **No two workers on the same book.** Books going to the same kept copy are
  handled one after another, never together. A single lock also keeps the
  clean-up from running at the same moment as a merge.
- **Counters.** Every run reports how many books were applied, skipped, or found
  changed since the plan.

## The owner's "no" is kept, and every clean-up is on record

- **A "no" follows the book.** When the owner has said no to cleaning up a trashed
  book, or has said two books are not the same work or not the same copy, the
  clean-up holds that book. Before, the check looked the "no" up by the book's id
  alone, so a "no" about a book that had since been merged, cleaned up or purged
  was not found and the book could be cleaned up anyway. The check now follows
  the book to wherever its files went. Each book is checked against the books as
  they are at that moment, so cleaning up one book does not hide a "no" about the
  next. If the list of "no" answers cannot be read, the whole run is refused
  rather than run without it.
- **Who did what.** Each cleaned-up book now gets an "applied" record, and an undo
  gets an "undone" record. Each names the plan it came from and the person who
  clicked Apply or Undo, or says it was automatic. A run cut off before it wrote
  the record writes it when the next run finishes the book, and credits the
  person who started the original run. An undo writes "undone" only for a book
  that was actually cleaned up.
- **Kept fast.** Writing these records no longer makes the next book re-read the
  whole list of decisions while every other merge waits.
- **Restored books are ordinary again.** A trashed book whose older versions were
  handed back to it is no longer treated as a cleaned-up shell by the library
  scan. During a scan, the check for files that already exist as an older version
  is now looked up once per scan instead of once per file. The scan drops that
  cached answer whenever a book is cleaned up, restored or rehashed, so it never
  imports a duplicate of a file that already lives in an older version.

## What is not finished, and must be before the switch is turned on in production

- **Undo of a clean-up operation.** The undo work (the fifth of nine changes
  in this series) can reverse the three kinds of step a clean-up writes, so a
  whole clean-up operation can be undone from the Operations page. An operation
  that holds a step the undo cannot read is still refused as a whole, with a
  plain message, instead of being half-undone.
- **Books from a combine that can still be undone are held, not cleaned up.**
  They are reported as "try again later" until the clean-up links itself to the
  combine it absorbed, so undoing the combine also undoes the clean-up.
- **The automatic-merge history is not read yet.** The only protection against
  cleaning up a book from a very recent automatic merge is the 14-day minimum age.
- **A snapshot, not a live view, of combines.** The clean-up reads the list of
  combines that can still be undone once when it starts, so a combine made while
  a run is in progress is not seen by that run. Undoing a combine now refuses a
  cleaned-up book, which covers the gap.
- **No "no" button for this clean-up yet.** The Review screen cannot yet record a
  "no" for a single trashed book in this clean-up. A "no" recorded another way is
  honoured, but the button, and a way to take the "no" back, still need building.
- **Three gaps in the records.** A book cleaned up before the records existed has
  no "applied" record. If writing an "applied" record fails, nothing writes it
  again. A run cut off at one exact point can leave an undo writing "undone" for
  a step that never landed. While the switch stays off, no clean-up runs in
  production, so none of these can happen yet.
- **Speed.** A single lock means one book at a time is written. The plan
  estimated 45 to 130 minutes for the whole trash. That is an estimate, not a
  measurement.

## What did not change

With the switch off, no book, file record or file on disk is modified, and the
Review screen looks the same as before.
