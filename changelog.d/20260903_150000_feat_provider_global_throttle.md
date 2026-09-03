### Added

- Metadata providers now get a **global, persisted back-off**. When a provider refuses
  us, it is left alone for a period matched to the refusal — 4 hours for an exhausted
  daily quota, 15 minutes for a burst rate limit (or longer, if the provider says so),
  6 hours for a rejected credential, 30 minutes for a server fault, 5 minutes for a
  connection failure. The hold applies to every operation at once and survives a
  restart, and `GET /api/v1/metadata/providers/throttles` lists what is held and for
  how much longer.
- A manual reset: `DELETE /api/v1/metadata/providers/throttles/{id}` clears one hold,
  `DELETE /api/v1/metadata/providers/throttles` clears them all. A lookup you start
  yourself on a single book also ignores the hold, and if it succeeds the hold is
  released — a provider that recovered early does not have to wait out its timer.

### Fixed

- A bulk metadata fetch no longer walks the entire library calling a provider that is
  refusing every request. Previously a blocked provider stayed in the chain and
  produced one failed lookup per book: a run over 22,934 books had to be cancelled
  after roughly 99% errors when Google Books' daily quota ran out. If every configured
  provider is held, the operation now refuses to start and says which providers are
  held and why, instead of recording a meaningless failure against thousands of books.
- Provider errors now carry the provider's own explanation instead of just a status
  number. Nine places reported "returned status 429" and discarded the body, so
  "Quota exceeded ... 'Queries per day'" — the one phrase that separates a
  day-long block from a 15-second one — never reached anything that could act on it.
- The "prefer Audible" option added an Audible client that had no failure protection
  at all: no circuit breaker, no throttle. It is now protected like every other
  provider in the chain.
