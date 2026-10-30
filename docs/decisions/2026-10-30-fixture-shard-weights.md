<!-- file: docs/decisions/2026-10-30-fixture-shard-weights.md -->
<!-- version: 1.0.0 -->
<!-- guid: a41d7c3e-92b6-4f08-b5e1-7d3c0a68e2f9 -->
<!-- last-edited: 2026-10-30 -->

# Fixture-shard weights from measured CI times (2026-10-30)

Follow-up to PR 4261, which split the heavy fixture packages across the three
`test-fixtures` shards using an estimated weight table. Taken autonomously; every
choice below is open to the owner to reverse.

## Decision log

### 1. Sidecar JSON, not a rewritten Python table

Weights moved to `scripts/ci/fixture_weights.json`; `fixture_test_packages.py` loads it
at import into the same `_WEIGHTS` dict, so the planner tests that mutate `_WEIGHTS`
are unchanged. Rejected: having the tool rewrite the dict literal in the `.py` file
(a code diff per recalibration, regex-editing source). The sidecar sits next to the
planner in `scripts/ci/`, not in `scripts/` as the brief wrote: the planner and
`go_test_shards.py` already live there and the brief's `scripts/fixture_test_packages.py`
path does not exist. A missing or malformed sidecar fails the planner loudly rather
than defaulting every package (which would put the whole suite in one shard).

### 2. Timing capture parses `go test`'s plain `ok <pkg> <secs>s` lines, not `-json`

The runner already captures each child's output and prints it for people to read.
`-json` would turn those logs into event streams and make the Woodpecker step log
unreadable; the `ok` line carries the identical per-package wall time `go test`
measured. Rejected: `go test -json` plus a decoder in the runner. Two exports: a
`::timing::<unit>=<seconds>` line per unit on stdout (Woodpecker), and
`FIXTURE_TIMES_FILE` JSON (GitHub artifact, `actions/upload-artifact` pinned by SHA,
`if: !cancelled()`, 30-day retention). Only passing packages are timed: a `FAIL`
line is not a healthy run's duration. A slice with no `ok` line falls back to its
wall time only if it succeeded.

### 3. A package's weight is the sum of its slices; samples combine by `max`

Slices of one package run in different shards, so a measurement needs all of a
pipeline's shards; `--sample` groups them, and a sample missing a slice is dropped for
that package (warned) rather than counted low. The seed used `--stat max` over
pipelines 1827, 1828, 1829, not the median the previous table used.

Why: the weight drives `split_count` (timeout safety) as well as balance. Per-package
times vary widely by agent (`internal/merge` 36 s on one agent, 354 s on another; the
maintenance package 524/577/884 s summed over its slices). The median of three picks
the fast agents: it put `internal/plugins/maintenance` at 577 s, under the 600 s split
target, although that package took 870-1475 s unsplit on the slow agents and timed out
in pipeline 1820. Sizing to the slowest agent seen keeps the split protecting the
agent that needs it. `--stat median` stays available.

Consequences, visible in the plan: `internal/undo` (771) and `internal/database` (670)
are now split in two (each was one process of about 680 s, far under the 1500 s
timeout, so this is conservative, and costs one extra `-race` link each);
`internal/plugins/maintenance` is two parts instead of three; `internal/scanner` (395)
and `internal/audiobooks` (427) run whole. Estimated shard loads 2056/2056/2055 s
(were 2240 each, on the old estimates).

### Known limits

- Three samples is thin, and 1827/1828/1829 are three commits of the same PR (all
  after the hash-cost change). Recalibrate after a few more green runs: `fixture-times-*`
  artifacts or `::timing::` lines from the step logs, `--sample` per pipeline.
- Slices are chosen by test-name hash, so their sizes are uneven (maintenance slices
  took 237/168/479 s in pipeline 1829). The balancer assumes weight/parts each; weighting
  by per-test durations remains the follow-up named in the PR 4261 changelog fragment.
- Slice times are measured under contention with shard-mates, as the old medians were;
  their sum approximates, not equals, a single-process run.
- All 25 packages were measured, so `estimated` is empty in the seed. A new fixture
  package falls to `_DEFAULT_WEIGHT` (10 s) until recalibrated.
