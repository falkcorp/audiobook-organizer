### Added

#### Identity flip groundwork: the gin auth bridge and typed permission denials (not wired yet)

`rbac.Require` and `rbac.RequireAll` now return `*rbac.DeniedError`, which still
matches `rbac.ErrDenied`, keeps the same text, and names the refused scope. A
native route that returns one answers 403 `permission_denied` with
`required_permission`; a handler that wraps the denial in its own error keeps
its body. Nothing calls `Require` in production yet, so no response changes
today. `rbac.RequireRequirement` checks a whole requirement (an `AllOf` such as
the `settings.manage` row) and audits a refusal the same way.

The new `internal/server/legacyauth` package is the bridge the identity flip
will use inside the gin mount. It fills HEAD's context values from core's
principal without authenticating, replaces raw in-handler `auth.Can` checks,
adapts `s.perm`, the admin, credential and owner guards, and resolves the ABS
surface through books' compat handle. It refuses by default: no principal, no
checker, an unknown atom or an atom whose table entry no checker evaluates.
The auth-disabled principal is denied in-handler checks and the credential and
owner guards, as 05-rbac §4.11 states. No route uses it until the flip.
