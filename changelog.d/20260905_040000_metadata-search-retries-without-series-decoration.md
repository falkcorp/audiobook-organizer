### Fixed

- **Metadata search no longer gives up on a series-decorated title.** Library titles
  such as "Eternal Dominion, Book 04 - Assertions" carry a series slot no provider
  indexes, so the literal query came back empty from all four providers and the book
  was recorded as not found: 73 of the first 100 books in the 2026-09-05 bulk fetch,
  each a real, findable book. Both the bulk fetch and the review dialog's lookup now
  retry, only after the literal titles miss, with the book's own name ("Assertions")
  and the decoration-free title, stopping at the first variant that answers. On the
  bulk path — which caches and counts a hit unseen — a variant answer is accepted only
  when its title names the book, so a series-name answer can never be cached as one of
  its siblings, and a title that names only a series and a number ("Path Of The
  Voidwalker - BK07") stays not found and retryable. A provider that is throttled or
  has its circuit breaker open now closes that book's ladder after one refusal, and the
  failure shown is the provider's own message rather than the refusal. Each bulk ledger
  row records which variant, if any, produced the hit.
