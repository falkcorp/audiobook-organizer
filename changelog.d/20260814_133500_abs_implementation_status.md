### Added

#### ABS implementation ground-truth status document

`docs/reference/abs-implementation-status.md` classifies every route on the
Audiobookshelf-compatible surface — 49 in production (45 unconditional + 4
bookmark routes gated on a store the wiring asserts) — as value-conformant,
handler-tested, referenced-only, untested, or stub, each with its evidence.
Derived from the real `router.Routes()` table and a read of
`Handler.Register`, never from grep or the stale 2026-08-11 audit.

Findings recorded along the way: N-1 (socket.io) and N-2 (value comparison)
are fixed; all 28 golden fixtures are referenced by tests (N-7 apparently
closed); `absRouteList()` still under-reports by exactly the two OpenID
routes (N-8, now precise: 47 listed vs 49 real); five fixtures do carry query
parameters, narrowing the params-sweep fragment's premise; collections is the
single remaining stub route and the only missing entity.
