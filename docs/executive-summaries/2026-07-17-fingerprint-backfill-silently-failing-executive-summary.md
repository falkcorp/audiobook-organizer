<!-- file: docs/executive-summaries/2026-07-17-fingerprint-backfill-silently-failing-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e8b3a91-2c47-4d6f-b083-9a1c5e7d40f2 -->
<!-- last-edited: 2026-07-17 -->

# The audio-fingerprinting job had been quietly failing every single restart

## What was wrong

The app keeps a job that listens to your audiobooks and computes an audio
"fingerprint" for each one — a compact signature of how the audio actually sounds.
Fingerprints are the most reliable way to tell whether two books are genuinely the
same recording, as opposed to two books that merely share a title.

That job never ran. Every time the server restarted, it started up, tried to load
the list of books, immediately hit an error, and gave up. It then reported itself as
failed in a log file and nothing else happened. No alert, no retry, nothing visible
in the interface. As far as anyone using the app could tell, fingerprinting was
simply on.

The practical cost: roughly two-thirds of the library has no fingerprint. That is
the single biggest reason duplicate detection has to fall back on comparing titles
and durations, which is fuzzier and produces more false matches for a human to sort
through.

## What was actually happening

The database stores books in a list, and stores a set of lookup shortcuts — "find
the book with this ISBN", "find the book at this file path" — right alongside them.
The code that reads "every book" walked past the end of the real books and started
reading the shortcuts, trying to interpret each one as if it were a book.

Most of those shortcuts have nothing stored in them; the useful information is in
the label, not the contents. So the code opened an empty one, tried to read a book
out of it, found nothing, and treated that as a fatal error rather than skipping it.

There was already a rule to skip one specific kind of shortcut — the file-path one.
But nine other kinds had been added over time and nobody extended the rule. The
first one the code reached happened to be the ISBN-style shortcut, and that is
exactly where it died.

Two things kept this hidden for a long time:

- It only breaks when something asks for *all* the books at once. Anything asking
  for a page at a time stops before reaching the shortcuts and works perfectly.
- The app normally serves reads from a fast in-memory copy, which quietly skips
  unreadable entries instead of failing. Only jobs that run at startup — before that
  in-memory copy is ready — read the database directly and hit the error.

So it needed both conditions at once. The fingerprint job happened to be the one job
that met both, on every startup.

## What we changed

The code now recognises a shortcut by its shape rather than by a hand-maintained
list of names. Book identifiers never contain a colon; every shortcut label does.
That single distinction is enough, and it means future shortcuts cannot silently
break this again — which is the failure that actually happened here.

We also added a test that reproduces the original error exactly, and confirmed it
fails without the fix. It cannot regress unnoticed.

## How we found it

We built a disposable, full-fidelity copy of the production system — real library,
real database, isolated so that even a genuine delete cannot touch production media.
The bug announced itself in the first twenty seconds of running it, in the startup
log. It had been sitting in production logs for months, doing so silently that
nobody had cause to read them.

Three other jobs read the book list the same unbounded way and were exposed to the
identical failure: the fingerprint rescan, the duplicate-detection engine, and the
relink report. All four are fixed by this one change.

## What this does not fix

Fingerprint coverage does not repair itself. The job can now run, but the roughly
two-thirds of the library that was never fingerprinted still needs a backfill pass
to catch up. That is a separate, deliberate step.
