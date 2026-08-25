### Fixed

- **`CreateAuthor` no longer mints a duplicate row for every concurrent caller.**
  It was check-then-create with nothing serializing the two steps, and the window was
  not narrow: 24 concurrent calls with an identical name produced 24 distinct author
  rows, reproducibly. Because the `author:name` index maps one name to exactly one id,
  every duplicate beyond the indexed one was *unreachable by name lookup* — so books
  attached to those rows were silently orphaned from any name-based resolution. The
  scanner resolves authors from inside its worker pool, once per book, so an import
  that first meets an author across several books at once minted a row per worker.
