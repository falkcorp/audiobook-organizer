### Fixed

- **The ABS playlist-detail reservation was wider than what ABS actually serves,
  and it 404'd six working app routes.** Claiming the playlist detail route for the
  AudiobookShelf API reserved the whole `/api/playlists/` subtree, while ABS answers
  exactly one route in it (`GET /api/playlists/:id`). On an ABS-enabled deployment
  the unversioned forms of `PUT`/`DELETE /api/playlists/:id`,
  `POST /api/playlists/:id/books`, `DELETE /api/playlists/:id/books/:bookID`,
  `POST /api/playlists/:id/reorder` and `POST /api/playlists/:id/materialize`
  stopped redirecting to their `/api/v1` twins and started returning 404. The
  reservation now matches on method plus exactly one path segment, so anything
  deeper or with a different verb keeps its redirect.
