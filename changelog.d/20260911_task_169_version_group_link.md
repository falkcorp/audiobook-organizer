### Added

- **Book detail's version-group tray links to the other versions of a book.**
  The format tray on the book detail page's Files tab now shows an "Other
  versions" link whenever `book.version_group_id` is set, targeting the
  Library page pre-filtered to that version group
  (`/library?filters=[{"field":"version_group_id",...}]`). This was
  explicitly blocked in TODO.md until the underlying `version_group_id`
  field-filter worked server-side (fixed 2026-08-14); it also required two
  pieces of frontend plumbing that had never existed: the Library page could
  not parse a `version_group_id`/`filters=` URL parameter at all, and its book
  list unconditionally requested `is_primary_version=true`, which would have
  hidden every non-primary sibling the link exists to show. The link now
  carries `is_primary_version=false` to opt out of that default. A book with
  no version group renders no link at all, and the link appears once per
  version group rather than once per format tray.
