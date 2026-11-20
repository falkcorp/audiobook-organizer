<!-- file: docs/executive-summaries/2026-11-20-merge-queue-race-nightly-executive-summary.md -->
<!-- version: 0.1.0 -->
<!-- guid: d51fc831-aa0f-4f01-b14d-da44bbd5ad83 -->
<!-- last-edited: 2026-11-20 -->

# The merge queue gets faster, and the slow race checks move to the night

**Date:** 20 November 2026. PR #4818. Owner decision 2026-11-20.

## Executive Summary

- Every change waits in GitHub's merge queue, which tests it one more time
  before it lands. Most of that hour went to one expensive kind of check: the
  race detector, which hunts for bugs where two parts of the program touch the
  same data at once. The queue's test jobs no longer run it. The estimate is
  35-45 minutes per change instead of 60; the time spent waiting for a free
  build machine is the floor and is not something this change controls.
- The race checks did not go away. They still run on every proposed change
  before it is queued, and again right after it lands, where a failure undoes
  the change automatically.
- On top of that, the whole race-check suite now runs every night at 3 a.m.
  Eastern on one of the Mac build machines, never on the machine next to the
  owner whose fans would be heard at night. If that run fails, or does not run
  at all, an issue is filed on GitHub the next morning.
- While checking how the queue decides "pass", we found a gap: when the main
  test group failed, the one required check that depends on those tests was
  marked "skipped", and GitHub counts a skipped required check as a pass. A
  change with failing tests could have landed. That check now fails when the
  tests fail.
- Verification: a rehearsal of the queue's new settings on this change ran the
  test jobs without the race detector in 9-14.5 minutes each (14-25 with it).

## The race checks in the merge queue

**What it was.** The merge queue ran the full race-detector test suite on every
change, in eight parallel jobs of 14-25 minutes each, plus about half an hour
waiting for a free build machine.

**Why it mattered.** Every change took about an hour to land, and the waiting
grew as more changes queued.

**The fix.** In the queue, the quick test jobs run without the race detector,
and the "fixture" test jobs are skipped. Those jobs run whole groups of tests
in their slow, full form; most of those tests also run in the quick jobs, so
what the queue gives up is only the slow tests that the quick jobs leave out.
Those still run on every proposed change, after every landing, and nightly.
The separate test run for the shared core library follows the same rule.

## The nightly race run

**What it was.** Moving the race checks out of the queue would leave no place
that runs the whole suite on the latest code every day on our own machines.

**Why it mattered.** A race bug that only appears when two changes combine
could otherwise sit unnoticed.

**The fix.** A scheduled job runs the whole race suite at 3 a.m. Eastern on a
Mac build machine, expected to finish in one to one and a half hours. A
morning check on GitHub reads its result and files (or updates) one issue when
it failed, ran past its time limit, got stuck, or never ran.

## The skipped check that counted as a pass

**What it was.** The coverage check (which also confirms the tests ran) only ran
when every test group passed. When one failed, GitHub marked it "skipped", and
a skipped required check counts as passing.

**Why it mattered.** The queue could have merged a change whose tests failed.
With the race detector gone from the queue, these tests are its whole test
gate, so the gap had to close.

**The fix.** The coverage check now runs once the tests finish and fails unless
every test group passed. The rehearsal confirmed it: a test group failed and
the check failed with it instead of being skipped.
