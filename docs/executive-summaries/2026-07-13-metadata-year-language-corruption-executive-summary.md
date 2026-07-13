<!-- file: docs/executive-summaries/2026-07-13-metadata-year-language-corruption-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4a1f8e2c-9b73-4d16-8e05-2c7a6f9b1d38 -->
<!-- last-edited: 2026-07-13 -->

# Executive Summary: Stopping Wrong Publication Year and Language on Books

## Executive Summary

When the app pulled book details from an outside catalog, two kinds of wrong
information could quietly replace correct information — and, in the case of the
year, get written into the audio files themselves. We fixed both so the correct
value is kept and the wrong value never overwrites it.

## What was wrong, in plain terms

**1. The wrong year could overwrite the right year.**

There are two different "years" for an audiobook: the year the *audiobook* was
released (from Audible), and the year the *original book* was first printed
(from book catalogs like Open Library and Google Books). For a classic, these
can be decades apart — a 1937 novel with a 2010 audiobook edition.

The app treated them as one field. Whenever it matched a book against a book
catalog, it copied that catalog's *print* year into the *audiobook release year*
slot, painting over a correct Audible release year. Worse, that value then got
stamped into the audio file's own `year` tag — so the mistake spread from our
records into the files on disk.

**2. The wrong language could get attached.**

Open Library returns a book's languages as an unordered jumble across every
edition ever published. The app just took the first one in the list. If the
first listed edition happened to be a translation, the book got tagged with that
language — and because that tag drives the "filter by language" feature in
review, an English book could quietly disappear from the English view.

## What we changed

1. **We taught the app which kind of year it's looking at.** Audible and its
   sister source now mark their year as an *audiobook release* year; every book
   catalog's year is treated as an *original print* year. Release years go to the
   release-year field, print years go to a separate print-year field, and neither
   can overwrite the other. A print-year match can no longer touch a correct
   audiobook release year.

2. **We stopped guessing the language.** When Open Library's editions disagree on
   language, the app now attaches no language at all — because no label is better
   than a wrong one that hides the book from its own language filter. When the
   editions agree, it still sets the language as before.

## Why it matters

Both bugs silently replaced correct data with wrong data, and the year bug
reached all the way into the audio files' embedded tags — the hardest kind of
error to notice or trace. Publication year and language are also core to how a
library is browsed and filtered, so a wrong value doesn't just look bad, it makes
books hard to find.

The fix only ever keeps or correctly routes this data; it never erases a good
value, and it ships with tests that reproduce each corruption and prove it no
longer happens. It can be rolled back as a single change. One bounded, harmless
after-effect: for a short while, previously-cached Audible results may leave the
audiobook release year unfilled (never wrong) until the cache naturally refreshes.
