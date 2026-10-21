<!-- file: docs/executive-summaries/2026-10-20-split-books-distinct-works-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e2b4d19-3a6c-4f80-b5d1-0c9a8e7f6b24 -->
<!-- last-edited: 2026-10-20 -->

# The split-books repair stopped proposing to merge different books

**Branch:** fix/split-books-distinct-works-veto (not yet pushed).

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
  with different subtitles, a folder called "Books 1-4" or "Series", a run of
  years, copy-numbered files, subfolders that name different things, or any
  single file as long as a whole book (the same two-hour limit the chapter
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
- **Estimated effect on the 64 rows that were offered.** About 3 have no file
  records, about 29 have a file at or over two hours (nearly all of them also
  tripping a name-based rule), 2 more are caught by name alone, 1 has unknown
  lengths, and about 14 stay applicable. 15 very large folders could not be
  checked offline and need a fresh trial to settle.
- **One thing to do.** Run the Split Books scan again. Candidates stored by
  earlier scans are not re-checked when merged, so they stay as they were until
  the scan replaces them.
