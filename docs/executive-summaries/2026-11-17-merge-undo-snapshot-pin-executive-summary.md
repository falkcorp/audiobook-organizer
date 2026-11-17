<!-- file: docs/executive-summaries/2026-11-17-merge-undo-snapshot-pin-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6d2c9e41-0b7a-4f3d-8e15-a3c7b94d1f62 -->
<!-- last-edited: 2026-11-17 -->

# A merge the system made on its own can no longer lose its way back

**Date:** 17 November 2026. This is the first of a short series of changes to
how automatic merges are recorded and reviewed. It changes no merge behaviour
and merges nothing.

## Executive Summary

- When the library merges two copies of the same book on its own, it writes down
  how to put things back: it keeps a saved copy of each book as it was just
  before the merge, and a journal entry that points at those saved copies.
  Undoing the merge means restoring both books from them.
- The library also trims old saved copies of a book to save space. That trim did
  not know the journal existed. It kept the newest few copies of each book and
  deleted the rest, so after a handful of ordinary edits to a book and one trim,
  the copy the undo needed could be gone.
- When that happened, the undo did not refuse cleanly. It put one book back, then
  failed on the other and stopped half way, leaving the pair in a state that
  could not be finished by trying again.
- The trim now leaves alone every saved copy a live journal entry points at.
  There is one rule for what may be deleted, used both by the real trim and by
  its preview, so the two cannot disagree. If the journal cannot be read, the
  trim deletes nothing rather than guessing. An entry that has been undone
  releases its copies again.
- The undo now checks that both saved copies exist before it changes anything.
  If one is missing it stops, changes nothing, and names the missing copy.
- A merge that is still being written down is protected too. Its journal entry
  exists before the merge finishes and is completed afterwards, and the saved
  copy it will point at is created by the merge itself. So until the entry is
  completed, the trim keeps every saved copy of those two books newer than the
  newest one that existed when the merge began. There is no timer on this: an
  entry whose merge failed keeps those copies for as long as it exists, and the
  preview counts such entries so they can be looked at.
- The preview of the trim now says how many copies it is keeping for this reason,
  and counts, without changing anything, the merges whose saved copies are
  already gone and the entries that were never completed, so we learn whether any
  real merge has already lost its way back.
- The automatic merge pass, when run as a preview, now keeps its list. It used to
  print counts and a short sample with book titles and then forget them, so
  nobody could review the exact pairs before the pass was switched on. It now
  stores the full list, capped, with the evidence for each pair and which rule
  decided it, and no titles. It also counts the pairs by the reason that let them
  through, so the number that rest on one older, loosely worded rule is visible
  before anyone switches the pass on.

## The trim could delete what the undo needed

**What it was.** Two parts of the library that never looked at each other: the
trim of old saved copies, and the journal of automatic merges.

**Why it mattered.** A merge that cannot be undone is a merge nobody can
correct. If the system ever merged two different books by mistake, one of them
would be gone from the library with no way to bring it back, and a failed undo
could leave its sibling editions half moved.

**The fix.** The trim asks one shared list of "copies a live journal entry needs"
before it deletes, and the undo checks before it acts.

## The preview of the automatic merge pass forgot its answer

**What it was.** The preview computed which pairs it would merge, showed fifty,
and discarded the rest. Only the counts reached the log.

**Why it mattered.** The switch that lets the pass actually merge is meant to be
turned on only after someone has looked at what it would do. Without the list
that review was not possible.

**The fix.** The preview stores its list as the result of the run, with a clear
cap and a count of anything beyond it. The real pass is unchanged and the switch
stays off.

Not done here, on purpose: nothing repairs a merge whose saved copies are already
missing, and the loosely worded rule above is recorded as a task to tighten
before the pass is ever enabled, not changed in this work. The preview only
counts those.
