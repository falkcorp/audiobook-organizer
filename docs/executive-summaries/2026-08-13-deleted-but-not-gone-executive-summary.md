<!-- file: docs/executive-summaries/2026-08-13-deleted-but-not-gone-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 075d5d8a-d32d-428d-81fe-04c5c5039da6 -->
<!-- last-edited: 2026-08-13 -->

# Executive Summary: Deleted, But Not Gone

**Date:** 2026-08-13 (evening session)
**In one line:** chasing eleven stubborn books uncovered that nearly four thousand
deleted books had never actually stopped being processed — and the eleven turned out to be
the same story.

---

## The night in three findings

You reported one thing: books missing from the library page in the browser, while the
phone app showed them fine. That single report unwound into three separate findings, only
the first of which was what you asked about.

---

## 1. The books the library page refused to show

**What was wrong.** The library keeps groups of "the same book in different formats" and
marks one copy in each group as the one to display. The web page shows only marked copies.
Some groups had no marked copy at all — so every book in them was unreachable from the
browser, while the phone app, which does not filter, showed them normally.

**Why it happened.** The iTunes importer created a fresh group for each new book and then
marked that book as "not the one to display" — even though it was the group's only member.
A group of one, electing nobody.

**What was done.** Three fixes: the importer now marks a lone book as its own display copy;
the organizer no longer creates a *second* display copy when adding to an existing group;
and a repair pass was written for the groups already in that state.

**Result on your library:** the repair elected display copies for **479 groups**, with zero
errors. Groups with no display copy dropped from 490 to 11, and the books trapped in them
from 737 to 12. Your original search now returns the two books you were looking for, at
positions one and two.

---

## 2. The 14 TB disk emergency that was not one

**What was reported — by me, wrongly.** A duplicate folder tree appeared to be consuming
about 14 TB, and I recommended reclaiming it.

**What was actually true.** Nothing to reclaim. The storage system already shares the
underlying blocks between those duplicate files — it had been doing so all along, saving
about 21.8 TB. The tools I used to measure (`du`, and the pool's logical-usage figure)
are both blind to that sharing: they count each shared copy as if it were independent.

**How it was settled.** By actually doing it, at small scale. Fifty files were verified
byte-for-byte, de-duplicated, verified again, and the disk was measured before and after.
The space recovered was **zero** — against a background noise floor larger than the effect.
Genuinely independent copies would have released about 5.4 GB.

**What this means for you.** No disk work is needed. The duplicate folders are a tidiness
problem in how the library is presented, not a capacity problem. The recommendation to
reclaim has been withdrawn and the audit record corrected.

---

## 3. Nearly 4,000 deleted books that were never really deleted

This is the one with the longest reach, and it was found only because eleven groups from
finding #1 refused to be repaired.

**What was wrong.** When you delete a book here, it goes to a trash — recoverable, by
design. There is a function every part of the system calls to ask "give me all the books."
That function exists in two versions internally, and **only one of them remembered to leave
out the trash.** The version that forgot is the one that actually runs in production.

**What that meant in practice.** Every operation that walks the whole library — organizing,
duplicate detection, all the background data-repair jobs, and the count reported to
Audiobookshelf-compatible apps — has been including deleted books. On your library that is
**3,953 books**, almost all of them deleted in a single cleanup on 18 July. They have been
organized, scanned, counted and re-grouped for four weeks after you deleted them.

**Two consequences that were worse than wasted effort:**

- One repair job sets deleted books back to "organized" — effectively **undeleting them**.
- Another finds duplicate books and keeps one, and it could **keep a deleted copy and
  delete the live one** instead.

**What was done.** The rule for "is this book in the trash?" now exists in exactly one
place, and both versions of the function are held to it by a test that runs the same data
through both and fails if they disagree. That test was verified by deliberately
reintroducing the bug and confirming it caught it.

**One risk the fix itself created, and how it was handled.** A deleted book still owns its
file records, because restoring it needs them. A separate cleanup job finds "file records
with no book" and deletes them — and those records were being protected *only* by the very
bug being fixed. Left alone, the first cleanup run after this fix would have destroyed the
file records of all 3,953 recoverable books, quietly making them unrestorable. That job now
protects trashed books explicitly, and refuses to run at all if it cannot confirm which
books are in the trash.

---

## The eleven, resolved

The 11 groups that finding #1 could not repair are now explained, and the explanation is
finding #3.

Every one of those 11 groups contains **nothing but deleted books** — 12 of them in total.
There was no display copy to elect because there was no book left to elect one from. The
repair pass was not failing; it was correctly declining to promote a book out of the trash.

**These groups need no further work.** They are not 12 hidden books — they are 12 deleted
books, correctly not being shown.

*How this was checked, since the count alone would not have been enough:* all 3,870 groups
containing a deleted book were queried individually, and exactly 11 came back with zero
live members. Eleven matching eleven could be luck. But the earlier census had also
recorded that those groups held **12 books**, and the 11 groups found here hold exactly 12
— ten groups with one book and one with two. Two independently measured numbers agreeing
is what makes this an answer rather than a coincidence.

---

## What is left

- The `Unknown Author` folders remain untidy. That is now known to be a presentation
  problem, not a storage one.
- The fix in finding #3 is committed but **not yet running on your library.** Until it
  ships, the ~4,000 deleted books are still being processed.

---

## A note on the other report from today

There is a second write-up from today, *The Books Search Could Not See*, which explains the
same original symptom — your *All Jobs and Classes* search — a different way: some books
were missing from the search catalogue.

**Both are true, and they are separate faults.** That one query happened to land on two
different defects at once, which is also why it stayed confusing for so long. Neither
report is the complete story on its own; read them together.

---

## The thread worth noticing

All three findings share a shape: **a number that looked authoritative but came from an
instrument that could not see what was being asked of it.** `du` cannot see shared disk
blocks. A book count taken from the trash-blind function cannot see that it is counting the
trash. And in the first investigation, counting hidden books using the very search that was
suspected of hiding them produced 6,157 — the true figure was 724.

In each case the correction came from measuring a different way, not from reasoning harder
about the first measurement.
