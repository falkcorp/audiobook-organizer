### Fixed

- **Deleting an author no longer leaves books pointing at it.** `DeleteAuthor`
  removed the author from each book's credit list but left the book's primary
  `author_id` naming the deleted row (about 212 dangling ids across 499 books on
  prod as of 09-07). It now moves that primary onto the book's next credited
  author by position, or onto the "Unknown Author" row the name index resolves
  when no other author is left. It never clears the field. If no successor can
  be found, the delete is refused. "Reclassify author as narrator" also stopped
  clearing the primary author.

### Added

- **`maintenance.author-id-repair`** (dry run by default). Phase (a) repoints
  books whose `author_id` names a deleted author, using the same rule as
  `DeleteAuthor`. Phase (b) merges author rows whose names normalize the same
  onto the id the name index resolves, and deletes a duplicate only when both
  memdb and a Pebble-only scan find zero books crediting it. Every planned or
  applied change is in the op result.
