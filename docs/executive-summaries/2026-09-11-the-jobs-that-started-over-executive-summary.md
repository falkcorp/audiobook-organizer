<!-- file: docs/executive-summaries/2026-09-11-the-jobs-that-started-over-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: d4421faa-291a-49d0-aa10-ecf5eb9a6fc0 -->
<!-- last-edited: 2026-09-11 -->

# The jobs that started over

**Pull request:** fix/resume-restart-uncheckpointed-ops (number filled in at merge).
Closes the `TODO.md` entry "8 ops declare `ResumeRestart` but never checkpoint".

## Executive Summary

Long-running jobs in the app — a metadata fetch across thousands of books, a
tag write-back that touches every file — are supposed to survive a restart.
Each job declares what should happen if the app stops while it is running:
pick up where it left off, start again as a brand-new job, stop and wait to be
asked, or stop for good. "Pick up where it left off" only works if the job has
been leaving notes about how far it got. Eight jobs declared that policy and
never left a single note.

- **Eight jobs said "resume me" but had nothing to resume from.** When the app
  restarted, each one was handed back its original instructions with no memory
  of the work already done, so it silently began again from the first item.
  For a fetch that takes eight hours, or a file write-back that takes six, a
  restart at hour five threw the whole five hours away and repeated every
  external lookup and every file write. This has been the case all along, but
  only became visible now that restart-and-resume itself works reliably.
- **Two of the biggest jobs now leave real notes.** The metadata candidate
  fetch and the bulk tag write-back record which books are still owed every
  25 books and once more when interrupted. On resume they are handed only the
  unfinished list, so no book is fetched or written twice. The record is a list
  of what is left, not a running count: these jobs use several workers at once
  that finish out of order, and a count would have skipped whichever books were
  still in flight.
- **Three jobs are now stopped rather than restarted, on purpose.** Merging
  authors, resolving a production company into real authors, and folding
  numbered series names together all change the library in ways that must not
  be applied twice or applied beyond what the operator asked for. Rather than
  quietly running again from zero, an interrupted run of these now shows up as
  interrupted so a person can look and re-run it deliberately. For the series
  job in particular, a silent restart would have overwritten the rollback
  report and applied a second batch on top of a canary the operator had capped.
- **Three jobs were checked and left alone, with the reasoning written down.**
  The ampersand-name repair, the ISBN sweep and the AI author scan each turn out
  to be safe to run again from the top: they look at the current state of the
  library, find only what is still unfinished, and never repeat a paid call.
  The ISBN sweep's description had claimed it "checkpoints every 100 books";
  it does not and never did — it resumes from its own saved position — and the
  description now says so.
- **Verified:** automated tests run each of the two newly checkpointed jobs,
  cancel it partway, and resume it exactly the way the app would after a
  restart, confirming the resumed run touches only the books still owed. The
  full test suites for both affected packages pass with the race detector on.

## What was found

Each of the eight jobs declared "resume from saved state" without ever saving
state. The app's resume step is not at fault: it does exactly what the policy
says and hands the job whatever notes exist, which for these eight was none.
The result is indistinguishable from "start a new job", except that nobody had
ever reviewed whether starting again was safe — which is precisely the review
the "start again" policy requires.

## Why it mattered

For the two long jobs, the cost was wasted hours and repeated external
requests against daily quotas, every time the app was restarted mid-run —
which on production, with a deploy most days, is most runs. For the three
library-changing jobs, the risk was worse than waste: a merge or a series
consolidation running a second time, unrequested, over a half-finished first
attempt.

## The fix

Each job was reviewed one at a time and given exactly one of three outcomes,
with the reasoning recorded next to the policy in the code: a real checkpoint
where the job has a natural list of items to remember; an explicit "stop and
show the operator" where re-running could apply a change twice or exceed what
was asked; or a written proof where running again from the top is provably
both safe and cheap. The one piece deliberately left for a follow-up is a
registry-level rule that refuses to accept "resume from saved state" from any
job that never saves any, so this cannot recur silently.
