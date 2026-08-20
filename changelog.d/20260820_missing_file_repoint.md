### Added

- **`maintenance.missing-file-repoint` — restores books whose files were renamed out
  from under them.** A 2026-08-20 audit found 71,954 of 532,296 `book_file` rows
  pointing at paths that no longer exist, leaving **16,265 books with no resolvable
  file at all** — unplayable and undownloadable. Just under half those rows (35,296)
  are recoverable: the bytes are still on disk under a flattened, zero-padded name
  (`…/Corruption - 2/35.mp3` in the database vs `…/Corruption - 02.mp3` on disk).
  This operation rewrites those rows to point at the real file.

  It **never deletes a row** — that is why it is a separate operation from
  `maintenance.missing-file-repair`, whose delete path was removed. It defaults to a
  dry run, and refuses to touch anything it cannot prove: a row is skipped when
  several rows would land on the same file (the flattened-directory case), when the
  target is already claimed by another row, when the file's size on disk disagrees
  with the size recorded for the row, or when both padded and unpadded names exist.
  Every skip is counted and reported, so nothing is silently passed over.
