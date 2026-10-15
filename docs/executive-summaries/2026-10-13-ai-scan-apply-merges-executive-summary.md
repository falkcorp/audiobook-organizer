<!-- file: docs/executive-summaries/2026-10-13-ai-scan-apply-merges-executive-summary.md -->
<!-- version: 1.6.0 -->
<!-- guid: 3c8e5b71-9a24-4d06-b7f3-6e1d2a94c850 -->
<!-- last-edited: 2026-10-15 -->

# The AI scan's "Apply selected" button now merges authors, and merges can be undone

PR #4013 (03-PR8, branch `task/03-pr8`), not yet merged.
There is no separate spec.

## Executive Summary

- **Apply selected now does what it says.** The AI author scan finds names that
  are probably the same person ("J. R. R. Tolkien" and "Tolkien, J.R.R."). You
  tick the suggestions you accept and press Apply selected. That button used to
  mark the suggestions done without merging anything. Now it merges each one,
  and marks one done only when its merge fully worked. Anything that failed
  stays on the list with the reason.
- **Locked authors are respected.** A merge, from the AI scan or the Authors
  view, now leaves a book with a locked author alone and says so. Before, a
  merge could quietly move a locked book to another author.
- **Each suggestion says what it will do.** A merge row reads "keep X, merge Y,
  Z" with names. Only merge suggestions can be applied here. Renames, splits and
  the like are shown read-only, with a link to the Authors view.
- **Author merges can be undone, wherever they happen.** A merge used to delete
  the merged author for good. Now every way the library merges authors records
  each step first: the Authors view, the AI scan's Apply selected, the older AI
  review's apply, and the four background clean-up jobs that merge duplicate
  authors. Undoing the operation brings the author, its pen names and its
  books' credits back, including authors with odd names such as "Epigraph".
  The undo still refuses, and reports, the cases where putting things back would
  overwrite something that changed since. Merges made before this change cannot
  be undone.
- **A narrator is no longer merged into the author.** The AI has two ways of
  flagging a name as a narrator. One of them was lost on the way to the review
  list, so the narrator's books were merged into the author. The flag is now
  kept. A narrator the apply can identify is reclassified as a narrator. One it
  cannot identify refuses the whole suggestion.
- **Scans no longer get stuck or get replaced mid-apply.** Two quick clicks can
  no longer start two paid scans. A scan being applied cannot be replaced by a
  newer one part way through. A scan that failed to start no longer blocks every
  later one, and a brief storage hiccup is retried before a scan is given up.
- **Verification.** Automated tests with made-up data cover each item above,
  including a merge followed by its undo, a second undo that finishes a partial
  one, and two scans started at the same moment. Each code fix made in review
  was also checked by breaking it and confirming its test then failed. This has
  not yet been run against the production library.

## A button that recorded work it never did

**What it was.** Apply selected updated a flag on each selected suggestion and
stopped there. No author was merged, relinked or deleted.

**Why it mattered.** The scan's list is the only record of what is left to
review. With the flag set, the suggestions disappeared from the to-do list
whether or not anything had happened, so real duplicates were hidden from the
people who would have fixed them.

**The fix.** Applying now queues a background job. It performs each merge with
the same code the Authors view uses, and sets the flag only for suggestions
whose merge succeeded. The job previews unless told otherwise, like the
library's other jobs that change data.

## Merges that ignored locked authors

**What it was.** A book can have its author locked so automatic processes leave
it alone. The author merge never checked that lock.

**Why it mattered.** Merging many suggestions with one click could move locked
books to a different author, undoing a decision the owner had made on purpose.
The change is silent: the book simply shows another name afterwards.

**The fix.** The merge now checks the lock for every book it would move. Locked
books stay with their author, that author is kept while they do, and the report
lists each one. An owner who really wants the old behaviour can ask for it
explicitly.

## Merges that could not be undone

**What it was.** Merging author B into author A moved B's books to A and deleted
B. Nothing recorded how to reverse that. B's aliases (alternate names, such as
pen names, that the library uses to recognise an author) stayed attached to the
deleted B, reachable only through the redirect the merge left from B to A.

**Why it mattered.** A wrong merge, which one click on a bulk Apply can produce
many of, could only be repaired by hand, book by book, and B's pen names could
not be handed back to a re-created B.

