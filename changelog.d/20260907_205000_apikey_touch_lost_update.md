### Fixed

- **API-key usage counters were silently undercounting, and by a lot.** Every authenticated
  request spawned a goroutine calling `TouchAPIKeyLastUsed`, whose body was an unsynchronised
  read-modify-write: read the key, `UseCount++`, write it back. Concurrent requests on the same
  key overlapped constantly, so two requests would both read `UseCount = N` and both write
  `N+1` — one increment gone. Measured before the fix: **200 concurrent touches recorded 37**,
  losing 163 of them. The same window made `LastUsedAt` and `LastUsedIP` reflect whichever
  goroutine wrote last rather than the most recent request, so "when was this key last used,
  and from where" could be wrong as well as undercounted. No shutdown or unusual condition was
  needed — ordinary concurrent traffic was enough, and this was live.

  The read-modify-write is now serialized on a dedicated store mutex, following the same
  pattern already used for author creation and review-item upserts. A mutex rather than an
  atomic because the state is a whole record, not a single word.

- **Removed the per-request goroutine behind that call.** Besides the lost update it carried two
  more defects: it fanned out one unbounded goroutine per authenticated request, each performing
  a store read and write; and because it was detached from its handler it belonged to no
  WaitGroup, so `httpServer.Shutdown` could return — and the database close — while it was still
  writing. It now runs in the handler's own goroutine, which is what shutdown actually waits on.
  The cost is a memtable read plus an unsynced write, on a path that has already read the same
  key in order to authenticate it.
