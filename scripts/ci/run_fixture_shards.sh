#!/bin/bash
# file: scripts/ci/run_fixture_shards.sh
# version: 1.0.0
# guid: 6764bb97-f172-4a15-ac8e-c3f871f6c701
# last-edited: 2026-10-18
# Run `make test-fixtures` as three sequential shards inside one Woodpecker
# step (.woodpecker/test-fixtures.yaml). Every shard runs even if an earlier one
# fails, so the step reports all failing packages like the old single `go test`
# did; the exit status is the combined result. Elapsed seconds print per shard.
#
# Time budget: the repo's Woodpecker pipeline timeout is 60 min and three
# 25m shards could sum to 75m. Each shard gets min(25m, budget - elapsed) as its
# go test -timeout, so Go times out and prints its goroutine dump before
# Woodpecker kills the job. A shard with under MIN_SECS left is not started.
#
# Overridable for tests: FIXTURE_MAKE (the make command), FIXTURE_BUDGET_SECS.
# $SECONDS is bash's seconds-since-start counter, reset below.
SECONDS=0
make_cmd=${FIXTURE_MAKE:-make --no-print-directory}
budget=${FIXTURE_BUDGET_SECS:-3300} cap=1500 min_secs=120 rc=0
for s in 1 2 3; do
  left=$((budget - SECONDS))
  if [ "$left" -lt "$min_secs" ]; then
    echo "fixture shard $s/3: NOT STARTED, only ${left}s of the ${budget}s step budget left (need ${min_secs}s)" >&2
    rc=1; continue
  fi
  t0=$SECONDS
  # shellcheck disable=SC2086 # make_cmd is intentionally word-split
  $make_cmd test-fixtures FIXTURE_SHARD="$s/3" FIXTURE_TIMEOUT="$((left < cap ? left : cap))s" || rc=1
  echo "fixture shard $s/3: $((SECONDS - t0))s elapsed (step total ${SECONDS}s of ${budget}s)"
done
exit $rc
