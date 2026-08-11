### Fixed

#### The version-group index backfill now logs, and no longer buffers the whole rebuild in memory

This backfill is the production repair for a version-group index that could
silently under-report. It could not be observed and could not finish
economically on a large library.

- **It logged only on success.** A sentinel-gated skip, a missed type assertion
  in the caller, and a still-running scan were all silent and identical from
  outside — so a deployment could not tell whether the repair had run. Deploying
  to a 366,922-book library produced no log line at all for over twelve minutes.
  Every exit path now logs: start, "already complete, skipping", periodic
  progress, completion with counts and duration, and an explicit error on each
  failure.
- **The caller failed silently.** `server_lifecycle.go` asserted the store to an
  interface and did nothing when the assertion missed — if the store is ever
  wrapped or decorated, the index would never be rebuilt and nothing would say
  so. It now logs a warning naming the concrete type.
- **One unbounded batch held the entire rebuild.** Every index row for every
  book accumulated in memory and was committed once at the very end, so an
  interrupted run discarded all of its work. Writes are now committed in chunks;
  the completion sentinel joins the final batch, so it can never become durable
  without the rows it claims were written.
- **A real read error was treated as "not yet run."** Only `ErrNotFound` now
  means that; any other sentinel read failure aborts instead of silently
  triggering a full rebuild on every boot.
- The row filter is now structural (a primary `book:<id>` row has exactly one
  colon) rather than a blacklist of index prefixes that listed one twice and
  would have silently accepted any prefix added later.
