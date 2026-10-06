<!-- file: docs/executive-summaries/2026-10-05-trash-keeps-your-progress-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 9e710e72-dafe-4d60-921b-b004e34b8f48 -->
<!-- last-edited: 2026-10-05 -->

# The trash keeps your listening progress, without keeping books forever

PRs: https://github.com/falkcorp/audiobook-organizer/pull/3771,
https://github.com/falkcorp/audiobook-organizer/pull/3777

## Executive Summary

- Emptying the trash never throws away a listener's progress on a book.
- When another copy of the same book is still in the library and the
  audiobook apps can see it, the progress (where you stopped, finished or
  not, how much you listened, bookmarks) is moved onto that copy first, and
  only then is the trashed copy deleted. A copy the apps cannot see never
  gets it, so progress is never moved somewhere out of sight. If the move
  does not finish completely, nothing is deleted and the progress stays
  exactly where it was.
- When there is no such copy, the book stays in the trash. The trash list
  now marks it "has progress" and says whose progress it is (for example
  "reader: 42%").
- For those books there is a new button: "Discard progress and purge". It
  asks first, naming the book and the progress that will be lost. Only then
  does it delete the book and the progress together. It refuses a book that
  is not in the trash, and every use is written to the activity log.
- The nightly cleanup now reports three new numbers: books whose progress was
  moved to another copy, books kept because of progress, and books kept
  because a move failed.

## The problem

**What it was.** A week ago the nightly trash cleanup learned to skip any
book someone had listening progress on, so that progress was never lost.
But nothing ever cleared those books, so they would sit in the trash
forever, and nobody could see why.

**Why it mattered.** Progress is the one thing the library cannot rebuild.
Keeping it was right, but the trash slowly filling with books that could
never leave, with no explanation, was not.

**The fix.** The cleanup now moves the progress to a living copy of the book
when there is one, using the same careful all-or-nothing move the merge
tools use. When there is none, the book is kept and labelled, and the owner
decides: restore it, or discard its progress on purpose.

## Follow-up: every delete button now protects progress the same way

A review of the first change found a few gaps, now closed (PR #3777).

- **"Purge now" behaves like the nightly cleanup.** Pressing it on a trashed
  book used to delete the book and its progress straight away. Now it moves
  the progress to the copy the audiobook apps can see and then deletes, or,
  when there is no such copy, it stops and says why, and offers "Discard
  progress and purge" so dropping the progress is always a deliberate
  choice. Deleting a book from the library, alone or in bulk, follows the
  same rule.
- **Bookmarks count.** A book whose only progress was a bookmark was treated
  as having none, so it could be deleted with the bookmark. It is now kept
  or moved like any other progress.
- **Listened time is never lowered when copies combine.** When two copies of
  a book were combined, the total listening time could drop to the smaller
  copy's number (for example from 5,000 seconds to 200). It now keeps the
  larger one, and never adds the two together.
- **Undo steps cannot lose your place half way.** Putting progress back after
  an undo used to clear your saved positions and then write them again one
  by one; a failure in between could leave a book with no position at all.
  It is now one step that either happens completely or not at all. A few
  related edge cases in the undo (an old position without a date winning
  over a newer choice to mark a book unfinished, and listening done while
  the undo was running) were fixed too.
- **The trash list tells the truth.** It now says whether there is a copy to
  move the progress to, and offers "Move progress and purge" instead of
  "Discard" when there is. It shows "progress unknown" when progress could
  not be read, and each person sees only their own progress plus a count of
  other people with progress, unless they manage users.
- **iTunes stays intact.** Moving a copy to the trash, or purging it, no
  longer removes a track from the iTunes library that another copy of the
  book still uses, and a purge changes nothing in iTunes unless the book was
  really deleted.
