<!-- file: docs/executive-summaries/2026-10-20-split-books-distinct-works-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 7e2b4d19-3a6c-4f80-b5d1-0c9a8e7f6b24 -->
<!-- last-edited: 2026-10-21 -->

# The split-books repair stopped proposing to merge different books

**Branch:** fix/split-books-distinct-works-veto (PR 4105).

## Executive Summary

- **What was wrong.** The "Split books" repair looks for one book that was
  stored as many short books, one per chapter file. It decided a folder was
  one book when the files shared an author and a series and carried a run of
  numbers. A series of separate books looks exactly the same: same author, same
  series, numbered volumes. The repair offered to merge those books into one.
- **Why it mattered.** Approving such a row would have silently combined
  distinct books into a single entry, with their files, listening progress and
  identifiers following the first book. Nothing would have flagged it as a
  mistake. It was found while reviewing the rows the repair proposed.
- **What it does now.** A folder is no longer treated as chapters when it shows
  signs of being several books: different volume numbers in the names, titles
  with different subtitles ("Episode 1: ...", "Episode 2: ..."), volume numbers
  written as Roman numerals or words ("Book II", "Book Two"), books carrying two
  or more different store (Audible/Amazon) IDs, a folder called "Books 1-4" or
  "Series", a run of years, copy-numbered files, subfolders that name different
  things, or any single file as long as a whole book (the same two-hour limit the chapter
  repair already uses). A series position that differs between members is
  shown as evidence only.
- **Nothing disappears silently.** Those folders still appear in the Repairs
  list, marked "held", with the reasons written out. The scan that fills the
  old Split Books tab, the bulk merge and the command-line tool simply no longer
  list them. The repair also now holds a folder whose books have no file
  records, and one whose file lengths are unknown, as the chapter repair does.
- **Easier to see what you are approving.** The confirmation before applying
  now lists the rows (title and number of books, ten at most, then "and N more"),
  and the "Apply finished" message names the rows that were applied.
- **Estimated effect on the 64 rows that were offered.** Offline checking
  classified 29 of them as wrong. 21 of those 29 are caught by a name-based
  rule; the other 8 are caught by the two-hour file limit alone. The rest of
  the wrong rows have no file records (about 3) or unknown lengths (1). About 14
  stay applicable, and 15 very large folders could not be checked offline and
  need a fresh trial to settle. After this is deployed, expect most of the
  wrong rows to be held. Also expect about 6 genuine rows (real chapter splits)
  to be held too, by the two-hour limit or because their file lengths are not
  recorded. Those need a manual review by the owner; the exact counts will only
  be known after the next scan and a fresh Repairs trial.
- **Known cases where it errs on the safe side.** Every rule holds a row; none
  merges it. So a genuine chapter split is held, not merged, when: a chapter
  file is two hours or longer; its folder is called "Series" or "Collection";
  its chapter titles read "Name N: Subtitle" without the word Chapter or Part;
  its sections are called "Book One", "Book Two" inside a single novel; its
  rows carry different store IDs because one was matched wrongly; or a chapter
  number is 1900 or more. A held row is reviewed by hand in the Repairs list;
  nothing is lost.
- **Old saved suggestions are checked again at merge time.** The Split Books
  tab keeps the suggestions from the last scan. Merging one used to trust that
  saved copy. Now, whether you merge one at a time or in a batch, the app looks
  at the books as they are at that moment and refuses if they no longer look
  like chapters of one book (different volume numbers, a file as long as a whole
  book, a file with no recorded length, a book with no files). A refused
  suggestion stays on the list and nothing is changed. A practice run of the
  batch merge now tells you which ones would be refused.
- **One thing to do.** Run the Split Books scan again after this is deployed so
  the list itself is refreshed, and look over the 15 very large folders that
  could not be checked offline.
