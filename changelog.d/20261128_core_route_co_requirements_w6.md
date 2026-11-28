### Added

#### `app.Options.RouteCoRequirements` — an app can AND its own scope onto a core route (W6)

A core route may require scopes of core's own namespace only (R6), so core could
not express ABO's `integrations.manage` atom, which is
`AllOf(core:integrations.manage, books:integrations.manage)`. The app now names
a core route in `app.Options.RouteCoRequirements` (core route name to
`rbac.Requirement`) and the router ANDs the requirement onto the route's own:
`effective = AllOf(own permissions + co-requirement permissions)`. It can only
narrow; it is never an OR and never a replacement. `checkPerm` still judges the
route's own `Perm` unchanged: the co-requirement lives on the route entry
beside it, and the 403 body, the `core.permission.deny` audit event, the
OpenAPI `Permission` and `Routes()` all show the combined requirement.

Boot is refused with a named error when an entry names a route that does not
exist (`ErrCoRequirementRoute`), is not `AllOf` or `Need`, or targets a route
whose own requirement is not `AllOf` (`ErrCoRequirementShape`), names Public,
AnyAuthenticated, `core:admin` or a scope of a namespace core owns
(`ErrCoRequirementScope`), or names a scope the rbac catalog has not registered
(`ErrCoRequirementUnregistered`, also `ErrUnknownScope`). An empty or nil map
behaves exactly as before. ABO's map is empty in this change; the
`books:integrations.manage` entries for the six integrationhttp manage routes
are a B3-a follow-up.

#### `archtest.CoreScopesOnly` — no non-test core code names a foreign scope

An AST scan over core's non-test Go files that fails when a `Need`, `AllOf`,
`AnyOf`, `ParsePermission`, `ParsePattern` argument, a `Permission(...)`
conversion, a Permission-typed const, var, slice or map literal, or a
`PermissionSpec` name carries a namespace other than `core`. Test-support
packages and `examples/` are exempt.
