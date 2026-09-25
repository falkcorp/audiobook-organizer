### Changed

- **Non-ABS API request/response keys are now consistently snake_case.**
  2026-09-25's interface-naming audit (`docs/audits/2026-09-25-interface-naming-consistency.md`,
  class 11/12) found 16 camelCase response keys and one camelCase-only
  `bookIds` request field leaking into an otherwise snake_case wire format:

  - `GET /api/v1/import-paths`, `POST /api/v1/import-paths`: `importPaths` →
    `import_paths`, `importPath` → `import_path`.
  - `GET /api/v1/dashboard`: 12 keys (`formatDistribution`, `stateDistribution`,
    `recentOperations`, `totalSize`, `totalBooks`, `totalDuration`,
    `organizedBooks`, `unorganizedBooks`, `fingerprintedBooks`,
    `partiallyFingerprintedBooks`, `unfingerprintedBooks`,
    `fingerprintCoveragePercent`) → snake_case. No `web/src` code read any of
    these fields, so this is a server-only rename.
  - `GET /api/v1/review/count`, `POST /api/v1/review/items/:id/approve`:
    `byKind` → `by_kind`, `chosenAction` → `chosen_action`. `web/src`'s
    review store, api client, and every affected test mock were updated to
    match.
  - `maintenance.chapters-backfill`'s `bookIds` op parameter is now
    `book_ids`; the old `bookIds` spelling is still accepted as a request
    alias, and sending both with disagreeing values is a hard error rather
    than a silent pick — the same contract `parseAuthorOpDryRun` uses for
    `dry_run`/`dryRun` in `internal/plugins/maintenance/author.go`.

  Two audit-flagged spots were deliberately left alone: `regroup_shattered_ai.go`'s
  `memberBookIDs` is a **persisted** review-queue hold payload (not a live
  request/response key) that the frontend already reads defensively via
  snake_case fallbacks, and renaming its primary key would silently orphan
  already-written production holds. `author_path_link.go` and
  `duration_backfill.go`'s existing dual `bookIds`/`book_ids` fields were left
  untouched because both param structs also carry the `dry_run`/`dryRun` pair
  a separate in-flight preview-by-default sweep owns.
