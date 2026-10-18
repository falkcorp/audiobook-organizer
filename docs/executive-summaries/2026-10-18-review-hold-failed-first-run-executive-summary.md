<!-- file: docs/executive-summaries/2026-10-18-review-hold-failed-first-run-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9c6a21-7d84-4b0e-a152-c8e47d90b6f3 -->
<!-- last-edited: 2026-10-18 -->

# A review hold whose first merge failed part way is no longer handed back as if nothing happened

Branch `fix/review-hold-failed-first-run-if-wrote`, PR not yet merged. This
follows the owner's decision of 2026-10-18 on the open question left by the
earlier review-apply work. There is no separate spec.

## Executive Summary

- **A half-finished first try now stays held.** When approving a review hold
  failed after it had already changed part of the library, the hold used to
  return to the review queue as if nothing had happened. Now it stays held, out
  of the queue, and refuses every other decision until someone finishes it or
  abandons it.
- **A clean failure still returns to the queue.** When the approval failed
  before it changed anything (a refused precondition, or the protection that
  keeps books in the iTunes library untouched), the hold goes back to where it
  was, as before.
- **When in doubt, the hold is kept.** Each kind of approval now states
  whether its failure changed nothing. Any failure that does not say so, and
  any crash in the middle, is treated as having changed something, so a
  mistake in that classification can only keep a hold longer, never hand a
  half-changed library back.
- **The way out is the same as for a failed second try.** Approving again
  finishes the work (it skips what was already done), and "Abandon" moves the
  hold to the "needs attention" list for someone to check the library.
  Approving many holds at once reports a held one as skipped, with the reason.
- **Verification.** Automated tests with made-up data cover a version-group
  approval that fails after linking one book (held, then finished by approving
  again), one that fails before touching anything (returned to the queue), and
  the bulk report. This has not been run against the production library.

## A first try that failed after writing came back as undecided

**What it was.** Approving a hold runs one of three actions: combine books into
one, link editions as versions of each other, or merge debris into a book that
exists elsewhere. None of them is all-or-nothing. Linking editions writes the
link first and the "which edition is primary" choice second, and a merge can
stop after moving some files. When such a first try failed, the hold went back
to the review queue exactly as it had been.

**Why it mattered.** The hold then looked untouched while the library was half
changed. A reviewer could reject it, or approve a different action, and that
decision would be recorded on top of changes nobody had accounted for. A second
try of an interrupted approval was already protected this way; the first try
was not.

**The fix.** Each approval now reports whether it changed anything before it
failed. Only a failure that changed nothing returns the hold to the queue;
every other failure holds it, with a clear message and the two ways out
described above.
