### Fixed

- **Bulk metadata fetch now actually resumes, and no longer dies at the 6-hour
  wall.** The operation derived its resume ledger key with `ulid.Make()` — a
  fresh random value on every run — directly beneath a comment stating it was a
  "deterministic sub-ID so OperationResult rows survive restarts". They never
  did: the lookup always missed, the completed-book map was always empty, and
  the operation had never once resumed anything. The key is now a `run_key`
  carried across a chain of runs.
- A run that reached the registry's 6-hour timeout was **terminal**: a timeout
  is mapped to `canceled`, which is excluded from the resumable set, so the
  remaining books had no route back. With 36,159 books to fetch at the measured
  rate the run needed ~6.3 hours, so it died at roughly 95% every time. A run
  now stops shortly *before* the deadline and queues a successor that resumes
  the same ledger, so the work continues across as many links as it needs.
- The successor's parameters carry an incrementing `continuation` counter. This
  is load-bearing rather than cosmetic: the registry returns the *existing*
  operation id for byte-identical parameters while one is active, so an
  otherwise-identical successor would have been silently swallowed and the chain
  would have stopped while appearing to complete.

### Changed

- `skip_cached` now defaults to **true** for bulk metadata fetch. It was absent
  by default and absent meant false, so a plain dispatch re-hit every provider
  for books whose cached metadata was still fresh. Pass `"skip_cached": false`
  explicitly to force a full refresh.
