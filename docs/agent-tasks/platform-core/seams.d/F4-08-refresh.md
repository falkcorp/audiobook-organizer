<!-- file: docs/agent-tasks/platform-core/seams.d/F4-08-refresh.md -->
<!-- version: 0.1.0 -->
<!-- guid: 0b808901-8ffd-4527-b276-65f7de3728b5 -->
<!-- last-edited: 2026-11-20 -->

# auth.Service.Refresh (added by F6-03 E4, for F4-08 to keep)

F6-03 slice E4 (#4817, round 1) added `(*auth.Service).Refresh(ctx, *Principal) (*Principal, error)` to `core/auth`, which is F4-08 territory. It is the realtime hub's required `Deps.Refresh`: the hub calls it on every heartbeat and on a `core.rbac.changed.v1`, so a role change reaches an open stream. A separate file from the other F4-08 seams so no open PR collides on it.

## What it does

- It first applies the same standing check `Revalidate` does (now shared as `stands`): the user active and unrestricted; a session alive and at the user's epoch; a key (core `MethodAPIKey` by `CredentialID`, or a compat key by the unexported `keyID`) owned by the user and usable (`keyUsable`: status and expiry); an identity extension's principal before its `notAfter` (round 2, below); any other method at the epoch it was built at. The auth-off binding stands while auth is off.
- A compat surface's own principal (a compat method, no key) is also refused while its user has `PasswordResetRequired`, through `compatSurfaceStanding(u)`, the same check `Compat.PrincipalFor` makes before it builds one. It is deliberately not part of `standing`: core sessions and keys do not check it.
- It then rebuilds through `PrincipalFor` with the principal's own `Method`, and copies `CredentialID`, `keyID`, `AuthTime`, `Assurance`, `ClientAddr` and `notAfter` across. A key principal is rebuilt over the **intersection** of the scopes it was built with and the stored key's scopes (`keyScopes`), so it is never wider than either; an empty intersection is refused (`ErrRevoked`). Anything else is rebuilt over the scopes it was built with.
- **Refresh trusts its caller to pass the request's own principal** (the one stage 12 bound to the request holding the stream), as `Revalidate` does. It re-checks that principal's credential; it does not, and cannot, check that the principal belongs to the request.
- **It never widens.** `Principal` now records `scopes` (what `PrincipalFor` built it over: nil is the method's normal rule, a list a narrowing) and `built` (`PrincipalFor` made it). A narrowed identity-extension assertion or compat principal stays narrowed after a role grant. A principal assembled outside `PrincipalFor` carries no record of its narrowing and is refused (`ErrInvalidCredentials`) rather than rebuilt over the user's roles.
- Errors are `Revalidate`'s: a store failure stays `ErrStoreUnavailable` (the hub retries it), everything else is definite.

## Extension principals end (round 2, M1)

- `auth.Assertion.ExpiresAt` (zero means none) is when the asserted credential ends. Stage 12 records it on the principal as the unexported `notAfter`. For an extension that asserts none, `notAfter` is `core.auth.extension_max_age` after `AuthTime` (after now when `AuthTime` is zero or in the future). The setting is `auth.Settings.ExtensionMaxAge`: default 24h, zero or negative takes the default, capped at `auth.MaxExtensionMaxAge` (168h), read live.
- Stage 12 refuses an assertion already at or past `notAfter` with `expired`. That is a 401 on every request, not only on streams.
- `stands` refuses at or after `notAfter` with `ErrExpired`, which covers both `Revalidate` and `Refresh`. `Refresh` copies `notAfter` and never extends it, so a refreshed principal ends at the original bound.
- `internal/cfaccess` sets `ExpiresAt` from the verified JWT's `exp` (`VerifiedAssertion.ExpiresAt`), so an `ext:cfaccess` stream ends when its Access token does.
- **Not yet wired:** `core.auth.extension_max_age` is declared in `core/kernel/core_sections.go`, but nothing maps `authSettings` onto `auth.Settings` yet (the kernel does not build the auth service). F4-02's wiring (`Deps.Settings` to the config snapshot) must map it to `ExtensionMaxAge`. Until then the 24h default applies.
- Tests: `core/auth/ext_expiry_test.go` covers a past exp refused, a future exp standing until it, the bound itself refused, a refreshed principal ending at the original bound, the configured cap, the default, the ceiling and a future `AuthTime`. `internal/cfaccess/extension_test.go` `TestCFAccess_AssertionCarriesTheTokensExp` checks the exp mapping.

## What F4-08's identity flip must keep

- **Every principal that can reach a long-lived request is built by `PrincipalFor`**, so it records its scopes and `built`. A new authenticator that assembles a `Principal` literal will have every stream it opens ended at the first heartbeat.
- **Compat keys keep `keyID` set** (the compat `AuthenticateKey` path), so `stands` re-reads the key and Refresh rebuilds over its scopes.
- **`ext:cfaccess` stays a registered extension** whose principal stage 12 builds via `PrincipalFor(u, Spec.Method, Assertion.Scopes, epoch)`, so Refresh rebuilds it over the asserted scopes and keeps its owner assurance.
- A key's scopes are immutable in `authstore` (`PutAPIKey` keeps them). If that changes, Refresh still never widens: it rebuilds over the intersection of the principal's recorded scopes and the key's stored ones, so a scope removed from the key leaves the principal, and a scope added to the key does not reach it.
- **Every extension that has an expiry sets `Assertion.ExpiresAt`.** Without it the principal falls back to `core.auth.extension_max_age`, so its stream can outlive the credential by up to that long.
- Tests: `core/auth/refresh_test.go` runs every method over a real `auth.Service` (session and core key both built directly and bound by stage 12, compat key, compat surface nil and narrowed, `ext:` nil and narrowed, auth-off, revoked, expired, another user's key, deactivated user, stale epoch, a principal auth did not build, a failed store read staying `ErrStoreUnavailable`, concurrent calls). Every production path that builds a principal (`resolveKey`, `authenticateSession`, the extension loop, compat `PrincipalFor`) goes through `PrincipalFor`; the only literal is the auth-off binding, which Refresh answers on its own branch.
