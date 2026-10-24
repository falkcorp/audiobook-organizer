<!-- file: docs/executive-summaries/2026-10-23-retired-versions-apply-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b7e34a18-52c9-4f60-9d1b-8a63c0e5f247 -->
<!-- last-edited: 2026-10-23 -->

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
- writes a record of every step before taking it, so a later undo has something
  to work from.

Books the preview left alone (too recent, owner said no, listening progress,
Doctor Who and the other protected series, files under the iTunes folder, or no
clear kept copy) are still left alone.

## The nightly trash purge now leaves these books alone

The trash is emptied automatically after 30 days. A book whose files were moved
onto another copy has no files of its own any more, so the purge would have
treated it as empty and deleted it, taking with it the only record of where
those older versions came from. Until the purge that keeps a snapshot is built,
the purge, "Purge now", "Discard progress and purge", the hard delete and
"Restore" all refuse such a book and say why (restoring it would only bring
back an empty book). If the check cannot be read, the book is left alone.

## Why it is safe to run

- **Preview first.** An apply is a dry run unless it is explicitly asked to write.
  The dry run counts what it would do and changes nothing.
- **Checked again at the last moment.** Just before each book, the clean-up looks
  at it afresh. If anything about it changed since the plan was made, it is
  skipped and counted as "changed since plan", never applied on stale facts.
- **Stops and restarts cleanly.** A book that is already done is skipped, so a
  second run applies nothing. If a run is cut off after a book's files moved but
  before its identifiers did, the next run finishes just that book.
- **No two workers on the same book.** Books going to the same kept copy are
  handled one after another, never together. A single lock also keeps the
  clean-up from running at the same moment as a merge.
- **Counters.** Every run reports how many books were applied, skipped, or found
  changed since the plan.

## What is not finished, and must be before the switch is turned on in production

- **Undo is not wired up yet.** The record of each step is written, but the
  button that reverses an operation does not yet understand these steps. That
  arrives with the undo work (the fifth of nine changes in this series). Do not
  turn the switch on before it lands.
- **Books from a combine that can still be undone are held, not cleaned up.**
  They are reported as "try again later" until undoing a combine also undoes the
  clean-up. This is also in the undo work.
- **The automatic-merge history is not read yet.** The only protection against
  cleaning up a book from a very recent automatic merge is the 14-day minimum age.
- **Speed.** A single lock means one book at a time is written. The plan
  estimated 45 to 130 minutes for the whole trash. That is an estimate, not a
  measurement.

## What did not change

With the switch off, no book, file record or file on disk is modified, and the
Review screen looks the same as before.
