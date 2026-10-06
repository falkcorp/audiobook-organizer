### Added

- **Consolidation leftovers: same-path twin class (owner decision
  2026-10-06).** A leftover whose file rows are all gone from disk but whose
  own book path is on disk and owned by exactly one other live,
  Audiobookshelf-listed book (the 255 `held_book_path_present` rows of the
  2026-10-06 plan, e.g. a 0-min "35 - Splashdown" next to the live 15.7-min
  "35 - Splashdown" on the same file) is now the `same-path-twin` class: it
  is retired into that book, ahead of any size or hash match into a third
  book. The dead rows are marked missing and kept, and external ids move,
  all journaled and undoable. A shared path does not prove the same audio:
  a position or a finished state follows only with positive evidence (the
  dead rows' total size or a hash equals the owner's file, or both lengths
  agree), by the whole-book rule when that file is the owner's one live
  file and as a slice over its live rows otherwise; without it, state
  follows with no position and never as finished. The row shows both sides'
  sizes, lengths, titles, authors and ids. Held with their own
  reasons: more than one owner, an owner not listed, an iTunes owner (a
  row's iTunes path reference included), an iTunes copy in either version
  group that is not explicitly non-primary, an owner with no file row at
  the path, a recorded leftover duration that differs from the owner's
  file, a title / author / ASIN / same-source id that disagrees, and a
  primary hand-off that would not leave the owner its group's sole live
  primary. The merge fixers now share one iTunes-ownership predicate
  (`itunesOwnershipWhy`), which also counts a file inside an "iTunes Media"
  folder for the duplicate-copies and fragment fixers. Doctor Who / Big Finish / Torchwood
  on either side is held by the framework guard. Every condition is re-read
  under the merge lock at apply.
