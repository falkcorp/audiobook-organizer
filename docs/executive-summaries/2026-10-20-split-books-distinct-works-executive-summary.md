<!-- file: docs/executive-summaries/2026-10-20-split-books-distinct-works-executive-summary.md -->
<!-- version: 1.2.1 -->
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
- **Estimated effect on the 64 rows that were offered.** 49 of them could be
  checked offline; the other 15 very large folders could not and need a fresh
  trial to settle. Of the 49, 35 are no longer applicable and 14 stay
  applicable. The 35 are the 29 wrong rows plus about 6 genuine chapter splits.
  The 29 wrong rows split into 3 now skipped because their books have no file
  records, 2 held as distinct works, and 24 held by the two-hour file limit.
  27 of the 29 also trip a name-based or store-ID rule; one relies on the
  two-hour limit alone, and one only on having no file records. The 6 genuine
  rows are held by the two-hour limit or because their file lengths are not
  recorded, and the owner must review them by hand. These are an offline
  estimate: the exact counts will only be known after the next scan and a
  fresh Repairs trial.
- **Known cases where it errs on the safe side.** Every rule holds a row; none
  merges it. So a genuine chapter split is held, not merged, when: a chapter
  file is two hours or longer; its folder is called "Series" or "Collection";
  its chapter titles read "Name N: Subtitle" without the word Chapter or Part;
  its parts are labelled "Section N:" or "Episode #N" although they are one
  single work (those words are now treated as separate installments, not as
  chapters); its sections are called "Book One", "Book Two" inside a single
  novel; its rows carry different store IDs because one was matched wrongly; or
  a chapter number is 1900 or more. A held row is reviewed by hand in the
  Repairs list; nothing is lost.
- **A case it can still let through.** The rules look for evidence of separate
  books. A folder of short, separate novellas (each under two hours) that have
  no store ID, no Book/Volume words in their titles or file names, and only bare
  numbers like "01", "02" looks exactly like chapters and can still be offered.
  Read each row's titles before applying it.
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
