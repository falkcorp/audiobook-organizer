<!-- file: docs/executive-summaries/2026-08-23-the-second-set-of-books-executive-summary.md -->
<!-- version: 1.3.0 -->
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
  (See the correction below — this was right about *preview or not*, and wrong about
  *whether five jobs resumed at all*.)
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

## Correction, same day

The line above — "if a job is interrupted, it resumes as what you asked for" — was only
half true when it was written, and the wrong half was not the half we checked.

The preview setting is genuinely safe now; that part holds. But five jobs stopped
resuming **at all**. Clearing out the old ledger also removed the piece of startup code
that had been quietly restarting them, and those five had been depending on it while
declaring, in their own settings, that they should be dropped rather than resumed. The
declaration had never had to be correct, because the old code restarted them regardless.
Remove the old code and the declaration is suddenly the only thing left, and it said the
wrong thing.

The five: the Deluge bulk import, the empty-folder cleanup, the missing-author refetch,
the missing-file repair, and the composer-tag scan. One of them deletes folders from disk,
which is exactly the job you would least like to see quietly change behaviour.

A sixth job, the bulk metadata fetch, kept resuming but forgot what it had already done.
It tracks its finished books against the job's identifier, and the way it was set up to
resume issued a *new* identifier each time — so an interrupted run started the whole
library over, re-fetching tens of thousands of records across the network for no reason.

All six now declare the right thing, and where resuming happens at all it keeps both the
preview setting and the record of what was already finished.

**That is a smaller claim than it sounds, and the smaller version is the honest one.**
Checking whether the fix actually fires turned up a separate, older gap: when the app is
shut down in the ordinary way — a deploy, a restart — a running job is marked finished
in a way that removes it from the list the startup code consults. So the startup code
never sees it, whatever the job's settings say. The settings only get consulted if the
app was killed outright, without a chance to shut down cleanly.

So: the five jobs were declaring the wrong thing and now declare the right thing, which
had to be fixed either way. But an interrupted job still may not come back after a normal
restart, and that is a second problem in a different part of the system, now written down
rather than assumed away. It is not fixed here, because fixing it changes restart
behaviour for every long-running job in the app and deserves its own change and its own
testing.

The same trap twice in one document is the point worth keeping: the first draft of this
correction said the six jobs "now resume the original run in place" — a claim that had
not been checked, inside a section whose entire subject is a claim that outlived its
reason. Two smaller faults in the same release were fixed alongside — a database failure while looking up a job's results was
being reported as "no such job", which would have sent an operator hunting for a bad
identifier instead of a sick database; and a warning that was supposed to fire when a job
could not obtain an identifier was positioned so that it only covered part of what goes
wrong in that case.

**Why this is worth writing down rather than quietly patching.** Nothing failed loudly.
The five jobs did not error; they simply never came back after a restart, and every
comment, plan document and note in the codebase said the opposite was happening. The
reason we found it is that the code was re-read after it shipped, against the question
"is the thing this comment claims actually true?" — and it was not. A justification had
outlived the reason it was written.

## Second update, same day: half of the shutdown gap is now closed

The correction above ends by saying an interrupted job still may not come back after a
normal restart, and that fixing it "deserves its own change and its own testing." Part of
it has since had exactly that, so the paragraph above is no longer the current state.

**What was wrong.** When the app shut down cleanly — a deploy, a service restart — a job
that was running got written down as **cancelled**. That is the same word the app uses
when *you* press the cancel button. So the record could not tell the two apart: a job the
system stopped in the middle of a deploy looked identical to a job a person deliberately
stopped. Anything downstream trying to decide "should this come back?" had nothing to go
on, because the one fact it needed had been thrown away at the moment it was recorded.

**What changed.** A shutdown-interrupted job is now recorded as *interrupted*, and it
carries which of the two kinds it is: one that is safe to pick back up, or one that is
meant to be dropped. A job **you** cancelled still says cancelled, which was the thing
most at risk of being broken by this and is now covered by its own test.

**What has not changed, and we want to be plain about it.** Those jobs still do not come
back on their own. The list the startup code consults contains only jobs that are queued
or running, and an interrupted job is in neither state, so nothing looks at it. Making
the startup code go and find them is the other half, and it is deliberately not in this
release: it changes restart behaviour for every long-running job in the app, and that
needs to be a decision somebody makes on purpose rather than a side effect of a
record-keeping fix.

So the honest description is: **the app now writes down the truth about why a job
stopped, and still does not act on it.** That ordering is on purpose. The record has to
be right before anything can safely be built on top of it — if the resume sweep had been
built first, it would have been reading the same word for "the server restarted" and "the
operator said stop", and the most likely outcome is that it would have restarted work
somebody had deliberately halted.

**Also cleaned up in the same pass.** One unused setting was removed from the maintenance
jobs: a fourth way of spelling "re-run this from scratch" that nothing in the app called,
and that would have been the wrong choice for a maintenance job anyway — it issues a new
identifier, which is the exact thing that made the bulk metadata fetch forget its
progress and start the library over. Removing it means the wrong option is no longer
sitting there to be picked by accident.
