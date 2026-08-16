### Added

- `make mutate PKG=<pkg>` / `make mutate-dry PKG=<pkg>` run
  [gremlins](https://github.com/go-gremlins/gremlins) mutation testing on a single
  package, to answer the question a green suite cannot: would these tests fail if
  the code were wrong? Install the pinned binary with `bash scripts/setup-gremlins.sh`.

  Both targets run through `scripts/run-mutation.sh`, which enforces a disk budget.
  gremlins copies the entire module directory once per worker, and this module root
  is ~34GB because `.worktrees/` lives inside it — an unguarded `--dry-run` filled a
  926GB volume. The wrapper refuses to run from the primary checkout (the same copy
  is 1.8GB from a worktree), projects peak usage before starting, and kills the run
  if free space crosses a floor.
