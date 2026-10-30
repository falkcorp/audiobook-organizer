### Changed

#### Fixture-shard weights come from measured CI times, not a hand-edited table

The weights `scripts/ci/fixture_test_packages.py` balances the three `test-fixtures`
shards by (and decides which packages to split with) were a hand-edited `_WEIGHTS`
dict whose heaviest entry, `internal/server` at 830 s, was an estimate. They now live
in `scripts/ci/fixture_weights.json` and are regenerated from real runs.

- `make test-fixtures` prints a `::timing::<unit>=<seconds>` line per package or
  slice that passed, and writes the same as `fixture-times.json` when
  `FIXTURE_TIMES_FILE` is set. The GitHub `fixture-tests` job sets it and uploads it
  as a `fixture-times-N` artifact; Woodpecker has no artifacts, so its step log is
  the record.
- `scripts/ci/fixture_shard_weights.py` reads those outputs (or raw step logs) grouped
  into pipelines with `--sample`, sums a split package's slices, combines samples by
  `--stat median|max`, and rewrites the sidecar. A package no sample measured keeps
  its old weight and is listed under `estimated`.
- Seeded from the shard logs of Woodpecker pipelines 1827, 1828 and 1829 with
  `--stat max` (all 25 packages measured, none left as an estimate). Result:
  `internal/undo` and `internal/database` are now split in two, `internal/plugins/maintenance`
  in two rather than three, and `internal/scanner` and `internal/audiobooks` run whole;
  the estimated shard loads are 2056/2056/2055 s. Reasoning and rejected alternatives in
  `docs/decisions/2026-10-30-fixture-shard-weights.md`.
