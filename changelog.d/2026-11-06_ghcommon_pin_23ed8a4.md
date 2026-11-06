### Changed

- CI: every `falkcorp/github-common` reusable-workflow pin, and `.github/ghcommon-ref.txt`, moves to one SHA, `23ed8a46` (github-common main, 2026-11-06). Before this the nine callers sat on five different SHAs. What each one gains:
  - `nightly.yml` (`reusable-ci.yml`, from `861d4e83`): the gha-ci-workflow-helpers fix for a frontend run that exited 0 when its npm script failed, and Playwright browsers installed as the runner user.
  - `security.yml` (`reusable-security.yml`, from `d0c3326b` of 2026-07-05): the github-common#374 fallback script-ref fix, plus four months of dependency updates.
  - `ci.yml` (`reusable-ci-minimal.yml`) and `frontend-ci.yml`: dependency updates only.
  - `prerelease.yml` and `release-prod.yml` (`reusable-release.yml`, from `861d4e83`): github-common#388. Changelog collection now runs in its own job, so a refused push can no longer skip the package publish. When the `main` ruleset refuses the push, the collected `CHANGELOG.md` lands through a same-repo PR set to auto-merge.
  - The three burndown and triage callers, which are disabled: the newer runner image.
- Workflow headers: `triage-poll.yml`, `hard-burndown.yml` and `nightly-burndown.yml` get the `guid` and `last-edited` lines they were missing.
