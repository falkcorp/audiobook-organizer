<!-- file: docs/executive-summaries/2026-10-16-op-catalog-ledger-gate-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 01ae79f9-c6b1-4232-a0d5-131bc080a672 -->
<!-- last-edited: 2026-10-16 -->

# One list of background jobs, and a startup check that none went missing

**Pull request:** #4044 (task 05-PR7).

## Executive Summary

- **One list of background jobs.** The server's background jobs (scans,
  metadata fetches, cleanup tasks) were pulled in from several places. A job
  could be fully written and still never be included in the program, and
  nothing would notice. The metadata-fetch jobs were missing from production
  for weeks that way. There is now exactly one list, and a test fails if a
  job package exists but is not on it.
- **The server checks its job list when it starts.** The project keeps a
  ledger: a file of every job name that has ever existed. On startup the
  server now checks that every name in the ledger is either a job it knows,
  an old name that points to a current job, or a name deliberately retired
  with a written reason. If a name matches none of these, the server refuses
  to start instead of running with a job silently gone.
- **Optional services do not trip the check.** Some jobs only switch on when
  an optional service is configured, such as the Deluge download client. The
  check counts those jobs as known even when the service is off, so a server
  without Deluge still starts.
- **A stronger code checker.** The project's job checker (oplint) used to
  match on file names, crashed on its own command line, and ran nowhere
  automatically. It now understands the code's types, and it is part of the
  standard pre-merge checks. It catches a job written but left out of its
  package's list, a job writing to the database without going through the
  shared writer, and any use of the delete capability, which it sends to the
  owner for review.
- **No change to how jobs run today.** Every existing job registers and runs
  exactly as before. The checker reports nothing on the current code. When
  pointed at the older job style for information, it lists 13 places that
  write to the database directly; these are noted, not enforced.

## The startup check

**What it was.** The ledger of job names was only checked by a test, so the
check ran on developer machines and in CI, never on the server itself.

**Why it mattered.** A job that was renamed without a forwarding entry, or
left out of the build, would vanish quietly. Scheduled runs and saved
references to it would stop working with nothing on screen to say why.

**The fix.** The server reads the ledger, which is now built into the
program, after all optional services have started. It refuses to start if
any name in the ledger resolves to nothing, and it lists every problem at
once rather than only the first.

## One job list

**What it was.** A hand-kept file pulled in five job packages, and a separate
test checked that file.

**Why it mattered.** Adding a job package meant remembering to edit that
file. Forgetting is what kept the metadata-fetch jobs out of production.

**The fix.** A single catalog package now holds the list, and a test fails
if any package that defines jobs is not on it.

## The code checker

**What it was.** The old checker only looked at which packages imported which.
It reported hundreds of false alarms, and nothing ran it.

**Why it mattered.** The rules it was meant to enforce for the new job style,
such as every database write going through the shared writer, had no guard.

**The fix.** The checker was rewritten to understand the code's types. It
reports nothing on today's code and now runs as part of the standard
pre-merge checks.
