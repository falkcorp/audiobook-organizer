### Fixed

- **Diagnosed why new books never appear.** The scan finds and ingests them, but the
  rows it writes are structurally malformed: each track becomes its own book, the
  folder name is used as the author, and the filename as the title. Separately,
  **536 books sit in 312 version groups that elect no primary**, which makes them
  unreachable from a library page that requests primary-only. Measured via the
  existing `elect-missing-primaries` dry run. The write-up also clears three dead
  ends — the scan root is correct and must NOT be repointed, an import path's empty
  `last_scan` proves nothing because only `library.folder-auto-scan` writes it, and
  the `AUDIOBOOK_ROOT_DIR` env var is inert.
