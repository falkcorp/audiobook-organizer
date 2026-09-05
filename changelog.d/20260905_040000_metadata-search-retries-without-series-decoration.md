### Fixed

- **Metadata search no longer gives up on a series-decorated title.** Library titles
  such as "Eternal Dominion, Book 04 - Assertions" carry a series slot no provider
  indexes, so the literal query came back empty from all four providers and the book
  was recorded as not found: 73 of the first 100 books in the 2026-09-05 bulk fetch,
  each a real, findable book. Both the bulk fetch and the review dialog's lookup now
  retry, only after the literal titles miss, with the book's own name ("Assertions")
  and the decoration-free title, stopping at the first variant that answers. The
  book's name is read off the series slot ("Eternal Dominion, Book 04 - Assertions",
  "A Game of Thrones: Book 1 of A Song of Ice and Fire", "The Expanse 04 - Cibola
  Burn"), and on both paths — the bulk fetch caches a hit unseen, and the review
  dialog's search also feeds the bulk-apply endpoint — an answer is accepted only when
  its title carries the words that name this book (not the series' own words, not
  "novel" or "edition") and its author agrees, so a series-name answer can never be
  filed as one of its siblings. A one-word name is searched only with the author. A
  title that names only a series and a number ("Path Of The Voidwalker - BK07") stays
  not found and retryable. A provider that is throttled or has its circuit breaker
  open now closes that book's ladder after one refusal, and the failure shown is the
  provider's own message rather than the refusal. Each bulk ledger row records which
  variant, if any, produced the hit.
