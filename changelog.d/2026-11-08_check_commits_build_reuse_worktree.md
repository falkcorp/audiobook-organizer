### Fixed

- CI: `check_commits_build.py` now builds every commit of a move PR in one reused worktree instead of a new directory per commit. Go's build cache keys include the package directory, so every commit used to recompile from scratch, and a 33-commit PR ran past the 30-minute job timeout.
