### Changed

#### fs-regroup-xml: repair chapter-per-folder books in three categories

`maintenance.fs-regroup-xml` now sorts books laid out one chapter per folder
(`<Book>/<Book> - N/<file>`) into three categories and reports each one
separately in its dry run: counts, the largest examples, and one plan line per
group.

- **fragments**: one book row per chapter. The apply moves each shell's
  book_file rows onto one survivor and creates a row only for a chapter path
  that has none. It then soft-deletes the emptied shells.
- **duplicates**: single-file rows whose files a multi-file book already owns.
  The apply soft-deletes the duplicate shells and leaves the owner and every
  book_file row in place.
- **chapter_folder_layout**: one book whose files each sit in their own chapter
  folder. This category is detection only, and an apply that asks for it
  refuses.

The apply no longer hard-deletes books and never deletes a book_file row. It
skips the iTunes tree and configured protected paths, and honours a user-locked
title. Every change is journaled under the operation id, and an apply with no
operation id refuses. The apply also refuses while a library scan is queued or
running, and holds the scan stand-down while it writes. A failed path lookup now
skips the group instead of creating a second row for the same path. Dry run is
still the default.
