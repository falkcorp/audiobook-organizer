- [ ] **LIB-DATE-ADDED** Library view: search and "sort by date added" give
      poor results, while the phone app's Recently Added shelf (the ABS API,
      `addedAt` mapped to `created_at` in `internal/server/handlers/abs/browse.go`)
      shows the right books. Find why the web Library view disagrees with that
      shelf for the same sort (a different field, a stale index, or search
      ranking overriding the sort), and make the two agree. Done means the first
      page of the Library view sorted by date added, newest first, lists the
      same books in the same order as the phone app's Recently Added. Low
      priority: after the platform-core plan (owner, 2026-11-27).
