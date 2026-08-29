### Fixed

#### Hitting the 10-RC limit now cuts a minor release instead of failing the build

Once a version accumulated 10 release candidates, the `Prerelease on Merge`
workflow went red on *every* subsequent merge to `main` and stayed red — while
doing nothing at all to stop the pile-up. `v0.219.3` reached `rc.33`, twenty-three
past the limit, with the guard failing the whole way.

The guard could never have worked as written, because it runs *after* the
`prerelease` job has already minted the RC tag. Failing the run reports a problem
that has already happened; it cannot prevent the next one. The only thing that
actually resets the ordinal is cutting the next stable version.

So the threshold check now promotes instead of failing:

- `.github/scripts/check-rc-ordinal.sh` (2.0.0) only **counts and reports**. It
  emits `rc_count` and `at_threshold` to `$GITHUB_OUTPUT` and exits `0` whether or
  not the threshold was reached. Deciding what to do about the verdict is the
  caller's job. Usage errors still exit `2`.
- `.github/workflows/prerelease.yml` (3.0.0) dispatches `release-prod.yml` with
  `release-type=minor` when `at_threshold` is true.

Two details that would otherwise have made this a silent no-op:

- The dispatch **must** authenticate with `JF_CI_GH_PAT`. A `workflow_dispatch`
  made with the default `GITHUB_TOKEN` is silently ignored by GitHub — a
  `GITHUB_TOKEN` run cannot trigger another workflow run. Falling back to
  `GITHUB_TOKEN` would have produced a green step that promoted nothing, so a
  missing PAT is a hard failure here rather than a quiet success.
- The step refuses to stack dispatches. Between this run and the release landing,
  every further merge to `main` mints another RC and re-enters the job; without a
  check for an already-queued or in-progress `release-prod` run, each one would
  queue a duplicate minor cut.
