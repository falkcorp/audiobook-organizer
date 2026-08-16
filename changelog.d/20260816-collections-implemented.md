### Added

- **Collections are implemented on the Audiobookshelf surface.** Creating one from
  the app previously failed: `POST /api/collections` had no route and answered 404
  (measured in production 2026-08-16, five attempts in two seconds), while the list
  route was a stub returning a permanently empty page — so the tab showed "no
  collections" rather than an error.

  The surface now serves list, detail, create, update, delete, and single-book
  add/remove. Collections are **server-wide**: every user sees every collection,
  unlike playlists, which are per-user. Creating, editing and deleting requires the
  new `collections.manage` permission; the admin role receives it automatically on
  the next restart, and reading is open to any authenticated user.

  Both **static** and **dynamic** collections are supported. A dynamic collection's
  members come from a saved query, and the read path serves its last evaluation
  rather than re-running the query, so an unevaluated collection renders empty
  instead of failing the library tab.
