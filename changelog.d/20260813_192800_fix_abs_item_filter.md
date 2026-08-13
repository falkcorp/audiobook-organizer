### Fixed

- **AudiobookShelf API: the `filter` query parameter was read by nothing.** Every
  filtered request — opening a series, a genre, a narrator — returned the entire
  library, unfiltered and in default order, which is why the app appeared to show
  "random books" for every series. `GET /api/libraries/:id/items` now honours the
  `filter=<group>.<base64 value>` form ABS clients send: `series.<id>` returns that
  series' books in sequence order, and any group we do not implement yet returns an
  empty page (and logs the group name) instead of silently falling through to the
  whole library.
