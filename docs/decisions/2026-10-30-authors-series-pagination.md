<!-- file: docs/decisions/2026-10-30-authors-series-pagination.md -->
<!-- version: 1.0.0 -->
<!-- guid: 71937198-3754-4ca2-a744-c5826701d95c -->
<!-- last-edited: 2026-10-30 -->

# Authors and Series pages: server-side pagination (WEB-01, WEB-02)

TODO items WEB-01 and WEB-02; briefs TASK-324 and TASK-327. The owner was not asked; each open choice below took the recommended option and lists what was rejected, for review.

## What was already there

`GET /authors` already honoured `limit`/`offset` (sliced after the cache, "omit limit and you get everything"). It had no search, filter or sort. `GET /series` had no parameters at all. Both pages ignored the paging and filtered, sorted and sliced ~17k rows in the browser.

## Decision log

1. **Slice the cached list, do not push into the store.** The expensive part of both endpoints is building the list (book/file counts, aliases, author names). One cached build serves every page, search and sort. Filtering and sorting 17k rows per request is cheap next to that. *Rejected:* store-level paging queries (loses the cache, and the store has no sorted-by-count index); a per-sort cache (more invalidation surface for no measured gain).
2. **Parameters:** `limit`, `offset` (kept, same as `getBooks`), `q`, `filter`, `sort`, `order`. The brief said `page`/`search`; `limit`/`offset` was already shipping on authors and is what the Library page uses, so `page` was not added. Filter vocabulary is the one each page already used (`with_books|zero_books|all` for authors, `with_books|empty|all` for series).
3. **Response:** `count` is matches across all pages (so the pager and "N shown" are right), new `total` is the whole table (the "N total" header and "(N total)" series note). Previously `count` was always the whole table; with no filter the two are equal, so nothing that reads `count` unfiltered changes.
4. **Unparameterised requests still return the whole list, unsorted.** Callers that need it: the Library page's author and series filter dropdowns (`getAuthors`, `getSeries` in `useLibraryFilters`). `getAuthorsWithCounts` had one caller (the Authors page) and was removed in favour of `getAuthorsPage`. *Rejected:* a default page size (would silently truncate the Library dropdowns).
5. **Validation:** a negative `limit`/`offset`, an unknown `sort`/`filter` or an `order` other than asc/desc is a 400; an unparseable (non-numeric) `limit`/`offset` is still ignored, as before. The window clamp in `audiobooks.window` is also total over every int, so `MaxInt` cannot overflow. This replaces the earlier behaviour of ignoring a negative offset. *Rejected:* silently ignoring a bad sort (the table header would then show a direction the data does not have).
6. **Sort semantics:** names compare lower-cased and byte-wise on the server, with ties broken by id ascending in both directions so a page boundary is stable. The browser used `localeCompare`, so names with accents or punctuation can order slightly differently (for example "Émile" sorts after "Zed" byte-wise). *Rejected:* `x/text/collate` (a new dependency and per-request key building for a cosmetic difference in a list of mostly ASCII names).
7. **Search:** authors match the name or any alias name; series match the series name or its author name; case-insensitive substring, same as before. Debounced 300 ms in the page (`useDebouncedSearch`, the hook the review lanes use) so typing does not send a request per keystroke.
8. **Series "Created" column is no longer sortable.** Series rows carry no creation time (the server never sends `created_at`), so sorting by it was already a no-op and the server has nothing to sort by. The column is still listed in the column picker. Cheap to restore if a creation time is ever stored.
9. **Selection across pages.** Selection stores the rows, not only ids, because merge and bulk-delete dialogs name the selected authors/series and a row on another page is no longer loaded. It persists across page turns, sort and search changes (as it did when everything was in memory). The header checkbox selects or clears the CURRENT PAGE's rows only and leaves rows selected elsewhere alone. There is no "select all matching" mode: nothing on these pages should merge or delete every row matching a search. *Rejected:* `useRowSelection` (clears on filter change and its all-matching mode materialises every key); clearing selection on page turn (breaks merging two authors found on different pages, which worked before).
10. **After a merge, delete, split, rename or undo, only the current page is refetched** (the same `loadAuthors`/`fetchSeries` the effect uses). If that empties the last page, the page steps back to the new last page. Responses from superseded requests are discarded, so a slow earlier page cannot overwrite a newer one. Stored selection rows are refreshed from each fetched page so a rename shows up in the merge dialog.
11. **Sort and search reset to page 0;** filter change did already.

## Not done

- No server-side `created_at` for series (see 8).
- Executive summary not written: a performance change with no data-loss angle does not meet the criteria in `docs/process/executive-summaries.md`.
