### Fixed

- Organizing a book that was already in the right place no longer leaves it in
  the "Needs Organizing" backlog forever. The organize stamp recorded *when* a
  book was organized and *which operation* did it, but never updated the book's
  library state — and that state is exactly what the dashboard counts. Because
  already-correct books are diverted out of the organize path before reaching
  the code that sets the state, re-running organize could never clear them. The
  backlog was self-refilling and no amount of organizing would empty it.
