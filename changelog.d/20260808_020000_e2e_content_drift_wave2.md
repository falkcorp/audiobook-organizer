### Fixed

- **E2E content-drift refresh, wave 2** — the shared Playwright mock
  (`web/tests/e2e/utils/test-helpers.ts`) now returns the `{ data: ... }`
  response envelope the frontend api layer unwraps for `/auth/status`,
  `/import-paths`, `/authors`, `/series`, `/audiobooks/soft-deleted`, the bare
  `/audiobooks/:id` route, `/audiobooks/:id/versions` and every
  `/filesystem/*` endpoint. Without it the mocked app could not initialize
  auth, could not load a book detail page and could not browse the server
  filesystem, which was failing 34 tests across four spec files. Assertions
  that referenced renamed UI affordances were refreshed to match what the app
  renders today. No product code changed.
