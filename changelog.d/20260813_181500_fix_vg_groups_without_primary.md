### Fixed

- **Books imported from iTunes since April were invisible in the web UI.** The
  importer minted a fresh version group for each newly-created book and then
  marked that book non-primary. Because the group was brand new the book was its
  only member, so the group elected no primary at all — and the web Library page
  filters `is_primary_version=true` by default, so those books could never
  appear. The same books were always visible to API clients (including the
  mobile app), which is why this surfaced as "search works in the app but not in
  the browser" rather than as missing data. 479 version groups holding 724 books
  reached production in this state.

### Added

- `POST /api/v1/operations/elect-missing-primaries` repairs version groups that
  elect no primary, choosing the earliest-created member deterministically. It
  **defaults to a dry run** — an apply must be requested explicitly with
  `?dry_run=false` — and re-reads live group membership before each write so it
  can never add a second primary to a group that gained one concurrently.
