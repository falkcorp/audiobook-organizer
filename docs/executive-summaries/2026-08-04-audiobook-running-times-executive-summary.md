<!-- file: docs/executive-summaries/2026-08-04-audiobook-running-times-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4c2a5631-03db-4f50-ab49-7056ec114fe6 -->
<!-- last-edited: 2026-08-04 -->

# Almost every audiobook was showing the wrong running time

## What was wrong

Book lengths in the library were nonsense. A twelve-hour book listed as **158 hours**.
Another showed **294 hours** — twelve solid days. Elsewhere the opposite: a
seventy-eight-minute book listed as **4 minutes**.

This was not cosmetic. The running time is the number everything else divides by, so
when it is wrong, your listening progress reads **0%** no matter how far in you are,
the "time remaining" is meaningless, and chapter positions land in the wrong place.
About **26,000 books** were affected.

There were also two smaller complaints that turned out to be worth chasing: the
Authors tab was full of names that were not authors, and the Narrators tab was nearly
empty.

## What was actually happening

**Two completely separate problems** were producing the same symptom, which is why it
looked like one big mysterious mess.

**The first was arithmetic, and it was the larger of the two.** Audiobook lengths are
stored in seconds. Somewhere along the way the code became convinced they were stored
in *milliseconds*, so every time it added up a book's length it first divided by
1,000 — and threw away the remainder. A 78-minute book (4,680 seconds) became 4,680 ÷
1,000 = 4.68, rounded down to **4**. The rows being destroyed were the *correct* ones.

Only about 2% of records genuinely were in milliseconds, left behind by an old import.
The fix was not to divide everything, nor to divide nothing, but to work out each
record individually: knowing a file's size and its claimed length tells you its
implied bitrate, and only one of the two interpretations produces a bitrate a real
audio file could have. That test is safe to run repeatedly — a record already correct
stays correct.

**The second problem was duplicate records**, and it was stranger. A single audio file
could be recorded in the database dozens of times over. One book had **130 copies** of
the same file. Another had a single audiobook's runtime counted **26 times** — the
294-hour book was one 21,877-second file added up 26 times, exactly.

## What we changed

The arithmetic fix went out first, since it was the bigger cause and carried no risk
of losing anything: it only changes how a number is *read*. Every multi-file book we
could check now shows a sane length.

For the duplicates we built a dedicated cleanup tool that **does nothing at all unless
explicitly told to** — it reports what it would remove and stops there. Deleting
records is irreversible, and duplicate rows are not identical: one copy might carry
the acoustic fingerprint used to recognise the audio, another the length, another the
file checksum. Choosing wrong destroys something expensive to rebuild.

We then ran it on **ten books only**, and checked the outcome book by book.

## What the ten-book trial showed

All ten came out correct — 158 hours to 12.15, 294 to 19.66 — with every acoustic
fingerprint intact.

One of them looked at first like a failure and turned out not to be, and the
distinction is worth recording because it nearly caused a wrong conclusion. A book
called *The Trapped Mind Project* had 130 copies of one record, and after cleanup it
read **0.00 hours**. That looks exactly like a length being wiped.

It was not. That book's entire audio content is a **13.5-second, 91-kilobyte file** —
a stub or truncated download, not a real audiobook. 130 copies of 13 seconds added up
to the roughly 28 minutes it showed before; one copy of 13 seconds is 0.00 hours. The
tool was right, and the reported "damage" was a rounded display value being mistaken
for evidence.

The lesson generalises: when a number goes to zero, check the underlying file before
concluding something was destroyed. The stored data and the file agreed here all along.

The tool was still hardened as a result, because the concern behind the false alarm is
real even if this was not an instance of it: it picks which *record* to keep, and the
best record is not always the most complete one. It now takes the best of *every* piece
of information from the records it is about to remove and merges it into the one it
keeps — only ever filling a gap, never overwriting. The rescued record is saved
**before** anything is deleted, and if that save fails the whole group is skipped
rather than half-cleaned. That is protection against a hazard we reasoned about, not a
repair of a loss we observed.

Two unrelated things about that book are genuinely wrong and are now on the list: its
stored file size reads **532 MB** for a 91 KB file, and the app reports the file as
present at a path where it does not exist.

## One more thing worth knowing

The trial surfaced a second, subtler issue: after the cleanup ran, every length still
looked wrong. Only after restarting the server did the corrected values appear — they
had been right in the database the entire time, but the app's fast in-memory copy had
not been told to refresh.

That is its own bug and it is now written down rather than patched in a hurry, because
it affects anything that changes a book's files, not just this one tool. In the
meantime the cleanup tool says so plainly when it finishes, so nobody concludes it did
nothing and runs it a second time.

## Also fixed

- **The Authors tab listed people who were not authors.** The library was showing
  16,491 books but building the author list from all 44,888 records, including hidden
  and duplicate ones. Both lists now come from the books you can actually see.
- **The Narrators tab was nearly empty.** Narrators are recorded in three different
  places depending on where the data came from, and the tab was only reading one of
  them, while the book's own detail page read all three. The two now share the same
  logic, so they cannot drift apart again.
- **Metadata from automatic transcription was overwriting too much.** When the app
  matched a book by listening to it, it applied *every* field it had inferred. It now
  applies only title and author — the two it is actually reliable at.
