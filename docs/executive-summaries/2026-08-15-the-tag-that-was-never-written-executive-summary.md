<!-- file: docs/executive-summaries/2026-08-15-the-tag-that-was-never-written-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f3a1e57-6b02-4c8d-9e14-3a70b5d82c6f -->
<!-- last-edited: 2026-08-15 -->

# The tag that was never written

**2026-08-15 — why saving metadata was slow, and what it was quietly ruining**

## The complaint

Fetching metadata and writing it into your audiobook files was painfully slow.
The obvious explanation was that the work happened one book at a time and simply
needed to be spread across more of the machine. That turned out to be half the
story, and acting on it alone would have made things worse.

## What was actually happening

Audiobooks that are split into many files — one file per chapter — were being
**completely rewritten every single time**, whether or not anything had changed.

The program tries to be smart about this. Before writing, it compares the
information it wants to save against what the file already contains, and skips
the file if they match. That check was broken in two places at once, and the two
breaks fed each other:

- The comparison did not know about the **track number** ("chapter 3 of 12"), so
  it always concluded something had changed.
- The part that actually saves information into the file had no instructions for
  the track number either, so it quietly threw it away.

The result was a loop with no exit. The program decided the track number needed
saving, rewrote the entire file, discarded the track number on the way, then read
the file back, found no track number, and decided it needed saving again. Every
run. Forever. For a piece of information that never once made it onto disk.

Fixing either half alone would have changed nothing.

## The part that was destroying your data

While rewriting those files, the program also **overwrote the title of every
chapter**. A file called "Chapter 1: Departure" became "01 - Book Title". The
original chapter name was gone, with no copy kept anywhere.

Because of the loop above, this was not an occasional accident — it happened on
every write, to every multi-file book. Chapter titles across the library were
being steadily flattened into numbered placeholders.

Chapter titles are now kept. They are only replaced when they carry no real
information: blank, or identical to the book title on every file, or a numbered
placeholder this program wrote itself.

## Why it was slow

Saving information into an audio file was doing the same expensive work twice.

Each save makes a complete copy of the audio file, writes into the copy, and
swaps it into place — sensible, because it means a crash mid-write cannot leave
you with a damaged file. But the step that wrote the information was *also*
doing its own full copy-and-swap. So every save copied the entire audio file
twice and read it end-to-end four times to compute checksums, half of which were
calculated and immediately thrown away.

For a large audiobook on network storage, that copying — not the actual writing —
was essentially the whole wait. The duplicate layer is gone, and the checksums
are now calculated only when there is somewhere to record them.

## Cover art

Cover images had a similar all-or-nothing flaw. The program checked whether the
**first** file of a book already had the right cover and, if so, skipped the
entire book. Any remaining files that were missing their artwork stayed missing
permanently, and re-running never repaired them. Each file is now checked on its
own, so every file ends up with the cover while files that already have it are
still skipped.

## One more thing: there were two copies of all of this

The code that writes information into files existed twice, in two near-identical
versions, and they had drifted apart. Books saved through one path silently
missed out on embedded cover art, on updates being carried to alternate editions
of the same book, and on being recorded as "already saved" — which is why some
books kept being reprocessed. There is now one copy, and every path gets the same
behaviour.

## How we know it is fixed

The existing tests for the broken comparison could never have caught this: they
all pointed at files that did not exist, so the check exited early and the
comparison never ran. They passed no matter what it did — and one of them
actively asserted the broken behaviour was correct.

The fix is verified against a **real audio file**: write the information, read it
back, confirm it is there, then confirm a second run correctly does nothing at
all. That last step is the one that used to be impossible. A companion test
confirms a genuine change is still saved, so "skip everything" cannot masquerade
as success.

## What has not been established

No stopwatch has been put on a real library yet. The duplicated work is provably
gone and unchanged books are provably skipped, but the actual time saved on your
collection has not been measured. That is the next thing to check.
