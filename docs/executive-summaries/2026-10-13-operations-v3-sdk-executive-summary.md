<!-- file: docs/executive-summaries/2026-10-13-operations-v3-sdk-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9d0eaa44-fcb8-4058-bb16-c2e4a2c47636 -->
<!-- last-edited: 2026-10-13 -->

# A new way to write background operations

Branch `task/05-pr5`. Task brief:
[05-PR5](../proposals/2026-10-holistic/tasks/05/05-PR5.md).

## Executive Summary

- **Operations get a new, safer authoring kit.** A background operation (a
  library-wide repair, a backfill, a fixer) can now be written as one short
  declaration: what it reads and writes, who may start it, and the work for
  one item. The framework supplies everything around that work: how many
  workers run at once, cancel, pause, progress, resume after a restart, and
  safe writes. Nothing in production uses it yet. Existing operations are
  ported to it in later steps.
- **Parallel by default.** A batch operation now runs on every processor
  core unless its author writes down why it must run one item at a time.
  Under the old system, leaving out one setting quietly made a
  whole-library job run on a single core.
- **A restart no longer throws work away.** A batch is cut into chunks of
  256 items. Each finished chunk is written down. When the server restarts
  mid-run, the operation picks up under the same run id and redoes only the
  chunks that were not finished. Under the old system most operations were
  simply dropped by a deploy. Others redid everything after the first
  unfinished item, even when most later items were already done.
- **Progress only moves forward.** The progress count includes only whole
  finished chunks, so it never jumps backwards after a restart and never
  counts half-done work.
- **Writes are guarded.** Every write goes through one gatekeeper. Once a
  run is canceled, abandoned or loses its hold on the library scan, the
  gatekeeper refuses its writes. It notes each write before making it, so
  a crash in the middle leaves a record of what may be half-applied. It
  records history only after the write really happened. It has no way to
  delete a book or a book file. A run whose writes start being refused
  stops right there and is reported as failed, rather than finishing as if
  nothing were wrong.
- **A restart does not lose the list of what was applied.** A fixer
  interrupted by a deploy still reports, and follows up on, every change it
  made before and after the restart, and its follow-up step runs once.
- **The existing Repairs tools use the same gatekeeper.** They behave as
  before. A test checks that none of the tools they call has disappeared
  or changed shape.

## What it was

Operations were written by hand against a loose set of conventions. Each
author had to remember to make their loop parallel, to check for cancel, to
report progress, to save a resume point, and to write safely. Many did not.
Most operations were dropped on every deploy, and several whole-library jobs
ran on a single core.

## Why it mattered

A deploy during a long repair or backfill threw away hours of work. A job
that forgot to run in parallel took many times longer than it needed to. A
job that kept running after it was canceled could keep writing. That last
case caused a real incident on 2026-10-01, when an apply kept retiring
books after it was canceled.

## The fix

The framework now owns the loop, the resume record and the write path, so
an author cannot forget them. A careful test interrupts runs at random
points and checks two rules every time. Every item runs at least once. No
chunk that was recorded as finished ever runs again. A second test restarts
the real job runner in the middle of a batch and checks that the resumed
run, under the same id, does only the unfinished chunks.

Two smaller fixes shipped with it. A late write can no longer re-create
storage for a run that has already ended; before, that storage was never
cleaned up. And once a batch run's progress comes from its chunk record,
an older progress write can no longer roll its count back.
