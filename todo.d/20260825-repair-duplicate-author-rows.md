## Data repair

- [ ] **Decide how to repair the duplicate author rows that already exist.** The
      `CreateAuthor` race that produced them is fixed, but preventing corruption is
      not repairing it — the existing bad rows have no route back on their own.

      Known shape: two rows both named `Unknown Author` (id 54845 with 0 books,
      id 54846 with 2,128). 17,947 author rows total, of which ~4,643 (25.9%) were
      measured as non-people (track/volume fragments like `19 - Apocalypse`).

      Because `author:name:<normalized>` maps one name to ONE id, duplicates beyond
      the indexed row are unreachable by name lookup, so any repair must REPOINT
      `book.AuthorID` at the indexed row rather than delete — and note `DeleteAuthor`
      does not sweep `book.AuthorID`, which is how ~212 books ended up with a
      dangling `AuthorID`. Related: `todo.d/20260825-createauthor-check-then-create-race.md`.
