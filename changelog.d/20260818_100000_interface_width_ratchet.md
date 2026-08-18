### Added

#### Interface width is now a ratchet, not a suggestion

`scripts/check-interface-width.sh` compares the `interfacebloat` finding count
against `.interface-width-baseline` (28 at time of writing) and fails if it goes
up — or if it goes down without the baseline being lowered in the same change,
so ground taken stays taken.

Two escape hatches, both leaving a reviewable trace: `//nolint:interfacebloat`
with a mandatory explanation (a bare or unexplained directive is itself a
finding), or raising the number in a file whose only purpose is to hold it.

The gate counts findings rather than listing files because the count is the
stable half of golangci-lint's output and the paths are not. Measured on
`b7f4627b`: 28 findings from the repo root, from inside a worktree, and from an
isolated clone — but three of those four runs attributed findings to the wrong
checkout, because the result cache is keyed by file content and replays whichever
path was recorded first. `.golangci.yml` now excludes `.worktrees/` for
determinism; that does not change the number.

The two tools driving the interface split sweep also moved out of `/tmp` into
`scripts/`: `split_interface.py` and `verify_interface_split.py`.
