<!-- file: docs/agent-tasks/platform-core/seams.d/F4-08-refresh.md -->
<!-- version: 0.1.0 -->
<!-- guid: 0b808901-8ffd-4527-b276-65f7de3728b5 -->
<!-- last-edited: 2026-11-20 -->

# auth.Service.Refresh (added by F6-03 E4, for F4-08 to keep)

F6-03 slice E4 (#4817, round 1) added `(*auth.Service).Refresh(ctx, *Principal) (*Principal, error)` to `core/auth`, which is F4-08 territory. It is the realtime hub's required `Deps.Refresh`: the hub calls it on every heartbeat and on a `core.rbac.changed.v1`, so a role change reaches an open stream. A separate file from the other F4-08 seams so no open PR collides on it.

## What it does

- It first applies the same standing check `Revalidate` does (now shared as `stands`): the user active and unrestricted; a session alive and at the user's epoch; a key (core `MethodAPIKey` by `CredentialID`, or a compat key by the unexported `keyID`) owned by the user and usable (`keyUsable`: status and expiry); any other method at the epoch it was built at. The auth-off binding stands while auth is off.
- It then rebuilds through `PrincipalFor` with the principal's own `Method`, over the stored key's scopes for a key, or over the scopes the principal was built with for anything else, and copies `CredentialID`, `keyID`, `AuthTime`, `Assurance` and `ClientAddr` across.
- **It never widens.** `Principal` now records `scopes` (what `PrincipalFor` built it over: nil is the method's normal rule, a list a narrowing) and `built` (`PrincipalFor` made it). A narrowed identity-extension assertion or compat principal stays narrowed after a role grant. A principal assembled outside `PrincipalFor` carries no record of its narrowing and is refused (`ErrInvalidCredentials`) rather than rebuilt over the user's roles.
- Errors are `Revalidate`'s: a store failure stays `ErrStoreUnavailable` (the hub retries it), everything else is definite.

## What F4-08's identity flip must keep

- **Every principal that can reach a long-lived request is built by `PrincipalFor`**, so it records its scopes and `built`. A new authenticator that assembles a `Principal` literal will have every stream it opens ended at the first heartbeat.
- **Compat keys keep `keyID` set** (the compat `AuthenticateKey` path), so `stands` re-reads the key and Refresh rebuilds over its scopes.
- **`ext:cfaccess` stays a registered extension** whose principal stage 12 builds via `PrincipalFor(u, Spec.Method, Assertion.Scopes, epoch)`, so Refresh rebuilds it over the asserted scopes and keeps its owner assurance.
- A key's scopes are immutable in `authstore` (`PutAPIKey` keeps them); if that changes, Refresh already follows the stored scopes, never the principal's.
- Tests: `core/auth/refresh_test.go` runs every method over a real `auth.Service` (session and core key both built directly and bound by stage 12, compat key, compat surface nil and narrowed, `ext:` nil and narrowed, auth-off, revoked, expired, another user's key, deactivated user, stale epoch, a principal auth did not build, a failed store read staying `ErrStoreUnavailable`, concurrent calls). Every production path that builds a principal (`resolveKey`, `authenticateSession`, the extension loop, compat `PrincipalFor`) goes through `PrincipalFor`; the only literal is the auth-off binding, which Refresh answers on its own branch.
