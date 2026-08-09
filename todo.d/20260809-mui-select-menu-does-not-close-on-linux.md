<!-- file: todo.d/20260809-mui-select-menu-does-not-close-on-linux.md -->
<!-- version: 1.0.0 -->
<!-- guid: c47a0e63-91d8-4f52-bb06-3d5e28a71c9f -->
<!-- last-edited: 2026-08-09 -->

- [ ] **A MUI Select's menu does not close after choosing an option on the ubuntu CI
      runner — suspected REAL defect, not test rot.** Found 2026-08-09. A workaround was
      written, measured, and **reverted** because it did not work and the measurement
      showed why.

      **Do not "fix" this by changing the tests.** Two tests are red on linux because the
      application (or MUI, on that environment) is not doing what it does on macOS.

      ## The evidence, in the order it was obtained

      **Round 1 — the symptom.** Two chromium tests time out on `locator.click` after 30s
      on ubuntu while passing on macOS. The Playwright call log names the obstruction:

      ```
      <div class="MuiBackdrop-root MuiModal-backdrop"> from
      <div id="menu-" class="MuiPopover-root MuiMenu-root MuiModal-root">
      subtree intercepts pointer events
      ```

      - `library-browser.spec.ts` — selects "Organized", then clicks the **Author** combobox
      - `scan-import-organize.spec.ts` — selects "Imported", then clicks **Select All**

      **Round 2 — the hypothesis, and it was wrong.** Read as a close-transition race: MUI
      tears the backdrop down on a CSS transition, so a click issued in that window hits the
      backdrop. A `waitForMenuClosed` helper was added at **all 18** option-selection sites
      across 5 spec files, and verified locally (59 passed / 0 failed, exit 0).

      **Round 3 — the measurement that killed it.** On CI the count stayed at **3 failed**;
      the failures merely changed shape. Both now fail on the *new wait itself*:

      ```
      Error: expect(locator).toBeHidden() failed
      Locator: locator('.MuiPopover-root').first()
      Received: visible
      Timeout: 5000ms
      14 × locator resolved to <div id="menu-" role="presentation"
           class="MuiPopover-root MuiMenu-root MuiModal-root">
             - unexpected value "visible"
      ```

      **The menu is still open five seconds after the option was clicked.** That is not a
      transition; a MUI menu transition is ~200-300ms. And it fully re-explains round 1:
      the backdrop blocks the next click for 30s because **the menu never closes**, not
      because it is mid-animation.

      ## Why this reads as a real defect

      The Selects involved are **single**, not `multiple` — checked in
      `web/src/components/audiobooks/FilterSidebar.tsx` (Series at :143, Language at :181;
      the only `multiple` in the file is at :222, a different control). A single MUI Select
      closes its menu on selection. On ubuntu it does not.

      A menu that stays open for 5+ seconds after a click would be user-visible if it
      happens on real hardware. Whether it does is the first thing to find out.

      ## What was reverted and why

      The `waitForMenuClosed` workaround and its 18 call sites are **not merged**. Two
      reasons: it does not work, and merging it would have encoded "wait for a thing that
      never happens" into 18 places. Also reverted: a `prettier --write` pass that
      reformatted 965 lines of `test-helpers.ts` — unrelated churn that should never have
      been in the change.

      ## Next steps

      1. **Determine whether this is the app, MUI, or the environment.** Cheapest
         discriminator: a minimal page with one MUI Select, run on the ubuntu runner. If the
         menu stays open there too, it is MUI/environment; if it closes, it is our code.
      2. **Check whether `handleFilterChange` re-renders the Select in a way that cancels
         the close.** `FilterSidebar.tsx` refetches on change; if the option list identity
         changes mid-close on a slower machine, the menu may be re-created open. This is a
         hypothesis, not a finding — the last two were wrong, so measure before believing it.
      3. Only after the cause is known, decide whether the fix is product-side or test-side.

      Blocks flipping `continue-on-error` off — see
      `todo.d/20260809-three-linux-only-e2e-failures-block-ci-gate.md`.
