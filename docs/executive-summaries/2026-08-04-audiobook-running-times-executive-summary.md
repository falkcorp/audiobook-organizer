<!-- file: docs/executive-summaries/2026-08-04-audiobook-running-times-executive-summary.md -->
<!-- version: 1.3.0 -->
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

## The cleanup is finished

Every duplicate record in the library is gone. The final check, run against the whole
library:

> **314,153 records scanned. 0 books affected. 0 duplicates. 0 failures.**

In total **3,239 duplicate records across 204 books** were removed, with nothing lost
along the way.

It took three attempts, and the reasons are worth knowing because they were all about
the tool rather than your data.

The first attempt was **killed by a safety mechanism**. The app watches long-running
jobs and cancels anything that stops reporting progress, on the assumption it has
hung — a sensible rule that exists because a job once ran silently for hours. But this
job only reported after finishing each book, and one book took longer than the
five-minute limit. From the outside, working hard and hanging looked identical. It was
stopped 19 books in.

The second attempt ran out of its own two-hour budget at book 78 of 176. It was
processing books **one at a time**, roughly a minute and three quarters each. At that
rate the job could not finish the work it was created to do — it would have needed
three or four separate runs.

So the job was changed to process many books **at once**. That is safe here for a
specific reason: each book's records belong to that book alone, so two workers can
never reach for the same thing. The difference was stark — the third attempt finished
**95 books in nine and a half minutes**, work the one-at-a-time version had spent two
hours only half-completing.

Nothing was ever at risk during any of this. Each book is saved as it completes, and
re-running simply picks up where the last attempt stopped.

## And now the second half is fixed too

The millisecond problem described below has since been repaired, and the fix came in
two parts.

The **first** matters more than the cleanup itself. Audiobook lengths are saved in
several different places in the code, and all but one of them already corrected a
millisecond value on the way in. The one that didn't was the path used when a book is
**updated** — so an edit could quietly reintroduce the very problem the others existed
to prevent. That gap is now closed, which means this cannot come back.

The **second** was a one-time repair of the records already stored wrong:

> **314,153 records scanned. 214 books affected. 1,384 corrected. 0 failures.**
>
> Re-checked afterwards: **0 remaining.**

The two stubborn books are now right — the one that read **19,294 hours** now reads
**9.90 hours**, and the one that read 15,557 now reads 8.05. Every one of the ten
books tracked through this work now shows a believable length, between 8 and 17 hours.

A detail worth trusting the tool for: within those same 214 books, **9,352 records
were examined and correctly left alone** because they were already in seconds. The
test is applied to each individual record, not assumed for a whole book.

One correction to an earlier figure. This was estimated at "about 6,000 records"
based on a sample. The true number is **1,384** — the sample was not representative,
and the estimate was roughly four times too high. The full scan is the number to
trust.

## What was still wrong (now fixed — see above)

Duplicate records turned out to be only **half** the problem.

Of ten books checked closely, eight are now correct — 144 hours became 12, 261 became
17, 205 became 15. **Two are still wrong**, and for a different reason: their stored
lengths are recorded in *milliseconds* rather than seconds. Reading them as
milliseconds gives a bitrate that a real spoken-word recording would have; reading
them as seconds gives one no audio file could possibly have. And 9,906 hours divided by
1,000 is 9.9 hours — an ordinary audiobook.

The earlier fix corrected how these are **displayed**. It never rewrote what is
**stored**. About 1.9% of records are affected — roughly six thousand.

The good news is this cannot spread: new files are already corrected as they are saved,
so the affected records are purely old history. It is a one-time repair, and the tool
for it already exists. That is the next job, and it will be previewed before anything
is changed.

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
