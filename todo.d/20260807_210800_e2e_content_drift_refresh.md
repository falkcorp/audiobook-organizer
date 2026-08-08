- [ ] **Refresh the 43 content-drift e2e failures unmasked by the `_page` fix.**
      PR #2178 (2026-08-07) fixed the fixture error that had silently killed six
      e2e spec files since April 2026. With the mask gone the suite fails
      honestly: **43 failures, all pre-existing assertion drift** — tests assert
      hardcoded UI text the app no longer renders (Dashboard expects
      `45.0 GB / 500.0 GB`, `9% of disk used`, fixed stat-card counts; the
      `organize all` quick action expects navigation to `/operations` but the
      dialog now stays on `/dashboard`). Per-file counts: Dashboard 6,
      Book Detail 3, Error Handling 3, File Browser 9, Import Audiobook File 14,
      Operation Monitoring 10. This is a mock-fixture/assertion refresh pass —
      bring the mocks and expected copy up to what the app actually renders,
      file-by-file; no product code should need to change. Budget ~1 session;
      each e2e run rebuilds frontend+backend, so batch fixes per file rather
      than per test.
