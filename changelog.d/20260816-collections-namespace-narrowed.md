### Fixed

- **`/api/collections` no longer 404s the routes the native API serves.** The ABS
  compatibility layer reserved the whole `/api/collections` subtree, which was
  correct for exactly one commit — the commit that added the native
  `/api/v1/collections` twin turned that reservation into the playlists defect
  (#2332 → #2333 → #2335): `GET /api/collections`, `PUT /api/collections/:id` and
  `POST /api/collections/:id/materialize` are served only by the native API, and
  the wide reservation converted their redirects into 404s on every deployment.

  Collections now uses the mechanism built for this case — a method-aware route
  list gated on `ABSAPIEnabled` — so with the ABS surface off the namespace
  redirects exactly as before, and with it on ABS claims precisely the six routes
  it registers. The matcher was generalized from "exactly one trailing segment" to
  a gin-style pattern match, since ABS serves `/api/collections/:id/book/:bookId`.

- **Reading a dynamic collection no longer writes to the database.** Its
  re-evaluated membership was persisted unconditionally, so every `GET` was an
  fsync and a version bump, and the `updatedAt` it moved is what the ABS DTO
  exposes as `lastUpdate` — a client caching on it would re-fetch an untouched
  collection forever. It now persists only when membership actually changed.
