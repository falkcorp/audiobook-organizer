- [x] **Refresh the remaining content-drift e2e failures unmasked by the `_page` fix.**
      PR #2178 (2026-08-07) fixed the fixture error that had silently killed six
      e2e spec files since April 2026. With the mask gone the suite fails
      honestly: all failures are pre-existing assertion drift — tests assert
      hardcoded UI text the app no longer renders. Wave 1 (2026-08-07) fixed
      Dashboard (6) and Book Detail (3): the api layer's `{ data: ... }`
      response envelope, the unmocked `/api/v1/system/storage` endpoint, the
      `/operations` → `/activity` route rename, and unmocked auth endpoints.
      Wave 2 (2026-08-08) cleared the remaining **34** chromium failures across
      four files — Error Handling 3, File Browser 8, Import Audiobook File 13,
      Operation Monitoring 10. (The per-file counts recorded here originally
      said File Browser 9 / Import 14; the measured baseline was 8 / 13, total
      34.) Root cause for 24 of them was the same missing `{ data: ... }`
      envelope in `web/tests/e2e/utils/test-helpers.ts` — `/auth/status` in
      particular meant `AuthContext` never initialized, degrading every mocked
      page. The rest was renamed-affordance drift. `operation-monitoring.spec.ts`
      needed a full rewrite: its target page was deleted in afe18e8f and
      `/operations` is now a redirect to `/activity`. No product code changed.
