<!-- file: docs/executive-summaries/2026-09-07-the-work-that-kept-running-after-shutdown-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c8e2a17-3b94-4f60-8d21-7a6f0e94b3c5 -->
<!-- last-edited: 2026-09-07 -->

# The work that kept running after the lights went out

**Pull requests:** [#3117](https://github.com/falkcorp/audiobook-organizer/pull/3117),
[#3118](https://github.com/falkcorp/audiobook-organizer/pull/3118),
[#3119](https://github.com/falkcorp/audiobook-organizer/pull/3119) — all merged.

## Executive Summary

- The automated test system flagged one narrow fault: a piece of background work
  could still be writing to the database at the moment the app was shutting down.
  Chasing it properly turned up **a whole family of the same mistake** — around
  thirty places in the app where a job is started and then nobody keeps track of
  it. Three are fixed; the rest are catalogued.
- **The most damaging one had nothing to do with shutdown.** Every time an API key
  is used, the app records the time, the address it was used from, and bumps a
  usage counter. That record was being updated without any coordination between
  simultaneous requests. Measured directly: **200 simultaneous uses of one key
  recorded 37**. Just over **four out of five uses were being lost** — and the
  "last used" time and address were being lost the same way. Anyone who has looked
  at an API key's usage figures to decide whether a key is still needed has been
  reading a number that was far too low.
- Why the app *crashed* rather than complained: the database this app uses does not
  return an error when something writes to it after it has been closed. It stops
  the whole program. So each of these untracked jobs was a potential crash during
  shutdown — the kind that looks like "the server didn't come back cleanly" and
  leaves nothing useful in the log.
- In two places, someone had already noticed the crashes and wrapped the code in a
  catch-all that swallows them. That does not fix anything: the write still fails,
  the data is still lost, and the only thing that changes is that nobody finds out.
  One of those had a comment confidently explaining a *different* problem than the
  one it was actually hiding.
- A restart in the first minute after starting up could hang. A one-time copy of
  the activity history waits sixty seconds before beginning, and that wait could
  not be interrupted — so a restart in that window had to sit through it, and then
  the copy woke up to find the database already closed underneath it.
- Everything here was proven twice: once by making the failure happen on demand,
  and again by putting each fix back the way it was to confirm the new safeguard
  actually catches it. **Every one did.**

## 1. What started it

A routine automated check reported a conflict between two pieces of code touching
the same data at the same time. The narrow fix was obvious. The question worth
asking was whether it was the only one.

It was not. A review of the whole codebase against the project's own coding
standards found roughly thirty places with the same shape: start a job, don't keep
a handle on it, hope it finishes before anything important happens. The standard
this project already follows is explicit — *"jobs started at startup must be waited
for at shutdown"* — and these were simply not doing that.

## 2. The one that was losing data every day

This one deserves separate billing because it is not a shutdown problem. It happens
during ordinary use, under ordinary traffic, right now.

When a request arrives carrying an API key, the app records that the key was used.
Doing that means reading the current count, adding one, and writing it back. If two
requests arrive at once, both read the same number, both add one, and both write the
same result. One of the two uses vanishes.

Worse, this bookkeeping was being done on a *detached* job — one started per request
and then forgotten — so a burst of requests became a burst of simultaneous
read-add-write cycles all fighting over the same record.

The measurement, run directly against the real database:

| | Uses recorded out of 200 |
|---|---|
| Before | **37** |
| After | **200** |

The fix has two parts, because there were two problems. The record is now updated
under a lock, so simultaneous updates queue instead of overwriting each other. And
the update now happens as part of handling the request rather than on a detached
job — which is also what makes shutdown able to wait for it. The cost is a single
small database write on a path that has already read that same key in order to
check it.

## 3. Why these crashed instead of failing quietly

The database at the core of this app makes a deliberate choice: writing to it after
it has been closed is treated as a programming error, and it stops the program
rather than returning a failure the caller might ignore.

That is a reasonable choice. It becomes a problem when jobs are running that nobody
is waiting for, because shutdown then becomes a race — close the database, or let
the job finish, whichever happens first. Most of the time the job wins and nothing
is noticed. Occasionally it does not.

## 4. The catch-alls that were hiding it

Two places had a catch-all wrapped around the failing code. A catch-all here cannot
help. The write that was supposed to happen still does not happen; the data is still
lost; the only change is that the failure becomes invisible.

One of them carried a comment explaining that it was there to handle a situation
that arises in tests. That explanation was wrong — it named a mechanism the code
does not even use. So a real crash, in production, was being silently absorbed by a
guard whose stated reason was a test-only concern that nobody had re-checked.

Both are now either fixed properly or documented as the backstop they actually are,
with the real mechanism named.

## 5. What is fixed, and what is not

**Fixed and shipped (3):**

1. The reporting job that writes an operation's progress is now properly waited for
   at shutdown, with a ten-second limit and a clear warning in the log if it runs
   over.
2. API key usage records are no longer lost (section 2).
3. The one-time activity-history copy can now be interrupted, so a restart shortly
   after startup no longer waits out a full minute and no longer wakes up to a
   closed database.

**Catalogued, not yet fixed (about 30).** Two stand out:

- A background recalculation of the library's summary figures is started and never
  waited for — the same crash-on-shutdown shape as the three above.
- More visibly: the code that manages those summary figures is documented as
  keeping the old numbers available while it recalculates. It does not. It deletes
  them and starts over. The practical effect is that **every dashboard load during a
  library scan takes the slow path, which has been measured at 87 seconds**. This is
  a documentation error that has been quietly setting expectations for how the code
  behaves, and it is the likely explanation for the dashboard feeling unresponsive
  while a scan is running.

Neither is fixed here. Both are written down with the evidence needed to fix them.

## 6. How much of this can be trusted

Each fix was verified the same way, and the second half is the part that matters:

1. Make the failure happen on demand and measure it.
2. Apply the fix and confirm the measurement changes.
3. **Put the code back the way it was** and confirm the new test fails.

Step 3 is what separates a test that checks something from a test that merely
passes. Every safeguard added here was reverted and observed to fail — the API key
counter test, the shutdown-interruption test, and the progress-reporting test alike.

One correction worth recording: an early version of one test passed even with the
protection it was supposed to be checking removed entirely. It was checking
something the surrounding machinery did anyway. It was rebuilt, and the code
comments were rewritten to describe the weaker thing the protection actually buys
rather than the stronger thing it had been credited with.
