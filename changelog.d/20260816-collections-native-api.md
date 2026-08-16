### Added

- **Native collection API** at `/api/v1/collections` (list, get, create, update,
  delete, materialize). This is the only surface that can create a **dynamic**
  (query-backed) collection: Audiobookshelf has no such concept, so everything
  created through the app is static.

  A dynamic collection is evaluated once at creation and refreshed whenever its
  query changes, whenever it is read through this API, and whenever it is explicitly
  materialized — reusing the same query engine as smart playlists. If the search
  index is unavailable, creating one still succeeds and stores the query — an index
  that happens to be closed does not discard what the user typed.

  **"Dynamic" means *manually* refreshed in this first version.** Nothing
  re-evaluates in the background, and the ABS read path deliberately never
  evaluates — it serves the stored membership — so a dynamic collection created
  here and then only ever viewed in the app shows its creation-time membership
  until something refreshes it. Tracked in `todo.d`.

  Every write requires the `collections.manage` permission. Reads are open to any
  authenticated user, because collections are server-wide.
