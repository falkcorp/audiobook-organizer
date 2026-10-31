<!-- file: docs/executive-summaries/2026-10-30-retired-versions-apply-preconditions-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e3f5b12-7a94-4c0d-b6e1-2f58a9c4d703 -->
<!-- last-edited: 2026-10-30 -->

# Retiring a trashed book no longer skips books that a combine absorbed, and a version that still holds files cannot be deleted by an undo

## Executive Summary

The retired-versions clean-up moves the audio files of a trashed duplicate onto the live
book it lost to, as a hidden "retired version", and never deletes a file record. Three
preconditions stood between that clean-up and being switched on. Two are now met. The third
turned out to be met already, and now has tests that prove it.

## What changed

- **One code path writes the retire.** The clean-up used its own copy of the code that
  creates the hidden version, moves the file records and writes the undo journal. It now
  uses the shared Repairs writer, the same code the rest of the Repairs tab uses, and the
  copy is deleted. The hidden version keeps its notes about where the book came from.
- **Books that a merge absorbed are no longer skipped.** A book that was merged into another
  and can still be un-merged used to be held back, because undoing that merge could not
  find the clean-up. The clean-up now records which merge the book came from, so undoing the
  merge finds and reverses the clean-up first. A book that two un-undone merges both name is
  still held back, since one record can point to only one merge; undoing all but one of those
  merges releases it.
- **An undo cannot delete a version that still holds files.** Undoing the creation of a hidden
  version while some file record still belongs to it would have removed the version from under
  that file. The undo refuses, says which step has to be undone first, keeps going with the
  rest of the operation, and the version stays. This protection was already in place; new
  tests now prove it for a half-finished undo and for a file record put there by a different
  operation.

## What did not change

Undoing a merge whose absorbed book has received files since the merge is still refused by the
merge's own safety check, whether or not the clean-up touched it. That is a separate decision
for the owner and is recorded in the decision note for this change.
