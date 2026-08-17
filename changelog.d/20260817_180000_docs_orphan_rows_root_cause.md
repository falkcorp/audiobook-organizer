### Added

- **Root-cause note for the orphan destination rows**
  (`docs/audits/2026-08-17-orphan-destination-rows-root-cause.md`). Identifies the
  code path that records a `book_file` row for bytes that were never written:
  `resolveOrganizedFilePath` (`internal/organizer/service.go:1254`) prefers the
  planned target if it exists, falls back to the source if that exists, and
  **when neither exists returns the planned path anyway** — silently, with no
  warning on that branch. The value goes straight into `CreateBookFile`.

  Because a planned path is always built from `RootDir` plus the naming
  patterns, every row created this way lands under the organizer's own tree and
  none can land under iTunes — which matches the observed shape exactly.

  🔴 **This may change the repair.** #2479 made the file-naming pattern decide
  organized filenames, so a book organized before it has its bytes on disk under
  the *old* name while the recomputed plan names a sibling that never existed.
  If that is what production looks like, those rows should be **repointed, not
  deleted** — the note carries a directory-listing test that discriminates the
  two cases, and recommends running it before `missing-file-repair --apply`.
