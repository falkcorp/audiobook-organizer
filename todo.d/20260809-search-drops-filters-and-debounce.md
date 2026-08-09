<!-- file: todo.d/20260809-search-drops-filters-and-debounce.md -->
<!-- version: 1.0.0 -->
<!-- guid: a17c53e9-4820-4d6b-b95f-e3086c2741da -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Typing in the library search box silently drops every active filter and the sort
      order.** `useLibraryQuery.ts:192-193` branches on whether there is search text:

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
