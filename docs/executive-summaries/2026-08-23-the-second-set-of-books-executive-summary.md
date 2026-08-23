<!-- file: docs/executive-summaries/2026-08-23-the-second-set-of-books-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0f4c8a52-6b19-4d73-ae05-91c2f8d3b746 -->
<!-- last-edited: 2026-08-23 -->

# The second set of books

**2026-08-23 — every maintenance job had been writing its progress into two different
ledgers, one of which nobody read any more, and why that one kept resurrecting jobs that
had already finished**

## The short version

When you ran a maintenance job — cleaning up empty folders, repairing missing files,
recomputing totals — the app quietly recorded it **twice**. Once in the system it
actually uses, and once in an older one it stopped reading months ago.

The old record was never shown to you anywhere. But it was still there, still marked
"in progress", and every time the server restarted it got picked up and the job was
started again. Two ledgers, one of them stale, and the stale one driving real work.

That second ledger is now gone. A maintenance job records itself once, and the
identifier the app hands back is one you can actually look up.

## How it happened

This is what a half-finished move looks like from the inside.

The app used to track long-running work in one place. A better system replaced it — one
that knows how to resume a job properly, cancel it, and report progress. Everything moved
over piece by piece.

But the moves were done carefully, one subsystem at a time, and each one left a small
bridge behind: keep writing to the old ledger too, just in case something still reads it.
That was the right call at the time. The problem is that "just in case" is a claim about
the world that nobody re-checked. One by one, the things that read the old ledger were
themselves replaced — the operations screen, the activity bell — and the bridge outlived
every reason it was built for.

The comment in the code still said the old record existed **"so it appears in active
operations / activity bell."** Both halves had stopped being true. The operations screen
had been switched off entirely and returns an error if anything asks for it; the activity
bell had been reading from somewhere else all along. The note explaining why the thing was
necessary was the last surviving evidence that it ever had been.

## What was actually going wrong

Three things, in order of how much they cost you.

**Finished jobs came back.** A job that completed left its old-ledger entry sitting at
"pending", because the part that updated it only ever updated the winner of a
tie — and on a duplicate request there was no winner to update. On the next restart, the
app found that entry, concluded the job had been interrupted, and ran it again. On
2026-08-14 *every* maintenance job of that day was sitting in exactly that state.

**A preview could turn into a real change.** The old ledger had nowhere to record whether
you had asked for a preview or a real run. So if the server restarted mid-job, the resumed
job fell back to the default — which is "make the changes for real". Seven jobs are both
resumable and default to preview, and one of them deletes folders from disk. This had been
patched by saving that choice off to the side; the new system simply stores it with the
job, so there is nothing to lose track of.

**The ID you got back pointed at nothing useful.** Starting a job returned the *old*
ledger's identifier — a number for a record no screen displays. It now returns the real
one.

## What changed for you

- Maintenance jobs are recorded once, not twice. A finished job stays finished across a
  restart.
- If a job is interrupted, it resumes as what you asked for. A preview stays a preview.
- Two operator tools that fetch a job's detailed results — the composer-tag scan and the
  missing-file repair — accept identifiers from **both** eras. Runs from before this
  release keep working; runs after it stop failing.
- One internal measurement was removed. It counted how often the app had to guess at the
  preview-or-not question, and there is no longer anything to guess.

Nothing about how you start or watch a maintenance job changed.

## What this closes, and what it does not

This was the **last** place in the app that created a record in the old system. The
retirement that has been running since early August is finished on that front.

Two things are deliberately left alone. The old ledger still holds about **1,700 entries**
from before any of this, all in a state the restart sweep ignores — they are inert, and
clearing them out is its own task with its own risks. And a small piece of the old bridge
stays for two scheduled jobs that have not been moved across yet; it retires with them.
