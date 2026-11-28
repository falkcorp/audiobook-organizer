### Added

#### `app.Options.RouteCoRequirements` — an app can AND its own scope onto a core route (W6)

A core route may require scopes of core's own namespace only (R6), so core could
not express ABO's `integrations.manage` atom, which is
`AllOf(core:integrations.manage, books:integrations.manage)`. The app now lists
`httpapi.CoRequirement{Route, Param, Value, Need}` entries in
`app.Options.RouteCoRequirements` and the router ANDs each `Need` onto the named
core route's own requirement. It can only narrow; it is never an OR and never a
replacement.

An entry covers the whole route (`Param` and `Value` empty) or, for a route that
serves one operation per kind, only the requests whose path wildcard `Param`
equals `Value` (for example `getIntegrationInstanceHealth`, `kind`, `download`),
matched case-insensitively so a handler that folds case is no way round it. A
route may carry a route-wide entry and any number of per-value entries; a
request must pass the route's own requirement and every entry that applies to
it.

Stage 13 judges the route's own requirement and then each applicable entry
**separately** (`AuthzRequest.CoPerms`), never as one merged `AllOf`. The
auth-disabled checker drops the `AuthRequired` members of an `AllOf` before it
judges it, so a merged requirement would have admitted the auth-disabled
principal on a route whose own requirement refuses it (review round 1,
blocker). Judged apart, a co-requirement cannot widen access for any principal.
`checkPerm` still judges the route's own `Perm` unchanged: the entries live on
the route entry beside it. `Routes()` shows the route-wide entry combined into
`Perm` (display only) and the per-value entries in `RouteInfo.CoValuePerms`; the
OpenAPI operation carries them as `x-mediacore-co-requirements`.

Boot is refused with a named error when an entry names a route that does not
exist (`ErrCoRequirementRoute`), is not `AllOf` or `Need`, or targets a route
whose own requirement is not `AllOf` (`ErrCoRequirementShape`), names Public,
AnyAuthenticated, `core:admin` or a scope of a namespace core owns
(`ErrCoRequirementScope`), names a scope the catalog flags Sensitive or
Confining (`ErrCoRequirementRestricted`: a Sensitive scope is held by no API key
without an interactive method, so it would lock an administrator's key out of a
route its own requirement admits), names a scope the rbac catalog has not
registered (`ErrCoRequirementUnregistered`, also `ErrUnknownScope`), has a
`Param` that is not a single-segment wildcard of the route's pattern or a
`Param` or `Value` set alone or a `Value` with `/`, `{` or `}`
(`ErrCoRequirementParam`), or repeats another entry's `(Route, Param, Value)`
(`ErrCoRequirementDuplicate`). An empty or nil list behaves exactly as before.
ABO's list is empty in this change; the entries for the six integrationhttp
manage routes and the download kind of the health route are a B3-a follow-up.

#### `guards.CoreScopesOnly` — no non-test core code names a foreign scope

A typed scan (go/types, `archtest/guards`) that fails when a constant-valued
expression of type `rbac.Permission`, or a constant string given to
`ParsePermission` or `ParsePattern`, names a namespace other than `core`: an
untyped const to `Need`, `"ns"+":x"`, `Permission(ns+":x")` over constants,
`Checker.Has`, `httpapi.Forbidden`, a Permission-typed field, slice or map key,
a return or assignment, a `PermissionSpec` `Name` or `Implies`, through an
import alias too. A scope built at run time (`fmt.Sprintf`, a variable) is a
documented gap, pinned by a fixture. It replaces the AST-only
`archtest.CoreScopesOnly`, which found none of those forms; test-support
packages (including the packages below a `<pkg>/<pkg>test` suite) and
`examples/` are exempt.

### Changed

#### Route co-requirements are a list, not a map

`httpapi.Options.CoRequirements`, `kernel.Config.RouteCoRequirements` and
`app.Options.RouteCoRequirements` are `[]httpapi.CoRequirement` instead of
`map[string]rbac.Requirement`, and `httpapi.AuthzRequest.CoPerm` is `CoPerms`.
No shim: the map form shipped in the same PR train and had no caller outside it.
