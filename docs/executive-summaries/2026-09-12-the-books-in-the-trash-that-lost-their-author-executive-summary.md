<!-- file: docs/executive-summaries/2026-09-12-the-books-in-the-trash-that-lost-their-author-executive-summary.md -->
<!-- version: 1.0.1 -->
<!-- guid: 9a2f6c1e-4b83-47d5-b0e9-5c71d8f23a64 -->
<!-- last-edited: 2026-09-12 -->

# The books in the trash that lost their author

**Pull request:** [#3309](https://github.com/falkcorp/audiobook-organizer/pull/3309)

## Executive Summary

The app has several ways to tidy up author names: merging two spellings of the
same person, splitting "Alice Smith & Bob Jones" into two people, turning a
mistaken author into a narrator, and a few automatic clean-up jobs that do the
same things in bulk. Each one works the same way. It first moves every book
from the old author record onto the right one, and then deletes the old record.

The step that gathered "every book" skipped the books in the trash. The delete
step did not. So each time one of these tidy-ups ran, any trashed book that
belonged to the old author had its author credit wiped, and the book was left
pointing at an author record that no longer existed. Nothing looked wrong at the
time, because trashed books are hidden. The damage only showed up if someone
restored one of those books: it came back with no author.

Every one of those paths now gathers books in any state, including the trash, so
a trashed book is moved to the right author like any other. The move leaves the
book in the trash; nothing is restored by accident.

Each path also now checks one last time, right before the delete, that no book
still names the old author. If anything was left behind (for example, a book
that failed to save), the old author is kept and the problem is reported,
instead of being deleted and taking that book's credit with it. Several of these
paths used to report the failure and delete anyway.

One automatic merge job was also too cautious in the opposite direction. It
refused to merge an author when it saw credits it could not move, but some of
those credits belonged to books that had already been permanently deleted and
were going to be cleaned up by the merge anyway. It now tells those apart and
only holds back for credits on books that still exist.

**What this does not fix:** trashed books already damaged by earlier merges and
splits are still damaged. They fall into two groups:

- A trashed book whose main author was the deleted record still points at that
  missing record, so it can be found. A separate repair job to find and fix
  those has been proposed but not built yet.
- A trashed book that listed the deleted author only as a second or later
  co-author has no trace left. The credit line was removed and nothing on the
  book records that it was ever there. The app's history shows which author
  records were deleted and when, but not which books lost a credit, so these
  books cannot be found from what is stored today.
