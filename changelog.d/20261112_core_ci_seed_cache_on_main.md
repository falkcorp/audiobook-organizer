### Changed

- ci: core-ci also runs on pushes to main, so its Go cache is saved under main's scope, the only one every pull request can read. A cache saved by a pull request or merge-queue run is visible to that ref alone, so each PR's first core-ci run was starting cold. Runs on main are never cancelled by a newer push.
