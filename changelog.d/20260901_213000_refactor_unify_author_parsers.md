### Changed

- The two path→author parsers now have one implementation each. `extractAuthorFromDirectory`
  and `parseFilenameForAuthor` existed as separate copies in `internal/scanner` and
  `internal/metadata`; both now live in `internal/authorname`. A fix to how a directory
  name becomes an author is now a fix to both, rather than to whichever copy happened to
  be edited. A third parser (`folder_parser.go`) still has its own shape rules; its
  placeholder handling is fixed below, the rest is tracked as follow-up work.

### Fixed

- A book filed directly under the organizer's own `Unknown Author` directory could have that
  placeholder read back as its author during metadata extraction. It was cleared again a few
  lines later, so no book was affected, but the two copies disagreed on it and only one had
  the guard.

- A book filed under the organizer's own `Unknown Author` directory could have that
  placeholder read back as its author by the folder parser — with high confidence, and
  past the guard meant to catch it, so an `Unknown Author` entry was created and attached
  as if a person had been identified. Books were still offered to AI re-parsing, so this
  cost accuracy rather than data.
