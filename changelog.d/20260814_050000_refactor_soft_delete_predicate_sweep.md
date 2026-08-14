### Changed

- The soft-delete check is now a single shared predicate everywhere. Twenty-five
  copies of `MarkedForDeletion != nil && *MarkedForDeletion`, spread across
  seventeen files in dedup, iTunes, organizer, undo, maintenance and the
  handlers, now call `Book.IsSoftDeleted()`. No behaviour changes — every copy
  was correct — but a rule written out twenty-five times is how the two
  implementations of the library scan drifted into disagreeing for months. One
  site turned out to hold a different row type and needed a `BookCore` twin; the
  compiler found it. `internal/scanner`'s remaining match is deliberately left
  alone: it compares two rows' flags for merge purposes and is not the same
  question.
