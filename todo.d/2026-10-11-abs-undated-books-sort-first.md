- [ ] **ABS-UNDATED-BOOKS-SORT-FIRST** A book with no `CreatedAt` sorts FIRST on every
      newest-first sort in the store, not just in the ABS home shelf. `compareSortInts`
      (`internal/database/book_sort.go`) puts a missing key (`missingSortKey`) after every
      present value ascending, and `SortBooks` / the memdb `GetReverse` walk negate that, so
      a descending walk serves the undated rows at the head. The Recently Added shelf now
      excludes them with a `Predicate` (`internal/server/handlers/abs/browse.go`,
      `recentlyAddedIDs`), but the Library tab's `sort=addedAt&desc=1` and every other
      descending sort on a nullable field (year, narrator, duration, ...) still lead with
      them. First count the undated visible rows in prod (primary, organized, not
      quarantined, `created_at` nil or zero): `CreateBook` always stamps the date, so the
      number may be ~0 and the problem moot. If it is not, pick: (a) backfill `CreatedAt` on
      those rows (from `UpdatedAt` or the first `book_file.CreatedAt`), which fixes both
      surfaces without touching sort semantics and is the lower-risk option; or (b) change
      `SortBooks` to sort missing values last in both directions, which also needs the memdb
      reverse walk (skip the missing-key prefix, emit it after), the Pebble fallback, the
      `"~"` title sentinel and the sort tests (`memdb_summaries_sorted_page_test.go`,
      `pebble_summaries_fallback_sort_test.go`, `sort_every_field_test.go`,
      `sort_indexed_path_test.go`) to move together, and is a user-visible change to every
      descending sort on the web and ABS Library tabs.
