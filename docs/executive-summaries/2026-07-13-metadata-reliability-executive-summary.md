<!-- file: docs/executive-summaries/2026-07-13-metadata-reliability-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9d2f6b41-8c37-4e5a-b0d9-1a2c3e4f5b6a -->
<!-- last-edited: 2026-07-13 -->

# Executive Summary: Metadata Lookups That No Longer Hammer or Hang

## What changed

When the app looks up book information (cover, author, series, narrator) from
outside services like Audible, Open Library, and Hardcover, it is supposed to be
polite: only so many requests per second, back off automatically when a service
is having problems, and never get stuck for minutes on a single book. Three bugs
meant it was doing none of those reliably during large batch runs.

- **It ignored its own speed limit.** The part that decides "only 10 requests a
  second" was checking the limit once per *book*, but each book fans out into
  many actual requests across several services. In a batch with several workers,
  that let it fire far more than 10 requests a second at the outside services —
  a stampede that can get us throttled or temporarily blocked. It now counts the
  real requests, so the limit means what it says.

- **It kept forgetting a service was down.** The app has a "circuit breaker"
  that stops calling a service after it fails repeatedly, plus a per-service rate
  limiter — but both were being thrown away and rebuilt for *every single book*.
  So across a batch they never added up: the breaker could never actually trip,
  and the rate limiter never accumulated. These are now shared across the whole
  run, so a failing service is skipped instead of retried thousands of times.

- **One missing book could freeze a lookup for over four minutes.** Looking up a
  book by its Audible ID tried nine regional stores one after another, each
  waiting up to 30 seconds, with no way to interrupt it — up to 270 seconds
  stuck on a single unfound book, and a "cancel" on the batch couldn't stop it.
  Each regional attempt is now capped at 10 seconds (worst case ~90 seconds
  instead of 270), and the loop stops immediately when the work is cancelled.

## Why it mattered

These only bit during bulk metadata runs, which is exactly when they hurt most:
a full-library fetch could stampede external services (risking rate-limit
blocks), keep pounding a service that was already down, and stall for minutes on
individual problem books — with a "cancel" that didn't fully work. No book data
was lost or corrupted; this is about the app being well-behaved and responsive
when talking to the outside world.

## Known limitation (now resolved — 2026-07-13)

The everyday auto-fetch path (used when importing) was *bounded* — it could no
longer hang for 270 seconds — but it could not yet be *instantly* cancelled
mid-book, because that entry point did not carry a cancellation signal (worst case
~90 seconds per stuck book). **This is now fixed:** the cancellation signal is
threaded all the way from the caller (import, organize, or a web request) down
through the lookup and into the regional-store loop, so pressing "cancel" on a
batch or import stops an in-flight lookup right away instead of waiting out the
~90-second bound.

Shipped alongside it: a small cache-correctness fix. When the app remembers a
previous Audible/Audnexus lookup, older remembered entries (saved before a recent
release-vs-print-year fix) could briefly file the audiobook's *release* year in the
*print* year slot until that memory expired. Those entries now correct themselves
the moment they're re-read, so the year lands in the right place with no re-fetch.
