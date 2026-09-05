### Fixed

- **Organizing a book no longer files it under the wrong author after a
  rescan.** When you apply an author, it is written to the durable `book_authors`
  join table AND to the book's legacy scalar author field. A later library scan,
  finding the file's own tags still name the old author and the author field
  unlocked, quietly reverts the scalar back to the tag author — but never touches
  the join table. The organizer built its target path from the scalar, so an
  applied-then-rescanned book was organized into the tag author's folder, not the
  one you chose. The organizer now reads the applied author from the join table
  first and only falls back to the scalar when the join has nothing usable, so a
  book with only a scanned author (the common case) is unaffected.
- Editing a book's author by ID (not by name) now updates the `book_authors`
  join table too, keeping it in step with the scalar field. Previously the ID
  path wrote only the scalar, which — now that the organizer trusts the join —
  could have refiled the book under a stale author. An ID-based author edit is
  single-author by definition, so the join becomes exactly that one author.
