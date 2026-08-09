<!-- file: todo.d/20260809-book-detail-purge-suite-only-flake.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e8b1c04-7a92-4d36-9f1b-2c0a8e64d7f3 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **`book-detail.spec.ts` "soft delete, restore, and purge flow" fails only in the full
      parallel suite, never in isolation.** Surfaced 2026-08-09 as the last remaining
      failure after the e2e repair took the suite to 551 passed / 1 failed / 16 skipped of
      568 across chromium + webkit.

      **This is deliberately NOT fixed.** It is not spec rot — the test passes 6/6 alone —
      so changing the test to tolerate it would be papering over an unknown, and unlike the
      webkit pagination flake there is **no measurement yet establishing the app is
      correct**. Per the no-papering-over rule this gets written up and left red.

      **The failure:**

      ```
      [webkit] › book-detail.spec.ts:423 › soft delete, restore, and purge flow
      Error: expect(page).toHaveURL(expected) failed
        Expected pattern: /\/library$/
        Received string:  "http://127.0.0.1:8484/dashboard"
        - unexpected value "http://127.0.0.1:8484/login"
      ```

      After "Purge Permanently" the test expects `/library`. Instead the page went to
      `/login` and settled on `/dashboard` — the signature of an auth guard firing, not of
      a broken navigation.

      **What has been ruled out (each by measurement, not reasoning):**

      | hypothesis | result |
      |---|---|
      | The test itself is stale / selector drift | **No** — 6/6 passes on webkit in isolation, `--repeat-each=6` |
      | `auth-flow.spec.ts:90` pollutes shared server state by creating an admin account | **No** — that test `test.skip`s itself unless `requires_auth && !has_users`, and it skipped in every run examined. Confirmed by arithmetic: the full run's 16 skips = 7 `test.fixme` × 2 browsers + this bootstrap test × 2 browsers. It never executed, so it mutated nothing |
      | Reproducible by pairing the two specs under parallel workers | **No** — `book-detail` + `auth-flow`, `--repeat-each=4`, webkit: 24 passed / 4 skipped |

      **What is still open.** The suite runs `fullyParallel: true` with `workers: 2`
      (`playwright.config.ts:18-20`) against a **single shared Go server on :8484**. Every
      spec mocks at the browser layer (`page.route` or a `window.fetch` patch), but the
      server underneath is common to all of them. Something in a concurrently-running spec
      plausibly moves real server auth state — but the obvious candidate is now excluded,
      so the actual polluter is unidentified.

      **The artifact was lost, and that is the main obstacle.** Playwright clears
      `test-results/` at the start of every run, so the `error-context.md` and trace from
      the failing run were overwritten by the isolation re-runs before they were read. That
      is the one procedural mistake here: **read the artifact before re-running.** A repeat
      full-suite run with `--trace=retain-on-failure` is the way to recapture it.

      **Next steps, in order:**

      1. Re-run the full suite with `--trace=retain-on-failure` until it reproduces, and
         read `test-results/*book-detail*/error-context.md` **first**. That artifact
         discriminates the two live possibilities and a pass/fail count cannot: was the
         `/login` hop a client-side route guard, or a document load? Did `/auth/status`
         return something different from the mock?
      2. If it is shared-server auth state, the fix is isolation, not tolerance — either a
         per-worker server, or a fixture that asserts the server's auth posture is
         unchanged at test start.
      3. **Frequency: 1 occurrence in 1,136 test executions.** A second full-suite run with
         `--trace=retain-on-failure` came back **552 passed / 0 failed / 16 skipped, exit
         0** — the whole suite green on both browsers, and this test among them. So it did
         not reproduce, no artifact was captured, and the rate is at most ~0.1% of runs of
         this test.

         That changes the priority but not the conclusion. It is rare enough that it should
         **not** block calling the suite green, and rare enough that hunting it by repeated
         full-suite runs is poor value. The right trigger is opportunistic: the next time
         CI or a local full run goes red on this test, **read
         `test-results/*book-detail*/error-context.md` before doing anything else** — that
         is the artifact that was lost the first time and it is what discriminates the
         remaining possibilities.

      **Do not** add a retry, a URL tolerance, or a `test.fixme` to this test on the
      strength of "it passes alone."
