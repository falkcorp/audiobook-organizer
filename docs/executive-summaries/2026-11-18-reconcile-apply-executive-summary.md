<!-- file: docs/executive-summaries/2026-11-18-reconcile-apply-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: e2bdcb58-8b1a-4c4d-bccb-3f57448befa9 -->
<!-- last-edited: 2026-11-18 -->

# Re-pointing a book at its found file now moves the whole record, and needs the right permission

**Date:** 18 November 2026. PR #4719. Three open items about the old "reconcile"
action, which re-points a book at an audio file the library found for it.

## Executive Summary

- When the library finds the file that a book lost, the "apply" step used to
  update the book and leave the book's separate file record pointing at the old,
  missing location, still marked as missing. Players and the Files view read that
  file record, so the book looked repaired in one place and broken in another,
  and the found file still counted as belonging to nobody.
- The apply step now moves the file record together with the book. It rewrites
  the existing record (it is never deleted or replaced), marks it present, and
  records the real size. If any check fails, nothing is written for that book. If
  the book cannot be updated after the file record was, the file record is put
  back.
- The old hash of the file is cleared rather than kept, because the apply step
  does not know how the file was matched, and a wrong hash could make two
  different recordings look like duplicates. A background job fills it in again.
- Undoing the operation now restores both the book and its file record. Before,
  the book's half of the history was in a form the undo engine cannot reverse, so
  undoing only the file record would have left the two out of step.
- The action was reachable by the editor role, although the same action started
  from the general operations page needs the settings permission. It now needs
  that permission everywhere. Editors lose the shortcut, and an admin key limited
  to that one permission gains access to it (the one widening, flagged for review).
- A second route that scanned the whole library on request, available to
  read-only users and outside the scan's single-run guard, is removed. Nothing
  used it.

## Book and file record moved together

**What it was.** A book and the audio file it plays are stored as two records.
The reconcile apply step updated the book's path and not the file record's.

**Why it mattered.** A book repaired this way still showed a missing file in the
places that read the file record, and the found file was still treated as unused.
A later cleanup could then act on a file the library believed nobody owned.

**The fix.** The apply step checks everything first (exactly one file record at
the old path, none at the new one, nobody else or no retired record holding the new
file), then writes the file record and the book, and puts the file record back if
the book write fails. A rerun finishes a half-done match.

## Undo that restores both halves

**What it was.** The book's change was written to the undo history under a name
the undo engine does not know how to reverse.

**Why it mattered.** Once the file record's change became reversible, an undo
would have put the file record back and left the book pointing at the new file.

**The fix.** The book's change is now written under the name the undo engine
reverses, and a test runs the real undo and checks both records come back.
Operations recorded before this change keep their old form and stay not undoable.

## Permission and the full-library route

**What it was.** The apply route accepted the editor role's scan permission,
while the action itself declares the stronger settings permission. A separate
preview route ran a full-library file sweep for any signed-in reader.

**Why it mattered.** An editor could re-point books through one door that the
other door refused. Any reader could start a heavy sweep beside a scan.

**The fix.** The apply route now needs the settings permission, matching its
definition, and the preview route is removed (it was the owner's call, taken on
evidence that nothing called it, and kept as its own commit so it can be reverted
alone).

Verified with 18 mutation checks on the new logic (18 caught), the permission
and reach tests, and a real undo round trip.
