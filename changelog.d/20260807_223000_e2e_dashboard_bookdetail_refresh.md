<!-- file: changelog.d/20260807_223000_e2e_dashboard_bookdetail_refresh.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9c1a72-5d4e-4b8a-9c6f-2e7d8a1b4c5d -->
<!-- last-edited: 2026-08-07 -->

### Fixed

- **E2E content-drift refresh, wave 1: Dashboard (6) + Book Detail (3) specs.** All nine
  failures were stale mocks, not product bugs: the frontend api layer now unwraps a
  `{ data: ... }` response envelope, but the e2e mock helper (`web/tests/e2e/utils/test-helpers.ts`)
  still returned raw bodies, so `getSystemStatus`/`startScan`/`startOrganize` resolved to
  `undefined` (zeroed stat cards, no `/operations` navigation). The Dashboard also now prefers
  the previously unmocked `/api/v1/system/storage` endpoint, so the real machine's statfs
  leaked into the storage card. Book Detail's in-page fetch mock didn't cover
  `/api/v1/auth/status` + `/api/v1/auth/me`, so the app bounced to the Login screen. Mocks now
  serve both the envelope and top-level fields; no product code changed.
