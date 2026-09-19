### Fixed

- **"Search providers" no longer re-asks every provider about books they already
  said they had nothing on.** Each run picked every book without a match, and
  about 8,000 of those are books all four providers (Audible, Open Library,
  Audnexus, Google Books) had already answered with nothing. Nothing remembered
  that answer, so every run sent each of them through the full search ladder
  again: runs on 09-08, 09-09, 09-11, 09-16 and 09-19 each spent hours
  returning the same ~8,000 "no match" results.

  The fetch now checks what it already knows before calling anyone. A book whose
  candidates were fetched for its current title and author in the last 30 days
  is served from the cache. A book that every enabled provider has already
  answered with nothing is reported as "no match" without asking again. That
  answer only counts when every provider really answered: one that errored or
  was out of quota is asked again next time. Editing the title or author, or
  enabling another provider, asks again. So does sending `force: true` to the
  batch-fetch endpoint. The progress line and the finish log now show how many
  books were answered from the cache and how many were already known to be empty.
