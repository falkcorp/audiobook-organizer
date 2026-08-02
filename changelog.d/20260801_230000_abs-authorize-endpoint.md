<!-- file: changelog.d/20260801_230000_abs-authorize-endpoint.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b3a97e2-1c48-4f5d-83b0-d29e70fa514c -->
<!-- last-edited: 2026-08-01 -->

### Fixed

- **`POST /api/authorize` was missing, so Audiobookshelf clients could log in and
  then silently lose their session.** The route was never registered, so gin's
  path-fixer answered it with a `301` to `/api/v1/authorize`. A `301` permits a
  client to downgrade the method, so the app re-issued the call as `GET`, took a
  `404` from the app API, and never refreshed — presenting as "connected" and then
  failing on the next authenticated request. Observed in production on 2026-08-01:
  `POST /api/authorize → 301`, `GET /api/v1/authorize → 404`, then
  `GET /api/libraries → 401` fifty seconds later.

  The endpoint now re-validates the caller's existing credential and returns the
  standard authorize payload. It **echoes** the presented access and refresh tokens
  rather than minting new ones, so an app foregrounding repeatedly no longer creates
  a session row per call.

  It carries the same §1.8.1 obligation as `/api/me`: AudioBooth's
  `MediaProgress.syncFromAPI` **deletes** every local progress row absent from
  `user.mediaProgress`, and it calls this endpoint on foreground — so a `200` with an
  empty or partial list would erase the user's position in every omitted book on
  every app launch. The handler therefore answers `5xx` rather than ever serving a
  short list, and a test asserts it.

  `/api/authorize` was also added to `absReservedPaths`, without which the `301`
  would return even with the handler present. The existing
  `TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute` guard derives its input
  from `absRouteList()`, so it now enforces that pairing automatically.

- **`GET /api/me/listening-stats` returning `404` is correct and was left alone.**
  The ABS sync spec prefers a `404` there (~12 non-optional fields; callers use
  `try?`), so a half-correct body would be worse than none.
