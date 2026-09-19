### Added

- New maintenance operation `maintenance.author-path-link`. Thousands of books carry no
  author at all even though the author's name is sitting right there in the folder the
  book is filed under; `maintenance.author-id-repair` counts that population and
  deliberately leaves it alone. This op links it: it walks a book's path with the
  scanner's own segment splitter and person-name shape test, resolves the name through
  the author name index, and — only when exactly one existing author matches — writes the
  credit and the book's primary author. A person-shaped name with no author at all gets a
  new author created and linked; a name that is within a couple of typos of an existing
  author is reported and never created, so a misspelled folder cannot mint a twin of a
  real author. Paths that point at two different authors, and matches against an author
  who has one book or none, are reported and skipped. Books that already have an author,
  books in the iTunes library, and the manually-curated Doctor Who / Big Finish /
  Torchwood shelves are never touched. It defaults to a dry run that reports exactly what
  an apply would do, takes an explicit list of book ids or a path prefix to scope a run,
  parks any running library scan while it writes, and records every link in the undo
  ledger.
