- [ ] **`transcode-and-counting.spec.ts` (11 failures) — two hypotheses tested
      and REJECTED. Read this before trying either again.**
      Investigated 2026-08-09; no code shipped, because nothing that was tried
      improved the count and one attempt made it worse.

      **What the page actually shows** (from the Playwright DOM snapshot, which
      is the only thing that has reliably told the truth in this effort): the
      Dashboard renders normally but **every count is 0** — Library Books,
      Import Path Books, Authors, Series.

      **Rejected hypothesis 1 — missing `{ data: ... }` envelope on the specs'
      inline `page.route` overrides.** Real in principle: these tests call
      `route.fulfill` directly, bypassing `jsonResponse()` in test-helpers, and
      `api.getSystemStatus()` reads `body.data`. Wrapping all 11 success
      payloads changed the result by **zero**. The envelope is not what is
      breaking this file.

      **Rejected hypothesis 2 — the mock ignoring `is_primary_version`.**
      Reasoning looked sound: `api.getBooks()` always sends
      `is_primary_version=true`, `countBooksFiltered()` reads
      `body.data?.count` from that same endpoint, and the fixture is exactly 2
      primary + 1 non-primary against an expected count of 2. Teaching the mock
      to honour the param took failures from **11 to 12** — a regression. It
      was reverted. Do not re-apply without understanding why it hurt.

      **The one solid lead:** "Library Books" does NOT come from
      `/system/status`, which is what these tests mock. It comes from
      `api.countBooksFiltered()` → `GET /audiobooks?...` → `body.data?.count`
      (`services/api.ts:1058`). So a test that overrides `/system/status` to
      control the dashboard count is mocking the wrong endpoint entirely. That
      is worth confirming as the root cause before writing any more code.

      **Method note.** Every real cause found in this repair effort came from
      reading `test-results/*/error-context.md` and looking at what was on the
      page. Every wrong guess — including both above — came from reasoning
      about what should be there. Look first.
