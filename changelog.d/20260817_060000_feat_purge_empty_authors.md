### Added

- **`maintenance.purge-empty-authors`** — deletes author rows attached to zero
  books. On the live library that is 4,975 of 12,854 authors (38.7%): not people,
  but track and chapter titles an importer parsed into the author field
  ("- Edgedancer", "04 - Heir to the Jedi"). No existing maintenance op covered
  this — the other author ops all operate on authors that have books.

  Dry-run by default; pass `apply=true` to delete. By default it refuses any author
  with a non-zero file count, since zero books plus files present is more likely a
  book that lost its junction link than an empty author, and deleting the author
  would make repairable damage permanent. `limit` caps a run so a first apply can be
  inspected before committing to all of it.
