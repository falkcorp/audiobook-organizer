### Changed

#### Merge queue runs without the race detector; the race suite moves to a nightly Woodpecker run

In the merge queue (`merge_group`) ci.yml's queue shards change: the short test shards run without `-race`, and the no-`-short` fixture shards are skipped. The fixture shards run whole packages without `-short`; those packages' other tests also run in the short shards, so the queue gives up only the tests that skip under `-short`. Core CI's `make -C core ci` also drops `-race` in the queue (`TEST_SHORT_RACE=`). Pull requests and pushes to main still run both under `-race`. The `race` step of the `changes` job makes the call; `workflow_dispatch` with `race=false` rehearses the queue's flavour on a branch. `make test-short-shard` takes `SHARD_RACE=` (default `-race`). Run 44770780353 spent most of its hour on race shards (15-25 min each) plus 30-37 min of runner wait; without `-race` the shards took 9-14.5 min in the rehearsal. Estimated queue time is 35-45 min, with runner wait as the floor.

The whole `-race` suite (short and fixture) runs every night at 03:00 America/New_York in the new `.woodpecker/race-nightly.yaml`, pinned to `platform: darwin/arm64` (llm1 or the Mac agent; never U1, never the prod host). The Woodpecker cron entry `race-nightly` is created on the server once (command in `docs/ci/woodpecker.md`). The new `race-nightly-watch.yml` reads its commit statuses (each must be from 06:00 UTC today or later) at 13:00 UTC and files or updates a `race-nightly` issue when it failed, is stuck, or did not run.

### Fixed

#### Coverage Floor (PR gate) fails when a short-test shard fails

It used to be skipped when a shard failed, and a skipped required check counts as passing, so the queue could merge a commit with failing short tests (merge-queue run 44717492360: shard 1/4 failed, the check was skipped). It now runs under `!cancelled()` and fails unless every shard succeeded, like extraction-gates' terminal job (`!cancelled()` so a superseded push run on main is not turned red). New page `docs/ci/merge-queue.md` lists the 11 required checks and how each is met in the queue.
