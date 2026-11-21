### Added

#### Kernel identity assembly, dark (F4-08 A3)

`core/kernel/identity.go` adds `buildIdentity`, which builds the rbac service over core's role store (with `authz.Compile`, the injected revocation journal and a Holders adapter over the identity store), declares each module's permissions and seed roles, freezes the catalog, and then builds the auth service. Nothing calls it yet. The identity flip's boot wiring (F4-08 B3) will call it in phase P5.

- **Holders adapter** (`identity_holders.go`): it guards every user record it reports, inactive holders included, in the role write's batch. If a demotion or re-enable commits first, the role write's commit then fails with a conflict and retries, so a role edit racing an auth demotion can never leave the install without an active admin. A deterministic interleave test and a free-running `-race` test cover this.
- **core.auth mapping** (`identity_settings.go`): this maps every `core.auth` key that has an `auth.Settings` field, `extension_max_age` included (the "not yet wired" item in `seams.d/F4-08-refresh.md`). Values pass through raw, so auth's own defaults and caps apply. A drift test pins every declared default to auth's exported default. `owner.required` and `disabled_allow_remote` become a pointer only when a layer above the declared default sets them. Mapping the declared `false` would switch the owner guard off where an owner verifier is configured, and would override the app's compiled `DefaultDisabledAllowRemote`. An empty `session.cookie_name` becomes `<AppID>_session`, per 05-authn §8.
- `identityMetrics` builds the core `rbac` and `auth` metric scopes. The auth scope declares the `extension` label key that `seams.d/F4-03.md` requires.
