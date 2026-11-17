<!-- file: docs/executive-summaries/2026-11-17-merge-undo-snapshot-pin-executive-summary.md -->
<!-- version: 1.5.0 -->
<!-- guid: 6d2c9e41-0b7a-4f3d-8e15-a3c7b94d1f62 -->
<!-- last-edited: 2026-11-17 -->

# A merge the system made on its own can no longer lose its way back

**Date:** 17 November 2026. This is the first of a short series of changes to
how automatic merges are recorded and reviewed. It changes no merge behaviour
and merges nothing.

## Executive Summary

- When the library merges copies of the same book without anyone clicking merge,
  it writes down how to put things back: it keeps a saved copy of each book as it
  was just before the merge, and a journal entry that points at those saved
  copies. Undoing the merge means restoring the books from them. Every merge the
  system records this way is covered by this change: the pass that merges
  automatically when it is certain (auto-resolve, when it is switched on), the
  merge the language model suggests, a single pair merged from the review screen,
  a bulk merge from a filter, the file-hash pass, and a whole cluster merged at
  once by the owner.
- The library also trims old saved copies of a book to save space. That trim did
  not know the journal existed. It kept the newest few copies of each book and
  deleted the rest, so after a handful of ordinary edits to a book and one trim,
  the copy the undo needed could be gone.
- If that happened, the undo as it was written would not have refused cleanly.
  It would have put one book back, then failed on the other and stopped half
  way, leaving the pair in a state that could not be finished by trying again.
  The undo has no button or schedule that calls it today, so this has not
  happened to a real merge. What is fixed is what would have happened the day
  someone first used it.
- The trim now leaves alone every saved copy a live journal entry points at.
  There is one rule for what may be deleted, used both by the real trim and by
  its preview, so the two cannot disagree. If the journal cannot be read, the
  trim deletes nothing and names the entry it could not read, rather than
  guessing. An entry that has been undone releases its copies again.
- The undo now checks that both saved copies exist before it changes anything.
  If one is missing it stops, changes nothing, and names the missing copy.
- A merge that is still being written down is protected too, and only as much as
  it needs. Its journal entry exists before the merge finishes and is completed
  afterwards, and the saved copy it will point at is created by the merge itself.
  So until the entry is completed, the trim keeps exactly one saved copy of each
  of the two books: the first one made after the merge began, which is the one
  the completed entry will name. Two merges in flight on the same book each keep
  their own. There is no timer on this: an entry whose merge failed keeps that
  one copy for as long as it exists, and the preview counts such entries so they
  can be looked at. An entry whose merge was refused before it wrote anything is
  closed and keeps nothing. (A closed entry still counts as holding its book, so
  the book is not deleted for good from the trash while the entry exists. That is
  the cautious choice and it is unchanged.)
- An entry written before a merge now lists every book the merge involves, not
  only the two it expects to be the winner and the loser. If the merge turns out to
  have kept a different book than the one expected, and the step that completes
  the entries then fails part way, no book of the merge is left without a saved
  copy being kept for it. The same entry also keeps every book of the merge from
  being deleted for good from the trash while it is unfinished, so a book the
  merge really removed is not lost to the clean-up just because the expected
  winner was not the one kept. (An entry written before this change lists only the
  two books it expected, and is read as it always was.)
- If, after a merge, the library cannot read a book's list of saved copies to
  complete the entry, it now leaves the entry unfinished and says so in the log,
  rather than completing it with "no saved copy". An entry completed that way would
  have let the trim delete the copy the undo needs, and the undo would have put
  one book back and left the other where it was.
- Two merges that start in the same instant can no longer overwrite each other's
  entry. A new entry is only written where there is none, and a merge that is
  refused closes only the entry it wrote itself.
- If the library cannot read a book's list of saved copies when a merge is about
  to start, the merge now refuses to run and writes nothing, instead of noting
  "this book had no saved copies", which would have been untrue.
- The preview of the trim now says how many copies it is keeping for this reason,
  and counts, without changing anything, the merges whose saved copies are
  already gone and the entries that were never completed, so we learn whether any
  real merge has already lost its way back.
- The automatic merge pass, when run as a preview, now keeps its list. It used to
  print counts and a short sample with book titles and then forget them, so
  nobody could review the exact pairs before the pass was switched on. It now
  stores the list, capped, with the strength of each signal and which rule
  decided it, and no titles and no description text from the files. It also
  counts the pairs by the reason that let them through, so the number that rest
  on one older, loosely worded rule is visible before anyone switches the pass on.
- The stored list is a review aid, not a promise. The real pass is not tied to a
  list: when it runs it looks at the pairs again and stops after a set number of
  merges that SUCCEEDED, so a pair it skips or that fails does not use up the
  number, and it can merge pairs further down the list than the number says. The
  list says so itself. Tying the real pass to a reviewed list is recorded as a
  task to finish before the pass is ever switched on.

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
missing, and the loosely worded rule above and the missing link between a
reviewed list and the real pass are recorded as tasks to finish before the pass
is ever enabled, not changed in this work. The preview only counts those.
