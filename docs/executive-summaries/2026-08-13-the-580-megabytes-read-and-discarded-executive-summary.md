<!-- file: docs/executive-summaries/2026-08-13-the-580-megabytes-read-and-discarded-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1f4c8b02-7e35-4d69-a018-9c26b5e07d31 -->
<!-- last-edited: 2026-08-13 -->

# Executive Summary: The 580 Megabytes Read and Thrown Away

**Shipped:** 2026-08-13 · **Pull requests:** #2387 (the split) and #2394 (the move)
**Status:** the code is merged and deployed. The one-off data move it enables is
**built but not yet run** — see "What has not happened yet" at the bottom.

---

## The short version

Every time the audiobook server restarted, it spent about two minutes reading the
library into memory before anything worked. Roughly **eighty percent of what it read
during the biggest part of that startup was data it threw away immediately** — 580
megabytes, read off disk, decoded, and discarded within microseconds, every single
restart, by design and entirely unnoticed.

This work stops that. It is in two halves, and the second half is the interesting one,
because the safe way to do it is not the obvious way.

---

## What the wasted data actually was

Each book carries a **signature** — a compact numeric fingerprint of what the audio
sounds like, used to work out when two files are really the same book. It is about 22
kilobytes per book, and with roughly 67,800 books that adds up.

The signature was stored in the same record as the book's title, author, file path and
everything else. So anything that read a book read its signature too, whether it wanted
it or not.

The startup routine did not want it. It builds a fast in-memory index of the library for
searching and browsing, and signatures play no part in that — so the very first thing it
did with each signature was discard it. It was paying full price to read 580 megabytes
in order to immediately delete it.

Nobody noticed because nothing was broken. Startup was slow, but it was *consistently*
slow, and slowness without an error message tends not to get investigated. It surfaced
only when someone measured where the startup time was actually going, byte by byte.

---

## The first half: give the signature its own drawer

The fix in principle is simple — store the signature separately, so that reading a book
does not mean reading its signature. Anything that genuinely needs a signature asks for
it explicitly.

The risk in practice is not simple, and it is worth spelling out, because it shaped
everything that followed.

**This system has been bitten by exactly this before.** There is already a repair tool
in the codebase whose entire job is to recover signatures that were silently wiped by an
earlier bug. That tool decides a book was damaged by checking whether its signature is
missing. Which means: **a book that has lost its signature and a book that never had one
look identical.** Get the move wrong and the damage is not just invisible — it is
invisible *to the one tool built to find it*, and the only symptom would be that
duplicate detection quietly stopped recognising some books.

So the split shipped in a way that could not break anything: when a book's signature was
not yet in its new location, the system simply read it from the old one. Every existing
book kept working, untouched, with no data moved at all. The new path proved itself on
live traffic before a single byte was migrated.

That safety is also why the saving did not arrive. Nothing had moved, so startup still
read all 580 megabytes. The measurement after that first release confirmed it precisely:
still 580 megabytes, exactly as designed.

---

## The second half: actually move it

This release adds the tool that moves the data — and three decisions in it are worth
recording, because in each case the obvious approach was wrong.

**It does not reuse the normal "save a book" routine.** That routine would have done the
job correctly, and it would have been three lines of code. But it also keeps a historical
copy of every book it saves, and those historical copies deliberately keep the full
signature. Running it across the whole library would have written roughly **1.5 gigabytes
of new historical copies** — in an operation whose entire purpose was to stop paying for
those bytes. It would also have marked all 67,800 books as freshly modified, sending the
search index and several background jobs off to re-process a library that had not
actually changed.

**It writes each book's two pieces together or not at all.** The old record and the new
one are updated in a single atomic step, so there is no instant at which a book has had
its signature removed from one place without it appearing in the other. That is the
failure mode described above — the one no tool could detect — and a test now asserts the
pairing across a whole mixed library rather than trusting that it holds.

**It gets out of the way of anything else writing at the same time.** The server does not
stop for this; scans and imports continue while it runs. Two things writing to the same
book at once could have meant the migration overwriting an unrelated edit — reverting a
book's title or file path, not merely its signature. The resolution came from noticing
something about the problem rather than adding machinery to it: *every ordinary save
already moves a book to the new layout anyway*, so the library migrates itself gradually
through normal use, and this tool only speeds that up. That makes **skipping a book
completely harmless**. So when it detects that a book changed underneath it, it skips it,
counts it, and moves on. Nothing is ever half-written, and running the tool again picks
up whatever it skipped.

---

## How we know it works

The tests are not taken at face value. Ten deliberate sabotages were applied one at a
time — removing each safety check in turn — and in every case the test that should have
caught it did fail. A test that passes is not evidence; a test that has been watched fail
for the right reason is. This matters here because an earlier test suite in this project
passed with its fix removed entirely.

The tool also defaults to **reporting only**. Someone has to explicitly ask it to write.
And before writing anything it prints a sanity check against the original measurement: if
it claims every book needs migrating, or almost none do, that disagreement with the known
580-megabyte figure is visible *before* the irreversible step, not after.

---

## What has not happened yet

**The data has not been moved on the live server.** That is a deliberate hold, not an
oversight: it is the one step that cannot be undone, so it is an owner's decision rather
than something scheduled automatically. The procedure is written down — report first,
check the numbers agree with the original measurement, migrate a hundred books as a trial,
verify, then run the rest.

Until that happens, **startup is exactly as slow as before**. Everything described here is
the mechanism being in place, not the improvement being delivered. The improvement arrives
when the migration runs.

One note on how it will be judged when it does. The obvious check is that the "wasted
bytes" number drops to zero — but that number would also read zero if the measurement
itself quietly stopped working, which would look like total success. So the check is
deliberately a positive one instead: the total startup reading must visibly fall from its
current 729 megabytes, **and** a named book must still return its signature correctly
afterwards. Both, or it does not count.
