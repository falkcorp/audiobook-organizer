### Fixed

#### ABS client — series search tiles were black, and the author page opened from a book was empty

`GET /api/libraries/:id/search` served every series hit with an empty `books`
array. The app draws a series tile from its books' covers, so every series hit
rendered black while still opening the series on tap (the id was right). Series
hits now carry the series' books through the same renderer `/series` and
`/series/:id` use, plus a nested `series` object where real ABS puts the
identity fields, so clients reading either shape work. Hits are capped at the
search limit because each one hydrates its books.

`GET /api/authors/:id` ignored `?include=items,series` and returned the bare
author row, which the app rendered as an empty author page. Both expansions
are now honoured: `libraryItems` from the same contributor index the author
tile count and the `?filter=authors.<id>` drill-down read, and `series` grouping
those items by series.

### Changed

#### ABS search: results cached for two minutes, and the per-search genre scan removed

Profiled on production 2026-09-05: a search took 7.45 s, of which 4.79 s was
`GetDistinctGenres` walking the whole book keyspace on every call. Search now
reads genres from the already-cached `/filterdata` document. On top of that,
each finished search document is replayed for the same library and
(case-folded) query for two minutes, single-flighted and bounded to 256
queries, as the user asked, so backing out of a result and searching again is
instant.
