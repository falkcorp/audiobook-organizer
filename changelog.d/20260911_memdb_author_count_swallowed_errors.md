### Fixed

- `MemStore.GetAllAuthorBookCounts` now returns an error when a book lookup fails, instead of treating the failure as "book absent" and silently under-counting the author. `maintenance.author-dedup-scan` now fails on a `GetAllAuthorBookCounts` error instead of discarding it with `_` and ranking duplicates against an empty count map.
