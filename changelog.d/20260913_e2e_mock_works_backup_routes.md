### Fixed

- E2E mock API (`web/tests/e2e/utils/test-helpers.ts`): the works route now
  matches bare `/api/v1/works` exactly plus `/api/v1/works/` sub-paths, so a
  future sibling such as `/api/v1/workspaces` is no longer swallowed by a
  prefix match; and `/api/v1/backup/list` answers `GET` only, so a
  `DELETE /api/v1/backup/list` reaches the DELETE catch-all instead of getting
  a backup listing (TODO-MOCKWORKS, #2849).
