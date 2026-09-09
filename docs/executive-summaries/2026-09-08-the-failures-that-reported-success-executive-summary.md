<!-- file: docs/executive-summaries/2026-09-08-the-failures-that-reported-success-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7e2b48d1-05fc-4a93-b6e0-3c81df97a624 -->
<!-- last-edited: 2026-09-08 -->

# The failures that reported success

**Pull request:** [#3147](https://github.com/falkcorp/audiobook-organizer/pull/3147)

## Executive Summary

- When the app cannot work out a book's title and author from its file, it asks an
  AI to read the filename and guess. That job runs in the background, dozens of
  times a day, and the Activity page is the only place you can see how it went.
- **Every one of those jobs was reporting success, including the ones that failed
  completely.** A page full of them read "0 of 5 books parsed" next to a green
  "completed" badge and a full green progress bar. The work had not been done, and
  nothing on the screen said so.
- The cause was a narrow one. The job asked itself the wrong question — "did I stop
  early?" instead of "did anything go wrong?" A job that had one batch of work to do,
  failed it, and then had nothing left to try had not stopped early. It had stopped
  right on time, having achieved nothing.
- **When a job did fail, it would not say why.** The names of the books it was working
  on and the error the AI service returned both existed at the moment of failure and
  were thrown away. What survived was a count: "1 batch failure". You could see that
  something broke, and nothing else.
- A third fault sat behind the other two: the counter that tracks failures was skipped
  by the single worst kind of failure — an expired key or an exhausted quota, the ones
  that kill every remaining batch. Those runs reported **zero** failures.
- **The progress bars were filling themselves in.** Any job that had ended drew a
  full bar, whatever it had actually finished, so "0 of 4 completed" was displayed as
  a solid green bar sitting directly above the text contradicting it.
- **Finally, the page was drowning in repetition.** One library scan can queue dozens
  of these background jobs, and every one got its own row on the Activity page and in
  the notification bell. Runs of the same job that happen back-to-back now collapse
  into a single row that says how many it stands for; open it and every run is still
  there.

Verified with the full test suite: 1,047 frontend tests and the affected backend
packages, including a test that forces several batches to fail at the same instant to
prove the new failure reporting is safe under concurrency.

## 1. Failed jobs were marked completed

**What it was.** The AI filename-parsing job decided whether it had failed by asking
whether it had *aborted* — a specific condition meaning it gave up partway through,
either because the AI service returned something permanent like an expired key, or
because three separate batches had failed and it stopped trying. A job with only one
batch of work that fails that batch matches neither: it never gave up partway, because
there was no "partway", and one failure is not three. So it finished, reported success,
and coloured itself green.

**Why it mattered.** These jobs are how books that arrive with unhelpful filenames get
a title and an author. A silent failure means those books keep whatever the filename
suggested, indefinitely, while the operations log insists the work was done. And
because the failures came in runs, the page filled with a wall of green rows that were
all lying in the same way — which is worse than one loud error, because it teaches you
to stop reading the page.

**The fix.** The job now fails if any batch failed or any result could not be saved.
The distinction that had to be preserved is that doing nothing is not automatically a
failure: a library where every candidate already got its title from somewhere else
legitimately parses nothing, and that still reports success. What separates the two is
whether anything actually broke, not whether the count came out at zero.

## 2. The failure message said nothing useful

**What it was.** At the moment a batch failed, the job knew which books were in it and
exactly what the AI service had said. Both were written to the server's own log file
and neither was attached to the operation record, which is what the Activity page
reads. The record kept the count and dropped the content.

**Why it mattered.** A report that something failed, with no indication of what or why,
cannot be acted on. Answering "why did these books never get titles?" meant having
shell access to the server and knowing which log to search — for a failure the app had
already detected and recorded.

**The fix.** The book names and the error are now captured when the failure happens and
carried through to the operation record: the first cause appears on the row itself, and
the full detail — one line per failed batch, naming the books — appears when the row is
opened. The lists are capped, because they are written into the activity history and
that history's size is a live production concern; a capped list now states how many
entries it left out rather than presenting itself as the complete set.

## 3. The worst failures were counted as zero

**What it was.** Two things happen when a batch fails: a counter goes up, and the job
checks whether the error is permanent. The counter came second. So a permanent error —
a revoked key, an exhausted quota, the kind that will kill every remaining batch —
returned from the failure path before the counter was ever touched.

**Why it mattered.** This is the failure most worth knowing about, and it produced the
most reassuring record: a summary line reading "0 batch failures" for a run in which
nothing succeeded. It also undercut the first fix, since a failure count of zero is
indistinguishable from a healthy run that had nothing to do.

**The fix.** The counter is incremented first, before any decision about what kind of
failure it was. The rule for when to give up entirely is deliberately unchanged — that
rule governs how many requests get sent to a paid service, and correcting a counter is
no reason to alter it.

## 4. Progress bars that filled themselves in

**What it was.** Any operation that had ended drew a full progress bar, coloured by
outcome. The number beside it was calculated from the real figures. On a job that ended
having processed none of its work, the two disagreed on the same line: "0 / 4 (0.00%)"
under a solid bar.

**Why it mattered.** The bar is the part you see first and the part you believe. It
treated "this has ended" and "this has finished its work" as the same statement. They
are the same for most jobs, which is why this went unnoticed — and it is exactly the
jobs that failed, the ones worth spotting, where they come apart.

**The fix.** A finished job with a known amount of work now draws the fraction it
actually completed. Jobs that genuinely have no total to measure against — some are
counting something that cannot be counted in advance — still show a full bar, which is
the right answer when there is no fraction to draw.

## 5. Dozens of identical rows

**What it was.** A single library scan can queue dozens of these background jobs, and
the Activity page and the notification bell listed every one of them as a separate row.
The page had the ability to nest related operations under a single parent row, complete
with indentation and a collapse control, but nothing had ever created such a parent, so
the feature sat unused and the two buttons that operated it did nothing.

**Why it mattered.** A log you have to scroll past is a log you stop reading. The
repetition was also actively hiding things: a genuinely different operation could be
sitting between two dozen identical rows and be missed entirely.

**The fix.** Runs of the same operation that happen close together — less than two
minutes apart, spanning no more than half an hour — are now shown as one row that says
how many runs it stands for. Opening it shows every one. Two design choices are worth
noting. First, nothing is written down: the grouping is worked out fresh each time the
list is displayed, so it cannot go stale, cannot be split across a page boundary, and
leaves nothing behind to clean up. Second, a group only ever contains runs that ended
the same way, so a failure can never be absorbed into a group of successes and hidden
behind a green badge — which is the whole point of doing this after the first four
fixes rather than before them.

There is a third choice worth naming, because getting it wrong would have undone the
first fix. Every section on the page carries a count in its heading — "Failed (12)".
That count is now taken from the operations themselves rather than from the rows on
screen, so folding twelve failures into one row still reports twelve, and the number
does not change when you open the group. A count that moved when you clicked a
disclosure triangle would have been a new way of hiding the same eleven failures.
