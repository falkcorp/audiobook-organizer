### Changed

#### iTunes `ListBooks` search path no longer over-fetches the entire book set

`ITunesHandler.ListBooks`'s search path called `store.SearchBooks(search, 0, 0)`
— `limit=0` means "no limit" — to materialize every substring match before
post-filtering for a non-empty `ITunesPersistentID`, since `SearchBooks` has no
PID filter of its own. On a large library that is a full unbounded scan on a
request path (PERF-4).

Bounded the fetch to a new `itunesSearchOverfetchWindow = 10000` constant,
mirroring the existing `searchPostFilterWindow` over-fetch-then-post-filter
precedent in `internal/audiobooks/service_query.go`. When the window is
exhausted, the handler now logs `slog.Warn` with the query and window size so a
truncated result is never silently reported as complete — the PID-narrowing
behavior is otherwise unchanged, since a naive small limit could hide
legitimate iTunes-linked matches found further down the scan. A `search=""`
request is unaffected: it already takes the separate
`ListBooksByITunesPID` pushdown path.
