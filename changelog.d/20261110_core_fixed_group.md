### Added

#### httpapi: CoreFixedGroup and CoreExactPatterns, with Build's exact-pattern rule (dark)

`httpapi.Router.CoreFixedGroup(base)` returns a core group at a HEAD path kept where it is, and `httpapi.CoreExactPatterns()` lists the only two core patterns allowed inside another owner's claim: `GET /api/v1/auth/oauth/{provider}/callback` and `GET /api/v1/auth/oauth/{provider}/start`. Fixed-group routes run the whole core chain and record owner `core`; the group claims nothing, so it does not collide with the legacy mount's `/api/v1/` claim and its two entries raise no `ErrShadow`. `Build` fails with `ErrPrefixCollision` for a fixed-group route outside the list and for a legacy manifest route that matches a path of an entry core serves. A router that registers no fixed-group route accepts and refuses exactly what it did before. No app registers a fixed-group route yet (the OIDC handlers and the mount arrive in F4-05 and F4-08), so no route, schedule or stored row changes.
