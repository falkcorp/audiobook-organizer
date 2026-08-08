<!-- file: changelog.d/e2e-unknown-page-parameter.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f7a91c2-6d48-4b0e-9a25-8c1e5d4f7b32 -->
<!-- last-edited: 2026-08-07 -->

### Fixed

#### E2E suite: `unknown parameter "_page"` broke six spec files on main

A lint sweep in April (`68d2a0ed`, "resolve all E2E test lint errors") renamed
the unused `page` fixture parameter to `_page` in seven no-op
`test.beforeEach(async ({ _page }) => { ... })` hooks. Playwright validates
fixture names in the destructured parameter object at run time, so every test
inside those `describe` blocks failed immediately with
`Error: Test has unknown parameter "_page"` — the suite was broken on main
and gated nothing.

The seven hooks (in `dashboard.spec.ts`, `book-detail.spec.ts`,
`error-handling.spec.ts`, `file-browser.spec.ts`,
`operation-monitoring.spec.ts`, and two in `import-audiobook-file.spec.ts`)
have comment-only bodies — each test does its own setup via
`openLibrary()`/`openDashboard()`/`setupRoutes()` etc. — so the fix is to
declare them with no parameters at all: `test.beforeEach(async () => { ... })`.
This satisfies both the linter (no unused parameter) and Playwright (no
unknown fixture).
