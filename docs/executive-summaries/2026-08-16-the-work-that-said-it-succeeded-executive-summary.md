<!-- file: docs/executive-summaries/2026-08-16-the-work-that-said-it-succeeded-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 24b33837-c4c8-4096-822a-83fee40f0194 -->
<!-- last-edited: 2026-08-16 -->

# The work that said it succeeded

**2026-08-16 — six places the program told you it had done something it hadn't, and the rule that now stops it**

## The common thread

Every fix below is the same mistake wearing a different coat: **the program did a job,
the job failed, and the program reported success anyway.**

That is worse than an outright error. An error tells you to look. A false success tells
you to stop looking — and everything downstream then treats the work as done.

## 1. "Applied" when nothing had moved

When you accept new metadata for a book, two things happen: the details are saved, and
the files are renamed and tidied on disk to match.

The renaming step had no way to report a problem. If it failed — a permissions issue, a
disconnected drive, a file that had gone missing — it made a note in the log and returned
as though all was well. The part above it then reported **"applied: true"** and moved on.

So the screen said the change had been applied, the database agreed, and on disk nothing
had happened. There was even a counter meant to track exactly this kind of failure; it
could never be incremented, because nothing ever told it there had been one.

The renaming step can now report failure, and the result distinguishes the two halves
honestly: the details **were** saved (that part is real and permanent), but the file work
did not fully land.

## 2. Organising a book that had no files left

When the program organises a book, it creates the destination folder first, then copies
the files in. If a file has vanished from where it was expected, it is skipped.

Skip every file, and you are left with a freshly created, completely empty folder — which
the program handed back as the successful result, with no complaint. This did not require
anything to be marked as broken beforehand; the files simply had to be gone.

Three different parts of the program call this. **One** of them checked whether anything
had actually been copied. The other two believed the answer:

- one created a **new book record pointing at the empty folder**;
- the other **moved the book's official location** to it.

In both cases the library ends up confidently pointing at nothing.

The check now lives inside the organising step itself, so all three callers get it — and
the error names the book and says why, rather than leaving you to work it out.

That last point is the real lesson, and it came up three separate times last night: **a
safety check that lives in one caller is a check every other caller silently lacks.**
Whoever writes the fourth caller cannot be expected to rediscover it.

## 3. Activity messages that stopped mid-sentence

The activity list was showing entries like *"cover art saved to"* and *"ISBN enrichment
succeeded for"* — sentences that just stopped. Occasionally a neighbouring row showed raw
internal text with a stray quotation mark in it.

Those looked like two separate faults. They were one.

The program reads its own log lines to build these summaries, and it found the end of the
message by searching for the **last quotation mark in the entire line**. When a line
ended with the message, that happened to be right. When anything followed — the filename,
the book, the value that had been found — the message swallowed it, or the quotation mark
ended up displayed as text.

Lines are now read properly, once, in order. The details are put back into the sentence —
*"cover art saved to /library/Asimov/cover.jpg"* — **and** stored alongside it, so they
can be searched later rather than only looked at.

## 4. Maintenance jobs that never stopped saying "pending"

You reported that every maintenance job from 14 August was still showing as **pending**,
including two that had definitely finished and written full summaries of what they did.
It misled you twice in one day.

The program keeps two records of a job: an older one, which is what the operations screen
displays, and a newer one, which is what actually runs. The newer record was updated
throughout. The older one was written once, when the job was created, **and never touched
again.** Its status was permanently frozen at whatever it started as.

A handful of jobs happened to update it by hand. Everything started on a schedule did
not — and never would have, because each new job would have had to remember to do it.

The update now happens in the single place every job passes through when it finishes, so
it cannot be forgotten. Jobs that keep a progress count keep it; jobs that don't are
recorded as done rather than as "finished, 0%", which would have been a different lie.

**This one is confirmed working on your live system.** After the update was deployed, two
scheduled jobs ran and both correctly reported themselves finished, while the older
entries from before the fix are still sitting at pending — visible side by side.

Those older entries stay stuck. Repairing them means rewriting past records, which is a
deliberate operation to run while watching it, not something to slip in unattended
overnight.

## 5. Three iTunes jobs that did nothing and called it done

**iTunes Sync**, **Path Reconcile** and **Path Repair** have not done any work since
mid-July. Running any of them produced a green "completed" row within seconds.

