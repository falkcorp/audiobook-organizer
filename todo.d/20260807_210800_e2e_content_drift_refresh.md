- [ ] **Refresh the remaining content-drift e2e failures unmasked by the `_page` fix.**
      PR #2178 (2026-08-07) fixed the fixture error that had silently killed six
      e2e spec files since April 2026. With the mask gone the suite fails
      honestly: all failures are pre-existing assertion drift — tests assert
      hardcoded UI text the app no longer renders. Wave 1 (2026-08-07) fixed
      Dashboard (6) and Book Detail (3): the api layer's `{ data: ... }`
      response envelope, the unmocked `/api/v1/system/storage` endpoint, the
      `/operations` → `/activity` route rename, and unmocked auth endpoints.
      **Remaining: 34 failures in 4 files** — Error Handling 3, File Browser 9,
      Import Audiobook File 14, Operation Monitoring 10. This is a
      mock-fixture/assertion refresh pass — bring the mocks and expected copy up
      to what the app actually renders, file-by-file; no product code should
      need to change. Most fixes are the same envelope pattern already applied
      to `web/tests/e2e/utils/test-helpers.ts` (wrap responses as
      `{ ...body, data: body }`). Budget ~1 session; each e2e run rebuilds
      frontend+backend, so batch fixes per file rather than per test.
