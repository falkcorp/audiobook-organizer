### Added

- CI: a trial, non-required `Lint (GitHub-hosted, trial)` job (new `lint-trial.yml`) runs the same `make staticcheck` as Woodpecker's `checks-lint` on a GitHub-hosted runner, so the two verdicts can be compared before the Woodpecker job is retired. Woodpecker's `checks-lint` remains the gate; the trial is not in the branch ruleset.
