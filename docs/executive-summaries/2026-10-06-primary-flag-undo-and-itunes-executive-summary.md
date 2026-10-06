<!-- file: docs/executive-summaries/2026-10-06-primary-flag-undo-and-itunes-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9554db3c-0ebf-413d-9ff1-bbccec023eb0 -->
<!-- last-edited: 2026-10-06 -->

# Undoing a repair no longer hides the real book, and the active iTunes library's books are never re-ranked

PR: https://github.com/falkcorp/audiobook-organizer/pull/3805

## Executive Summary

- Some books exist in several copies: an abridged and an unabridged
  edition, an older import and a newer one, and so on. The library groups
  such copies together and marks one of them the group's "primary" copy.
  The primary is the copy the listening apps (Audiobookshelf and the
  phone apps) show; the others are hidden from them.
- Undoing a "consolidation leftovers" repair could hide the real book.
  That repair folds an empty leftover record (0 minutes of audio) into the
  real copy of the book (for example 15 minutes). If the leftover and the
  real copy had both been marked primary, the undo moved the primary mark
  back onto the empty leftover. It also took the mark off the real copy,
  which the repair had never changed, so the real book disappeared from
  Audiobookshelf.
- An undo now changes only what the repair itself changed. When another
  copy already had the primary mark before the repair, that copy keeps it,
  and the restored leftover comes back as a non-primary copy. This works
  both when the repair finished and when it stopped partway.
- Books from the owner's active iTunes library are never written to. That
  rule now also covers the primary mark, in every merge repair (duplicate
  copies, fragment consolidation, consolidation leftovers) and in undo. If
  a repair or an undo would have to change an iTunes book's mark, it
  changes nothing in that group and reports which book and why.

## Undo hid the book the repair had folded the leftover into

**What it was.** When a repair retires a book that was the primary copy,
it passes the primary mark to another copy in the group. In the leftover
case that other copy, the real book, usually already had the mark, so
nothing was actually passed on. The repair's record still read "handed to
the real book", and undo trusted that record. It put the mark back on the
leftover and removed it from every other copy, the real book included.
When the repair stopped partway (because the expected copy was no longer
the right choice), it recorded nothing, and undo did the same thing.

**Why it mattered.** After the undo, the listening apps showed a
zero-length leftover instead of the real 15-minute book. The real book
stayed in the database but was hidden from the apps, with no warning. Any
undo of these repairs would have done this.

**The fix.** The repair now records which of three things happened:

- it gave the primary mark to a copy;
- the copy already had the mark;
- it refused and changed nothing.

When the copy already had the mark, or the repair refused, undo leaves
that copy as primary and brings the leftover back as non-primary. When the
repair really did move the mark, undo moves it back as before. Repairs
recorded before this change cannot be told apart, so they still undo the
old way. Undo of those older repairs is being checked separately.

## The primary mark of an iTunes book could change

**What it was.** Before passing on the primary mark, a repair checked that
no copy in the group was an iTunes book. It then spent some time moving
listeners' progress before it actually changed any marks. If an iTunes
copy's mark changed during that gap, the later step could write to it.
Undo did not check iTunes books at all, and neither did the duplicate-copies
and fragment-consolidation repairs at that step.

**Why it mattered.** Never writing to the active iTunes library's books is
a standing rule, so the owner's iTunes collection stays exactly as the
owner keeps it. A rule that holds only most of the time does not do that.

**The fix.** There is now one shared iTunes check. Repairs and undo run it
on each copy at the moment they are about to change its mark, while
holding the lock that keeps other changes out of that group. If any copy
fails the check, nothing in the group changes. The repair stops and
reports partway done; undo leaves the group as it is and names the iTunes
book in its result.

Verified by automated tests, using made-up books, that reproduce both
problems: the real book keeps its primary mark after undo, and the iTunes
copy is never written, by the repair or by the undo.
