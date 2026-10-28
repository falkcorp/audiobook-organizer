### Changed

- CI: the Frontend CI workflow pins the reusable workflow at a github-common commit that installs Playwright browsers as the runner user. The reusable E2E step stays skipped, now because it duplicates the repo's own E2E workflow (same suite and config; chromium on PRs, chromium and webkit nightly) and because the pinned helpers still report a failed E2E script as a pass.