**The fix.** Every way the library merges authors now moves the merged
author's aliases to the surviving author, and writes every step to the undo
history before it happens (a book whose history entry cannot be written is not
moved, and a merge with no operation to record it under does not run). That
covers the Authors view, the AI scan's Apply selected, the older AI review's
apply, and the background clean-up jobs that merge duplicate names, names with
a stray "&", numbered names, and duplicate rows found by the id repair. The
clean-up jobs used to keep only a note of what moved, which the undo could not
act on.
Undoing the operation recreates the author under its old identity, moves its
aliases back, and puts each book's credits and main author back as they were.
Authors whose names the library would refuse to create today, such as
"Epigraph" or "read by narrator", come back too. It does not overwrite later
work: a book edited since the merge is left alone and reported, and the author
is not restored if a different author has taken its name since. A pen name that
was deleted since is put back; one that now belongs to another author is
reported instead.

The clean-up jobs' other changes are undoable too: the job that strips a stray
"&" renames a name in place when there is nothing to merge it into, and the id
repair points a book whose main author no longer exists at a real one. Undo
puts the old name back, and puts the old (missing) author reference back
together with the author name the book displayed before, so it no longer keeps
showing the replacement's name. The rename is not put back if another author
has taken that name since; that is reported instead. An author credit on a book
record that no longer exists is put back too.

What is still not undone: the older AI review's apply renaming the author it
keeps, its "rename" action, and its "alias" action adding the merged name to
the kept author as a pen name (after an undo the kept author keeps that name
and that pen name); the id repair creating the shared "Unknown Author" entry
when it needs one; and changes made before this release.

One small behaviour is now the same everywhere: when a merge makes the kept
author a book's main author, the book's author list names it too. Only the
clean-up jobs did that before. The list is changed only when the main author
really changes, in the same save: a book someone moved to a different author
while the merge ran keeps its list as it was, and a save that fails changes
neither. A preview of the undo shows the same refusals before anything
runs. Names that differ only in capitals or spacing, which are exactly what a
duplicate merge joins, are handled. An undo that stopped part way finishes when
run again, and the restored author appears without restarting the server.

## The narrator flag that was dropped

**What it was.** The AI can answer "this name is a narrator, not an author". The
nightly and batch scans ask for that answer in an older format, and the step
that turned the AI's answers into review suggestions threw that format away.

**Why it mattered.** Without the flag, a narrator looked like a spelling
variant of the author, so applying the suggestion merged the narrator's books
into the author. Each one would then need fixing by hand.

**The fix.** The flag is now kept in both formats. When the AI names the
narrator by its library identity, the apply reclassifies that author as a
narrator instead of merging it, and that step can be undone too. When it names
the narrator only by position in a list ("the second name"), which cannot be
mapped back reliably, the suggestion is refused rather than guessed at. A
suggestion both scan passes agreed on records which pass its flag came from, so
it is handled the same way. For older scans, the apply re-reads the AI's
original answer and refuses a suggestion whose flag was lost.

## The scan replaced in the middle of an apply

**What it was.** Only the newest scan's suggestions are shown for review. A new
scan, such as the nightly one, could arrive while an older scan's suggestions
were being applied.

**Why it mattered.** The older scan was then hidden part way through. The
suggestions it had already merged were never marked done, so the merges looked
undone and the list could not be trusted.

**The fix.** A scan is now held while its suggestions are applied, and a newer
scan cannot replace it until the apply ends. The hold is released when the job
stops, and it can never last more than three hours. A hold left by a job that
crashed, or by the server restarting, is cleared at once rather than blocking
new scans for those three hours.

## The failed scan that blocked every later one

**What it was.** A scan that failed while starting was left marked "running".
The server allows one scan at a time, so it refused every later scan until
someone found and cancelled the stuck one by hand.

**Why it mattered.** The nightly scan and any manual scan silently stopped
running. A scan can also have paid AI requests in flight, which kept running
for nothing.

**The fix.** A scan that cannot be started is now ended: it is marked failed
and its paid requests are cancelled. Ending it is a last resort, because it
throws those paid requests away. So a brief storage error is first retried a
few times over about twenty seconds, and the start is arranged so that a retry
can never launch the same work twice.

## Two paid scans from a double click

**What it was.** The page disabled Start while a scan was running, but only the
page knew. The server accepted every request, and a nightly job also records
scans.

**Why it mattered.** Each AI scan of the whole library is a paid call, and a
double click or a second browser tab could start two.

**The fix.** The server now refuses a second start while a scan is unfinished.
The check and the creation happen as one step, so two simultaneous requests
cannot both pass. The nightly job is never refused, because the results it
records have already been paid for.
