### Fixed

- **Every series reported an empty book list and zero duration — 14,625 of 14,625.**
  `LibrarySeries` hardcoded `"books": []any{}` and `"totalDuration": 0` on every row,
  while `numBooks` was populated correctly (14,295 of 14,625 non-zero). The app therefore
  showed a series insisting it held no books while displaying a count next to it. The
  series list now carries its own books, in sequence order, with `totalDuration` summed
  over the books actually served — as an int, never a float, since Dart throws on
  `42.0 as int?` during widget build and red-screens the series tile.

  The book lists are built from ONE pass over the visible set, cached for 5 minutes, and
  scoped by the same filter `/items` uses. Per-series lookups would have been 14,625 calls
  through a getter that falls back to a full Pebble scan when memdb is cold — a
  library-freezing bug rather than a slow endpoint. Using a narrower filter than `/items`
  would have made the Series tab disagree with the Library tab about what the library
  contains, which is how ~28,000 unorganized rows once leaked into the Authors tab.

- **Opening a playlist showed nothing.** The playlist LIST route shipped without the
  DETAIL route, so `GET /api/playlists/:id` fell through into a 301 to
  `/api/v1/playlists/:id` — the app-API twin — which answers `{"book_ids":[...]}` instead
  of ABS's `{"items":[{"libraryItem":…}]}`. The client followed the redirect, received
  HTTP 200 and valid JSON in the wrong shape, and rendered an empty playlist. Nothing
  logged an error, because nothing errored.

  The detail route is now served natively. Ownership is enforced in the handler and
  answers 404 rather than 403, because the underlying store getter resolves any playlist
  by id **without scoping to the owner** — without that check any authenticated user could
  read another user's playlist and its contents by guessing an id, and a 403 would confirm
  which ids exist.

### Changed

- The ABS surface now reserves `/api/playlists/<id>` from the `/api/*` → `/api/v1/*`
  compatibility redirect, **but only when `abs_api_enabled` is true**. The redirect
  middleware is not gated on that flag, so reserving the path unconditionally would make a
  working app route 404 on every ABS-disabled deployment — the defect that broke 46 live
  app routes twice (#2332 → #2333 → #2335). The bare `/api/playlists` list is deliberately
  NOT reserved and still reaches the app API; only the detail sub-tree is claimed. Both
  directions are pinned by tests, and no engine-level routing was touched.

### Known gaps

- **Collections remain unimplemented and that is honest, not a regression.** There is no
  `Collection` model, store or route anywhere in the codebase, so the empty response has
  nothing behind it to serve — unlike the playlist route, whose empty response was hiding
  a fully populated model. Building it is a new entity end to end and is costed
  separately rather than silently attempted.
