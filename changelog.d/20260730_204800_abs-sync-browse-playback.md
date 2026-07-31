<!-- file: changelog.d/20260730_204800_abs-sync-browse-playback.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d61f0ac-4b28-4e35-9a13-c0f582d64719 -->
<!-- last-edited: 2026-07-30 -->

### Added

- **Library browse over the Audiobookshelf-compatible API (abs-sync Phase 3).** `GET /api/libraries`,
  `/api/libraries/:id` and its `items` (paged), `personalized`, `series`, `authors`, `narrators`,
  `filterdata`, `search`, `collections`, `playlists` and `recent-episodes` sub-routes, plus
  `GET /api/items/:id` and `GET /api/items/:id/cover`. Covers serve with **no credentials required**
  (AudioBooth's home-screen widget sends none) while also accepting `?token=`, and carry
  ETag/Last-Modified so clients cache them. A client probing for podcasts gets a valid empty page
  rather than an error.

- **Direct playback over the Audiobookshelf-compatible API (abs-sync Phase 5b).**
  `POST /api/items/:id/play` returns a session with cumulative-offset `audioTracks`, chapters and the
  full embedded library item; `GET /api/items/:id/file/:ino` (and `/download`) and the
  **unauthenticated `GET /public/session/:id/track/:index`** — the path AudioBooth actually streams
  from — both serve byte ranges via the verified `httputil.ServeFileWithRange`, including the suffix
  ranges iOS `AVPlayer` needs to locate `moov` in an m4b. `POST /api/session/:id/sync` accepts both
  `timeListened` (a delta) and `timeListening` (a cumulative total) with their differing semantics,
  and writes the listener's position through to the durable progress store forward-only, so a stale
  device can never rewind newer progress. Direct play only: a client requesting HLS or a transcode
  gets a working direct-play session instead of an error.

  Client-visible ids come from the `sync_item`/`sync_file` keyspaces — `libraryItemId` is a 36-char
  UUID that follows dedup merges, and `ino` is a durable file id rather than a filesystem inode — so
  moving, retagging or merging a book does not orphan a device's saved place or a downloaded book's
  cached URLs. Every response is diffed field-by-field against golden fixtures captured from a real
  Audiobookshelf 2.36.0 server. The whole surface stays behind `ABS_API_ENABLED`, which is off by
  default.
