<!-- file: docs/executive-summaries/2026-10-05-trash-keeps-your-progress-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9e710e72-dafe-4d60-921b-b004e34b8f48 -->
<!-- last-edited: 2026-10-05 -->

# The trash keeps your listening progress, without keeping books forever

PR: https://github.com/falkcorp/audiobook-organizer/pull/3771

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
