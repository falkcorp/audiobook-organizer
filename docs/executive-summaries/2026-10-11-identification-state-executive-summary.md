<!-- file: docs/executive-summaries/2026-10-11-identification-state-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 66ff413b-18cf-4eb7-9d4d-d7b50c4533b9 -->
<!-- last-edited: 2026-10-11 -->

# The metadata goal number is now a live count you can click

Branch `task/02-pr4` (task brief 02-PR4 of the October filter and
identification plan).

## Executive Summary

- **What changed.** Every book now has one "identification state" that says
  where it stands on metadata: never looked up, looked up but nothing found,
  has suggestions waiting for review, metadata applied, or held (you ruled
  "no match"). The app keeps a running count of each, so the counts are
  instant instead of needing a scan of the whole library.
- **The goal number.** The goal of fewer than 1,000 books missing metadata is
  now a number the app shows directly: books never looked up plus books
  looked up with nothing found, among the books the Library shows by default.
  Books with suggestions waiting for review do not count as missing.
- **You can click through to the books.** Typing `ident:missing` in the
  Library search bar lists exactly those books, and the "Select all N items"
  number is the same count. The other states work the same way
  (`ident:has_candidates`, `ident:held`, and so on), and a minus sign
  excludes a state. A new `asin:` filter finds books by their Audible ID.
- **The review page's summary carries the counts** for each state and the
  goal number. Nothing on the review page shows them yet; that is later work.
- **After an update, the counts take a few seconds to appear.** The first
  start after the update builds the index (about 0.2 seconds per 5,000 books
  in testing). Until it finishes, an `ident:` search answers "try again
  shortly" rather than a number that could be wrong.

## Details

- **Speed, measured on 40,000 synthetic books:** the goal count takes under a
  microsecond; the first page of `ident:missing` takes about 4 ms.
- **Kept correct by every change.** A fetch of suggestions, an applied match,
  a "no match" ruling, a retitle, a book moved to the trash or out of it, a
  book deleted: each updates the count in the same database write as the
  change itself. A test drives all of these at once from several threads
  over 10,000 books and checks the counts against a full recount 50 times; it
  found no mismatch.
- **One question for the owner.** A book is "held" only when it was ruled "no
  match". A book where you rejected one suggestion is not held, because it may
  still have other suggestions. Confirm or change this rule.
- **Not yet done.** The background job that re-checks books whose state may
  have changed (task 02-PR14) is still to come; this change only lays down the
  list it will work from.
