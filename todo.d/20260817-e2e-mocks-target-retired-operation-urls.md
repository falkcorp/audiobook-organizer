- [ ] **Six E2E mocks point at operation URLs that no longer exist, and two
      separate things were confused because of it.**
      `getOperationStatus` now polls `GET /operations/v2/:id`; it used to poll
      `GET /operations/:id/status`, retired in #2502. These mocks still target
      the old shape, so the request stops matching, falls through to the real
      server and 404s — a stale mock here fails silently, it does not error:
      - `web/tests/e2e/dynamic-ui-interactions.spec.ts:269` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/dedup-operations.spec.ts:141` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/dedup.spec.ts:189` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/diagnostics.spec.ts:80` — `**/api/v1/operations/op-2`
      - `web/tests/e2e/diagnostics.spec.ts:175` — `**/api/v1/operations/op-1`
      - `web/tests/e2e/transcode-and-counting.spec.ts:97` — `**/api/v1/operations/op-transcode-1`
      Retargeting also needs a **body change**, not just a URL change:
      `getOperationStatus` reads `def_id` / `progress_current` /
      `progress_total` / `progress_message` off the v2 record, while every mock
      above returns the flat legacy `type` / `progress` / `total` / `message`
      shape. A URL-only fix yields progress 0 and an undefined type.
      **Measured 2026-08-16, and this is the part that matters:** retargeting
      `dynamic-ui-interactions.spec.ts` to `**/api/v1/operations/v2/*` with a v2
      body changed nothing — 6 failed / 4 passed before and after. A control run
      of that spec against `origin/main` (detached checkout, same machine, same
      command) also gives **6 failed / 4 passed**, so those six failures are
      PRE-EXISTING and have nothing to do with the route retirement. The failing
      assertions are all "spinner/loading button is visible"
      (`Scan All`, `Organize Library`, per-path scan, dashboard variants,
      visual-regression). Root cause still unknown — do not assume it is the
      mock.
      Note the daily scheduled E2E runs on `main` are green (8/8, 2026-08-09..16)
      while the `pull_request` run is red. Those are different triggers, so a
      green schedule history is NOT a control for a PR failure — that mistake is
      what made these look like a regression in #2502.
