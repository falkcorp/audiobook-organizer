### Fixed

#### Merged-away books no longer haunt the library page for 24 hours

Merging books worked correctly — the merge moved files, reassigned external
IDs, and hard-deleted the losers — but the library page kept listing the
deleted books for up to a full day. Measured on production on the UI's own
default list key: the cached response returned 40,957 books where the
identical cache-busted query returned 40,839, a drift of 118 rows. Three of
the merged-away books were still being listed while returning HTTP 404 on
fetch (`01KQAQEJ7HGX9YJC94WMYZG954`, `01KQAQEEWWQWXHGCB9KRR013H5`,
`01KQAQEHG337RJ2HEHN9PAA61W`). Not every phantom row was a deleted book:
when a merge elects a different winner the loser is demoted to
`is_primary_version=false` rather than removed, and the default primary-only
view kept showing it too.

The write path was never at fault. The bug was entirely in the HTTP response
cache, and it could not self-heal:

- The list cache was keyed on the raw query string alone, and an exhaustive
  search of non-test Go for `listCache.` turned up only `Get` and `Set` —
  there was no `Invalidate` or `InvalidateAll` call for this cache anywhere
  in the codebase. Three separate mutation handlers each invalidated some
  *other* cache and missed this one.
- `Get` moved an entry to the front of the LRU but did not extend its expiry,
  so an entry lived a full 24 hours from its `Set`.
- LRU capacity eviction never reached these entries, because library-page
  keys are the *most* recently used in the cache and capacity eviction takes
  the least recently used.
- The list warmer skipped any query it found already cached, which made it
  structurally incapable of refreshing a stale entry — it would find the
  phantom-row response, conclude the query was warm, and skip it forever.

Only a process restart cleared it.

Fixed by keying the library-list caches on a monotonic generation counter
that the store bumps on `CreateBook`, `UpdateBook` and `DeleteBook`. After a
mutation every reader computes a new key, so pre-mutation entries become
unreachable and age out on their own. This replaces the shape that had
already failed — scattering `InvalidateAll()` calls across mutation handlers
depends on every future handler author remembering, and three of them
already had not.

`UpdateBook` bumps the counter even though it only marks *targeted* quick
queries dirty rather than calling `MarkAllQuickQueriesDirty`. That detail is
load-bearing: `UpdateBook` is the path that demotes a merge loser, so hooking
the counter to `MarkAllQuickQueriesDirty` alone would have left every demoted
row on the page.

Two caches were involved, not one. The HTTP handler's cached response is
built from `GetAudiobooks`, which reads a *second* 24-hour cache inside the
audiobook service whose invalidation is gated behind
`CacheInvalidateOnBookUpdate` — off by default. Generation-keying only the
HTTP cache would have left the deleted books to be served straight back out
of the one underneath it. Both are now keyed on the same counter.

Book-*file* mutations deliberately do not bump the counter. They run once per
file during a scan, and bumping there would hold the list cache at a
near-permanent miss for the duration of every scan.

Both list cache TTLs drop from 24 hours to 10 minutes. With generation keying
doing the real work, the TTL is now only a backstop for mutation paths that
bypass the store's three book-level writes.

### Added

#### `POST /api/v1/cache/invalidate` (admin) for clearing a stale cache

Every existing cache route was read-only, so the only remedy for cache
staleness was restarting the process — roughly ten minutes of unusable
library while the in-memory query layer warms back up. The new admin-gated
endpoint takes an optional `{"cache": "list"}` body, defaults to every
registered cache, and returns per-cache drop counts read *before*
invalidating, so an operator gets confirmation of what was actually cleared
rather than a bare `200`. A mistyped cache name is rejected rather than
answered with "dropped 0", which would read as "already clean".

This is not made redundant by the generation counter: the counter only
advances on the store's three book-level writes, so batch writes, direct
in-memory writes and index-only edits still bypass it. The endpoint is the
lever for those.
