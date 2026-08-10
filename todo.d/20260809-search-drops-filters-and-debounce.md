<!-- file: todo.d/20260809-search-drops-filters-and-debounce.md -->
<!-- version: 2.0.0 -->
<!-- guid: a17c53e9-4820-4d6b-b95f-e3086c2741da -->
<!-- last-edited: 2026-08-09 -->

- [x] **FIXED — both halves.** Typing in the library search box silently dropped every
      active filter and the sort order, and queried on every keystroke.

      > ### ✅ The debounce half of this is FIXED (#2264) — and the original diagnosis was wrong
      >
      > This fragment said the search box "is not debounced at all". **A 300ms debounce
      > existed the whole time.** What was actually happening is worse and more specific:
      > `useLibraryQuery.ts:165` reads
      >
      > ```ts
      > const searchText = parsedSearch ? parsedSearch.freeText : debouncedSearch;
      > ```
      >
      > so the moment a search parses — which is always, once you type — the debounced
      > value is **ignored** and the raw parsed value is used. `parsedSearch` also sits in
      > that hook's `useCallback` dep array, so `loadAudiobooks` was recreated per
      > keystroke. The debounce was real, correct, and **dead code on the only path that
      > matters.**
      >
      > Fixed by moving `parsedSearch` and `searchQuery` off the same 300ms timer, rather
      > than debouncing one and leaving the other raw — debouncing only the free text would
      > let it disagree with the field filters mid-flight. `SearchBar`'s own UI still gets
      > the raw value so chips react instantly; `useLibrarySelection` gets the debounced one,
      > because "select all matching" must mean the query that produced the visible rows.
      >
      > `test.fixme('search debounces input to avoid excessive requests')` is now a real
      > passing test (search-and-filter.spec.ts: 11 passed / 1 skipped, exit 0).
      >
      > ### ✅ The filter-dropping half is ALSO fixed (#2265)
      >
      > It was a **branch**, not a missing capability:
      >
      > ```ts
      > searchText
      >   ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed, signal)
      >   : api.getBooks(itemsPerPage, offset, { sortBy, sortOrder, tags, libraryState, filters, ... })
      > ```
      >
      > Every option lived on the `getBooks` side only, so typing one character crossed to
      > a call that sends four parameters — dropping `library_state`, tags, field filters
      > and the sort order.
      >
      > **The server was never the problem.** `GetAudiobooks` applies the same post-filters
      > on the search path (`service_query.go:226`); it was simply never told about them.
      >
      > Fixed by collapsing the branch rather than adding nine parameters to
      > `searchBooksPage`: `getBooks` hits the same endpoint with the same
      > `is_primary_version`, so it only needed a `search` option. **One code path now** —
      > which also means a future filter cannot be added to one branch and forgotten in the
      > other, which is exactly the class of bug this was.
      >
      > `searchBooksPage` had exactly one production caller (checked); it is now
      > `@deprecated` with the reason rather than removed.
      >
      > `test.fixme('search works with other filters combined')` is a real passing test.
      > Verified: search-and-filter + library-browser, **33 passed / 0 failed / 0 skipped,
      > exit 0.**
      >
      > Lesson worth keeping: "feature X is missing" and "feature X exists but is bypassed"
      > look identical from the outside and have completely different fixes. Grep for the
      > mechanism before concluding it is absent. `useLibraryQuery.ts:192-193` branches on whether there is search text:

      ```ts
      searchText
        ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed, signal)
        : api.getBooks(itemsPerPage, offset, { sortBy, sortOrder, tags, libraryState, filters, ... })
      ```

      `api.searchBooksPage` (`web/src/services/api.ts:1023-1037`) sends only `search`,
      `limit`, `offset`, `is_primary_version` and optionally `show_quarantined`. **No**
      `library_state`, **no** `filters` (author/series/genre/language), **no** `tags`,
      **no** `sort_by`. So filtering to Organized and then searching an author returns
      matches from every state — while the Filters button keeps showing its count, so the
      filter still looks applied. Same family as the Deleted-filter cache bug fixed in
      #2230: a filter that silently does nothing is indistinguishable from one that
      matched everything. Covered by a `test.fixme` in
      `web/tests/e2e/search-and-filter.spec.ts`.

- [ ] **The library search is not debounced at all.** Measured 2026-08-09: typing the ten
      characters of "Foundation" fires **ten** requests to `/api/v1/audiobooks?search=…`,
      exactly one per keystroke. The e2e test is literally named "search debounces input
      to avoid excessive requests" and asserts `<= 3`; it has been marked `test.fixme` so
      it fails loudly as an unexpected pass once a debounce lands. On a large library each
      of those is a full-text query, so this is directly relevant to the backend-filtering
      work — no amount of server-side improvement helps if the client sends ten queries
      for one search. Related: the richer-backend-filtering TODO item.
