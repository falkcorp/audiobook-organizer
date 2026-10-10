<!-- file: docs/ci/2026-10-ci-throughput.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c1e4b2a-93d5-4f6e-8a07-2b5d9e3c1f84 -->
<!-- last-edited: 2026-10-09 -->

# CI throughput after the sharded short run (07-C4)

Measurement only. No workflow, baseline or script was changed. Source: the first ten
first-parent commits on `main` after the 07-C3 merge `b46bafe78` ("ci: shard the short Go
test run in four and merge coverage"), read from `gh run list`, `gh run view` and the Actions
jobs API on 2026-10-09. Woodpecker is a separate manual gate set and is not measured.

## Before (A.9, `A-measurements.md` section A.9)

| Run | Kind | Wall |
|---|---|---:|
| 37865893192 | pull_request, docs-only PR | 22 min |
| 37865901335 | push to `main`, docs-only merge | 45 min |

A.9 gives no figure for the `auto-revert.yml` free re-run (`gh run rerun --failed`,
line 94), so none is quoted.

## How to read the table

- A run is created per push, and a push of several rebase-merged commits runs CI only for its
  tip. The ten commits therefore map to **6 distinct runs**. A commit that is not the tip of
  its push is marked `shared with <tip>`; it has the same run id, wall and attempt as the tip.
- Three of the six runs were **cancelled** by the workflow concurrency group because the next
  merge landed minutes later. They are recorded as `cancelled (superseded by <sha>)`, count as
  rows, and are excluded from the `main` wall and shard medians (medians are over the **3
  runs that completed**).
- Wall minutes = `updatedAt - createdAt` from `gh run list`. For run 37995734374 (attempt 2)
  that spans both attempts.
- Cache column from each shard's `Cache restored from key:` line: plain `Linux-go-1.27-<hash>`
  is **cold**, `Linux-go-1.27-race-short-<hash>-<date>` is **warm**. The race cache was first
  saved by main run 37987123834, so that run is cold and every later run is warm. Cancelled
  runs never reached the step: n/a.
- Max shard = longest `Go Tests (short, race) shard N/4` job (`completedAt - startedAt`). For
  37995734374 it is attempt 1 (all four shards had finished before the Fixture failure).
  Cancelled-early run 37990212849 and 37995187216 have no finished shard: n/a.
- PR wall = last `pull_request` ci.yml run of the merged PR before merge. PR numbers are from
  `gh api repos/falkcorp/audiobook-organizer/commits/<sha>/pulls`.

## Table

| # | Merge | Run id | `main` wall (min) | Attempt | Conclusion | Failed on attempt 1 | Cache | Max shard (min) | PR | PR run id | PR wall (min) |
|---|---|---|---:|---:|---|---|---|---:|---|---|---:|
| 1 | a68074e75 | 37987123834 (shared with 9786a075d) | 26.6 | 1 | success | none | cold | 12.1 | #3882 | 37985456538 | 14.5 |
| 2 | 9786a075d | 37987123834 | 26.6 | 1 | success | none | cold | 12.1 | #3882 | 37985456538 | 14.5 |
| 3 | 6e0305d3a | 37990212849 | 0.5 | 1 | cancelled (superseded by 6285a7ab3) | n/a | n/a | n/a | #3876 | 37986811976 | 24.8 |
| 4 | b0fe0c90e | 37990246364 (shared with 6285a7ab3) | 9.4 | 1 | success | none | warm | 7.8 | #3877 | 37986818573 | 22.3 |
| 5 | 6285a7ab3 | 37990246364 | 9.4 | 1 | success | none | warm | 7.8 | #3877 | 37986818573 | 22.3 |
| 6 | abd67dabf | 37991414341 (shared with 829682bfc) | 11.0 | 1 | success | none | warm | 9.6 | #3879 | 37986822249 | 21.1 |
| 7 | 829682bfc | 37991414341 | 11.0 | 1 | success | none | warm | 9.6 | #3879 | 37986822249 | 21.1 |
| 8 | f9a06d474 | 37995187216 (shared with fa8f1afc2) | 7.0 | 1 | cancelled (superseded by ce64406c9) | n/a | n/a | n/a | #3883 | 37993191133 | 19.6 |
| 9 | fa8f1afc2 | 37995187216 | 7.0 | 1 | cancelled (superseded by ce64406c9) | n/a | n/a | n/a | #3883 | 37993191133 | 19.6 |
| 10 | ce64406c9 | 37995734374 | 14.8 | 2 | cancelled (superseded by 1a5b1ab98) | Fixture Tests (no -short, race) 3/3 | warm | 8.2 | #3884 | 37994152099 | 15.7 |

No commit lacked a run, and none was docs-only. Two further runs exist after the ten
(1a5b1ab98 success, 5c044e229 success) but are outside the sample.

## Statistics

Each figure is the output of the quoted command.

PR wall, over the 6 distinct PRs (rows sharing a PR counted once):

```
python3 -c 'import statistics;print(statistics.median([14.5,24.8,22.3,21.1,19.6,15.7]))'
20.35
```

Maximum PR wall: 24.8 min (#3876). Minimum: 14.5 min (#3882).

`main` wall, over the 3 completed runs (37987123834, 37990246364, 37991414341):

```
python3 -c 'import statistics;print(statistics.median([26.6,9.4,11.0]))'
11.0
```

Maximum `main` wall: 26.6 min (the cold run). Warm completed runs only (2 runs):

```
python3 -c 'import statistics;print(statistics.median([9.4,11.0]))'
10.2
```

Max shard, over the 4 runs with finished shards (37987123834, 37990246364, 37991414341,
37995734374 attempt 1):

```
python3 -c 'import statistics;print(statistics.median([12.1,7.8,9.6,8.2]))'
8.899999999999999
```

Maximum shard: 12.1 min (cold run). Warm-only maximum: 9.6 min.

Attempts: 1 of 6 distinct runs (1 of 10 commits) has `attempt` greater than 1: run
37995734374. 5 of 6 are on attempt 1. Of the 3 completed runs, 3 of 3 passed on attempt 1.

## Verdict

| Target | Result | Numbers |
|---|---|---|
| PR run under 10 min | **not met** | median 20.35, max 24.8, min 14.5; no PR run in the sample was under 10 |
| `main` run on one attempt | **not met** (1 exception) | 5 of 6 runs on attempt 1; 37995734374 needed attempt 2. Completed runs: 3 of 3 on attempt 1. Warm completed `main` runs took 9.4 and 11.0 min, one PR-sized run versus 45 min in A.9 |
| Longest shard under 8 min | **not met** | median 8.9, max 12.1 (cold); 1 of 4 runs under 8 (7.8), 37995734374 was 8.2 |

Caveats: only 3 completed `main` runs, so the medians are thin. The PR runs in the table
(18:47 to 21:33 UTC) mostly predate the warm race cache and the 07-C3 weight recalibration
(#3883), so the PR figure does not yet show the steady state.

## Re-run-prone jobs

Threshold: a job needing a re-run on more than 2 of the 10 merges. None did, so no
`todo.d/` fragment is filed.

One re-run occurred (below threshold), recorded with its cause so it is not called a flake:
run 37995734374 attempt 1, job `Fixture Tests (no -short, race) 3/3` failed on
`TestProp_ChromemMatchesSqlite` (44.49 s): `dedup_engine_prop_test.go:312` reports
`[rapid] failed after 57 tests: sqlite→chromem overlap too low: 0 of 1 matched (chromem set=0)`,
raised at `internal/server/dedup_engine_prop_test.go:387`. The failing draw includes vectors with
values near 1e-30, and the chromem result set was empty. The root cause is not established
here; the rapid seed is randomized per run, which fits a re-run going green. It is a
property-test counterexample rather than an infrastructure failure, and should be triaged if
it recurs.
