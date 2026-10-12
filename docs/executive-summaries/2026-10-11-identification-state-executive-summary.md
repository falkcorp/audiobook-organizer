<!-- file: docs/executive-summaries/2026-10-11-identification-state-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 66ff413b-18cf-4eb7-9d4d-d7b50c4533b9 -->
<!-- last-edited: 2026-10-11 -->

# Missing metadata is now a live count you can click

Branch `task/02-pr4` (task brief 02-PR4 of the October filter and
identification plan).

## Executive Summary

- **What changed.** Every book now has one "identification state" that says
  where it stands on metadata: never looked up, looked up but nothing found,
  has suggestions waiting for review, metadata applied, or held (you ruled
  "no match"). The app keeps a running count of each, so the counts are
  instant instead of needing a scan of the whole library.
- **The "missing" count.** The app now shows directly how many books are
  missing metadata: books never looked up plus books looked up with nothing
  found, among the books the Library shows by default. Books with suggestions
  waiting for review do not count as missing. It is not yet exactly the goal
  number (fewer than 1,000): the goal leaves out the franchise you excluded
  (about 281 books), and the app cannot tell those books apart yet. Until a
  later change marks them, "missing" is higher than the goal number by however
  many of those books have no metadata.
- **You can click through to the books.** Typing `ident:missing` in the
  Library search bar lists exactly those books, and the "Select all N items"
  number is the same count. The other states work the same way
  (`ident:has_candidates`, `ident:held`, and so on), and a minus sign
  excludes a state. A new `asin:` filter finds books by their Audible ID.
- **The review page shows the counts**, each one a link: clicking "missing
  metadata" opens the Library already searching `ident:missing`, and the
  Library's "Select all N items" shows the same N.
- **After an update, the counts take a few seconds to appear.** The first
  start after the update builds the index (about 0.05 seconds per 5,000 books
  in testing). Until it finishes, an `ident:` search answers "try again
  shortly" rather than a number that could be wrong. If the build fails, the
  app tries again on its own for about two minutes, and after that there is a
  maintenance operation to rebuild it by hand.

## Details

- **Speed, measured on 40,000 synthetic books:** the missing count takes under a
  microsecond; the first page of `ident:missing` takes about 4 ms. Keeping the
  counts does not slow down saving books or fetched suggestions when many
  happen at once (an earlier draft did, by up to ten times, and was redesigned).
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
