- [ ] **SEARCH-CACHE** Search results are not cached anywhere on the server.
      Every keystroke-debounced query re-runs the full Bleve search plus the
      book hydration behind it.

      **Measured by reading the code 2026-08-11:** `AudiobookService` already
      owns a `listCache`, but it is consulted only on the **non-search** branch
      of `GetAudiobooks` (`internal/audiobooks/service_query.go`, cache key
      `all:<limit>:<offset>:p=…:sb=…:asc=…:noq=…`). The `if search != ""` branch
      goes straight to `searchWithBleve` / `store.SearchBooks` and never touches
      a cache on the way in or out.

      The frontend has `web/src/stores/useLibraryCache.ts` (50 entries, LRU-ish
      eviction), so a repeated query from the *same* browser tab may be served
      client-side — but nothing is shared between users, tabs, or the mobile
      app, and a cold tab always pays full cost.

      **Why it is worth doing:** the per-user post-filter path fetches a
      `searchPostFilterWindow`-sized candidate set from Bleve and narrows it in
      Go, so a search is markedly more expensive than a plain list page. That is
      also the reason the cache key cannot be the query string alone.

      **Cache key must include, or it will serve wrong results:**

      - the query string,
      - `limit`/`offset`,
      - `UserID` whenever per-user filters are active (`PerUserFilters` +
        `UserID`) — per-user narrowing happens AFTER Bleve returns, so two users
        running the same query legitimately get different sets,
      - sort field and direction,
      - every `ListFilters` value that participates in post-filtering
        (`LibraryState`, `Tag`/`Tags`, `FieldFilters`, `IsPrimaryVersion`,
        fingerprint status/coverage bounds).

      ⚠️ **Invalidation is the hard part, and it is the same gap as
      [MERGE-CACHE-EVICT].** A cached search that outlives an edit, merge or
      delete shows books that no longer exist, which is exactly the "I merged
      these and still see two copies" confusion. Prefer wiring it to the
      existing search-index dirty-set/reconciler
      (`internal/server/search_reconciler.go`) rather than a bare TTL — or use a
      short TTL (30–60s) as an explicit, documented first cut and say so in the
      log.

      Do NOT cache before deciding invalidation. A stale search result is worse
      than a slow one.
