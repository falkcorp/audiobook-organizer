<!-- file: docs/executive-summaries/2026-11-17-metadata-title-check-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b9d6e21-8c47-4f05-a1d2-6e0f7a4c9b83 -->
<!-- last-edited: 2026-11-17 -->

# Two parts of one story can no longer be mistaken for each other

**Date:** 17 November 2026. A fix to the automatic metadata apply, found while
reviewing how it compares titles.

## Executive Summary

- When the library applies information found online to a book (description,
  cover, series, narrator, and so on) without asking, it first checks that the
  found book really is the book in the library. One of those checks compares
  titles. It was too generous: it counted two titles as the same whenever one
  piece of them matched, for example the words before the colon.
- That meant two parts of one story, or two stories of one franchise, could be
  taken for each other. If the recording lengths were close and the author
  matched, the information for part 2 was written onto part 1 (and the other
  way round), and the same for a story whose subtitle merely mentions another
  story. The books looked fine in the library but carried the wrong
  description and cover.
- The title check now compares the whole title. If the words match but the
  part, volume or year number differs, or both titles carry something the other
  does not (two different stories under one franchise name), the match is
  refused and the book is left for you to review. Number words and roman
  numerals count as numbers, so "Part I" and "Part II" are told apart.
- What still matches as before: the same title with different capitals or
  accents, "and" against "&", a leading "The", "(Unabridged)", an edition label,
  a subtitle that only one side has, a series name in front of or behind the
  title when the series is known, and file names that start with a track
  number or end in a part counter.
- A second, smaller fix: applying from the results of a metadata search did not
  check whether the book had already been applied some other way, so it could
  apply the same book a second time (rewriting its files and tags again). It
  now skips such books, as the other bulk apply already did.
- Some pairs that used to apply on their own will now wait for review. How many
  is not known: it depends on the library, and the count has to come from
  running the read-only preview again before the next bulk apply. One case is
  still not caught: a candidate that is only the franchise name, with no series
  recorded for it, still matches a book in that franchise, as before.
