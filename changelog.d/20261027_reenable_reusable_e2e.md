### Changed

- CI: the Frontend CI workflow runs the reusable workflow's E2E step again. The reusable-ci pin now includes the fix that installs Playwright browsers as the runner user instead of root, so the browsers are found. Note: the pinned reusable-ci still uses gha-ci-workflow-helpers v1.1.4, so the helper that reports a failed E2E script as a pass is not yet fixed there.
