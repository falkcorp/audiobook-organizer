### Fixed

- **The dashboard no longer blocks for 87 seconds during a scan.** Dashboard stats
  were documented as stale-while-revalidate, but the mechanism had never actually
  run. Two independent reasons: `InvalidateLibraryStats` hard-deleted the cached
  value rather than marking it stale, and ~15 mutation paths call it (every
  book-file write among them), so during a scan there was never a cached value
  left to serve; and `readCachedLibraryStats` expired at a 10-minute TTL that was
  the *same number* as the recompute min-interval, so a value old enough to
  trigger a background refresh was always already old enough to be discarded —
  leaving the "serve stale, refresh behind it" branch reachable only in a
  one-second window. Whatever is cached is now returned immediately at any age,
  and a background recompute is kicked when the value is dirty or older than the
  refresh interval.
- **Refresh interval is now 5 minutes** (was 10). The dashboard still answers
  instantly; this only controls how soon a read starts refreshing behind itself.
- **A repeatedly failing background recompute is now visible.** Reads never block
  on it, so a recompute that keeps failing would otherwise show an ever-staler
  dashboard with nothing in the log saying why. Consecutive failures are counted
  and logged at ERROR from the third onward, and the cache is re-marked dirty so
  the next read retries. Responses already carry `computed_at`.
- **Shutdown no longer races the background stats recompute.** It iterates the
  Pebble database, which panics on use-after-close. `Close` now joins any
  in-flight recompute and prevents a new one from starting.
