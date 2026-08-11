- [ ] 🔴 **MUI v6 leaves the Drawer backdrop visible — the same mechanism as the
      2026-08-10 "invisible sheet" incident.** Blocks TODO-MUI-1 (5.14 → 6.5).
      Measured 2026-08-10 by running the full e2e suite on both versions on the
      same quiet machine, **twice per side**.

      | Spec | main (MUI 5.18.0) | branch (MUI 6.5.0) | Verdict |
      |---|---|---|---|
      | `scan-import-organize.spec.ts:259` [chromium] | ✅ 2/2 | ❌ **2/2** | **real v6 regression** |
      | `batch-operations.spec.ts:100` [webkit] | ✅ 1, ❌ 1 | ✅ 1, ❌ 1 | flake, unrelated to v6 |

      main: 556 passed / 0 failed / 8 skipped (run 2, exit 0).
      branch: 555 passed / 1 failed / 8 skipped (run 2, exit 2).

      So v6 introduces **exactly one** new failure, and it is this:

          Error: expect(locator).toHaveCount(expected) failed
          Locator: locator('.MuiDrawer-modal .MuiBackdrop-root').filter({ visible: true })
          Expected: 0   Received: 1   Timeout: 15000ms
            - 34 × locator resolved to 1 element

      **Why this specific assertion matters.** It is the guard added in #2283
      after a stuck MUI Drawer left an INVISIBLE full-viewport backdrop over the
      page that swallowed every click until reload. The assertion is not
      cosmetic test rot — it is the tripwire for a user-facing dead UI. Under v6
      the backdrop stays visible for the full 15s timeout, so the drawer's close
      transition is not completing (or is completing without removing/hiding the
      backdrop). **Do NOT relax or delete this assertion to get v6 green.** That
      is precisely the move the incident exists to prevent.

      Note the failure is browser-specific: the SAME test passes on webkit
      (5.8s) and fails on chromium (18.8s). And it passes when the two specs are
      run in ISOLATION (32/32) — it only fails inside the full suite. Both facts
      point at the close-transition timing rather than at logic, and both match
      the original incident's profile, whose root cause ("why the transition
      never completes") was documented as **still unexplained** and labelled so
      in-code.

      **Work:** find what changed in v6's Modal/Backdrop/Drawer close path —
      v6 reworked transitions and the ripple. Candidates: the backdrop's
      `transitionDuration` default, `keepMounted` behaviour, or the
      `closeAfterTransition` semantics. Fix the product behaviour, then re-run
      the full suite on both browsers. Only then land the v6 bump.

      Branch with the full v6 upgrade (codemods applied, build clean, 448/448
      vitest green) is `feat/mui-v6-upgrade` — everything except this one
      failure is ready.

- [ ] 🟡 **`batch-operations.spec.ts:100` [webkit] is an intermittent flake —
      find its mechanism.** Observed 2026-08-10 failing once on `main`
      (`76269d57`) and once on the MUI v6 branch, and **passing** on a re-run of
      each. `main` is green: 556 passed / 0 failed / 8 skipped, exit 0.

          Error: expect(locator).toBeChecked() failed
          Locator: getByLabel('Select Test Book 1', { exact: true })
          Timeout: 5000ms — element(s) not found

      "Batch Operations › selection persists across page navigation". When it
      does fail, the checkbox is not merely unchecked — its **label is absent**,
      so the row is not rendering as the test expects at all. Webkit only. That
      shape (row not rendered yet, rather than state lost) points at the
      navigation completing before the list re-renders, which is a timing
      mechanism worth finding rather than re-running past.

      🚨 **This entry previously claimed `main` was red.** That claim came from a
      single run and was wrong; it was published in a PR body and a memory file
      before being re-run. A failure seen once on a suite with known webkit
      flake is not a measurement — re-run before recording it. Per
      `feedback_fix_flaky_tests`, this still gets its mechanism found rather
      than being ignored as noise.
