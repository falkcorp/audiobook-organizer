### Fixed

- **Nightly Full CI now actually runs the test suites.** The nightly workflow called
  github-common's `reusable-ci.yml`, whose change detection compared the commit with
  itself on a scheduled run, saw no changed files and skipped every job. Go CI was
  skipped on every scheduled night checked, so the full non-short race suite only
  ever ran per-PR. `nightly.yml` and `frontend-ci.yml` now pin github-common#369 and
  `nightly.yml` sets `force-all: 'true'`, so each language the repo contains runs
  nightly: Go (full race suite, about 25 minutes), frontend with E2E, workflow lint
  and workflow scripts. Python CI stays off because the root `pyproject.toml` only
  holds black config and is not a package.