The cause is unusually clean. Each of those three jobs exists **twice** in the program:
a real working version, and an empty placeholder left behind by a half-finished
reorganisation. The placeholders load first, and whichever loads first wins the name.
So the placeholder took the job, did nothing, and reported success — while the working
version sat unreachable a few files away.

The program noticed. On every startup it wrote three warnings saying the name was
already taken — 378 of them in the last month. Nothing read those warnings, because the
part of the startup that collects them **logged them and moved on**. That has also been
changed: a job that fails to load now stops the server from starting at all, rather than
producing a server that looks healthy and is quietly missing a feature.

A fourth placeholder, **Position Sync**, had no working version to displace. It now
reports an honest failure instead of a false success. The real code it should be calling
exists and has never once been run; switching that on means writing bookmarks and play
counts across two systems, so it is left alone deliberately.

## 6. Library scans that finished the hard part and were killed anyway

A scan walks every file, then hands the filenames to the AI to tidy up the titles. The
walk finished — 3,917 files. The AI stage then ran for five minutes without saying
anything, and the supervisor that watches for frozen jobs concluded it had frozen and
killed it. **The completed walk was thrown away.**

Two things were wrong. The AI stage never reported progress, so a long stage was
indistinguishable from a dead one; it now reports on each batch. And every single AI
request was failing for a reason that could not improve — **the OpenAI account has run
out of credits** — yet the program kept retrying it, batch after batch, for around
twenty-five minutes of guaranteed-useless work. It now stops asking after the first
unrecoverable answer and says why.

**This one needs you.** Until credits are added, scans will complete, but those books
keep the titles derived from their filenames rather than AI-cleaned ones.

## The rule that now stops this whole category

Every one of these jobs was supposed to report its progress. Nothing ever checked, so
"didn't report" and "froze solid" looked identical from outside — and the natural
response to both, giving the job more time, permanently hides the first one. That is how
a broken progress connection survived three months and got worked around three separate
times.

Reporting progress is now a **requirement that is enforced, not a convention**. A job
cannot be added to the system at all unless it states how it reports: automatically,
by hand, or not at all. The last option has to say how long it may run in silence, and
is listed on every startup — so the set of unwatched jobs is visible instead of quietly
growing. All 146 existing jobs were reviewed and labelled.

This does not make every job report *well*. The nightly maintenance window was labelled
honestly and was still being killed, because it waits on other jobs and a single one can
outlast the five-minute limit; that was fixed separately. The rule closes the "never
wired up" hole, not the "reports too rarely" one.

## The thing that was found by accident, and matters most

While confirming the deployment had worked, the startup log turned out to be reporting
that **three scheduled jobs are switched on but can never actually run.** They have no
schedule, and the nightly maintenance window doesn't reach them. They have been in this
state for some time — this is not new, and not caused by any of the above.

One of them is the metadata upgrade. If you believed that had been running quietly each
night, it has not been.

Another is the one that organises the library. That matters right now for a specific
reason: a recent fix corrected a long-standing disagreement about *where books should
live*, and the correction only takes effect the next time books are organised. It is
therefore still waiting — and it will not start on its own.

**Nothing was changed here.** Switching that job on would begin moving files across the
entire library, unsupervised, overnight. That is a decision to make deliberately, with
someone watching.

## Also: the test suite was doing everything twice

Not a fault in the program itself, but the same species of problem.

The standard test run executed the **entire** suite twice — once checking for one class of
issue, once measuring coverage — when both can be done in a single pass. Measured properly:
16 minutes became 8, and the coverage figure came out **identical to the digit**.

The second run also threw away everything it printed. If a failure had happened only in
that run, it would have failed with nothing to read — a silent failure, in the tooling
built to catch silent failures.

## How we know

Each fix has a test that drives the **real** path, not just the repaired piece in
isolation. That distinction mattered: for the activity messages, deliberately re-breaking
the fixed line left every test still passing, because the tests were calling the repaired
function directly and never checking that the program used it. A test that goes through
the actual machinery now catches it.

The maintenance-job fix is verified on the live system rather than only in tests.

## What has not been established

Whether anyone actually triggered those three iTunes jobs while they were broken is
**not known**. That the jobs were unreachable is certain; how often they were asked to
run is recorded in the database rather than the system log, and was not measured.

The library rescan has still not completed, so the database still points at the old
locations for the files recovered earlier. It now fails for a reason that is understood
and fixed rather than a mystery, and will be re-run once that fix is deployed.

The library-wide file reorganisation has not run, and cannot until the scheduling decision
above is made. Until then, the disagreement about where books live is corrected in
principle but not yet on disk.
