### Added

- After an upgrade, open browser tabs offer to reload. The UI build now stamps
  its ID into the page, every API response is compared with it, and a tab whose
  build differs shows a "A new version is available" bar with a Reload button.
- `--version-json` and `--print-upgrade-floor` print machine-readable build and
  store facts. `--version-json` prints the app, version, commit, UI build ID and
  binary ID as one JSON line; `--print-upgrade-floor` prints nothing until a
  release declares a floor.

### Changed

- The deploy template checks the deployed version by running
  `--version-json` on the host over ssh instead of calling an HTTP route that
  does not exist.
- Release builds no longer pass `-X main.commit` and `-X main.date`, which named
  variables the program does not have.
- The `docker-image` check now also requires the `X-App-Build` header to equal
  the build ID in the served page.
