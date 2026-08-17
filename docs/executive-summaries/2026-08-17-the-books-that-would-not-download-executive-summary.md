<!-- file: docs/executive-summaries/2026-08-17-the-books-that-would-not-download-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e89d312-3d8e-489d-bf2b-1fb681daee1e -->
<!-- last-edited: 2026-08-17 -->

# The books that would not download

**2026-08-17 — why four in ten file entries point at nothing, the comma that created a
second person, and 4,975 authors with no books**

## The short version

Two things you reported turned out to be unrelated, and only one of them is fixed.

The **author and narrator pages that showed no books** are fixed and were deployed
yesterday. The **downloads that fail saying the file can't be found** are now understood
precisely — and the number is much worse than expected — but repairing them is a decision
for you, not something to do unattended overnight. This document explains why.

## 1. Four in ten file entries point at nothing

When you try to download a book and it says the file cannot be found, it is telling the
truth. The library holds a list of every audio file belonging to every book. A large
share of those entries name a file that **is not there**.

Measured directly — 120 books picked at random, every one of their audio files checked on
the actual disk rather than in the database:

| | |
|---|---|
| File entries checked | 1,322 |
| **Entries pointing at nothing** | **552 — 41.8%** |
| Books with at least one dead entry | 49 of 120 |
| Books where **every** entry is dead | 5 |

The pattern is sharp, and it is the most useful thing found all night. **Every single
missing file is in the folder tree the program organises for itself. Not one is in the
iTunes tree.** The typical broken book has a real, playable file sitting safely where
iTunes keeps it, and a second entry beside it pointing at a tidied-up location the file
never reached — or no longer occupies.

This is not guesswork about whether it affects you in practice. The server's own request
log independently records **1,036 genuine "file not found" answers** to real download
attempts.

**Your books are almost certainly not lost.** For 115 of the 120 sampled, at least one
real file survives. The library's *index* is wrong, not the collection.

### Why nothing caught this

Three existing maintenance jobs look like they would find it. None can:

- one looks for entries whose **book has been deleted** — these books all exist;
- one looks for **two entries naming the same file** — these name different files;
- one **compares file contents** — which requires the file to be there to compare.

Not one of them asks the simplest possible question: *is this file actually there?*

There is now a job that asks exactly that, across the whole library. It reads only — it
is incapable of changing or deleting anything, and that restriction is enforced by the
system rather than left to good behaviour. It reports how many entries are dead, which
folder they are in, and — importantly — separates books that have lost *some* files from
books that have lost *all* of them.

### Why it is not repaired yet

There are two ways to fix a dead entry, and they are not interchangeable: remove it, or
re-point it at the file that survived.

**Removing them is unsafe for the 5-in-120 books whose every entry is dead.** Doing that
would leave those books with no files at all — turning a book you can still play by
another route into a book that is definitively gone. A repair that is right for 44 books
and destructive for 5 is not a repair to run while you are asleep.

That is why the job counts the two groups separately, and why the decision is left with
you.

## 2. One error message that meant five different things

Chasing the above took four separate rounds of measurement, and it should have taken one.

When the program answers "file not found", there were **five different places** in the
code that could produce that answer — the book isn't there, the file entry isn't there,
the location couldn't be worked out, the bytes are missing, and so on. All five returned
the identical message, and **none of them recorded which one had happened.**

From the outside, five distinct faults were wearing one costume.

Each now records its reason and the location it tried to use. What you see is deliberately
unchanged — same message, same response — because the fix is about being able to diagnose
the next occurrence, not about changing the program's behaviour. A test exists specifically
to catch it if someone later turns this into a behaviour change by accident.

## 3. The stray comma that created a second person

Yesterday's fix taught the program to split a credit like *"Alan Barnes & Jonathan Morris"*
into two people. It did — and it introduced a smaller fault of its own.

When a list uses a comma **before** the ampersand — *"Lance Parkin, Stephen Cole, Alan
Barnes, & Jonathan Morris"* — the split happened at the ampersand first, which left a name
with a comma still stuck to the end of it. The program then treated **"Alan Barnes,"** as a
different person from **"Alan Barnes"**. One had 14 books; the other had 1.

Checked against your live list of 3,289 contributors before changing anything: **11 names
are affected, 8 of them merge back into the correct entry that already exists, and none
are lost.**

The trim is deliberately narrow. It removes commas, semicolons, ampersands, slashes and
plus signs from the ends of names — but leaves full stops and hyphens alone, because those
belong to real names: *Sammy Davis Jr.*, *E. E. Knight*, *Alex Hill-Knight*. A broader
clean-up would have damaged those.

## 4. Nearly five thousand authors with no books

You asked whether there is a job to purge empty authors. There wasn't; there is now.

The scale is larger than expected: **4,975 of 12,854 authors have no books at all** — a
little under two in five. They are the residue of splitting, renaming and merging over
time, and they clutter every author list and search result.

The job reports what it would remove before removing anything.

## What has **not** been established

Being explicit about the limits, because two of these were things I got wrong first:

- **The equivalent number for narrators is unknown.** An earlier figure of "922 empty
  narrators" was wrong — it came from a list that doesn't report book counts at all, so
  every entry looked empty. 922 is simply the total. A narrator purge also needs two
  pieces of machinery that don't exist yet, where the author version needed none.
- **The narrator/author swap is measured only as a lower bound** — roughly 96 links, not
  "a lot". It should go through the review queue for approval rather than being applied
  blind, because reversing a name pair automatically is exactly the kind of change that is
  hard to undo if the measurement is off.
- **What caused the dead file entries is suspected, not proven.** The timing points to the
  library-wide reorganisation of where books are stored. That is a strong lead, not a
  finding.
- **None of the above is live yet.** All four changes are merged and waiting. A full
  library scan has been running on your system throughout, and deploying restarts the
  server — which would throw that scan away and start it from zero. The deploy is holding
  until it finishes.

## The lesson worth keeping

The first attempt at measuring this reported that **100% of files were missing** — 1,786
out of 1,786. That was completely false. The check was using a form of request the server
does not answer at all, so everything failed identically, and uniform failure reads
exactly like uniform breakage.

It had even been sanity-checked against a deliberately fake file, which correctly came
back as missing — and that proved nothing, because a check that fails on everything also
fails on the fake one.

The rule that came out of it: **testing an instrument with a known-bad input is not
enough. It has to be tested with a known-good input in the same breath, and the two have
to disagree.** The real figure — 41.8% — only appeared once the check could tell a
working file from a broken one.
