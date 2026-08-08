<!-- file: docs/executive-summaries/2026-08-07-the-outage-that-marked-the-library-broken-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 075339af-a6a4-4f0a-88d5-7bff4bf8fe36 -->
<!-- last-edited: 2026-08-08 -->

# The outage that marked the library broken — undone

## What was wrong

On July 1st, the machine that listens to audiobook openings and writes down what
it hears was unreachable for about a day. Nothing was lost — but the system
recorded that outage as if **every book it tried that day had personally
failed**. Roughly 31,000 books, about three quarters of the library, were marked
"transcription failed" even though almost all of them had perfectly good
transcripts, taken four days *before* the outage, sitting right there in the
database.

For five weeks, every screen and every query that asked "what still needs work?"
got a wildly wrong answer: the library looked about four times more broken than
it was.

## What happened today

**The false marks are gone.** A repair pass re-checked every one of the 44,877
books against what is actually stored. 30,820 books got their status corrected.
The 1,463 books that genuinely did fail — and 7 that really are silent — were
deliberately left alone, so no real problem was hidden. A second, independent
re-check afterwards found nothing left to repair.

**33,633 books gained a per-file transcript for free.** For every book made of
a single audio file, the transcript the system already had was copied down onto
the file itself — no re-listening, no cost. This is the foundation for telling
apart books that got merged together wrongly, which is the larger project this
work serves.

**The root cause can't happen again.** The code that turned "the network was
down" into "this book is broken" has been restructured so a transport failure
now writes *nothing* and simply retries later. It took an outage recorded
34,000 times to prove why that distinction matters.

**A long-standing reading mistake was also fixed.** The parser that pulls the
book's title, author, and narrator out of the spoken opening had three blind
spots: it only recognized "Chapter One" (so "Chapter 12" leaked into people's
names), it had no idea what a translator credit was (so translated books had
their translator jammed into the author field), and it threw away the cover
artist entirely. All three are fixed; translated works and cover artists now
get their own proper fields. Replaying 346 real recordings through the old and
new logic: **103 corrupted fields before, zero after.** The cleanup pass that
rewrites the already-stored mistakes has since finished: **12,990 books had
their titles corrected.**

**Reparsing the library can no longer be done blind.** That 12,990-book
cleanup was dispatched with no preview — you asked for it and it simply
started rewriting. It was safe only because of an unrelated internal rule
that this particular job can only ever improve a record, never blank one,
which is luck rather than design. That job now **defaults to preview mode**:
asking for it shows you exactly what it would change, bucket by bucket, and
writes nothing until you ask again with preview turned off.

## What to watch for next

The transcripts on multi-file books (the remaining quarter of the library) still
need to be generated. The support for **using more than one transcription
machine at once is now built and running in production** — work is handed out
by priority across whichever machines are available, so adding a second
machine speeds the remaining quarter up rather than requiring anything to be
reconfigured. The safeguard above means that even if one of those machines
goes down mid-run, no book will ever again be blamed for it.

The one piece still outstanding is what happens to work that was handed to a
machine that then went away: it is currently set aside rather than
automatically handed back to another machine. That re-queueing is designed but
not yet built.
