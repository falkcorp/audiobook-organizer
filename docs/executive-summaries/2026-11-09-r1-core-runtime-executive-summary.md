<!-- file: docs/executive-summaries/2026-11-09-r1-core-runtime-executive-summary.md -->
<!-- version: 1.0.5 -->
<!-- guid: ce0c598d-0803-4350-976d-12990013b6bc -->
<!-- last-edited: 2026-11-09 -->

# R1: the app now runs on a new foundation, and your library is untouched

**Date:** 9 November 2026
**Pull request:** #4533 (this summary); the work it describes is listed under "Tracked work" below.
**Plan:** [`docs/plans/platform-core/`](../plans/platform-core/implementation-plan.md)

## Executive Summary

- The server now starts, stops, logs and answers web requests through a new
  shared foundation (called "core") instead of code written only for this
  app. The audiobook features themselves sit on top of it and work as they did.
  You should not see a difference when you use the app.
- Your library data was not rewritten. The database keeps the same layout it
  had before, so going back to the previous version is a matter of swapping
  the program, not converting data. On its first start after the upgrade the
  new version also takes a safety copy of the database before it changes
  anything, and deletes that copy on its own after seven days.
- Restoring a backup is safer. A restore from Settings is now unpacked and
  checked first, then applied at the next restart, with the old database set
  aside instead of deleted. A restore that cannot work is refused up front and
  the page says why. The server's two reset buttons are gone from the web
  server; a reset now runs only from the command line with the service
  stopped.
- A new command-line restore works with the service stopped, for the days
  when the app will not start at all. The server refuses to start while one
  of these commands is running, and the commands refuse to run while the
  server is up, so the two can no longer collide.
- Starting and stopping got more careful. The server tells the system when it
  is really ready, asks for more time during a slow start instead of being
  killed, and gets a full minute to shut down cleanly. Deploying now waits
  for the service itself to report ready and checks the restart really
  happened, and a guarded rollback refuses when the data would no longer match
  the old program. Both are deployed and healthy.
- The web page knows which version of the program it came from. If you leave a
  tab open across an upgrade, it now shows a banner asking you to reload,
  rather than quietly talking to a server it no longer matches.
- Behind the scenes, building and testing got stricter: every change is now
  checked in both the app and the core, the Docker image is built on every
  change, and web builds go through one entry point. These are for the people
  who maintain the project, not for readers of the library.
- What you have to do: nothing beyond reading the R1 release note. Nothing
  here asks for a setting change, a data conversion or a manual step.

## What changed for the library

### The server runs on core

**What it was.** The app had its own way of starting up, shutting down,
logging, handling web requests and keeping track of its parts. Those pieces
were tangled into the audiobook code, which made it hard to change one without
risking another.

**Why it mattered.** The long-term plan is to split the reusable foundation
from the audiobook features. Every later step depends on the foundation owning
those basics, and a tangled start-up is how a server ends up hanging at
shutdown or losing log lines.

**The fix.** The foundation now owns start-up, shutdown, logging with
sensitive values hidden, the request pipeline, the health and metrics
endpoints, and the list of the app's parts. The audiobook features plug into
it. The old web routes are mounted behind its router.

### Storage: the new engine is in charge, the data is the same

**What it was.** The program opened and closed the database in several
places, each with its own rules.

**Why it mattered.** A crash in the middle of an upgrade, a restore or a
reset is the moment a library is most at risk. Scattered rules make it
likelier that one of those paths leaves a half-finished database.

**The fix.** One engine opens the database. The audiobook code can only reach
its own records through declared claims, and an emergency setting can relax
that without a new build. A safety copy is taken before the first change, a
background job tidies up old safety copies, and a command removes them on
request. A crash-testing kit breaks the start-up at every step to check that
the next start recovers.

### Restores and resets

**What it was.** A restore unpacked straight over the live database, and the
web server had two reset buttons that did the same kind of thing while the
library was in use.

**Why it mattered.** A crash or a bad backup partway through could leave a
database that matched neither the old library nor the backup.

**The fix.** Restores and resets are lined up first and applied by a single
journaled swap before the database opens, so an interruption is finished or
undone on the next start. The old database is moved aside, not deleted. The
details are in the 9 November summary on offline restore and reset.

### Starting, stopping, deploying and rolling back

**What it was.** The service could be told it was ready before it was, killed
during a slow start, or given too little time to shut down. A rollback was a
plain program swap with no check that the data still fit the old program.

**Why it mattered.** Each of those is a way to lose recent changes or to
leave the server looking up while it is not usable.

**The fix.** Ready, stopping and "need more time" signals come from the
foundation. The shutdown budget is 25 seconds inside a 60-second allowance.
Deploy waits for the service to report ready and verifies the restart took
effect, and rollback refuses when the data is newer than the old program, with
a restore-based path for that case; both are deployed and healthy. A
continuous-test job now exercises a real service on a test machine on every
change.

### The web page

**What it was.** The page did not know which program build it belonged to,
and a major routing library was a version behind.

**Why it mattered.** After an upgrade, a stale tab could show wrong data or
fail in confusing ways. An old routing library gets harder to upgrade each
month.

**The fix.** The build identity is stamped into the program and the page, and a
banner appears on a mismatch. The routing library moved to its current major
version without changing how pages behave, and the shared web client now lives
in its own package.

### How the project is built and checked

**What it was.** One set of checks covered everything, the Docker image was
only built at release time, and web builds could differ from place to place.

**Why it mattered.** A problem found at release time is a late and expensive
way to find it, and it is the moment a bad build is most likely to reach the
server.

**The fix.** Core and the app are checked separately, the Docker image is built
on every change, the web build has one entry point, a switch lets the owner
freeze releases, and a nightly job reports how close the split is.

## Tracked work

All 37 R1 rows shipped and are deployed and healthy: 36 merged as the pull
requests below, and this summary is the 37th.

R1-01 #4397; R1-02 #4407; R1-03 #4417; R1-04 #4438; R1-05 #4442; R1-06 #4498;
R1-07 #4410; R1-08 #4444; R1-09 #4437; R1-10 #4430; R1-11 #4405; R1-12 #4411;
R1-13 #4450; R1-14 #4452; R1-15 #4467; R1-16 #4470; R1-17 #4487; R1-18 #4485;
R1-19 #4488; R1-20 #4453; R1-21 #4480; R1-22 #4406; R1-23 #4504; R1-24 #4507;
R1-25 #4522; R1-26 #4528; R1-27 #4531; R1-28 #4532;
R1-29 #4443; R1-30 #4495; R1-31 this summary; R1-32 #4436; R1-33 #4474;
R1-34 #4477; R1-35 #4394; R1-36 #4445; R1-37 #4458.
