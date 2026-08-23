### Changed

#### iTunes `ListBooks` search path no longer materializes every match at once

`ITunesHandler.ListBooks`'s search path called `store.SearchBooks(search, 0, 0)`
— `limit=0` means "no limit" — to materialize every substring match before
post-filtering for a non-empty `ITunesPersistentID`, since `SearchBooks` has no
PID filter of its own. On a large library a single broad query could pull the
whole matching set into memory on a request path (PERF-4).

Bounded the fetch to a new `itunesSearchOverfetchWindow = 10000` constant,
mirroring the existing `searchPostFilterWindow` over-fetch-then-post-filter
precedent in `internal/audiobooks/service_query.go`. When the window is
exhausted, the handler now logs `slog.Warn` with the query and window size so a
truncated result is never silently reported as complete — the PID-narrowing
behavior is otherwise unchanged, since a naive small limit could hide
legitimate iTunes-linked matches found further down the scan. A `search=""`
request is unaffected: it already takes the separate
`ListBooksByITunesPID` pushdown path.

**What this does not change:** the underlying scan still walks the entire
`book:*` keyspace. `PebbleStore.SearchBooks` has no cursor — it restarts at
`iter.First()` and counts forward — so the window only stops the walk early on
queries that match more than 10000 books. Typical queries match far fewer and
scan exactly as much as before; what is bounded is the memory the handler
holds, not the work the store does. Giving that path a real seek is a separate
change.
