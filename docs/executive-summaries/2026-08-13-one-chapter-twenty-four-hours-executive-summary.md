<!-- file: docs/executive-summaries/2026-08-13-one-chapter-twenty-four-hours-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: fe180abc-a102-4166-a487-adc0ab4dfac9 -->
<!-- last-edited: 2026-08-13 -->

# Executive Summary: One Chapter, Twenty-Four Hours

**Date:** 2026-08-13
**Changes:** PRs #2364, #2366, #2368, #2370, #2372, #2373
**Written for:** anyone who uses the audiobook organiser, not the people who build it

---

## In one paragraph

Almost none of your audiobooks had working chapter navigation, and there was no way to
tell. A 24-hour book would open in the player showing **exactly one chapter**, titled
with the book's own name, spanning the entire recording — so skipping forward a chapter
did nothing, and there was no way to jump to where you left off except by dragging a
slider across a full day of audio. The chapter information was sitting inside the audio
files the whole time; the organiser had simply never looked. It only reads chapters when
it sees a file for the very first time, and that behaviour was added in July 2026 — so
every book already in the library at that point was permanently skipped, and would have
stayed skipped forever. This is now fixed for the whole library.

---

## What you would have noticed

Opening a long audiobook and finding it had no chapters. Not an error, not an empty list
— a single chapter with the book's title on it, which looks like a book that genuinely
has no chapter markers. That is the part that made this hard to spot: **a missing chapter
list would have looked broken and been reported. A wrong one looked perfectly normal.**

If you have books that were added after July 2026, some of them would have had proper
chapters, which made it look like the ones without were just badly-tagged files rather
than a fault in the organiser.

---

## What was actually wrong

**The organiser only reads chapters when saving a brand-new file.** Chapter reading was
added in late July 2026 and wired into exactly one place: the moment a file is first
discovered. Routine re-scans deliberately skip files that have not changed — which is
normally a good thing, since it makes scanning fast — but it means a book already in the
library is never re-examined. No book that existed before July 2026 had ever had its
chapters read, and no ordinary scan would ever have done it.

**The player quietly covered it up.** When the organiser has no chapter information for a
book, it invents one on the spot rather than showing nothing. For a book split across many
files, it makes one chapter per file, which is a reasonable guess. For a book that is a
single long file, it makes **one chapter covering the whole thing** — which is technically
a valid answer and completely useless. From the outside, "we never looked" and "there is
nothing to find" produced identical results.

Measured before any of this was fixed: of 500 books checked, **every single one** reported
its chapter count as exactly equal to its file count — the signature of a guess, not a
reading. Of those, 213 were single files longer than three hours, and all 213 reported
exactly one chapter. Meanwhile, on a sample of 40 real audio files pulled off the disk,
**19 of them contained genuine chapter markers** — between 16 and 118 each — that nothing
was reading.

---

## The second problem, found by fixing the first

Building the repair tool meant running it across the library, and it immediately reported
that **16,130 books had unreadable audio files** — a third of the entire single-file
collection. That would have been alarming if it were true.

It was not true. The audio files were fine. The organiser's *record* of where each file
lives had gone stale: when a book gets tidied up and moved into a new folder, one of the
two places that stores its location gets updated and the other one does not. The repair
tool was reading the out-of-date one, looking for files that had not been there for
months, and reporting "cannot read this audio file" — pointing the finger at your
audiobooks when the fault was in the database.

It only checked the second, correct location when the first one was **completely blank**
— never when it was filled in but simply wrong. Checking whether the file is actually
*there* rather than whether the field is *empty* recovered almost all of them.

That distinction was worth verifying rather than assuming, so it was measured two
separate ways: once by the organiser itself, and once by asking the operating system
directly whether the files existed, on a random sample of 400 books. Both agreed —
roughly 30% of records were stale, and **97% of those books were sitting safely on disk
the whole time** under their other recorded location. Two books, out of the entire
library, are genuinely missing.

---

## What changed, in numbers

| | before this work | after |
|---|---|---|
| Books with real chapters read from the file | 0 | **24,647** |
| Chapters available to your player | 0 | **about 1,030,000** |
| Books reported as unreadable | 16,130 | **742** |
| Books genuinely missing from disk | — | **2** |

("Before this work" means before 2026-08-13. 888,732 chapters across 21,231 books were
written in the final pass; the remaining books were done earlier the same day during
smaller test runs used to prove the repair worked before it was turned loose on the whole
library.)

The point of all this is what the player now does, so that was checked directly rather
than assumed. Before, a sample of 213 single-file books longer than three hours served
**exactly one chapter each — all 213 of them**. Reading the same measurement back
afterwards, **89% of them now serve a real chapter list**: *Sequel.exe*, 24 hours long,
went from 1 chapter to 85. The remainder genuinely have no chapter markers in the file.

The 16,130 "unreadable" books turned out to be four different situations that had been
lumped into one:

- **9,563** were fine and had real chapters waiting to be read
- **4,675** were fine and genuinely have no chapter markers
- **1,127** have no usable location recorded at all
- **765** still cannot be read and are now a small enough group to look at individually

Separating those four apart is arguably worth more than the repair itself. A list of
16,130 problems is something nobody can act on. A list of 765 is.

---

## What this does not fix

**The stale location records are still stale.** The repair tool now works around them, but
it works around them *for itself only*. Anything else in the organiser that looks up a
book by its recorded location will still quietly get the wrong answer for about a third of
single-file books. Repairing those records properly is separate work and has been written
down rather than done.

**Books split across many files were left alone deliberately.** For those, the organiser's
on-the-fly guess — one chapter per file — is already correct, and storing a fixed copy of
it would create a new way to go wrong later if a book gains or loses a file. They were
skipped on purpose, not overlooked.

**Playlists, collections and series are still broken, and separately so.** Found while
finishing this work: opening a playlist shows nothing, series claim to contain no books,
and collections are empty. Those are unrelated faults in how the organiser answers the
audiobook app, and are being handled as their own piece of work.

---

## How to undo it

Chapter information is stored entirely on its own, separate from everything else the
organiser knows about a book. Nothing about your books' titles, authors, narrators or
listening progress was touched by any of this. Removing the chapters restores exactly the
state production was in beforehand.
