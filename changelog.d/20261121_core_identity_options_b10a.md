### Security

- A module could build a principal for any user, name a real credential on it and have core's `Refresh` hand back a genuine one, because `Deps.AuthRefresh` went to every module and `Refresh` believed the exported fields of the principal it was given. Core now records the user, method, credential, assurance, auth time and client address of every principal it builds in an unexported seal and re-seals after its own edits; `Refresh` refuses with `ErrInvalidCredentials`, naming the field, when any of them disagrees, and refuses a principal core did not build. `Deps.AuthRefresh` is handed only to a module that declares `Manifest.AuthRefresh`; every other module gets nil. Nothing is exported that builds or re-seals a principal.

### Added

- `app.Options` takes `ResolveAtom`, `DisplayName`, `AccountResolver` and (through `Auth.BootstrapTokenFile`) the bootstrap token file name, all read only by an identity boot. `AccountResolver` is an `oidc.LateResolver`: the kernel builds the OIDC account resolver over the identity stores with the auth service as its e-mail lock, binds it at P5 and unbinds it first thing at close; until then it answers `oidc.ErrResolverUnbound` and admits nobody. The bootstrap token file keeps its default name `bootstrap-token`; a name that is not a plain file name in the state directory fails the boot.
- The OpenAPI session cookie security scheme names the cookie from `core.auth` config instead of the compiled default.
