### Fixed

- **AudiobookShelf API: the series list ignored `page`, `limit` and `sort`.**
  Confirmed against production: `?limit=100` and `?limit=500` both returned all
  14,625 series, and the app's own `?limit=50&page=2&sort=name` got page 0,
  unsorted, every time. `GET /api/libraries/:id/series` now sorts deterministically
  (name-ignoring-prefix, id as tie-break) and honours `page`/`limit`, reporting the
  full series count as `total` so the client can tell more pages exist. An absent
  limit or `limit=0` still returns everything, so non-app callers are unchanged.
  This also keeps the newly-populated `books` array from turning the unpaginated
  response into a ~10.8 MB payload.
