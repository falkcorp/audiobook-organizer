### Fixed

#### Authors and Series pages fetch one page at a time (WEB-01, WEB-02)

The Authors and Series pages used to download the whole table (about 17,000 authors with their alias lists) on every visit and after every merge, delete, split and undo, then search, sort and page it in the browser. `GET /authors` and `GET /series` now take `limit`, `offset`, `q`, `filter`, `sort` and `order`, answer with `count` (matches across all pages) and `total` (the whole table), and reject a negative `limit`/`offset`, an unknown `sort`/`filter` or a bad `order` with a 400. Both pages request just the page on screen, driven by the pager, the page size, the debounced search box, the filter and the column sort, and refetch only that page after a change. Selection now persists across pages; the header checkbox selects the current page only. A request with none of the new parameters still returns the whole list, because the Library page's author and series filters read it that way.
