### Fixed

- **Prereleases stopped reporting `failure` on every single run.** The release
  pipeline detected Python from `pyproject.toml` and ran a full Python release
  (setup-python, twine, `pytest`) against a file that only holds Black's
  line-length config -- no `[project]` table, no pytest -- so it died with
  `pytest: command not found` (exit 127). That failure tripped the
  `always() && !failure()` gate on `Create GitHub Release`, which silently
  skipped the draft, the changelog body, the floating tags, the GitHub Packages
  publish, and the "fail at 10 RCs" guard, while goreleaser kept minting RC tags
  from inside the Go job. `prerelease.yml` and `release-prod.yml` now pass
  `disabled-languages` (ghcommon #350) to opt out explicitly, and their
  `reusable-release.yml` pin moves to `6acc4d03`.
- Prerelease builds no longer build Docker images. `docker-enabled: false` was
  documented in-repo as not working: the reusable workflow's language flags are
  "force ON" switches where `false` means "auto-detect", so "never build this"
  was unexpressible. `disabled-languages: 'python,docker'` expresses it.
  `release-prod.yml` keeps Docker on deliberately.
