<!-- file: docs/executive-summaries/2026-10-11-shared-provider-answer-cache-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: cb42a5eb-f314-4ade-9d5d-8912d1ceaede -->
<!-- last-edited: 2026-10-11 -->

# Metadata lookups stop asking the same question twice

Branch `task/02-pr6` (holistic plan task 02-PR6).

## Executive Summary

- **What was wasteful.** When the app looks a book up on Audible, Audnexus,
  Open Library or Google Books, it remembered the answer for that one book
  only. A second copy of the same book, a book split into one row per track,
  or a re-import asked the very same question again and paid for it again.
  And when a provider had nothing, the app did not remember that at all, so
  every pass over the library asked again about every book nobody can find.
- **The change.** Answers are now remembered by the question itself (the
  title and author, written the same way however they were typed), shared by
  every book that asks it. "Nothing found" is remembered too, for 14 days on
  Audible and Audnexus, 30 on Open Library and 7 on Google Books, the periods
  you chose. A failed or rate-limited request is never remembered, so a
  provider hiccup cannot pose as "nothing found".
- **Your control.** "Search again" on a book clears what was remembered for
  that book's questions and asks fresh. The settings
  `metadata_fetch.negative_ttl_days.*` change the periods, and
  `metadata_fetch.shared_cache` set to off returns to the old per-book
  behaviour exactly.
- **Measured.** In a made-up library of 1,000 books asking 600 different
  questions, the first pass now makes 600 provider calls instead of 1,000, and
  an immediate second pass makes none instead of 400. That matters most for
  Google Books, which allows 1,000 lookups a day.
