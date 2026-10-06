### Added

- **Consolidation leftovers: same-path twin class (owner decision
  2026-10-06).** A leftover whose file rows are all gone from disk but whose
  own book path is on disk and owned by exactly one other live,
  Audiobookshelf-listed book (the 255 `held_book_path_present` rows of the
  2026-10-06 plan, e.g. a 0-min "35 - Splashdown" next to the live 15.7-min
  "35 - Splashdown" on the same file) is now the `same-path-twin` class: it
  is retired into that book, ahead of any size or hash match into a third
  book. The dead rows are marked missing and kept; listening state follows
  by the whole-book rule when the owner is that one file (a slice otherwise)
  and external ids move, all journaled and undoable. Held with their own
  reasons: more than one owner, an owner not listed, an iTunes owner, an
  iTunes copy in either version group that is not explicitly non-primary,
  an owner with no file row at the path, and a recorded leftover duration
  that differs from the owner's file. Doctor Who / Big Finish / Torchwood
  on either side is held by the framework guard. Every condition is re-read
  under the merge lock at apply.
