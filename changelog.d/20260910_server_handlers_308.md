### Fixed

#### SSE handler no longer overrides the app's CORS allowlist

`GET /api/events` (the Server-Sent Events stream) was hardcoding
`Access-Control-Allow-Origin: *` in its own handler, which silently replaced
the allowlisted origin that the app's CORS middleware had already set for
that request, while leaving `Access-Control-Allow-Credentials: true` in
place — an invalid header combination. The SSE handler now leaves CORS
headers to the existing middleware, matching the restrictive, origin-checked
policy used by the rest of the app.
