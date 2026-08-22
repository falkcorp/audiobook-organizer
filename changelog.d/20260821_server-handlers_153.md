### Added

#### `POST /api/session/local` — the offline-upload probe no longer 404s the ABS connection

The Audiobookshelf-compatible surface registered `/api/session/:id/sync` and
`/api/session/:id/close` but had no `/api/session/local`, so every request to it fell
through to the SPA catch-all. That matters because ShelfPlayer sends this endpoint after
every play/pause with `maxAttempts:1` and treats a 404 as "the connection is offline"
(design spec §1.8.8 item 1); §1.9.1 softens it to non-fatal for the two clients we
actually target, but the minimum bar is still a 2xx with a non-empty body. The new
handler is deliberately a stub: it authenticates, then answers the same bare `OK` that
`/sync` and `/close` answer via `respondPlainOK`, and persists nothing. Its sibling
`/api/session/local-all` is the endpoint that will actually apply offline progress. The
route is also added to `absRouteList()` so the reserved-prefix guard test covers it —
without that entry the route would 301 into the `/api/v1` app API and look implemented
while returning the wrong shape.
