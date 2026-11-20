### Changed

- CI: Binary Smoke, CodeQL and Security now cancel a superseded run when a pull request gets a new push. Runs on main, scheduled runs and manual runs are never cancelled. This cuts the GitHub Actions queue, which had over 100 waiting runs.
