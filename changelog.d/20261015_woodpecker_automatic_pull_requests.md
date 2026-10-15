### Changed

- Woodpecker CI runs automatically on pull requests again, which supersedes the manual-only change from 2026-10-02 listed in this release. Manual runs through the API still work, and each CI machine runs as many pipelines as its own capacity allows. `make ci-woodpecker` now follows the pull-request pipeline its push starts instead of queueing a second run of the same commit.
