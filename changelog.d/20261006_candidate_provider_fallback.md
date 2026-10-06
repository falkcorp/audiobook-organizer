### Added

- **Candidate fetch falls back to Open Library, then Google Books, for books
  Audible cannot match.** Owner decision 2026-10-06 ("Both, spread over
  days"). `metadata.candidate-fetch` (and the lost-candidates refetch, which
  shares its per-book path) no longer asks Open Library and Google Books
  alongside the rest of the chain: it asks the chain first, and only when
  that finds nothing asks Open Library, then Google Books. Results land as
  ordinary cached candidates with their source recorded, for the owner to
  review; nothing is applied. Each result row lists the fallback turns
  (`fallback`: provider, outcome, time).
  - Google Books fallback lookups are capped by a persisted daily budget
    (800 of the key's 1,000/day for background lookups; a negative
    `google_books_fallback_daily_limit` turns the Google fallback off). The count rolls over
    at midnight Pacific and survives restarts (Pebble key
    `provider_daily_budget:google-books`).
  - A spent budget, a provider throttle hold or a failed Google lookup
    DEFERS the book (new result status `deferred`), never `no_match`: its
    cache row carries no Google answer, and the scheduled candidate fetch
    selects it again on a later quota day, capped at that day's remaining
    budget. A provider that already answered "nothing" for the same search
    identity is not asked again.
  - Books whose metadata is applied, and owner-manual-only books (Doctor
    Who / Big Finish), spend no fallback quota.
  - Other search paths (the interactive search dialog, the bulk fetch)
    still ask every enabled source at once. Their Google Books lookups now
    share the same daily counter (see the fallback follow-ups entry).
