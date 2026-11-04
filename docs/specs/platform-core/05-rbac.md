<!-- file: docs/specs/platform-core/05-rbac.md -->
<!-- version: 1.0.0 -->
<!-- guid: c07c6c34-fabe-451c-ae99-262b2c83232f -->
<!-- last-edited: 2026-11-04 -->

# 05 — Authorization (`core/rbac`) and audit (`core/audit`)

Evidence commit `748fd787a`. Raw inputs: [`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md)
(13 atoms, 4 seed roles, gaps G1–G15 — all dispositioned in §13),
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §1.3, §4,
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §9,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §8.
Identity: [05-authn](05-authn.md). Route enforcement: [05-http-api](05-http-api.md) §4.2.

## 1. Purpose & scope

`core/rbac` decides **what a principal may do**. It owns the permission grammar, the catalog of
permissions declared by core and modules, roles as data (with a CRUD API), seed roles and their
merge with operator edits, API-key scope semantics, the compiled permission checker every
enforcement point uses, and the validation that stops a module from granting itself more than its
own namespace.

`core/audit` is the append-only record of security-relevant events — authentication, permission
denials, admin/role/key changes, task approvals — with a fixed schema, retention and a query API.

Single tenant, multi-user (D7/N5): no tenant IDs; a user's capabilities across apps/modules are the
union of namespaced scopes from their roles.

## 2. Current state at HEAD

| Topic | Today | Anchor |
|---|---|---|
| Permission type | `type Permission = string` (alias), 13 constants `"<area>.<verb>"` | `internal/auth/permissions.go:18,24-85` |
| Vocabulary | closed hand-maintained list: `All()` and `IsKnown()` switch; adding an atom edits 3 places | `internal/auth/permissions.go:91-129`, `internal/auth/seed.go:44-80` |
| Seed roles | `admin` (= `All()` recomputed each boot), `editor`, `viewer`, `fp-worker`; **upserted every boot, overwriting** any stored permissions/description | `internal/auth/seed.go:33-38,86-130` |
| Role storage | `Role{ID, Name, Description, Permissions []string, IsSeed, Version}` in `role:`; no role CRUD route; only seeding writes | `internal/database/store.go:966-977`, `raw/10` §3.2 |
| Effective permissions | union over `user.Roles`; unknown role skipped | `internal/server/middleware/auth.go:430-455` |
| Enforcement | `s.perm(atom)` on 430/497 routes; role-NAME `RequireAdmin` on 20; in-handler `auth.Can` for op-defs, v3 ops, repairs, maintenance; 36 authenticated routes with no permission | `internal/server/server_lifecycle.go:1693-1698`, `internal/server/middleware/auth.go:464-537`, `raw/09` §1, §4, §4.1 |
| Admin by role name | `slices.Contains(user.Roles, "admin")` in middleware and 2 handler sites | `internal/server/middleware/auth.go:522-537`, `internal/server/handlers/user.go:285,453` |
| Key scopes | same strings; effective = role perms ∩ scopes; **empty = all role perms** | `internal/server/middleware/auth.go:405-425`, `internal/server/handlers/apikeys.go:199-226` |
| Admin gate ignores key scopes | `RequireAdmin` checks the user's role names, not the request's effective set, so an admin's key scoped to `library.view` passes admin-gated routes (e.g. `PATCH /api/v1/admin/debug/books/:id`, `raw/09` §3.1 row 244) | `internal/server/middleware/auth.go:522-537`, `internal/server/middleware/auth.go:384-386` |
| Role assignment bound | invite carries any `RoleID`; creator needs only `users.manage` (latent: only seed `admin` holds it today) | `internal/server/handlers/user.go:155-190` |
| Op/task permissions | op def `Permissions []auth.Permission`, all required, default `settings.manage`; v3 cancel = starter or `scan.trigger`+def perm; approve = interactive + approver perm (default `settings.manage`) | `internal/operations/registry/types.go:165-166`, `internal/server/handlers/operations_v2.go:781-800`, `raw/09` §8.4 |
| Realtime filtering | v3 stream re-checks per event and closes when the credential no longer stands | `internal/server/handlers/opsv3/events.go:569-618`, `internal/server/handlers/opaccess/repairs.go:227,272` |
| UI | `permissions: string[]` from `/auth/me`; advisory hooks; Users page filters by role name `admin` | `web/src/hooks/useLibraryPermission.ts:4-30`, `web/src/pages/Users.tsx:519`, `raw/13` §9 |
| Audit | `event=abs.auth` slog; "audit"-typed activity rows for guest tokens, email displacement, user merge, admin debug edits; no general login/role/key/denial audit | `internal/server/absauth/audit.go:14-40`, `internal/activity/user_data.go:38-52`, `raw/10` §6 |
| Prior art worth keeping | permission property tests; route-recording guards with reviewed allowlists | `internal/auth/permissions_prop_test.go`, `internal/server/credential_routes.go:23-58`, `internal/server/wire_admin_role.go:18-104` |

## 3. Go API

Package `github.com/falkcorp/mediacore/rbac` (imports stdlib, `storage`, `events`, `audit`, `log`,
`metrics`; **not** `auth` — `auth` imports `rbac`, and binds the checker into the context through
`rbac.WithChecker` so `rbac.Can` works without knowing `auth.Principal`). Package `github.com/falkcorp/mediacore/audit` (imports
`storage`, `log`, `metrics`, `tasks`-free; tasks register its prune job from the kernel).

### 3.1 Scopes and requirements

```go
package rbac

import (
	"context"
	"errors"
	"iter"
	"time"
)

// Permission is "<ns>:<action>[.<sub>]" or a role-only wildcard (§4.2).
// ns     = module ID or "core"            [a-z][a-z0-9]{1,15}
// action = [a-z][a-z0-9_]{0,31}
// sub    = [a-z][a-z0-9_]{0,31}  (one level only)
type Permission string

func ParsePermission(s string) (Permission, error)          // strict grammar, rejects wildcards
func ParsePattern(s string) (Permission, error)        // also accepts "<ns>:*" and "<ns>:<action>.*"
func (s Permission) Namespace() string
func (s Permission) Action() string                    // "edit" for "books:edit.metadata"
func (s Permission) IsWildcard() bool

const CoreAdmin Permission = "core:admin" // superuser: satisfies every check (§4.6)

// Requirement is what a route, task, topic or settings section demands.
// Its zero value is invalid; every registration API rejects it (R6).
type Requirement struct{ /* kind + scopes */ }

var (
	Public           Requirement // no principal needed
	AnyAuthenticated Requirement // a principal with ≥1 scope in the owner's namespace or core (§4.10)
)

func Need(s Permission) Requirement
func AllOf(s ...Permission) Requirement
func AnyOf(s ...Permission) Requirement

// Delegated marks a legacy/compat mount whose routes enforce inside the mounted
// handler (05-http-api §8.2). Accepted only by httpapi.MountLegacy, never by Handle.
func Delegated(reason string) Requirement

func (r Requirement) IsZero() bool
func (r Requirement) Permissions() []Permission
func (r Requirement) String() string // "books:edit", "public", "authenticated", "all(a,b)"
```

### 3.2 Catalog (module-declared permissions)

```go
// PermissionSpec is declared in a module manifest (06-app-sdk Manifest.Permissions)
// or by core. Field names match 06-app-sdk §6.4.2; this file owns the type.
type PermissionSpec struct {
	Name          Permission   // concrete, in the declarer's namespace
	Title         string       // UI label (Users/Roles page)
	Description   string
	DefaultGrants []RoleName   // seed roles that receive it by default: core "editor"/"viewer" or the declarer's own roles; merged by §4.8
	Implies       []Permission // same namespace only (core may imply its own); closure computed, cycles rejected
	Sensitive     bool         // never in a key-scope preset; a check of it fails for a non-interactive method (complements route Guards.Interactive)
	AuthRequired  bool         // never granted to the auth-disabled principal (05-authn §4.11)
	Deprecated    Permission   // non-empty: the replacement; still valid, flagged in UI
}

// RoleSeed is a module's own seed role (in addition to DefaultGrants into core roles).
type RoleSeed struct {
	Name        RoleName     // "<ns>.<name>"; a legacy un-namespaced ID only if listed in the module's LegacySurfaces (books: "fp-worker")
	Title       string
	Description string
	Permissions []Permission // patterns allowed (roles may hold wildcards), own namespace only
}

// LegacyAlias maps an old permission string to new permissions (§6), used by
// compat paths and the stored-data migration.
type LegacyAlias struct {
	Old string
	New []Permission
}

// Declarer is what the kernel uses to feed a module's manifest into the catalog.
type Declarer interface {
	Permissions(specs ...PermissionSpec) error
	Roles(seeds ...RoleSeed) error
	LegacyAliases(a ...LegacyAlias) error // core-only for aliases that map to core:* (§4.9)
}

type Catalog struct{ /* frozen after kernel validation */ }

func (c *Catalog) Lookup(s Permission) (PermissionSpec, bool)
func (c *Catalog) All() iter.Seq[PermissionSpec]
func (c *Catalog) Namespace(ns string) iter.Seq[PermissionSpec]
func (c *Catalog) Expand(p Permission) []Permission       // pattern -> concrete scopes currently in the catalog
func (c *Catalog) Legacy(old string) ([]Permission, bool)
```

### 3.3 Roles

```go
// RoleName identifies a role. Core seed roles keep ABO's IDs so stored
// User.Roles and Invite.RoleID need no rewrite (09-abo-on-core §9.2.6):
const (
	RoleAdmin  RoleName = "admin"  // locked; holds core:admin
	RoleEditor RoleName = "editor" // core:self + module DefaultGrants
	RoleViewer RoleName = "viewer" // core:self + module DefaultGrants
)
// Module seed roles: "<ns>.<name>" (e.g. "tv.admin"); custom roles: "custom.<slug>".
type RoleName string

type Origin uint8

const (
	OriginSeed   Origin = iota + 1 // contributed by core or a module manifest
	OriginCustom                   // created by an operator
)

type Role struct {
	Name        RoleName
	Title       string
	Description string
	Origin      Origin
	// Seed bookkeeping (Origin == OriginSeed): the last seed applied, and the
	// operator's edits as a diff against it (§4.8).
	SeedPermissions []Permission
	SeedDigest      string       // sha256 of the sorted SeedPermissions
	Added           []Permission // operator additions (patterns allowed); custom roles keep everything here
	Removed         []Permission // operator removals of seed permissions
	Locked          bool         // "admin": immutable, undeletable
	Tombstone       bool         // a deleted seed role: not recreated by later seeds
	CreatedAt, UpdatedAt time.Time
	Version         int64
}

// Permissions returns the role's effective patterns: (SeedPermissions ∪ Added) \ Removed.
func (r *Role) Permissions() []Permission

type RoleInput struct {
	Name        RoleName // custom.<slug> on create
	Title       string
	Description string
	Permissions []Permission // desired effective set; the service computes Added/Removed for seed roles
}
```

### 3.4 Service and checker

```go
type Service struct{ /* unexported */ }

func New(ctx context.Context, opts Options, deps Deps) (*Service, error)

type Deps struct {
	Store   storage.Keyspace // c/rbac/
	Audit   audit.Logger
	Events  events.Bus       // publishes core.rbac.changed.v1
	Log     *slog.Logger
	Metrics *metrics.Registry
}

// Kernel-only: catalog assembly and validation (§4.9), then seed merge (§4.8).
func (s *Service) Declarer(moduleID string) Declarer
func (s *Service) Freeze(ctx context.Context) error

func (s *Service) Catalog() *Catalog

// Roles CRUD (actor checks + escalation rules inside; §4.7).
func (s *Service) Roles(ctx context.Context) iter.Seq2[*Role, error]
func (s *Service) Role(ctx context.Context, id RoleName) (*Role, error)
func (s *Service) CreateRole(ctx context.Context, actor Checker, in RoleInput) (*Role, error)
func (s *Service) UpdateRole(ctx context.Context, actor Checker, id RoleName, in RoleInput, ifVersion int64) (*Role, error)
func (s *Service) DeleteRole(ctx context.Context, actor Checker, id RoleName, ifVersion int64) error
func (s *Service) ResetRole(ctx context.Context, actor Checker, id RoleName) (*Role, error) // drop Added/Removed

// CanAssign reports whether actor may give roles to a user (actor must hold every
// scope of every role, or core:admin).
func (s *Service) CanAssign(actor Checker, roles []RoleName) error

// Effective compiles the union of the roles' scopes (cached by role-set +
// generation). Unknown role IDs are skipped and counted.
func (s *Service) Effective(roles []RoleName) Checker
func (s *Service) Generation() uint64

// Checker is a compiled, immutable permission set.
type Checker interface {
	Has(s Permission) bool
	Satisfies(r Requirement) bool
	Concrete() []Permission // expanded against the catalog, for /me and UI hints
}

func Intersect(a, b Checker) Checker  // role set ∩ key scopes
func All(c *Catalog) Checker          // every concrete scope (auth-disabled principal minus AuthRequired)

// Context plumbing: auth's middleware calls WithChecker (and audit.WithActor)
// when it binds a principal.
func WithChecker(ctx context.Context, c Checker) context.Context
func CheckerFrom(ctx context.Context) (Checker, bool)

// In-handler checks (dynamic permissions). No checker in ctx → false / ErrDenied.
// Require emits a denial audit event (actor from audit.ActorFrom(ctx)) and
// returns ErrDenied wrapped with the permission.
func Can(ctx context.Context, s Permission) bool
func Require(ctx context.Context, s Permission) error
func RequireAll(ctx context.Context, s ...Permission) error

var (
	ErrDenied            = errors.New("rbac: permission denied")
	ErrUnknownScope      = errors.New("rbac: unknown scope")
	ErrBadScope          = errors.New("rbac: malformed scope")
	ErrForeignNamespace  = errors.New("rbac: scope outside declarer namespace")
	ErrCoreGrant         = errors.New("rbac: modules may not grant core scopes")
	ErrWildcardInKey     = errors.New("rbac: wildcards are not allowed in API key scopes")
	ErrImpliesCycle      = errors.New("rbac: implication cycle")
	ErrEscalation        = errors.New("rbac: actor lacks scopes being granted")
	ErrLockedRole        = errors.New("rbac: role is locked")
	ErrRoleExists        = errors.New("rbac: role exists")
	ErrVersionMismatch   = errors.New("rbac: version mismatch")
	ErrLastAdmin         = errors.New("rbac: change would remove the last core:admin")
)
```

### 3.5 Audit

```go
package audit

import (
	"context"
	"iter"
	"time"
)

type Outcome string

const (
	Success Outcome = "success"
	Failure Outcome = "failure" // e.g. bad password
	Denied  Outcome = "denied"  // permission/guard refusal
	Error   Outcome = "error"   // the system failed
)

type Actor struct {
	UserID       string // "" for anonymous
	Username     string // snapshot at write time
	Method       string // auth.Method value
	CredentialID string // session public id / key id; never a secret
	ClientAddr   string // as resolved by httpapi (trusted-proxy aware)
	UserAgent    string // truncated to 256 bytes
}

type Target struct { // polymorphic EntityRef (02 decision 6)
	Module string
	Kind   string
	ID     string
}

// Event is what callers submit.
type Event struct {
	Action     string          // "<ns>.<noun>.<verb>", e.g. "core.auth.login", "core.role.updated", "books.user.merged"
	Outcome    Outcome
	Actor      Actor           // filled from ctx principal when zero
	Target     Target
	Permission string          // scope that was checked, for denials and approvals
	Route      string          // "POST /api/core/v1/roles/{id}" (pattern, not raw path)
	RequestID  string          // filled from ctx when empty
	TraceID    string
	Reason     string          // short machine reason: "scope_escalation", "bad_password"
	Details    map[string]any  // redacted by key name + value scan, capped at 4 KiB JSON
}

// Record is what is stored and returned.
type Record struct {
	Seq      uint64    // gap-free, monotonic per store
	ID       string    // ULID
	Time     time.Time // UTC
	Event
	Coalesced uint32   // >1 when identical denials were folded (§7.3)
	PrevHash  [32]byte // hash chain (§7.4)
	Hash      [32]byte
}

type Logger interface {
	// Record writes synchronously; use for state changes. Returns an error and
	// the caller must fail the operation (fail-closed).
	Record(ctx context.Context, e Event) error
	// RecordIn appends the record to b so it commits atomically with the change.
	RecordIn(ctx context.Context, b storage.Batch, e Event) error
	// Emit queues asynchronously (bounded); use for high-volume events (denials,
	// login failures). Never blocks; drops are counted.
	Emit(ctx context.Context, e Event)
}

type Filter struct {
	Since, Until time.Time
	ActorUserID  string
	ActionPrefix string // "core.auth."
	Outcome      Outcome
	TargetModule, TargetKind, TargetID string
	AfterSeq     uint64
	Limit        int
}

type Reader interface {
	Query(ctx context.Context, f Filter) iter.Seq2[Record, error]
	Verify(ctx context.Context, fromSeq, toSeq uint64) error // recompute the hash chain
}

func New(ctx context.Context, opts Options, deps Deps) (*Store, error) // implements Logger and Reader

// Actor plumbing: auth's middleware binds the actor once per request; Event.Actor
// left zero is filled from here.
func WithActor(ctx context.Context, a Actor) context.Context
func ActorFrom(ctx context.Context) (Actor, bool)
```

## 4. Behaviour

### 4.1 Grammar

`<ns>:<action>[.<sub>]`, lowercase, `ns` is a module ID or `core` (03 §3.6). Exactly one optional
sub-level (`books:edit.metadata`), so wildcard matching is two-level and cheap. Examples:
`books:view`, `books:edit`, `tv:request`, `tv:request.approve`, `core:users.manage`, `core:admin`.

### 4.2 Wildcards (decided)

| Pattern | In roles | In API key scopes | In route/task/topic requirements | Meaning |
|---|---|---|---|---|
| `<ns>:*` | allowed | **rejected** (`ErrWildcardInKey`) | rejected | every scope of the namespace, including subs and scopes added in future releases |
| `<ns>:<action>.*` | allowed | rejected | rejected | every sub of the action (and the bare action) |
| `*:*`, `*:<action>` | rejected | rejected | rejected | use `core:admin` |

Why keys differ: a key is automation with a fixed purpose; a wildcard key silently gains every
permission later releases add to the namespace. Roles are meant to track "editor of books" as the
module grows. The UI's "all permissions of this module" checkbox for keys expands to the concrete
list at creation.

### 4.3 Implication

A `PermissionSpec` may declare `Implies` within its namespace (e.g. `books:edit` implies
`books:view`; `tv:request.approve` implies `tv:request`). The closure is computed at `Freeze`;
cycles → boot error. Implication applies to roles **and** keys (a key scoped `books:edit` can read),
removing today's need to list both atoms. Cross-namespace implication is forbidden except that
`core:admin` implies everything.

### 4.4 Effective permissions

`Effective(user.Roles)` = union of each role's patterns, compiled into a `Checker` with:
a hash set of concrete scopes, a set of namespace wildcards, a set of action wildcards, and an
`admin` flag. `Has(s)` = admin ∨ exact ∨ `ns:*` ∨ `ns:action.*` ∨ any concrete scope whose
implication closure contains `s` (precomputed into the exact set at compile time). For API keys the
principal's checker is `Intersect(roles, keyScopes)`: `Has(s)` = both hold. Nothing is materialized
per request; compiled checkers are cached by (sorted role IDs, `Generation()`).

### 4.5 API key scopes (G6)

- **New keys: scopes required, non-empty, concrete (no wildcards); empty = refused.** Recommendation
  accepted from `raw/10` G6. A key may hold `core:admin` (bootstrap key, admin automation) — that is
  explicit, not a wildcard.
- At creation every scope must be in the catalog and held by the actor (and by the key owner when
  creating for another user, 05-authn §4.4).
- Legacy keys migrated with empty scopes get `LegacyFullScope=true`: effective = role permissions
  (today's semantics), shown in the UI as "legacy full-access key, expires <date>", never
  creatable. All keys expire within 365 days (`raw/10` §2.3), so this state ends by itself; no
  forced revocation.
- Per-key module binding is expressed by scopes (a `tv:*`-only automation lists `tv:` scopes); no
  separate field.

### 4.6 Admin is a permission (G4)

- `core:admin` is a permission held by the locked seed role `admin`. It satisfies every requirement
  (superuser). Every role-name check is removed: `RequireAdmin` → `Need(core:admin)` (or a narrower
  permission where the route is not really "admin", see §6.2); `handlers/user.go:285,453` →
  `Can(core:admin)`. Holding the role *named* `admin` confers nothing by itself; holding
  `core:admin` (through any role) does.
- Because it is a permission, **key scoping applies to it**: an admin's key scoped `books:view` no
  longer passes admin routes (closes the `RequireAdmin` key-scope bypass in §2).
- Per-module administration is a module seed role holding `<ns>:*` (e.g. `tv.admin` seeded by the TV
  module with `tv:*`), which is how "admin of TV but viewer of Books" (G7) is expressed.
- `core:admin` cannot be granted by modules (§4.9), cannot be put in a role by an actor who lacks it
  (escalation rule), and the last active holder cannot lose it (`ErrLastAdmin`, checked in the same
  storage batch as the change).

### 4.7 Roles as data, CRUD API

Routes under `/api/core/v1` (all writes: `Guards.Interactive`, `If-Match` on version):

| Method + path | Perm |
|---|---|
| `GET /permissions` (catalog: name, title, description, implies, sensitive, module) | AnyAuthenticated |
| `GET /roles`, `GET /roles/{name}` | `core:roles.view` |
| `POST /roles` (custom only, name `custom.<slug>`) | `core:roles.manage` |
| `PATCH /roles/{name}` (title, description, permissions) | `core:roles.manage` |
| `POST /roles/{name}/reset` (seed roles: drop operator diff) | `core:roles.manage` |
| `DELETE /roles/{name}` (custom: delete if unassigned or `?reassign=`; seed: tombstone) | `core:roles.manage` |
| `PUT /users/{id}/roles` | `core:users.manage` (05-authn §4.13) |

Escalation rules (enforced in `Service`, not only in handlers):

1. Writing a role: every permission in the resulting set must be held by the actor (patterns: the
   actor must hold the pattern itself or `core:admin`).
2. Assigning roles to a user (direct, invite, OIDC default role): `CanAssign` — the actor holds
   every permission of every assigned role. A `core:users.manage` holder therefore cannot hand out
   `admin` (latent at HEAD, §2).
3. `admin` is `Locked`: no edit, no delete.
4. Deleting a role assigned to users requires `?reassign=<name>` (applied in the same batch) or
   fails 409 `role_in_use`.

### 4.8 Seed merge that never clobbers operator edits (G3)

Seed input for a role = core's seed for that role ∪ every enabled module's `PermissionSpec.DefaultGrants`
naming it ∪ (for module roles) the module's `RoleSeed.Permissions`. Each seed role row stores
`SeedPermissions` (the seed input last applied) and the operator's diff `Added`/`Removed`. At
`Freeze`, for every seed role:

| Stored state | Action |
|---|---|
| absent | create with `SeedPermissions = seed`, no diff |
| `Tombstone` | leave deleted (operator deleted it on purpose) |
| present, `SeedDigest` equal | nothing |
| present, digest differs | set `SeedPermissions = new seed`, keep `Added`/`Removed`; drop entries of `Removed` that are no longer in the seed; log + audit `core.role.seed_updated` with the permissions the release added or withdrew |

Effective = `(SeedPermissions ∪ Added) \ Removed`. Consequences: a permission a new release grants to
`editor` reaches every `editor` role the operator did not explicitly strip of it; an operator's
removal survives upgrades; a permission the release withdraws disappears unless the operator had added
it explicitly. `POST /roles/{name}/reset` clears the diff. Today's behaviour (overwrite every boot,
`internal/auth/seed.go:117-127`) is the special case "diff always empty". 06-app-sdk §6.4.2 describes
`DefaultGrants` as "seeded on first boot of the module only; admin edits win afterwards"; this merge
is the precise form of that rule (admin edits win, and grants added by later releases still reach
roles nobody edited).

Core seed roles (IDs unchanged from ABO, so stored `User.Roles` and `Invite.RoleID` need no rewrite,
09-abo-on-core §9.2.6):

| Role | Core seed permissions | Typical module grants |
|---|---|---|
| `admin` (locked) | `core:admin` | — (implies everything) |
| `editor` | `core:self`, `core:system.view`, `core:tasks.view` | `books:view, books:edit, books:delete, books:organize, books:scan, books:playlists.create, books:request, books:request.approve, books:reading` |
| `viewer` | `core:self`, `core:system.view`, `core:tasks.view` | `books:view, books:request, books:reading` |

`core:self` (own profile, own sessions and keys) is the explicit grant behind `AnyAuthenticated`
self-service routes; a role without it (a worker role) cannot use them.

### 4.9 Module declarations and escalation validation

At `Freeze` the kernel validates every module's declarations; any failure aborts boot (errors
joined, before listeners open):

| Rule | Error |
|---|---|
| Every `PermissionSpec.Name` is concrete and `Namespace() == moduleID` | `ErrForeignNamespace` |
| `RoleSeed.Name` is `<moduleID>.<name>` (or a legacy ID listed in the module's `LegacySurfaces`); its permissions' namespaces == moduleID | `ErrForeignNamespace` |
| No module permission, seed, default grant or legacy alias targets `core:*` (in particular `core:admin`) | `ErrCoreGrant` |
| `DefaultGrants` name only `editor`, `viewer` or the module's own roles (never `admin`, never another module's role) | `ErrForeignNamespace` |
| `Implies` stays in namespace; closure acyclic | `ErrForeignNamespace`, `ErrImpliesCycle` |
| A module's routes, tasks, topics, settings sections require only own-namespace permissions, `core:admin`, or `Public`/`AnyAuthenticated` (requiring *more* is not escalation; requiring another module's permission is coupling, R3) | `httpapi.ErrForeignScope` |
| Permission uniqueness across catalog | duplicate error |
| `LegacyAlias.New` within the declarer's namespace; `core:` targets **only for core-declared aliases** | `ErrCoreGrant` |

The 13 ABO legacy atoms include core-bound meanings (`users.manage`, `settings.manage`), so the
alias table of §6 is declared by **core** (`core/rbac/legacy_abo.go`, enabled by
`app.Options.LegacyPermissionAliases`), not by the `books` module — a module can never alias an old
string to a core permission. (06-app-sdk's `LegacySurfaces.Permissions` map is therefore limited to
aliases whose targets are all in the module's own namespace; the kernel enforces it.)

### 4.10 Enforcement points

| Point | Mechanism | Fail |
|---|---|---|
| HTTP routes | `Route.Perm` checked in httpapi stage 12; denials audited (coalesced) | closed |
| In-handler dynamic checks | `rbac.Require(ctx, scope)` (op-def permissions, repairs fixers) | closed |
| `AnyAuthenticated` | principal's checker holds ≥1 scope in the route owner's namespace or `core:self`; a credential scoped only to another module or only to a worker scope is refused | closed |
| Tasks (05-tasks §5T.3.7) | definition declares `Permissions{Start, Cancel, Approve, View}`; `Start` **required** (registration fails like R6); `Cancel` zero = `Start`, the run's own actor may always cancel, `core:tasks.manage` may cancel any; `Approve` required iff the definition needs approval; `View` zero = `core:tasks.view`; approvals need an interactive method | closed |
| Realtime topics (05-realtime) | `Topic.Perm` checked per subscriber per event; principal revalidated on heartbeat and invalidation | closed |
| Settings sections (05-config) | each section declares `ReadPerm`, `WritePerm` (default write `<ns>:settings.manage`, core sections `core:settings.manage`); secret fields are write-only regardless | closed |
| Storage/backup/updater admin ops | core routes with `core:system.manage` | closed |
| UI hints | `/api/core/v1/me` returns `permissions` (concrete, expanded) and `patterns`; `@mediacore/shell` `can(scope)` implements §4.4 in TS. **Advisory only**: the server is authoritative. Matching vectors shared in `core/rbac/testdata/match_vectors.json`, run by Go and vitest | n/a |

### 4.11 Interplay with auth modes

- Auth disabled: principal checker = `All(catalog)` minus `AuthRequired` scopes (05-authn §4.11).
- Zero users: anonymous; only `Public` routes (05-authn §4.2). Today's `HoldsPermission` first-run
  bypass (`internal/server/middleware/auth.go:498-504`) is removed.

## 5. Lifecycle

1. Kernel creates `rbac.Service` and `audit.Store` early (R4: same layer as `auth`).
2. Core declares its permissions, seeds and (when enabled) the ABO legacy alias table.
3. Each enabled module's `Register` declares via `Declarer`. Disabled modules declare nothing; their
   scopes stay stored in roles but are inert (not in catalog → never `Has`), and reappear when the
   module is re-enabled (G1 of the 2026-09-18 gates).
4. `Freeze`: validate (§4.9), compute closures, run seed merge (§4.8) in one storage batch with its
   audit records, bump generation.
5. `httpapi.Build` validates route requirements against the frozen catalog.
6. Runtime: role writes bump `Generation()` and publish `core.rbac.changed.v1`; caches key on
   generation, so the next request sees the change.
7. `audit` starts its async writer goroutine; the kernel registers `core.audit.prune` (daily). On
   shutdown the async queue is drained (bounded 5 s), remaining events counted as dropped.

## 6. Migration from ABO (atoms, roles, stored data)

### 6.1 The 13 atoms → new permissions

09-abo-on-core §9.2.6 fixed the invariant (allow/deny identical for all 572 routes × 4 seed roles,
`TestLegacyRoleReachUnchanged`) and proposed names; this table is the normative mapping. It equals
09's proposal except where noted in the last column.

| Legacy atom | Routes (`raw/09` §1.3) | Canonical expansion | Reverse (mirror) rule | Difference from 09 proposal |
|---|---|---|---|---|
| `library.view` | 147 | `books:view`, `core:tasks.view` | iff `books:view` | 09 also lists `core:events.subscribe`: dropped — the event stream is `AnyAuthenticated` with per-topic permissions (05-realtime §4.4), so no subscribe permission exists |
| `library.edit_metadata` | 111 | `books:edit` (implies `books:view`) | iff `books:edit` | — |
| `library.delete` | 22 | `books:delete` (implies `books:view`) | iff held | — |
| `library.organize` | 9 | `books:organize` (implies `books:view`) | iff held | — |
| `scan.trigger` | 15 | `books:scan` | iff held | 09 also lists `core:tasks.run`: dropped — task start is the definition's `Permissions.Start` (05-tasks §5T.3.7), there is no global run permission |
| `integrations.manage` | 12 | `core:integrations.manage`, `books:integrations.manage` | iff **both** | — |
| `users.manage` | 20 | `core:users.manage` (implies `core:users.view`), `core:roles.view` | iff `core:users.manage` | adds `core:roles.view` so holders can still see role names when assigning |
| `settings.manage` | 83 (and old op-def default) | `core:settings.manage`, `books:settings.manage`, `core:tasks.manage` | iff **all three** | — |
| `playlists.create` | 0 | `books:playlists.create` | iff held | now enforced on playlist write routes (among the 36 permission-less routes, `raw/09` §4) |
| `collections.manage` | 6 | `books:collections.manage` | iff held | — |
| `requests.create` | 0 | `books:request` | iff held | — |
| `requests.approve` | 0 | `books:request.approve` (implies `books:request`) | iff held | — |
| `fingerprint.worker` | 5 | `books:fingerprint.worker` | iff held | — |
| role-name check `admin` | 20 `RequireAdmin` + handlers | `core:admin` | role `admin` unchanged | see §6.2 |

Reverse rule is conservative (fail-closed): a canonical permission with no legacy equivalent is
dropped from mirrored rows, never widened (09 §9.2.3).

New permissions with no legacy atom: `core:admin`, `core:self`, `core:roles.view`,
`core:roles.manage` (`Sensitive`), `core:users.view`, `core:audit.view`, `core:system.view`,
`core:system.manage` (`AuthRequired`), `core:tasks.view`, `core:tasks.manage`, `core:metrics.read`,
`books:reading` (own progress/state: the reading-state and position routes among the 36),
`books:admin.debug` (`AuthRequired`, `Sensitive`).

### 6.2 Seed roles and admin-gated routes

| Legacy role (ID kept) | After migration |
|---|---|
| `admin` | core seed, locked, `core:admin` |
| `editor` | core seed `editor`; books' `DefaultGrants` supply the books permissions (§4.8 table) |
| `viewer` | core seed `viewer`; books' `DefaultGrants` supply `books:view, books:request, books:reading` |
| `fp-worker` | books module seed role with legacy ID `fp-worker` (declared in books' `LegacySurfaces`), `books:fingerprint.worker` only — no `core:self` |

The 20 `RequireAdmin` routes map to `core:admin` except: `/api/v1/admin/debug/*` →
`books:admin.debug` (held only via `core:admin` by default); master-list routes (today `users.manage`
**and** the admin role, `raw/09` §3.1) → `books:admin.debug`; cache-key introspection →
`core:system.view`. Each exception keeps every seed role's allow/deny unchanged, because only
`admin` holds the new permissions by default.

### 6.3 Stored-data migration and compatibility

- Data movement follows 09 §9.2.3 (adopt + mirror): `role:` → `c/rbac/role/…` with role IDs
  unchanged; legacy `Permissions` translated through §6.1 into `SeedPermissions` for seed roles
  (diff empty — ABO never allowed edits) or `Added` for any non-seed row (09 probe P2: any atom
  outside §6.1 makes boot refuse naming the role — fail-closed, owner maps it). `apikey:`.Scopes
  translated; empty → `LegacyFullScope`. During the window every native write mirrors a
  reverse-mapped legacy row in the same batch; a failed mirror fails the write (09 §9.2.3).
- Compatibility window (two minor releases, 02 §2.5 Q5), enabled by
  `core.rbac.legacy_aliases=true` (ABO default):
  - key-creation and role-write APIs accept legacy atoms and store the canonical expansion;
    responses carry `Deprecation` and a `deprecated_permissions` field;
  - `/api/v1/auth/me` (legacy alias) returns both `permissions` (canonical) and
    `legacy_permissions` (reverse-mapped) until the shell migrates;
  - the gin bridge maps `s.perm(atom)` and in-handler `auth.Can(ctx, atom)` through
    `Catalog.Legacy` and requires **all** of the expansion, preserving the old meaning.
- After the window (09 contract C4), `core.rbac.legacy_aliases` defaults false and legacy strings
  are refused with 400 `unknown_permission` naming the canonical permission.

## 7. Audit log (`core/audit`, G13)

### 7.1 What is recorded

| Action (prefix) | Write path | Examples |
|---|---|---|
| `core.auth.*` | login success **sync**; failure/throttle **async** | `login`, `login_failed`, `logout`, `bootstrap.redeemed`, `setup`, `oidc.linked`, `email_displaced`, `temp_login.minted/consumed`, `invite.created/accepted` |
| `core.session.*`, `core.apikey.*` | sync (in batch) | `revoked`, `created`, `status_changed` |
| `core.user.*` | sync (in batch) | `created`, `disabled`, `enabled`, `password_reset`, `roles_changed` |
| `core.role.*` | sync (in batch) | `created`, `updated`, `deleted`, `reset`, `seed_updated` |
| `core.permission.denied` | async, coalesced | every 403 from httpapi stage 12 and `rbac.Require` |
| `core.task.*` | sync | `approved`, `rejected`, `cancelled_by_other` |
| `core.settings.changed` | sync (in batch) | section + changed key names (values of secret fields never) |
| `core.system.*` | sync | `backup.restored`, `factory_reset`, `update.applied` |
| `<module>.*` | module-defined, via `audit.Logger` | ABO: `books.user.merged`, `books.guest_token.minted/revoked`, `books.admin_debug.edit/undo` |

Routine successful reads are **not** audited (that is the access log's job).

### 7.2 Storage layout

Keyspace `c/audit/`: `rec/<seq:8B big-endian>` → record JSON; secondary indexes
`idx/actor/<userID>/<seq>`, `idx/action/<action>/<seq>`, `idx/target/<module>/<kind>/<id>/<seq>`
(empty values). `seq` is allocated under the store's single writer mutex. Synchronous records
committed via `RecordIn` share the caller's batch (the change and its record commit or fail
together).

### 7.3 Volume control

Denials and login failures go through `Emit` → bounded channel (`core.audit.async_buffer`, 4096) →
one writer goroutine that batches up to 256 records or 100 ms. Identical denials (same actor,
route, permission, outcome) within `core.audit.coalesce_window` (1 min) are folded into one record
with `Coalesced=n`. Full queue → drop + `mediacore_audit_dropped_total` (fail-open: availability
under a credential-stuffing burst beats completeness; the throttle and access log still see it).

### 7.4 Tamper evidence

`Hash = SHA-256(PrevHash || canonical JSON of Record without Hash)`. `Verify(from, to)` recomputes.
This detects edits/deletions in the middle of the range (not truncation of the tail by someone
with storage access — out of scope for a single-host app). Pruning records the last pruned
record's hash as the new chain anchor in `c/audit/anchor`.

### 7.5 Retention

Delete when older than `core.audit.retention.max_age` (default 365 d) **or** beyond
`core.audit.retention.max_records` (default 1,000,000), whichever trims more; daily task
`core.audit.prune`, `tasks.Parallelism` sequential with reason "single ordered range delete on one
keyspace". Pruning writes `core.audit.pruned{from_seq,to_seq,count}` as a record.

### 7.6 Query API

`GET /api/core/v1/audit?since=&until=&actor=&action=&outcome=&target=&cursor=&limit=`
(`core:audit.view`, cursor pagination over `seq` descending), `GET /api/core/v1/audit/export`
(NDJSON stream, `core:audit.view`, `RateExpensive`), `POST /api/core/v1/audit/verify`
(`core:admin`). Records whose action is `core.auth.*` or `core.user.*` about **another** user are
visible only to `core:users.manage` holders as well (today's activity-log rule,
`internal/activity/user_data.go:78-90`).

### 7.7 Redaction

`Details` keys matching `(?i)(password|secret|token|key|cookie|authorization)` are replaced with
`"[redacted]"`; string values matching core credential prefixes (`mcs_`, `mck_`, `mcb_`, `mci_`,
`mct_`, legacy `abk_`/`abbs_`) are redacted regardless of key. Usernames are kept (needed for an
audit trail); emails are stored only where the action is about an email (`email_displaced` keeps
account IDs only, as today, `internal/oauth/oauth.go:67-71`).

## 8. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| Malformed / unknown scope at registration or in a manifest | boot error | closed | typos would silently deny or grant |
| Module grants foreign or core scope | boot error | closed | escalation |
| Implication cycle | boot error | closed | |
| Unknown role ID on a user (role deleted out of band) | skipped, `mediacore_rbac_unknown_roles_total` | closed (grants nothing) | today's behaviour (`middleware/auth.go:436-442`) |
| Stored scope unknown to the catalog (module disabled or removed) | inert, kept in storage | closed | re-enable restores |
| Permission check with no principal | false | closed | |
| Role write without actor holding scopes | 403 `scope_escalation` | closed | |
| Version mismatch on role write | 412 | closed | |
| Last-admin removal | 409 `last_admin` | closed | lockout protection |
| Seed merge storage error at boot | boot error | closed | partial seed state is worse than not starting |
| Sync audit write fails | the change fails (same batch) | closed | attribution of security changes is required |
| Async audit queue full | drop + metric | open | availability |
| Audit query store error | 503 | closed | |
| Hash-chain verify mismatch | `Verify` returns error naming first bad seq; `mediacore_audit_chain_breaks_total` | — | detection only |
| Prune fails | task fails, retried next day; growth alarm when > 1.5 × max_records | open | |

## 9. Concurrency contract

- `Catalog` is immutable after `Freeze`; lock-free reads.
- Compiled checkers are immutable; cached in a sharded LRU keyed by `(sorted role IDs, generation)`
  (`core.rbac.cache_size`, 4,096); a generation bump makes old entries unreachable (no explicit
  purge needed).
- Role writes: one storage batch per write with optimistic `Version` check; last-admin check reads
  holders inside the same batch snapshot (05-storage snapshot reads), so two concurrent demotions
  cannot both pass (`testing/synctest` test).
- `audit`: one writer goroutine for async events (`wg.Go`, stopped by app context), a mutex around
  `seq` allocation and `PrevHash` for sync records; sync records never wait on the async queue.
- No goroutine per check, per request or per record.

## 10. Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.rbac.legacy_aliases` | bool | false (ABO: true during the window) | no |
| `core.rbac.cache_size` | int | 4096 | no |
| `core.audit.retention.max_age` | duration | 8760h | yes |
| `core.audit.retention.max_records` | int | 1000000 | yes |
| `core.audit.async_buffer` | int | 4096 | no |
| `core.audit.coalesce_window` | duration | 1m | yes |
| `core.audit.details_max_bytes` | int | 4096 | yes |

## 11. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_rbac_denials_total` | counter | `owner` (module/core), `scope` (catalog-bounded) |
| `mediacore_rbac_checker_cache_total` | counter | `result` (`hit`,`miss`) |
| `mediacore_rbac_unknown_roles_total` | counter | — |
| `mediacore_rbac_generation` | gauge | — |
| `mediacore_rbac_roles` | gauge | `origin` |
| `mediacore_audit_records_total` | counter | `namespace`, `outcome` |
| `mediacore_audit_dropped_total` | counter | — |
| `mediacore_audit_queue_depth` | gauge | — |
| `mediacore_audit_write_errors_total` | counter | `path` (`sync`,`async`) |
| `mediacore_audit_pruned_total` | counter | — |
| `mediacore_audit_chain_breaks_total` | counter | — |

## 12. Tests

| Test | Asserts |
|---|---|
| `FuzzParsePermission` / `TestScopeGrammar_Table` | grammar, one sub level, lowercase |
| `TestWildcard_RolesAllowedKeysRejected` | §4.2 table |
| `TestChecker_MatchVectors` | runs `testdata/match_vectors.json` (also run by vitest in `@mediacore/shell`) |
| `TestImplies_ClosureAndCycle` | |
| `TestIntersect_KeyNarrowsRoles` | incl. wildcard role ∩ concrete key |
| `TestAdmin_IsScopeAndKeyScopingApplies` | admin user's `books:view` key → 403 on `core:admin` route (regression for `RequireAdmin`) |
| `TestManifest_RejectsForeignNamespace` / `TestManifest_RejectsCoreGrant` / `TestManifest_RejectsCoreLegacyAliasFromModule` | §4.9 |
| `TestSeedMerge_PreservesOperatorRemoval` / `TestSeedMerge_NewSeedScopeReachesUneditedRole` / `TestSeedMerge_TombstoneNotRecreated` / `TestSeedMerge_Idempotent` | property test over random seed/edit sequences: effective = (seed ∪ added) \ removed |
| `TestRoleWrite_EscalationRefused` / `TestCanAssign_CannotGrantUnheld` | |
| `TestLastAdmin_ConcurrentDemotions` | `testing/synctest`, two concurrent demotions → one 409 |
| `TestLegacyAtomMapping_Golden` | the 13 rows of §6.1 |
| `TestMigration_RolesUsersKeys` | fixture DB from ABO key families → expected `c/rbac`/`c/auth` rows |
| `TestTaskDefinition_RequiresStartPerm` | (in 05-tasks, uses rbac requirement) |
| `TestAnyAuthenticated_RejectsForeignOnlyCredential` | key scoped only `tv:*`-concretes refused on books `AnyAuthenticated` route |
| `TestAudit_SyncInSameBatch` | failing batch → neither change nor record |
| `TestAudit_DenialCoalescing` | `synctest` clock |
| `TestAudit_HashChainVerify` / `TestAudit_PruneMovesAnchor` | |
| `TestAudit_RedactsSecretsByKeyAndPrefix` | |
| `TestAudit_QueueFullDropsWithoutBlocking` | |
| `TestNoGlobals` (archtest) | no package-level registries in `rbac`/`audit` (R5) |

## 13. Gap disposition G1–G15 (all gaps from `raw/10` §8)

| Gap | Disposition | Where |
|---|---|---|
| G1 closed hard-coded vocabulary | **Fixed**: catalog of module-declared `PermissionSpec`s; `IsKnown` → `Catalog.Lookup` | §3.2, §4.9 |
| G2 no namespace | **Fixed**: `<ns>:<action>[.<sub>]`; 13 atoms mapped; stored data migrated with compat window | §4.1, §6 |
| G3 seed-only roles, boot overwrites edits, no CRUD | **Fixed**: roles as data with CRUD API; seed merge by diff; tombstones; module seeds and grants | §4.7, §4.8 |
| G4 admin by role name | **Fixed**: `core:admin` scope; role-name checks removed; key scoping applies | §4.6 |
| G5 imperative route binding, 36 permission-less routes, unenforced atoms | **Fixed**: declarative `Route.Perm` (R6, boot error), `AnyAuthenticated` made explicit and narrowed, `playlists.create` enforced | 05-http-api §3.1, §8.2; §4.10, §6.1 |
| G6 key scopes: empty = unrestricted | **Fixed**: new keys require non-empty concrete scopes; legacy empty → `LegacyFullScope` until expiry; key-mints-key escalation closed | §4.5; 05-authn §4.4 |
| G7 no per-app membership | **Fixed by composition**: roles composed of namespaced permissions (a user holding `tv.admin` (`tv:*`) and `viewer` (books view grants)); per-user data ownership stays app-specific | §4.6 |
| G8 plugin routes unauthenticated | **Fixed**: modules only get chained `Group`s | 05-http-api §4.2 |
| G9 auth wiring inline/global | **Fixed** | 05-authn §13 |
| G10 single-binary bootstrap | **Fixed** | 05-authn §4.6 |
| G11 CF/owner machinery in auth layer | **Fixed** (extension point; policy editing stays in ABO) | 05-authn §4.8 |
| G12 secrets at rest | **Fixed**: auth credentials hashed (05-authn §4.1); config secrets encrypted via `core/secrets` (05-config); N10 | 05-authn, 05-config |
| G13 no uniform audit trail | **Fixed**: `core/audit` schema, sync/async paths, retention, hash chain, query API | §7 |
| G14 throttling tied to tunnel topology | **Fixed + documented** | 05-authn §4.9 |
| G15 two auth stacks | **Fixed**: compat resolves via `auth.Service.PrincipalFor` | 05-authn §4.10 |

## 14. Open items

1. Whether `core:self` should be implicit for every active human user instead of a grant in the
   `editor`/`viewer` seeds; granted is chosen here because it keeps "a user with no roles can do
   nothing" (today's safe default, `middleware/auth.go:424-427`) and keeps worker roles out of
   self-service routes.
4. Coordination with 09-abo-on-core §9.2.6: drop `core:events.subscribe` and `core:tasks.run` from
   its proposed expansions (§6.1 explains); the route-reach invariant is unaffected.
5. Coordination with 06-app-sdk §6.4.2: its `rbac` sketch (`PermissionSpec{Name, Title, Description,
   DefaultGrants, Sensitive}`, `RoleAdmin/RoleEditor/RoleViewer`) is adopted here and extended with
   `Implies`, `AuthRequired`, `Deprecated`; its `rbac.RoleViewer` etc. resolve to the IDs in §3.3.
2. Resource-level permissions (per-library or per-root ACLs) are not designed; `rbac.Require` plus
   module-side filtering covers today's per-user ownership (playlists, reading state).
3. Audit export to an external sink (syslog/OTLP logs) — 05-logging may add a sink; not required.

## Objections to 03

1. **Amendment:** 03 §3.6 defines permission names but not **role IDs** or **audit action names**.
   Proposed rows: Role ID — core seed roles `admin`, `editor`, `viewer` (kept from ABO), module
   seed roles `<moduleID>.<name>` (example `tv.admin`), custom roles `custom.<slug>`; Audit action `<moduleID>.<noun>.<verb>` (no version suffix, unlike event
   topics, because records are stored, not consumed), example `core.role.updated`.
2. **Amendment:** R6's "explicit `rbac.Public` / `rbac.AnyAuthenticated`" should note that
   `AnyAuthenticated` is namespace-aware (§4.10); otherwise a credential scoped to one module could
   reach every "any authenticated" route of every other module, which is exactly the hole the
   fingerprint-worker path confinement patches today (`internal/server/middleware/auth.go:271-306`).

## Out-of-scope defects found

1. **`RequireAdmin` ignores API-key scopes** (`internal/server/middleware/auth.go:522-537` reads
   `user.Roles`; `handleAPIKeyAuth` binds the full user at `:384-386`): a key an admin scoped to
   `library.view` passes every admin-gated route not also behind `credGuard` (7 `admin/debug`
   routes including `PATCH …/books/:id` and `…/edits/:edit_id/undo`, `raw/09` §3.1 rows 241-247).
   Hotfix in ABO: in `RequireAdmin`, refuse when the request's method is `api_key` and the key's
   scopes are non-empty and do not include every atom (or simply require `auth.Can(users.manage)`
   in addition). Fixed by design in §4.6.
2. **Invite role not bounded by the creator's permissions** (`internal/server/handlers/user.go:155-190`):
   latent today because only the seed `admin` role holds `users.manage`, but any future custom role
   with `users.manage` could invite itself an admin. Fixed by design in §4.7 rule 2.
