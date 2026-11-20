<!-- file: docs/executive-summaries/2026-11-20-merge-queue-race-nightly-executive-summary.md -->
<!-- version: 0.1.0 -->
<!-- guid: d51fc831-aa0f-4f01-b14d-da44bbd5ad83 -->
<!-- last-edited: 2026-11-20 -->

# The merge line gets faster, and the slow race checks move to the night

**Date:** 20 November 2026. PR #4818. Owner decision 2026-11-20.

## Executive Summary

- Every change waits in a "merge line" (GitHub's merge queue) that tests it one
  more time before it lands. Most of that hour went to one expensive kind of
  check: the race detector, which hunts for bugs where two parts of the program
  touch the same data at once. The merge line no longer runs it, so a change
  should land in well under the hour it used to take.
- The race checks did not go away. They still run on every proposed change
  before it joins the line, and again right after it lands, where a failure
  undoes the change automatically.
- On top of that, the whole race-check suite now runs every night at 3 a.m.
  Eastern on one of the Mac build machines, never on the machine next to the
  owner whose fans would be heard at night. If that run fails, or does not run
  at all, an issue is filed on GitHub the next morning.
- While checking how the merge line decides "pass", we found a gap: when the
  main test group failed, the one required check that depends on those tests
  was marked "skipped", and GitHub counts a skipped required check as a pass.
  A change with failing tests could have landed. That check now fails when the
  tests fail.

## The race checks in the merge line

**What it was.** The merge line ran the full race-detector test suite on every
change, in eight parallel jobs of 14-25 minutes each, plus about half an hour
waiting for a free build machine.

**Why it mattered.** Every change took about an hour to land, and the waiting
grew as more changes queued.

**The fix.** In the merge line the tests now run without the race detector, and
the group of slow "full fixture" tests (which the quick tests already cover in
short form) is skipped. Proposed changes and landed changes still get the full
race checks.

## The nightly race run

**What it was.** Moving the race checks out of the merge line would leave no
place that runs the whole suite on the latest code every day on our own
machines.

**Why it mattered.** A race bug that only appears when two changes combine
could otherwise sit unnoticed.

**The fix.** A scheduled job runs the whole race suite at 3 a.m. Eastern on a
Mac build machine. A morning check on GitHub reads its result and files (or
updates) one issue when it failed, got stuck, or never ran.

## The skipped check that counted as a pass

**What it was.** The coverage check (which also confirms the tests ran) only ran
when every test group passed. When one failed, GitHub marked it "skipped", and
a skipped required check counts as passing.

**Why it mattered.** The merge line could have merged a change whose tests
failed. With the race detector gone from the line, these tests are its whole
test gate, so the gap had to close.

**The fix.** The coverage check now always runs once the tests finish and fails
unless every test group passed.
