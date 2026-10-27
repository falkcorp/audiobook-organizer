### Changed

- CI: the Frontend CI workflow no longer runs the reusable workflow's E2E step, which could never pass or fail (Playwright browsers installed under root, tests run as the runner user, and the helper exits 0 on failure) and only cost ~20 minutes. The "E2E (chromium)" workflow remains the E2E gate.
