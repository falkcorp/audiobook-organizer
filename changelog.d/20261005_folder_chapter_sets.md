- `fragment-consolidation`: a new class, `folder-chapter-set`, for chapter files that were split into separate books (one book per file). It covers lone chapters in one folder whose names match once the numbers are removed (`Turn Coat - 27 1`, `Metro 2034 - 03 Chapter 3`); the chapter-key groups keep those as `skipped_lone_chapter`. A set is applicable only when all of these hold:
  - the numbers run from 0 or 1 with no gaps. Disc numbering in the hundreds (`Part 102`) counts as an explained gap; any other gap is held and listed as `skipped_chapter_gaps`.
  - the chapters total at least one hour (`skipped_set_too_short`).
  - no live book in the folder (`skipped_parent_in_folder`) or in a member's version group (`skipped_version_group_parent`) could be the parent.
  - no live book outside the set holds the same audio, by hash or by size plus duration (`skipped_duplicate_audio`).
  - the existing-book check finds no live book of the same title. This check now also covers the title the set gets from its file names.

  Sets under the iTunes library get their own class, `itunes-chapter-set`, and are never applicable. Apply is the no-parent apply: it moves the files onto one book in number order, retitles the book, and retires the emptied fragments with their listening state. Every step is journaled and revertable. The plan-time library snapshot now also reads each book's version group.
