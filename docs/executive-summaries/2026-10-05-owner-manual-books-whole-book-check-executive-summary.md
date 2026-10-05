<!-- file: docs/executive-summaries/2026-10-05-owner-manual-books-whole-book-check-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: cffa8daf-c296-49c9-9e18-b963b1e63104 -->
<!-- last-edited: 2026-10-05 -->

# Hand-applied books are now recognized by everything they carry

PR: https://github.com/falkcorp/audiobook-organizer/pull/3754

## Executive Summary

- Doctor Who, Big Finish and Torchwood books are applied by hand, one at a
  time. No bulk job may change them. Six maintenance jobs decided whether a
  book belonged to those libraries by looking only at the book's own record
  (its folder, title, narrator and publisher), and some looked only at the
  folder.
- A book could carry the signal somewhere else: in the name of one of its
  audio files, in what the intro recording says, in an author credit such as
  "Big Finish Productions", in its series, or in a franchise tag. Such a book
  slipped past those six jobs.
- All six now use the same complete check that the metadata bulk-apply path
  already used. It reads the book's record, its series, its author credits,
  its tags and every one of its files.
- The jobs covered are: regrouping iTunes albums into books, copying iTunes
  books into the library folder, linking authors from folder names, moving
  the main-copy flag between two versions of a chapter, merging chapter files
  into one book, and replacing title-shaped author names.
- When the check cannot finish because a read failed, the book is left alone
  and reported as a failed check. It is not counted as a Doctor Who book and
  it is not changed.
- The old record-only check can no longer be called from outside its home
  module, so a new job cannot fall back to it by mistake.
- The large iTunes regroup job reads files, series and tags in bulk and runs
  the remaining per-book credit reads in parallel. On a 5,000-book test
  library its whole planning snapshot took about 55 milliseconds.

## Recognizing a hand-applied book

**What it was.** Each of these jobs had its own short test for "is this a
Doctor Who / Big Finish / Torchwood book". The test read only the book's own
record, or only its folder path.

**Why it mattered.** Many iTunes-imported Big Finish releases sit in neutral
folders with junk titles. Their only clue is an author credit, a file name, a
spoken intro or a tag added by the franchise tagger. A bulk job could merge,
regroup, re-credit or demote one of those books, which breaks the owner's
standing rule that these are only ever changed by hand.

**The fix.** Every one of these jobs now asks the same complete question.
The answer comes from everything stored about the book, and a book that
cannot be checked is skipped rather than assumed safe. Tests give each job a
book whose record is clean and whose only clue is a file, a credit or a tag.
They fail with the old record-only check and pass now.
