### Fixed

- Test-only: running test servers in the books package no longer bind the default port 8484 when a test supplies its own environment, so two test runs on one machine (parallel packages, worktrees or CI pipelines) no longer fail with "address already in use".
