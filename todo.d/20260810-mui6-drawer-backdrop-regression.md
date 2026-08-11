- [ ] 🔴 **MUI v6 leaves the Drawer backdrop visible — the same mechanism as the
      2026-08-10 "invisible sheet" incident.** Blocks TODO-MUI-1 (5.14 → 6.5).
      Measured 2026-08-10 by running the full e2e suite on both versions on the
      same quiet machine.

      | Spec | main (MUI 5.18.0) | branch (MUI 6.5.0) |
      |---|---|---|
      | `scan-import-organize.spec.ts:259` [chromium] | ✅ passes | ❌ **fails** |
      | `batch-operations.spec.ts:100` [webkit] | ❌ fails | ❌ fails |
      | totals | 555 passed / 1 failed / 8 skipped | 554 passed / 2 failed / 8 skipped |

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

- [ ] 🔴 **`main` is NOT green: `batch-operations.spec.ts:100` [webkit] fails on
      main and nobody noticed.** Measured 2026-08-10 on `76269d57`:
      **555 passed / 1 failed / 8 skipped**, not the 556/0/8 recorded after the
      2026-08-10 repair.

          Error: expect(locator).toBeChecked() failed
          Locator: getByLabel('Select Test Book 1', { exact: true })
          Timeout: 5000ms — element(s) not found

      "Batch Operations › selection persists across page navigation" — a
      checkbox that should stay checked across navigation is not merely
      unchecked, its LABEL IS ABSENT, so the row is not rendering as expected at
      all. Webkit only; chromium passes.

      **Why it went unnoticed:** `e2e.yml` is `paths:`-filtered, so the e2e
      suite does not run on most PRs — a regression here lands silently and the
      only signal is someone running the suite locally. That filter is already
      recorded as the blocker for an org-level required-check rule
      (`todo.d/20260810-e2e-gate-not-required.md`); this is the first measured
      instance of it actually costing something.

      Do not conflate with the MUI v6 item above — this one is independent of
      any upgrade and is present on plain `main` today.
