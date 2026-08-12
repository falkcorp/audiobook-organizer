### Fixed

#### Activity log queries no longer scan and decode the entire log (production OOM)

`GET /api/v1/activity?limit=5` on production ran for 120 seconds and never
returned. A heap profile put **8.86 GB — 71.9% of the process heap — inside
`database.(*PebbleActivityStore).scanTierKVs`**, 8.02 GB of that in
`encoding/json.Unmarshal`, split between `handlers.ListActivity` →
`activity.Service.Query` (5.65 GB) and `activity.Service.GetDistinctSources`
(3.21 GB) running concurrently. The server was OOM-killed repeatedly.

`PebbleActivityStore.Query` had no index for its default case. It called
`scanTier` for all seven tiers, JSON-decoded **every entry in the entire
activity log** into one `[]ActivityEntry`, filtered it in Go, sorted the whole
slice, and only then sliced out the requested page. The cost of a five-row page
was the size of the whole log. `GetDistinctSources` did the same full scan
again, from scratch, on every page load.

Activity keys are time-ordered (`act:<tier>:<20-digit-unix-nano>:<ulid>`), so
the store can be read directly in result order. `Query` now walks the tier key
ranges newest-first as a k-way merge over reverse iterators and stops as soon as
it has `offset + limit + 1` matching rows. The merge compares the timestamp
embedded in the *key*, so only the rows actually taken are decoded — a
`limit=5` page now decodes 6 entries instead of the entire log.

Also in this change:

- **A hard scan bound.** Even a filter that matches nothing stops after 20,000
  examined rows rather than degrading back into a full scan. Hitting the bound
  is logged at WARN with the examined/matched counts and the filter — never
  silently truncated.
- **`GetDistinctSources` is bounded and memoized** (45s TTL, keyed on every
  filter field that can change the result), instead of full-scanning on every
  page load.
- **Undecodable rows are counted and reported.** `scanTierKVs` dropped entries
  whose stored JSON failed to parse via a bare `continue`. Drops are now counted
  on the store and logged once per scan in aggregate with the first failing key.

**Behaviour change — `total` is now a lower bound when the query stops early.**
It remains exact whenever the walk exhausts the matching rows (the common case:
any page whose limit exceeds the remaining matches). When the walk stops at the
probe, `total` is `offset + limit + 1` — enough for the caller to know another
page exists, but not the true count. The activity log UI computes
`Math.ceil(total / pageSize)`, so on a very large log the page count
under-reports rather than showing the full history depth. This is the deliberate
trade for not materializing the log; an exact count cannot be produced without
decoding every row, which is the bug being fixed.

Regression tests assert the *cost* of a query, not only its result, using a
decode counter incremented at every entry-decode site in the store. Verified by
negative control: with the bounded path reverted to the old full scan, the
guard test reports `query decoded 400 of 400 seeded entries` and fails.
