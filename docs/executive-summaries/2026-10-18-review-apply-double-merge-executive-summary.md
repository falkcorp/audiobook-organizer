<!-- file: docs/executive-summaries/2026-10-18-review-apply-double-merge-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 599fbed0-86a9-46e6-8bcf-3d81a0563082 -->
<!-- last-edited: 2026-10-18 -->

# Approving a review hold now merges it once, reports every failure, and recovers after a crash

PR #4062 (01-P81j, branch `task/01-p81j`), not yet merged.
There is no separate spec.

## Executive Summary

- **Bulk approve no longer stops silently.** Approving a group of review holds
  used to stop at the first one that failed, with a bare error. Holds before it
  had been merged but were not reported; holds after it were never tried. Now
  every hold is tried, and the result lists which were merged, which failed and
  why, and which were skipped.
- **A merge can no longer run twice.** Approving a hold that says "these files
  are one book" merges the books and permanently retires the extra book records.
  Two approvals arriving together, or an approval and a reject arriving
  together, could run that merge twice. Now an approval "holds" the hold while
  its merge runs, and nothing else can approve, reject or re-decide it until the
  merge has finished.
- **A hold interrupted by a crash or restart can be finished.** If the server
  stopped in the middle of a merge, the hold was left marked "in progress"
  forever and was hidden from the review list. Now the server recognises at once
  that the merge was interrupted, and approving the hold again finishes it.
- **A failed second try keeps the hold out of the queue.** If finishing an
  interrupted merge fails, the hold stays held instead of returning to the
  list, because the first try may already have moved books. A new "Abandon"
  button moves a hold that cannot be finished to a "needs attention" list,
  where someone checks the library and then approves or rejects it.
- **A merge whose result could not be saved is not run again.** If the merge
  worked but recording "done" failed, approving again now just records it,
  without merging a second time.
- **Held holds are visible.** The review page shows a "held mid-apply" notice
  with a count, a list of those holds, and a Finish button for each.
- **The nightly hold clean-up no longer erases a decision.** It used to be able
  to delete a hold a reviewer had approved or rejected moments earlier, losing
  the decision. It now deletes only holds that are still undecided.
- **Verification.** Automated tests with made-up data reproduce each failure
  above, including the exact timings that used to cause a double merge and a
  real server restart in the middle of a merge. Each safety check was also
  tested by deliberately breaking it and confirming a test then failed. This has
  not yet been run against the production library.

## Bulk approve that stopped at the first failure

**What it was.** The "approve all" action processed holds one at a time and gave
up at the first merge that went wrong, answering only with a generic server
error.

**Why it mattered.** The reviewer could not tell what had happened. Some holds
had already been merged and were not listed as merged; the rest had not been
touched. Retrying could repeat work that was already done.

**The fix.** Each hold's outcome is now reported separately: merged, approved
(when merging is switched off), failed with its reason, or skipped with its
reason. A failure does not stop the rest. Failed holds keep their place in the
list, so trying again retries only them.

## The same merge running twice

**What it was.** Approving a hold first runs the merge and then records the hold
as done. Nothing stopped a second approval, a reject, or a "keep these separate"
decision from arriving while the merge was still running. A reject arriving at
that moment also wiped the record that a merge was underway, so a third approval
then started the merge again.

**Why it mattered.** A merge moves files and permanently retires the extra book
records. Running it twice on a changing set of books can merge the wrong things,
and those records cannot be brought back by the review page.

**The fix.** Approving a hold now first marks it "being applied", in a single
step that only one request can win. While it is marked, every other approval and
every decision (reject, keep separate, approve with merging switched off) is
refused with a clear message that a merge is in progress. Only the approval that
won can mark the hold done, or hand it back if the merge failed. If the hold
changed after the reviewer loaded it (for example, a rescan updated it), the
approval is refused and the reviewer is asked to reload, rather than merging
from an out-of-date view.

## A hold stuck after a crash

**What it was.** The earlier version of this fix promised that a hold left
"being applied" by a crash would be released after 15 minutes. In fact the
release could never happen, because every approval and reject refused the hold
before that check was reached. A fixed time limit would also have been unsafe:
a large merge can run longer than any fixed limit.

**Why it mattered.** A hold interrupted by a crash or restart stayed "being
applied" forever. It was in no list and counted nowhere, so nobody would see it.

**The fix.** Each running copy of the server gets a random identity when it
starts, and a hold marked "being applied" records which copy marked it. A mark
from the copy that is running now is never overridden, however long its merge
takes. A mark from an earlier copy means that copy has stopped, so the next
approval takes the hold over immediately and runs the merge again. The merge is
built to skip work that is already done: books already merged are passed over.
The one thing it does not redo is the disc and track numbering of a multi-disc
merge that was cut off part way. The re-run uses the same action the
interrupted merge was running, even if the hold now recommends a different one.
A "this folder duplicates an existing book" merge can also fail on its second
try if its first try got part way.

## A second try that fails

**What it was.** When finishing an interrupted merge failed, the hold went back
into the review list as if nothing had happened, and a reviewer could then
reject it or mark it "keep separate".

**Why it mattered.** The interrupted first try may already have moved books.
Recording "rejected" over a half-done merge leaves the library and the record
disagreeing, with nothing pointing anyone at it.

**The fix.** A failed second try leaves the hold held, with a message giving the
reason and the two ways forward: try again, or abandon it. Abandoning moves the
hold to "needs attention" and records which merge was abandoned. It undoes
nothing. The review page lists those holds separately, with Approve (run the
merge again from the start) and Reject, for someone who has checked the library.
Abandoning is also the way out for an interrupted merge whose action the server
no longer knows how to run.

## A merge that worked but was not recorded

**What it was.** If the merge succeeded but saving "done" failed, the hold stayed
"being applied".

**Why it mattered.** The reviewer could not know whether to try again, and
trying again could merge a second time.

**The fix.** Saving "done" is retried several times. If it still fails, the
reviewer is told the merge ran and the result was not recorded. Approving again
while the server keeps running records it without merging again. After a
restart, approving again re-runs the merge, which skips the work already done.

## What the owner can see now

The review page's regroup lane shows a notice, "N holds held mid-apply", whenever
any hold is being applied, was interrupted, or ran without being recorded. "Show
them" lists the holds with the action each was running; "Finish" approves one
again, which finishes an interrupted merge or records an unrecorded one, and
"Abandon" moves an interrupted one to the "needs attention" list. Each approve
or reject from the page carries the version of the hold the page showed, so a
hold that changed in the meantime is refused and the page reloads it. The
review count the page polls carries the same number. The report after an
"approve all" now gives holds that were skipped because they are already merged
or mid-merge their own heading, instead of saying they are "still pending".

## What remains

A hold interrupted by a crash accepts only approving it again with the same
action, which finishes the merge, or abandoning it. A reject or a "keep
separate" is refused, because the interrupted merge may already have moved
books, and recording "rejected" over that would not match what happened. While
merging is switched off (production has had it on since 2026-08-17), approving
cannot finish such a hold, so abandoning is the only way out. Abandoning leaves
checking the library to a person: nothing works out what a half-done merge
changed.
