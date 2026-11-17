<!-- file: docs/executive-summaries/2026-11-16-metadata-refresh-bad-asin-executive-summary.md -->
<!-- version: 1.1.1 -->
<!-- guid: 7a1c9e34-5b82-4d06-b3f1-0e8d2a64c957 -->
<!-- last-edited: 2026-11-17 -->

# The metadata refresh stops failing on nearly every book it picks

**Date:** 16 November 2026. PR 4681. Not deployed when this was written: the
scheduled refresh keeps failing the same way until the change is released.

## Executive Summary

- The scheduled job that refreshes each book's metadata suggestions failed on
  every book it picked in its latest run: 814 of 814. The same books were picked
  again six hours later, so the backlog of books with out-of-date suggestions
  (21,426 at the last count) could not shrink through this job, and each run
  tied up the Audible catalog service's request allowance for about twenty
  minutes.
- Almost all of the failures (800 of 814) had one cause. The search treated any
  ten letters and digits as a product code (an ASIN, Amazon's catalog number
  for an audiobook) and looked it up directly. A book whose title contained a
  ten-letter word, such as a long common noun, had that word sent to the catalog
  service as if it were a code. The service answered "bad code" for 332 of them
  and the other 482 ran out of time.
- The search now sends a lookup only for a value that has the shape of a real
  code. A word is never one, in any capitalisation. A code a person pasted into
  the search box, or one stored on the book in lower case, is still looked up,
  and the lower-case one is corrected first.
- A "bad code" answer is now treated as an answer, remembered for two weeks per
  code and not retried in every region. It no longer counts against the catalog
  service's health, which could otherwise have taken the service out of use for
  every other book because of one book's value.
- A run no longer loses a book's other results when one source cannot answer.
  If only the catalog service failed, the fallback sources that still owed the
  book an answer are asked, and the book is reported as partly refreshed
  instead of as an error.
- A book whose catalog lookup failed, but which found a suggestion from another
  source, is not left on that weaker suggestion for a month. The catalog service
  is the one source that can supply the book's own recording length and
  narrators, so the book is marked as still owed an answer from it. The next
  runs ask the catalog service again, and only the catalog service: after six
  hours, then a day, three days and a week. After five failed tries it stops,
  about eleven and a half days in, so a code the service never answers is not
  asked every six hours for ever. A pause forced by the service being held back
  for a while does not use up one of the five tries. When the service answers,
  its suggestion is added, ranked ahead of the weaker one, and the mark is
  removed. A failed try never removes or outranks a suggestion the book already
  has, and the other sources, including the one with a daily allowance, are not
  asked again on its account.
- Each run now ends with one line saying how many books were fully refreshed,
  how many were partly refreshed, how many failed (grouped by cause), how many
  were put off for later and how many were not asked at all.

## Title words were sent as product codes

**What it was.** The check for "does this look like a product code" accepted any
ten letters and digits. The refresh applied it to every word in a book's title.

**Why it mattered.** Every book with such a word failed, was never recorded as
checked, and came back at the next run. The failures also used up the shared
request allowance, which slowed the books that did not have the problem.

**The fix.** Lookups use a stricter rule that matches the catalog service's own:
a letter B followed by nine capital letters or digits, or ten digits (the last
may be an X). Free text only counts when the token already looks like a real
modern code as written. The older, looser check is kept for the fingerprint that
decides whether a saved suggestion is still current, so no saved suggestion is
marked out of date by this change.

## A "bad code" answer counted as a failure

**What it was.** The catalog service's "bad code" reply was handled like an
outage: the search tried two more regions, recorded the source as failed, and
asked again at the next run.

**Why it mattered.** The reply is final for that code, so every retry was
wasted, and a failed source blocked the book's other sources from being asked.

**The fix.** The reply is recognised by its exact message, saved as a "nothing
here" note that lasts two weeks, and not counted against the service's health.
Any other kind of error reply is still treated as a failure and is not saved.

## The time limit counted the waiting line

**What it was.** Each lookup was given ten seconds in total, but the clock
started before the request joined the queue for the shared request allowance.
With twenty workers queued behind two requests per second, the whole ten seconds
could pass in the queue.

**Why it mattered.** Lookups failed with "ran out of time" even though the
service answers in well under a second. This part of the diagnosis is inferred
from the numbers: the production logs do not record how long each request took.

**The fix.** The limit now starts when the request has its place in the line and
covers only the exchange with the service. A request that times out is tried
once more after a short pause, and no more than that. This retry applies only
to a service that has such a limit. A lookup that hangs now gives up after the first region that times out, since the next one would
wait the same way: at worst about twenty-two seconds and two requests, where it
used to be thirty seconds and three requests.
