<!-- file: changelog.d/20260730_213000_abs-sync-userdata-provider.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0595c914-c060-4e4c-8a35-263b9852403e -->
<!-- last-edited: 2026-07-30 -->

### Added

- **Audiobookshelf user progress + bookmarks (Phase 6).** `GET /api/me`, `POST
  /login` and `POST /auth/refresh` now serve the user's real `mediaProgress[]`
  and `bookmarks[]` instead of empty arrays. New
  `internal/server/handlers/abs/userdata.go` implements the `UserDataProvider`
  interface over the existing listening-progress and named-bookmark keyspaces,
  and `wireABSRoutes` wires it (the startup warning about a missing provider is
  gone).

  This closes the last blocker before an ABS client can resume a book where it
  left off. It is also the endpoint that can destroy data if it under-reports:
  AudioBooth deletes every local progress row absent from the server's list, so
  the list is built complete or not at all -- a read failure anywhere returns an
  error (rendered as 5xx) and the partially-built list is discarded, never
  served. `libraryItemId` is the 36-char sync UUID (a raw Book ULID
  mis-truncates at the client's fixed byte offset of 36), `lastUpdate` is an
  integer millisecond epoch taken from `UserBookState.LastActivityAt`,
  `duration` is always present (`isFinished` with a zero duration zeroes the
  client's saved position, so it is cleared instead), `progress` is a 0.0-1.0
  fraction derived from `currentTime/duration` rather than the store's lossy
  integer percent, and `isFinished` uses the 2-second tolerance so a
  fully-listened book does not sit at 99% forever.

  Both lists are built from user-keyed prefix scans, so the cost is proportional
  to the books that user has touched rather than to the library; the per-book
  follow-up reads run in a bounded worker pool.
