### Fixed

#### `absRouteList()` now includes the two OpenID routes

The hand-maintained ABS route list was missing `GET /auth/openid` and
`GET /auth/openid/callback` from their introduction (the N-8 audit finding,
made precise by the 2026-08-14 `router.Routes()` dump: 47 listed vs 49 real).
The list feeds the startup route log and the reserved-path guard tests, so the
OpenID surface was invisible to both. A membership test now pins the entries.
