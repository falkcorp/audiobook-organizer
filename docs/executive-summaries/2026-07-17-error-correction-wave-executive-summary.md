<!-- file: docs/executive-summaries/2026-07-17-error-correction-wave-executive-summary.md -->
<!-- version: 2.0.0 -->
<!-- guid: 9d47e2a8-1f3b-4c66-8e02-b5a19c384f7d -->
<!-- last-edited: 2026-07-18 -->

# July 17–18, 2026 — Quality sweep: 24 fixes, and the duplicate backlog cut by ~85%

## What happened, in plain language

We ran a full health inspection of the app — five independent reviews looking at
duplicate detection, file handling, background jobs, logging, and operations —
and then fixed the problems they found. Over two days that came to **24 separate
changes**, each tested before shipping, followed by a real cleanup of the
long-standing duplicate backlog.

## Why it matters to you

- **Your files are safer.** Several rare-but-real ways the app could overwrite or
  lose an audio file during renaming and organizing were closed: it now refuses
  to overwrite an existing file, undoes half-finished renames instead of
  stranding files, and recovers files left in a temporary state by an earlier
  crash.
- **Merging duplicates no longer loses track of a book.** The older "merge these
  two" action used to delete the losing copy outright — which orphaned its
  ISBN/ASIN lookups and stranded its iTunes tracks. Merging now moves those
  references to the surviving copy and keeps a recoverable record instead of a
  hard delete.
- **Your decisions stick.** When you dismissed a suggested duplicate, a later
  library scan could quietly bring it back — and grouped books could end up with
  two "main" copies. Both are fixed.
- **Background jobs stopped lying.** Some scheduled jobs reported "success" every
  few minutes while doing nothing (unfinished stubs) — they no longer run on a
  schedule and say so honestly if triggered. A job that hangs is now noticed even
  if it hangs immediately, a stuck job can no longer silently block all future
  jobs of its kind, and long jobs (re-encoding files, filling in metadata) now
  report real progress instead of going dark for hours.
- **The duplicate backlog was actually cleaned up.** This was the headline. Most
  of the ~9,000 "possible duplicate" suggestions were never real duplicates —
  they were junk pairs created by books whose titles had been wrongly taken from
  a chapter name. We built a three-step repair (fix the leaked titles, restore
  the missing evidence scores, then let the triage tool recognize and clear the
  junk class), proved it on a disposable full copy of the real library with zero
  risk, and — with explicit sign-off — deployed and ran it on the live server.

## Numbers

- **24 fixes** shipped across two waves: the review-day sweep (#1972–#1986) plus a
  coordinated follow-up wave (#2001–#2010) that closed the remaining findings.
  All deployed to the live server.
- **Duplicate backlog:** the "possible duplicate" list drops from **9,074 to about
  1,300** — roughly an **85% cut** — by clearing **~7,900 junk suggestions**.
  Those are *dismissed* (reversible), not deleted, and the real duplicate
  suggestions worth your review are left in place. Validated on a full copy of the
  library with **zero errors**, and the live-server dry run matched that copy
  exactly before the cleanup ran.

*(The live cleanup ran under explicit human sign-off after the copy-of-production
dry run matched. Final live figures are recorded in `docs/dedup/STATUS.md`.)*
