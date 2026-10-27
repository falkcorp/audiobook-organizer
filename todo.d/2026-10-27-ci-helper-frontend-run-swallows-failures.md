- [ ] **TODO-CI-FRONTEND-RUN-SWALLOWS** `falkcorp/gha-ci-workflow-helpers` `frontend_run` exits 0 when its npm
      script fails, so reusable-ci's frontend lint/build/unit-test/E2E steps gate nothing in any repo that relies on
      them (this repo is covered by Minimal CI and the separate E2E (chromium) workflow; #4208 skips the dead E2E step).
      Fix is open upstream (gha-ci-workflow-helpers#29, github-common#381 for the Playwright install path), NOT merged:
      owner decides release/tag/pin bumps. Expect consumers with broken frontends to go red after the pins move
      (migrate-loop, magnet-handler, subtitle-manager, ubuntu-autoinstall-agent). Owner 2026-10-27: note only.
