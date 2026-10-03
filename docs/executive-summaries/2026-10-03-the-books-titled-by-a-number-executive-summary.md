<!-- file: docs/executive-summaries/2026-10-03-the-books-titled-by-a-number-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9c4f2e71-3a8d-4b06-b5e2-7d1c0f6a9e38 -->
<!-- last-edited: 2026-10-03 -->

# The books titled by a number

## Executive Summary

Thousands of entries in the library had a title that was just a number, or a
number glued onto a name: `01`, `077 - Chill`, `1-06 Chapter 1_ Return to
Peril`. Most of them were not books at all. They were single chapter files
that an old import had registered as their own book, and the real book, the
one that already owned that chapter, sat right next to them.

Overnight on 2 to 3 October the count of such entries went from **11,934**
to **FINAL_COUNT** (primary books only; see the table at the end). Nothing was
deleted. Each stray entry was folded back into the book it belonged to, with
the change recorded so it can be undone.

## What was wrong

- **Ghost entries.** About 4,900 stray entries pointed at a file that no
  longer existed on disk, while the real book already had that file under a
  proven match (same import path, same hash, or the real book's own row
  already pointed at it). The repair tool listed every one of them as "held"
  and never touched them.
- **Two copies of the same chapter.** Where a chapter existed twice (for
  example once in the download folder and once in the library), the tool
  called both copies "ambiguous" and skipped them, 7,627 times, even though
  every one pointed at a single real book. The Bible was the largest case:
  one 71-hour book that already held all 1,189 files, plus 2,352 stray copies.
- **Bad title suggestions.** The tool that proposes a real title for a junk
  one offered the first spoken sentence of the recording ("Chapter 26 The
  apartment was in Asimov…"), the publisher's opening line ("Tantor audio
  presents…"), or leftovers like `copy1` and `(138-track)`.
- **Slow repairs.** Folding one stray entry back took 11 seconds on larger
  books because the database re-read the whole library twice for each one.
  A status counter also re-read the whole library every five seconds and was
  using a quarter of the server's processor all day.

## What changed

- Ghost entries and proven duplicate copies are now applied, not merely
  listed. A stray entry registered twice for the same file (one with the
  evidence, one with none) is folded in together with its twin instead of
  blocking it. The Bible was retitled **The Holy Bible**, its narrator credited
  correctly, and its copies folded in.
- The title classifier learned eleven more filename shapes, each with the
  look-alike real titles it must leave alone (`1984`, `2001: A Space
  Odyssey`, `11/22/63`, `9-11`, `10-Minute Toughness`).
- Title suggestions that are prose, publisher idents, chapter headings or
  file-name leftovers are refused.
- The two whole-library re-reads per repair are gone; the five-second counter
  now runs once a minute. Repairs run about forty times faster.
- The review page, which could take up to 105 seconds while a repair ran,
  now answers in about 2.3 seconds on the live server.
- New dashboards show the count of number-titled books, repair speed, review
  page latency and metadata fetch cache hits, so this can be watched rather
  than guessed.

## What is still open

- Chapter clusters where the real book does not exist as a single entry at
  all (a serial's 262 chapters, a novel's 57 chapters each registered as a
  book) cannot be folded into anything yet; they need a consolidation rule
  for "many short chapters in one folder with different titles".
- Copies that match only by name and size, with no hash recorded, stay
  skipped until their files are hashed.
- Titles that are a bare number with nothing else ("96 Hours", a year) are
  indistinguishable from real titles by shape alone.

## Numbers

| Measure | Before | After |
|---|---|---|
| Primary books with a number-leading title | 11,934 | FINAL_COUNT |
| Review page (`view=index&all=true`) | 8.3 s quiet, 40–105 s under load | 2.2–2.5 s warm, 4.0 s cold |
| Time to fold one stray entry | 0.7 s small rows, 11 s large rows | 0.25 s |
| Pull requests merged | — | 13 (#3672–#3685) |
