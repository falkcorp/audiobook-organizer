### Fixed

- CI: `scripts/ci/free_runner_disk.sh` now renames the unused preinstalled toolchains into a trash directory and deletes them in the background. On a slow runner the foreground delete had taken 8 minutes, which pushed Repo Guards past its 15-minute timeout and dropped a PR from the merge queue.
