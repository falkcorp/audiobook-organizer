<!-- file: docs/executive-summaries/2026-07-16-whole-library-truncation-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b3f9a41-2d78-4c05-9e16-8a4c0f7d2b39 -->
<!-- last-edited: 2026-07-16 -->

# Executive Summary: Maintenance Tasks Were Only Looking at Part of the Library

## What changed

Several "run across the whole library" maintenance tasks were quietly only looking at
the first chunk of books and ignoring the rest. Each asked the database for "all
books" but with a fixed ceiling — 10,000 or 20,000 — baked in. With roughly 30,000+
books in the library, that meant the tasks stopped well short of the end and never
touched the remaining two-thirds, with no error and nothing in the report to say so.

The affected tasks:

- **Tidy up combined author names** (splitting entries like "Author A & Author B" into
  two) — only the first 10,000 books were ever cleaned up.
- **Fill in metadata after an iTunes import** — on a large first import, only the first
  10,000 imported books got enriched.
- **The "which books have incomplete metadata?" report** — counted against only the
  first 10,000, so it under-reported how much was missing.
- **The one-time scan for permanently-unreadable files** — capped at 20,000, and because
  it marks itself "done" and never runs again, the rest of the library would have stayed
  unscanned forever.

All five now ask for the entire library with no ceiling. A test locks in the "no
ceiling" behavior so the cap can't quietly creep back.

## Why it mattered

Nothing was deleted or corrupted — but work the system reported as done was only
partially done, invisibly. A book past the cutoff would never get its author name
tidied, never get enriched after import, and never get checked for unreadable files.
The report that tells you how healthy your metadata is was itself only counting part of
the shelf.

## Also noted (follow-ups, not fixed here)

About ten other whole-library tasks use a much higher ceiling (100,000 or a million).
Those are fine at today's size but would hit the same problem as the library grows;
they're logged as a follow-up to switch to the same no-ceiling form.
