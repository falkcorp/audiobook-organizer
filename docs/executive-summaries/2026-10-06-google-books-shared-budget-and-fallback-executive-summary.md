<!-- file: docs/executive-summaries/2026-10-06-google-books-shared-budget-and-fallback-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: eecc3fe7-e8de-499f-8114-d48d6f8a6ec1 -->
<!-- last-edited: 2026-10-06 -->

# One Google Books allowance, and a fallback that helps stuck books

## Executive Summary

- **What was asked.** When Audible cannot match a book, the nightly metadata
  fetch now tries Open Library and then Google Books. The owner asked for
  three changes to that. First, the fallback should also run when Audible
  found something nobody can use. Second, every use of Google Books should
  share one daily allowance. Third, a list of review problems should be fixed.
- **When the fallback runs.** The fallback now also runs when every Audible
  match was rejected, conflicts with the book's ASIN, or scores too low to
  apply.
  - Books that had been stuck with only unusable matches are now picked up.
    Before, they were shown from the cache forever and never searched again.
  - New matches are added next to the old ones. They do not replace them.
    The stored list holds ten matches, so when the combined list is longer
    the weakest are dropped: matches you rejected go first, then low-scoring
    ones.
- **One allowance for Google Books.** Google allows 1,000 lookups a day.
  Every caller now draws from that one count, which resets at midnight
  Pacific.
  - Automatic work stops at 800, so 200 always stay free for you in the
    search dialog.
  - The limit is checked at the one place every Google request passes
    through, so a new feature cannot get around it by mistake.
  - Hitting the limit does not lock Google Books out for searches you start
    yourself.
- **Fixes from the review.**
  - If Audible itself fails, the fallback waits for the next run instead of
    spending Google lookups.
  - If Open Library fails, Google Books is still tried.
  - Only temporary Google problems (busy, down, out of allowance) put a book
    off until later. A permanent refusal is recorded so the book does not use
    up the allowance every day. Books put off longest go first.
  - Doctor Who and Big Finish books still get an Open Library search. They
    never use Google lookups.
  - The review page has a "deferred" chip that lists books waiting for a
    later Google lookup. Result counts now include deferred and skipped books.
    A book drops off that chip as soon as it has a usable match.
- **Your two decisions.**
  - Open Library and Google Books matches are only ever applied by you, from
    the review page. No automatic job applies one.
  - "Search again" on a single book counts as you asking, so it may use the
    200 reserved Google lookups. Searching many books at once is automatic
    work and stops at 800.
- **Second review round.** Adding Open Library or Google matches could, for
  most books, wipe the Audible matches already stored, or bring back a wrong
  volume an older search had filtered out. Both are fixed: new matches are
  always added beside the old ones, and filtered matches stay filtered.
- **Third review round.**
  - **Stored matches were being wiped.** For books first searched before
    late September, a nightly re-search that found nothing replaced the
    stored matches with an empty list. Rejected and low-scoring matches you
    could still review were lost, a little more every night. Pressing
    "Search again" could do the same. Both now keep the stored matches when a
    search finds nothing.
  - **A bulk apply could apply a match nobody saw.** "Select all" can include
    books whose match never loaded on screen. Such a book is no longer
    applied when its best match comes from Open Library or Google Books.
    Those are applied only when you can see them.
  - **The best Audible match stays on top.** An Audible match the system can
    use now ranks above an Open Library or Google match, even one that scores
    higher. Jobs that apply matches automatically read the top match.
  - **Unsearched books share the allowance.** Books never searched before can
    end up needing a Google lookup too. They now draw on the same daily
    allowance, after the books already waiting for one.
