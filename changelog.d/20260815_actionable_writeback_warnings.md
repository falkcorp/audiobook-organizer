### Fixed

- Write-back and organize warnings now name what they could not write. Every
  warning on the metadata-apply path used to report a bare count or an opaque
  `"value"` key, so a production log could tell you that something failed but
  never which tag, which file, or which book:
  - `write-back: unmapped tag key` now carries the tag key, its value, the file
    path, and the file's current title/album, plus a hint that an unmapped key
    forces a rewrite of the file on every run.
  - `files skipped during rename` now names the book and lists the missing
    source paths (capped at 20, with a `truncated` flag) instead of logging only
    `count`.
  - `write-back failed for file` now carries the book id/title, the book-file
    id, the file's position in the book (`3 of 12`), and the tag keys that were
    being written.
  - `organizeFile skipping unsafe destination` logged only an error with no path
    at all; it now names the book, the source, the computed destination, and the
    target directory.
  - `organizeFile skipping missing source file`, `tag writing failed for book`,
    `cover art embedding failed`, and the two "protected book has no library
    copy" warnings all gained book title and path context.
