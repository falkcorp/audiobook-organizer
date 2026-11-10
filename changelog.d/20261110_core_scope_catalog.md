### Added

#### `core/rbac`, `core/kernel` — fixed `core:*` scope catalog (dark)

Added `rbac.PermissionSpec`, `rbac.PermissionClass`, `rbac.Catalog` and the validating `rbac.NewCatalog` constructor (unique names, same-namespace and declared `Implies`, acyclic implication closure, `Confining` scopes outside implication). `rbac.CoreScopes()` lists the 19 `core:*` permissions with their class and flags, and the kernel builds a catalog from it without any optional subsystem wired. Nothing calls it at boot or enforces with it yet, so no route, schedule or stored row changes.
