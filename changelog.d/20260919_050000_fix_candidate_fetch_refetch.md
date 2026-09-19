### Fixed

- **"Search providers" no longer re-asks every provider about books they already
  said they had nothing on.** Each run picked every book without a match, and
  about 8,000 of those are books all four providers (Audible, Open Library,
  Audnexus, Google Books) had already answered with nothing. Nothing remembered
  that answer, so every run sent each of them through the full search ladder
  again: runs on 09-08, 09-09, 09-11, 09-16 and 09-19 each spent hours
  returning the same ~8,000 "no match" results.

  The fetch now checks what it already knows before calling anyone. A book whose
  candidates were fetched for its current title, author and narrator in the last
  30 days is served from the cache. For a book with no candidates, each provider's
  "nothing found" answer is remembered separately, and only when that provider's
  whole search ran without an error, a quota hold or a cancel. Only providers
  without such an answer are asked again, so Google Books running out of daily
  quota no longer makes Audible, Open Library and Audnexus get asked again too.
  Once every enabled provider has answered, the book is reported as "no match"
  with no calls at all.

  Each answer is re-checked after 90 days, because provider catalogs add new
  releases. Changing the title, the author (including renaming the author), the
  narrator, enabling another provider, or sending `force: true` to the
  batch-fetch endpoint also asks again. Empty results saved before this change
  do not record which providers actually answered (some were saved when every
  provider failed), so those books are asked once more on the first run after
  this ships and remembered from then on. The progress line and the finish log
  show how many books were answered from the cache and how many were already
  known to be empty.

- **A second "fetch all unmatched" click while one is running no longer queues
  the same books again.** The check for books already being fetched read a
  capped slice of operation history, so a long fetch could drop out of it while
  still running. It now reads the list of operations that are actually queued
  or running, ignores a "running" entry left behind by a crash or restart, and
  skips an entry it cannot read instead of refusing the request. `force: true`
  still waits for a run that already has the book, so two runs never fetch the
  same book at once.
