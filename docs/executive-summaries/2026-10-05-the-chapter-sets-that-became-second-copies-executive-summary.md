<!-- file: docs/executive-summaries/2026-10-05-the-chapter-sets-that-became-second-copies-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 947b3d82-2669-4187-8065-4f7e76ee4cf8 -->
<!-- last-edited: 2026-10-05 -->

# The chapter sets that became second copies

## Executive Summary

- **Duplicate books were being made.** The repair that gathers stray chapter
  files back into one book also built a brand-new book when it found a folder
  of chapters with no owner. It never asked whether that book was already in
  the library. On the night of 4 to 5 October, 13 of the 19 books it built
  were second copies of books the library already had ("Book 2 - Eldest",
  347 chapters, beside "Eldest", 349 files).
- **It now checks first.** Before building anything, the repair looks for a
  book with the same title. A leading series number such as "Book 2 -" is set
  aside but still has to agree, and a volume number ("Vol 3") is part of the
  title, so volume 3 is never folded into volume 1. If the same work exists,
  by the same author, and the running times agree, the chapters are folded
  into it instead. Otherwise nothing happens and the owner decides. A folder
  of chapters with no recognisable title is held rather than built blind.
- **Copies of a book's chapters are recognised.** Chapters that are re-tagged
  copies of another book's files (same place in the book, same length, a
  fixed size difference) are never built into a book.
- **Nothing falls through the cracks.** About 2,250 stray chapters used to be
  left out of the repair's list without a word. Each now appears with the
  reason it was not grouped.
- **Shared files follow the owner's rule.** When another book also holds one
  of the chapter files, it is compared with the whole book being repaired: a
  different title or author blocks the repair, a same-titled book of the same
  length takes the chapters, a same-titled book of a different length holds
  the repair for review, and only an untitled stray lets it go ahead.
- **Not yet addressed.** The 13 duplicates already made that night are not
  touched by this change; undoing or merging them is a separate decision.

## Second copies of existing books

**What it was.** The repair grouped a folder's chapter files and built a new
book from them without looking at the rest of the library.

**Why it mattered.** Each such run left two live entries for one work, with
the chapters split between them and listening progress on the wrong one.

**The fix.** Every group is compared with the library's books by a cleaned-up
title. A match with an agreeing running time (within 2% or 5 minutes) folds
the chapters into the existing book; any other match is held for review.

## Re-tagged copies

**What it was.** Some folders hold copies of another book's chapters whose
tags were rewritten, so their file sizes differ by a fixed amount.

**Why it mattered.** Built into a book, they would make one more duplicate of
audio the library already has.

**The fix.** When most chapters line up with another book's files by
position, length and a constant size difference, the group is held.

## Silent drops

**What it was.** Chapters that did not fit any group (too few, a stray in a
folder of several works, no chapter number) were simply left out.

**Why it mattered.** They could not be counted or reviewed, so the backlog
looked smaller than it was.

**The fix.** Each one is now listed, never applied, with a specific reason.

## Shared files

**What it was.** Any other book holding one of the files blocked the repair,
whatever that book was.

**Why it mattered.** Real repairs waited forever, and nothing told a second
edition from a different work.

**The fix.** The owner's rule decides by title, author and the running time
of the whole book, and every such repair is marked for review.
