### Fixed

- **Single-file audiobooks are no longer re-read and re-hashed on every scan.**
  The scan created file records only for multi-file and directory books, so a
  book that is one file got none. That was survivable until the scan cache moved
  to per-file records: with no file record there is nothing for the cache to key
  on, so every single-file book in the library was re-read in full on every scan,
  forever. The scan now gives every book its file record, and a single-file
  book's record correctly stays with its file rather than being moved to the
  containing folder.
