### Changed

- The two path→author parsers now have one implementation each. `extractAuthorFromDirectory`
  and `parseFilenameForAuthor` existed as separate copies in `internal/scanner` and
  `internal/metadata`; both now live in `internal/authorname`. A directory that names an
  author is read the same way on every path that reads one, so a fix to the behaviour is
  a fix everywhere rather than to whichever copy happened to be edited.

### Fixed

- A book filed directly under the organizer's own `Unknown Author` directory could have that
  placeholder read back as its author during metadata extraction. It was cleared again a few
  lines later, so no book was affected, but the two copies disagreed on it and only one had
  the guard.
