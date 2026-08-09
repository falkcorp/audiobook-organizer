<!-- file: todo.d/20260809-three-linux-only-e2e-failures-block-ci-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b2e9047-3d51-4a8c-b7f0-8e14c5920fda -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Three linux-only e2e failures block flipping `continue-on-error` off.** Measured
      2026-08-09 by dispatching the E2E workflow against current `main` — not inferred
      from the nightly, which was stale.

      **The numbers.** CI (ubuntu, chromium): **269 passed / 3 failed / 8 skipped of 280.**
      The same suite locally (macOS, chromium): **272 passed / 8 skipped of 280, 0 failed.**
      So exactly **3 failures exist only on linux.**

      | # | test | symptom |
      |---|---|---|
      | 1 | `dynamic-ui-interactions.spec.ts:449` — Button loading states visual check | `A snapshot doesn't exist at …/scan-button-loading-chromium-linux.png` |
      | 2 | `library-browser.spec.ts:382` — combines multiple filters | `locator.click: Test timeout of 30000ms exceeded` |
      | 3 | `scan-import-organize.spec.ts:259` — complete workflow: add import path → scan → organize | `locator.click: Test timeout of 30000ms exceeded` |

      **#2 and #3 are new information and the important part.** They pass on macOS and hang
      on linux. That is the whole reason this measurement was worth taking: a suite that is
      green locally is not evidence that CI is green, and this project has already been
      burned once by exactly that inference. Do NOT assume they are "just CI slowness"
      without looking — a 30s click timeout is a long time for a mocked page, and both are
      `locator.click`, which is suspicious enough to be a shared cause.

      **#1 is mechanical.** There are only two goldens in the repo, both `-darwin`
      (`scan-button-loading-chromium-darwin.png`, `…-webkit-darwin.png`), and Playwright
      fails rather than writes when `CI=true`. Generating a linux golden needs a container,
      because `playwright.config.ts`'s `webServer` builds the Go binary and that needs CGO +
      `libtag1-dev` — so it is a two-stage build (compile in a Go image, run in the official
      Playwright image), not a one-liner. Alternatively let CI produce it once and upload it
      as an artifact to be committed.

      **Two workflow defects found while measuring, worth fixing in the same PR as the flip:**

      1. **`conclusion: success` on this workflow means nothing.** `continue-on-error: true`
         makes the job succeed no matter how many tests fail. Every nightly to date reports
         green. Anyone glancing at the Actions tab would reasonably conclude the suite is
         passing — this morning's nightly reported `success` with **179 failures**. That is
         a green light attached to a red suite, which is the same shape as the incident this
         work exists to prevent.
      2. **The job name misreports what ran.** It renders
         `E2E (chromium + webkit)` for any non-`pull_request` trigger, including a
         `workflow_dispatch` with `projects=chromium`. The `projects` input *is* honoured by
         the test step — only the label is wrong. A label that does not match what executed
         is precisely how the 2026-08-08 false green was believed.

      **Order of work:** fix #2 and #3 first (they are real and may share a cause), then
      #1, then flip `continue-on-error: false` **and** restore the `pull_request` trigger in
      the same change — the workflow comment is explicit that they go together, because a
      non-blocking check people learn to ignore is worse than no check.

      **Acceptance:** a dispatched run against `main` reports 280 passed / 0 failed for
      chromium, and a PR touching `web/**` or `**.go` gets a blocking E2E check.
