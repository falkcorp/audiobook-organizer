<!-- file: docs/executive-summaries/2026-10-30-finish-without-resuming-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: e3a8c5d1-7f24-4b96-a1d0-62b9f4e80c37 -->
<!-- last-edited: 2026-10-30 -->

# A stopped repair job can now be wrapped up without finishing the rest of its work

## Executive Summary

Some repair jobs apply their changes in batches, and when the app is restarted part way
through, the job stops and waits for a person to decide what to do. The person could
resume it, or throw it away. Throwing it away deleted the job's record, and the batches
that had already been applied never got their closing step (the part of the job that
tidies up and records what was changed). This change adds a third choice, "Finish without
resuming", which runs that closing step over the batches that were already applied and then
ends the job, without doing any more of the work.

## What changed

- **A new button on the Activity page.** A job that is waiting for a decision now shows
  "Finish without resuming" next to Retry and Discard, when it is the kind of job that has
  a closing step. Jobs that have none do not show it.
- **It does only the closing step.** It does not look at the library again and does not
  start any new batch. It uses the list of batches the job had already completed.
- **Nothing is lost if it is interrupted.** If the app stops again while the closing step is
  running, the job goes back to waiting for a decision, and Retry still resumes it in full.

## What to know

- Changes that a batch had started but not completed when the app stopped are not counted as
  applied; resuming the job (Retry) is how to cover those.
- The button asks nothing before it acts, like Retry and Discard on the same page.
