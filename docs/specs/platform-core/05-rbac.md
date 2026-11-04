<!-- file: docs/specs/platform-core/05-rbac.md -->
<!-- version: 1.3.1 -->
<!-- guid: c07c6c34-fabe-451c-ae99-262b2c83232f -->
<!-- last-edited: 2026-11-04 -->

# 05 — Authorization (`core/rbac`) and audit (`core/audit`)

Evidence commit `748fd787a`. Raw inputs: [`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md)
(13 atoms, 4 seed roles, gaps G1–G15 — all dispositioned in §13),
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §1.3, §4,
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §9,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §8.
Identity: [05-authn](05-authn.md). Route enforcement: [05-http-api](05-http-api.md) §4.2.
Layers, `entity.Ref`, naming and the compatibility window are from [03-architecture](03-architecture.md)
v1.2.0 and [09-abo-on-core](09-abo-on-core.md) §9.2.1.

## 1. Purpose & scope

`core/rbac` decides **what a principal may do**. It owns the permission grammar, the catalog of
permissions declared by core and modules, roles as data (with a CRUD API), seed roles and their
merge with operator edits, API-key scope semantics, credential confinement, the compiled permission
checker every enforcement point uses, and the validation that stops a module from granting itself
more than its own namespace.

`core/audit` is the append-only record of security-relevant events — authentication, permission
denials, admin/role/key changes, integration and secret changes, task approvals — with a fixed
schema, retention and a query API.

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
| Admin routes with auth off | two kinds, recorded per route: read-only introspection (`adminRole`, opens with auth off) and always-authenticated (store wipes, debug edits, credential surfaces: `RequireAdmin`, 401 with auth off) | `internal/server/wire_admin_role.go:19-57`, `docs/decisions/2026-10-30-admin-routes-auth-off.md` |
| Worker confinement | a credential whose permissions are exactly `fingerprint.worker` is refused outside `/api/v1/fingerprint/worker/` | `internal/server/middleware/auth.go:265-298` |
| Key scopes | same strings; effective = role perms ∩ scopes; **empty = all role perms** | `internal/server/middleware/auth.go:405-425`, `internal/server/handlers/apikeys.go:199-226` |
| Admin gate ignores key scopes | `RequireAdmin` checks the user's role names, not the request's effective set, so an admin's key scoped to `library.view` passes admin-gated routes (e.g. `PATCH /api/v1/admin/debug/books/:id`, `raw/09` §3.1 row 244) | `internal/server/middleware/auth.go:522-537`, `internal/server/middleware/auth.go:384-386` |
| Role assignment bound | invite carries any `RoleID`; creator needs only `users.manage` (latent: only seed `admin` holds it today) | `internal/server/handlers/user.go:155-190` |
| Credential actions on other users | any `users.manage` holder can mint a temp-login or reset link for any user, or set any user's password through `PUT /api/v1/auth/me/password` with `user_id` | `internal/server/auth_reset_password.go:18-60`, `internal/server/auth_temp_login.go:225-227` |
| Op/task permissions | op def `Permissions []auth.Permission`, all required, default `settings.manage`; v3 cancel = starter or `scan.trigger`+def perm; approve = interactive + approver perm (default `settings.manage`); task start keeps a `scan.trigger` floor so a viewer cannot start a def that declares only `library.view` | `internal/operations/registry/types.go:165-166`, `internal/server/handlers/operations_v2.go:781-800`, `internal/server/handlers/opsv3/handler.go:236-246`, `raw/09` §8.4 |
| Realtime filtering | v3 stream re-checks per event and closes when the credential no longer stands | `internal/server/handlers/opsv3/events.go:569-618`, `internal/server/handlers/opaccess/repairs.go:227,272` |
| System status and logs | `GET /api/v1/system/status` and `/system/logs` need `settings.manage` (admin only) | `internal/server/wire_system_routes.go:28,31` |
| UI | `permissions: string[]` from `/auth/me`; advisory hooks; Users page filters by role name `admin` | `web/src/hooks/useLibraryPermission.ts:4-30`, `web/src/pages/Users.tsx:519`, `raw/13` §9 |
| Audit | `event=abs.auth` slog; "audit"-typed activity rows for guest tokens, email displacement, user merge, admin debug edits; no general login/role/key/denial audit | `internal/server/absauth/audit.go:14-40`, `internal/activity/user_data.go:38-52`, `raw/10` §6 |
| Prior art worth keeping | permission property tests; route-recording guards with reviewed allowlists | `internal/auth/permissions_prop_test.go`, `internal/server/credential_routes.go:23-58`, `internal/server/wire_admin_role.go:18-104` |

## 3. Go API

Package `github.com/falkcorp/mediacore/rbac` (layer L4; imports stdlib, `storage`, `audit`, `log`,
`metrics`; **not** `auth` — `auth` imports `rbac`, and binds the checker into the context through
`rbac.WithChecker` so `rbac.Can` works without knowing `auth.Principal` — and **not** `events`:
`events.TopicSpec` carries an `rbac.Requirement` (SIMP-5), so `rbac` reports changes through an
injected callback that the kernel wires to the bus, avoiding an import cycle inside L4). Its core
HTTP routes (§4.7, §7.6) are registered by `core/internal/rbachttp` and `core/internal/audithttp`
(L6, imported only by `kernel`, 03 §3.3). The compiled
checker and the two privileged checker constructors live in `core/internal/authz` (layer L4, the
implementation of `rbac`), so apps cannot reach them (§3.4, SEC-26). `rbac` also imports the leaf
`backup/journal` (L4; `Revocation`, `RevocationJournal`, `RestoreContext`), not `backup`, which lands
at 09 S8 (COR2-9). Package `github.com/falkcorp/mediacore/audit` (layer L4; imports `entity`,
`storage`, `log`, `metrics`; never `tasks` — the kernel registers its prune job).

**Landing (COR2-9).** The leaf types `Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`,
`Public`, `AnyAuthenticated` and `Delegated` (§3.1) land at 09 S2 with `httpapi`, whose
`Route.Perm`/`LegacyRoute.Perm` need them and whose S2 manifest annotates 36 routes
`AnyAuthenticated`; the catalog, roles, `Service`, the checker and `audit` land at S4.

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

func ParsePermission(s string) (Permission, error) // strict grammar, rejects wildcards
func ParsePattern(s string) (Permission, error)    // also accepts "<ns>:*" and "<ns>:<action>.*"
func (s Permission) Namespace() string
func (s Permission) Action() string // "edit" for "books:edit.metadata"
func (s Permission) IsWildcard() bool

// CoreAdmin is the superuser scope: it satisfies every requirement except a
// Sensitive scope for a non-interactive principal (§4.4). It is AuthRequired.
const CoreAdmin Permission = "core:admin"

// Requirement is what a route, task, topic or settings section demands.
// Its zero value is invalid; every registration API rejects it (R6).
type Requirement struct{ /* kind + scopes */ }

var (
	Public           Requirement // no principal needed
	AnyAuthenticated Requirement // a principal holding core:self or any non-Confining scope (§4.10)
)

func Need(s Permission) Requirement
func AllOf(s ...Permission) Requirement
func AnyOf(s ...Permission) Requirement

// Delegated marks a legacy route whose permission is enforced inside the
// mounted handler by its own compat authenticator (05-http-api §8.2,
// LegacyRoute.OwnAuth). Accepted only by httpapi.MountLegacy, and only on an
// OwnAuth route that names a compat method its module registered with
// Registrar.AuthMethod (SEC2-17); never by Handle.
func Delegated(reason string) Requirement

func (r Requirement) IsZero() bool
func (r Requirement) Permissions() []Permission
func (r Requirement) String() string // "books:edit", "public", "authenticated", "all(a,b)"
```

### 3.2 Catalog (module-declared permissions)

```go
// PermissionSpec is declared in a module manifest (06-app-sdk Manifest.Permissions)
// or by core. This file owns the type; 06 and 08 use these names (COR-16).
type PermissionSpec struct {
	Name          Permission      // concrete, in the declarer's namespace
	Title         string          // UI label (Users/Roles page)
	Description   string
	Class         PermissionClass // ClassAction (zero value) or ClassView; drives the task-start rule (§4.10)
	DefaultGrants []RoleName      // seed roles that receive it by default: core "editor"/"viewer" or the declarer's own roles; merged by §4.8, restricted by §4.9
	Implies       []Permission    // same namespace only (core may imply its own); closure computed, cycles rejected
	Sensitive     bool            // never in a key-scope preset; a check of it fails for a non-interactive principal, core:admin included (§4.4)
	AuthRequired  bool            // never held by the auth-disabled principal (§4.11, 05-authn §4.11)
	Confining     bool            // a principal holding only Confining scopes is confined to requirements naming them (§4.10)
	Warn          bool            // UI-only: show a warning when granting it (the facade's arr.request, 08)
	Deprecated    Permission      // non-empty: the replacement; still valid, flagged in UI
}

type PermissionClass uint8

const (
	ClassAction PermissionClass = iota // changes state or starts work
	ClassView                          // read-only
)

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
func (c *Catalog) Expand(p Permission) []Permission // pattern -> concrete scopes currently in the catalog
func (c *Catalog) Legacy(old string) ([]Permission, bool)
```

### 3.3 Roles

```go
// RoleName identifies a role. Core seed roles keep ABO's IDs so stored
// User.Roles and Invite.RoleID need no rewrite (09-abo-on-core §9.2.6):
const (
	RoleAdmin  RoleName = "admin"  // locked; holds core:admin
	RoleEditor RoleName = "editor" // core:self + core:tasks.view + core:system.status + module DefaultGrants
	RoleViewer RoleName = "viewer" // core:self + core:tasks.view + core:system.status + module DefaultGrants
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
	PendingSeed     []Permission // restricted seed additions held for admin acceptance (§4.8, SEC-25)
	Added           []Permission // operator additions (patterns allowed); custom roles keep everything here
	Removed         []Permission // operator removals of seed permissions
	Locked          bool         // "admin": immutable, undeletable
	Tombstone       bool         // a deleted seed role: not recreated by later seeds
	CreatedAt, UpdatedAt time.Time
	Version         int64
}

// Permissions returns the role's effective patterns: (SeedPermissions ∪ Added) \ Removed.
// PendingSeed is not effective until accepted.
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
	OnChange func(generation uint64) // kernel publishes core.rbac.changed.v1 on the bus; rbac never imports events
	Revocations journal.RevocationJournal // Append(ctx, b, r) of role.unassigned / role.narrowed / role.deleted into the change's batch b before it commits (§7.1, 05-backup §5B.3.6)
	Mirror  storage.Mirror   // role mirror during the compatibility window (§6.3); every role write, revocations included, is staged through it (R2-2)
	MirrorView storage.MirrorView // reads of mirror rbac.role: the native generation, or legacy row + side record while readThrough (05-storage §5S.3.11)
	Log     *slog.Logger
	Metrics *metrics.Scope   // "mediacore.rbac" (05-metrics)
}

// Kernel-only: catalog assembly and validation (§4.9), then seed merge (§4.8).
func (s *Service) Declarer(moduleID string) Declarer
func (s *Service) Freeze(ctx context.Context) error

func (s *Service) Catalog() *Catalog

// ReservedNamespaces returns the permission namespaces core owns ("core",
// "custom"); one input of module.ReservedIDs() (03 §3.6). A function, not an
// exported slice variable (R5).
func ReservedNamespaces() []string

// Roles CRUD (actor checks + escalation rules inside; §4.7).
func (s *Service) Roles(ctx context.Context) iter.Seq2[*Role, error]
func (s *Service) Role(ctx context.Context, id RoleName) (*Role, error)
func (s *Service) CreateRole(ctx context.Context, actor Checker, in RoleInput) (*Role, error)
func (s *Service) UpdateRole(ctx context.Context, actor Checker, id RoleName, in RoleInput, ifVersion int64) (*Role, error)
func (s *Service) DeleteRole(ctx context.Context, actor Checker, id RoleName, ifVersion int64) error
func (s *Service) ResetRole(ctx context.Context, actor Checker, id RoleName) (*Role, error)                    // drop Added/Removed
func (s *Service) AcceptSeed(ctx context.Context, actor Checker, id RoleName, ifVersion int64) (*Role, error) // apply PendingSeed

// CanAssign reports whether actor may give or take away roles (actor must hold
// every scope of every role named, or core:admin). Removal is checked like
// assignment (§4.7 rule 2).
func (s *Service) CanAssign(actor Checker, roles []RoleName) error

// Dominates reports whether actor may act on a user whose roles are target
// (actor holds every scope target holds, or core:admin). auth.Service calls it
// for every action on another user's credentials, status or roles (§4.7 rule 5).
func (s *Service) Dominates(actor Checker, target []RoleName) error

// Compile builds a principal's checker (cached by role-set + scopes +
// interactive + generation). Unknown role IDs are skipped and counted.
func (s *Service) Compile(in CheckerInput) Checker
func (s *Service) Generation() uint64

type CheckerInput struct {
	Roles       []RoleName
	Scopes      []Permission // key scopes; nil = role permissions only (sessions, LegacyFullScope keys)
	Interactive bool         // auth.Method.Interactive() of the credential (§4.4)
}

// Checker is a compiled, immutable permission set.
type Checker interface {
	Has(s Permission) bool
	Satisfies(r Requirement) bool
	Confined() bool         // every concrete scope held is Confining (§4.10)
	Concrete() []Permission // expanded against the catalog, for /me and UI hints
}

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
	ErrDenied           = errors.New("rbac: permission denied")
	ErrUnknownScope     = errors.New("rbac: unknown scope")
	ErrBadScope         = errors.New("rbac: malformed scope")
	ErrForeignNamespace = errors.New("rbac: scope outside declarer namespace")
	ErrCoreGrant        = errors.New("rbac: modules may not grant core scopes")
	ErrRestrictedGrant  = errors.New("rbac: DefaultGrants may not name a Sensitive, AuthRequired, Confining or *:settings.manage / *:integrations.manage scope")
	ErrWildcardInKey    = errors.New("rbac: wildcards are not allowed in API key scopes")
	ErrImpliesCycle     = errors.New("rbac: implication cycle")
	ErrEscalation       = errors.New("rbac: actor lacks scopes being granted")
	ErrDominance        = errors.New("rbac: actor does not dominate the target user")
	ErrLockedRole       = errors.New("rbac: role is locked")
	ErrRoleExists       = errors.New("rbac: role exists")
	ErrVersionMismatch  = errors.New("rbac: version mismatch")
	ErrLastAdmin        = errors.New("rbac: change would remove the last core:admin")
	ErrSeedRoleLocked   = errors.New("rbac: seed roles are read-only while the role mirror is active")
)
```

**Privileged checkers are not SDK (SEC-26).** The two checkers that bypass role resolution are
constructed only in `core/internal/authz`: `authz.AuthDisabled(cat *rbac.Catalog) rbac.Checker`
(§4.11) and `authz.ForSystem(perms []rbac.Permission) rbac.Checker` (the `_system` principal of
background tasks, built from the task definition's declared permissions, never `core:admin` by
default). Go's `internal/` rule keeps them out of app code; within core,
`archtest.NoSystemPrincipalOutsideKernel` allows calls to these two constructors only from `rbac`,
`auth`, the `tasks` executor and `kernel`. The public `rbac.All` of v1.0.0 is removed.

**A module's system actor (SEC2-15).** When a module's worker, hook or bus subscriber starts a run
outside a request, the kernel stamps the module's system actor (05-tasks §5T.3.10). Its checker is
`authz.ForSystem` over exactly the union of `Perms.Start` of the module's own kinds plus the kinds in
`Manifest.TaskStarts` (a reviewed list of foreign or core kinds the module may start, 06); `core:admin`
and every `AuthRequired` scope are never included, so a module cannot start `core:system.manage`
kinds (diagnostics export, backup create) or another module's undeclared kinds.

### 3.5 Audit

```go
package audit

import (
	"context"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
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

// Event is what callers submit.
type Event struct {
	Action     string         // "<ns>.<noun>.<verb>", present tense (03 §3.6, COR2-19): "core.auth.login", "core.role.update", "books.user.merge"
	Outcome    Outcome
	Actor      Actor          // filled from ctx principal when zero
	Target     entity.Ref     // the one entity reference type (03 §3.3, COR-17)
	Permission string         // scope that was checked, for denials and approvals
	Route      string         // "POST /api/core/v1/roles/{id}" (pattern, not raw path)
	RequestID  string         // filled from ctx when empty
	TraceID    string
	Reason     string         // short machine reason: "scope_escalation", "bad_password"
	Details    map[string]any // redacted by key name + value scan, capped at 4 KiB JSON
}

// Record is what is stored and returned.
type Record struct {
	Seq       uint64    // unique, increasing in allocation order; may have gaps (§7.2)
	ID        string    // ULID
	Time      time.Time // UTC
	Event
	Coalesced uint32 // >1 when identical denials were folded (§7.3)
}

type Logger interface {
	// Record writes synchronously; use for state changes. Returns an error and
	// the caller must fail the operation (fail-closed).
	Record(ctx context.Context, e Event) error
	// RecordIn appends the record to b so it commits atomically with the change.
	RecordIn(ctx context.Context, b *storage.Batch, e Event) error // the caller's batch, never a copy (COR2-12)
	// Emit queues asynchronously (bounded); use for high-volume events (denials,
	// login failures). Never blocks; drops are counted.
	Emit(ctx context.Context, e Event)
}

type Filter struct {
	Since, Until time.Time
	ActorUserID  string
	ActionPrefix string     // "core.auth."
	Outcome      Outcome
	Target       entity.Ref // zero fields match anything
	BeforeSeq    uint64     // cursor (§7.6)
	Limit        int
}

type Reader interface {
	Query(ctx context.Context, f Filter) iter.Seq2[Record, error]
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
`core:admin` implies everything (subject to §4.4).

### 4.4 Effective permissions

`Compile` builds a checker from the union of the roles' patterns: a hash set of concrete scopes
(implication closures precomputed into it), a set of namespace wildcards, a set of action
wildcards, an `admin` flag, and the `interactive` bit. With key scopes, the checker is the
intersection (a scope is held iff the roles hold it and the key names it, after implication).
`Has(s)` is evaluated in this order:

1. `s` is `Sensitive` and the principal is not interactive → **false**, even with `admin`
   (SEC-23). The bootstrap key (`["core:admin"]`, method `api_key`) therefore cannot write roles.
2. `admin` → true.
3. exact ∨ `ns:*` ∨ `ns:action.*` → true.

`Confined()` is true when the concrete set is non-empty and every member is `Confining` (an
`admin` principal is never confined). Nothing is materialized per request; compiled checkers are
cached by (sorted role IDs, sorted scopes, interactive, `Generation()`).

### 4.5 API key scopes (G6)

- **New keys: scopes required, non-empty, concrete (no wildcards); empty = refused.** Recommendation
  accepted from `raw/10` G6. A key may hold `core:admin` (bootstrap key, admin automation) — that is
  explicit, not a wildcard, and §4.4 rule 1 still applies to it.
- At creation every scope must be in the catalog and held by the actor (and by the key owner when
  creating for another user, 05-authn §4.4).
- Legacy keys migrated with empty scopes get `LegacyFullScope=true`: effective = role permissions
  (today's semantics), shown in the UI as "legacy full-access key, expires <date>", never
  creatable. All keys expire within 365 days (`raw/10` §2.3), so this state ends by itself; no
  forced revocation.
- Per-key module binding is expressed by scopes plus the key's `Purpose` (05-authn §4.4); a facade
  key's scopes are all `<m>:arr.*`, which are `Confining`.

### 4.6 Admin is a permission (G4)

- `core:admin` is a permission held by the locked seed role `admin`. It satisfies every requirement
  except a `Sensitive` scope for a non-interactive principal (§4.4). It is `AuthRequired`, so the
  auth-disabled principal never holds it (§4.11, SEC-2). Every role-name check is removed:
  `RequireAdmin` → the permission of §6.2; `handlers/user.go:285,453` → `Can(core:admin)`. Holding
  the role *named* `admin` confers nothing by itself; holding `core:admin` (through any role) does.
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
| `GET /permissions` (catalog: name, title, description, class, implies, sensitive, confining, warn, module) | AnyAuthenticated |
| `GET /roles`, `GET /roles/{name}` | `core:roles.view` |
| `POST /roles` (custom only, name `custom.<slug>`) | `core:roles.manage` |
| `PATCH /roles/{name}` (title, description, permissions) | `core:roles.manage` |
| `POST /roles/{name}/reset` (seed roles: drop operator diff) | `core:roles.manage` |
| `POST /roles/{name}/accept-seed` (apply `PendingSeed`) | `core:roles.manage` |
| `DELETE /roles/{name}` (custom: delete if unassigned or `?reassign=`; seed: tombstone) | `core:roles.manage` |
| `PUT /users/{id}/roles` | `core:users.manage` (05-authn §4.13) |

Escalation rules (enforced in `Service` and `auth.Service`, not only in handlers):

1. Writing a role: every permission in the resulting set must be held by the actor (patterns: the
   actor must hold the pattern itself or `core:admin`).
2. Assigning or removing roles of a user (direct, invite, OIDC default role): `CanAssign` — the
   actor holds every permission of every role added **or removed**. A `core:users.manage` holder
   therefore cannot hand out `admin` (latent at HEAD, §2), nor strip `admin` from a co-admin.
3. `admin` is `Locked`: no edit, no delete.
4. Deleting a role assigned to users requires `?reassign=<name>` (applied in the same batch) or
   fails 409 `role_in_use`.
5. **Dominance (SEC-4).** Any action that targets another user's credentials, status or roles —
   temp-login, reset link, set password, disable, enable, delete, revoke sessions, create or revoke
   keys for, change roles, merge — requires `Dominates(actor, target.Roles)`: the actor holds every
   scope the target's roles hold, or holds `core:admin`. Enforced inside `auth.Service`, so the
   legacy mount's handlers (which call it from 09 S4, 05-http-api §8) cannot bypass it. Refusal:
   403 `dominance_required`, audited.

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
| present, digest differs | set `SeedPermissions = new seed` minus any **restricted** addition (below); put restricted additions in `PendingSeed`; keep `Added`/`Removed`; drop entries of `Removed` that are no longer in the seed; log + audit `core.role.update_seed` with the permissions the release added, held or withdrew |

A *restricted* addition is a scope that is `Sensitive`, `AuthRequired` or `Confining`, or any
`*:settings.manage` / `*:integrations.manage`, added to a role that already existed. It becomes
effective only when an admin accepts it (`POST /roles/{name}/accept-seed`, audited
`core.role.accept_seed`); the Roles page shows the pending set (SEC-25). Roles created fresh
from the seed take the full seed.

Effective = `(SeedPermissions ∪ Added) \ Removed`. Consequences: an unrestricted permission a new
release grants to `editor` reaches every `editor` role the operator did not explicitly strip of it;
an operator's removal survives upgrades; a permission the release withdraws disappears unless the
operator had added it explicitly. `POST /roles/{name}/reset` clears the diff. Today's behaviour
(overwrite every boot, `internal/auth/seed.go:117-127`) is the special case "diff always empty".
06-app-sdk §6.4.2 describes `DefaultGrants` as "seeded on first boot of the module only; admin edits
win afterwards"; this merge is the precise form of that rule.

**Seed roles are read-only while the role mirror is active (DATA2-20).** HEAD rewrites every seed
role's permission set on every boot (`internal/auth/seed.go:80-129`, called at
`internal/server/server_lifecycle.go:1102`), so an operator's narrowing of `editor` on the new build
would be silently widened again for the whole duration of a rollback. Until contract C4 stops the
role mirror, `UpdateRole`, `ResetRole` and `AcceptSeed` on a seed role answer 409
`seed_role_locked_during_window` (`ErrSeedRoleLocked`); custom roles stay fully editable, and the
Roles page offers "duplicate as custom role" (a custom role holding the seed role's effective set,
then reassigned by the operator). This is no regression: HEAD has no seed-role edits at all (G3).
Release-driven seed changes still apply at `Freeze`; on rollback HEAD's per-boot seed is HEAD's own
behaviour. Of the reviewer's two options this one is chosen over silently cloning a seed role into a
custom role on edit, which would detach those users from every later seed grant.

Core seed roles (IDs unchanged from ABO, so stored `User.Roles` and `Invite.RoleID` need no rewrite,
09-abo-on-core §9.2.6):

| Role | Core seed permissions | Typical module grants |
|---|---|---|
| `admin` (locked) | `core:admin` | — (implies everything) |
| `editor` | `core:self`, `core:tasks.view`, `core:system.status` | `books:view, books:edit, books:delete, books:organize, books:scan, books:playlists.create, books:request, books:request.approve, books:reading` |
| `viewer` | `core:self`, `core:tasks.view`, `core:system.status` | `books:view, books:request, books:reading` |

`core:system.view` is **not** seeded to `editor`/`viewer` (SEC-8): today system status and logs need
`settings.manage` (`wire_system_routes.go:28,31`), which only `admin` holds. `core:system.status`
(readiness and maintenance-banner tokens only, no logs, paths or component detail) is what the
shell's banner needs. `core:self` (own profile, own sessions and keys) is the explicit grant behind
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
| `DefaultGrants` never appear on a `Sensitive`, `AuthRequired` or `Confining` spec, nor on any `*:settings.manage` / `*:integrations.manage` (SEC-25); such scopes reach roles only through a module's own `RoleSeed` or an admin edit | `ErrRestrictedGrant` |
| `Implies` stays in namespace; closure acyclic; a `Confining` scope implies nothing and is implied by nothing | `ErrForeignNamespace`, `ErrImpliesCycle` |
| A module's routes, tasks, topics, settings sections require only own-namespace permissions, `core:admin`, or `Public`/`AnyAuthenticated` (requiring *more* is not escalation; requiring another module's permission is coupling, R3) | `httpapi.ErrForeignScope` |
| Permission uniqueness across catalog | duplicate error |
| `LegacyAlias.New` within the declarer's namespace; `core:` targets **only for core-declared aliases** | `ErrCoreGrant` |

The 13 ABO legacy atoms include core-bound meanings (`users.manage`, `settings.manage`), so the
alias table of §6 is declared by **core** (`core/rbac/legacy_abo.go`, enabled by the compile-time
`app.Options.LegacyPermissionAliases`), not by the `books` module — a module can never alias an old
string to a core permission. (06-app-sdk's `LegacySurfaces.Permissions` map is therefore limited to
aliases whose targets are all in the module's own namespace; the kernel enforces it.)

### 4.10 Enforcement points

| Point | Mechanism | Fail |
|---|---|---|
| HTTP routes | `Route.Perm` checked in httpapi stage 13 (05-http-api §4.2); denials audited (coalesced) | closed |
| In-handler dynamic checks | `rbac.Require(ctx, scope)` (op-def permissions, repairs fixers; the legacy mount's `s.perm` adapter from 09 S4) | closed |
| `AnyAuthenticated` | the principal holds `core:self` **or any non-`Confining` scope**. Core routes that serve module resources (task start, event stream) add per-resource dynamic checks; they do not test the route owner's namespace (SEC-3) | closed |
| Confinement | a `Confined()` principal satisfies a requirement only when it **names** one of the principal's scopes literally: never `AnyAuthenticated`, never a requirement that the scope merely implies. `books:fingerprint.worker` and every `<m>:arr.*` scope are `Confining`, which reproduces today's worker path confinement (`middleware/auth.go:265-298`) and 08's facade-key rule without a path list | closed |
| Tasks (05-tasks §5T.3.7) | definition declares `Permissions{Start, Cancel, Approve, View}`; `Start` **required** (registration fails like R6) and **not satisfiable by a `ClassView` scope** unless the definition sets `StartableByViewers` with a reviewed reason (SEC-9; the legacy translation of an op def declaring only `library.view` becomes `AllOf(books:scan, books:view)`, which keeps today's floor, `opsv3/handler.go:236-246`); `Cancel` zero = `Start`, the run's own actor may always cancel, `core:tasks.manage` may cancel any; `Approve` required iff the definition needs approval; `View` zero = `core:tasks.view`. **Approval** (SEC-24, today's rule kept, `opsv3/runs.go:395-410`): by a `session`, an API key, or a verified identity extension (CF Access); an API key must name the `Approve` scope **explicitly** in its scopes (not through implication or `core:admin`); `LegacyFullScope` keys and `session_delegated` are refused | closed |
| Realtime topics (05-realtime) | the topic's permission from the one events topic catalog (05-events, SIMP-5) checked per subscriber per event; principal revalidated on heartbeat and invalidation | closed |
| Settings sections (05-config) | each section declares `ReadPerm`, `WritePerm` (default write `<ns>:settings.manage`, core sections `core:settings.manage`); keys carry a `Protection` level (05-config), and keys whose change could mint admins, spoof client addresses or apply updates are `ProtectAdmin` (needs `core:admin` + an interactive method, SEC-6; the per-key column is in 05-authn §8 and 05-http-api §10); secret fields are write-only regardless | closed |
| Storage/backup/updater admin ops | core routes; backups per 05-backup's route table (SEC2-8): list and verify `core:system.view`; create `core:system.manage`; delete, import and download `core:system.manage` + `Guards.Interactive` (import and download audited); restore `core:backup.restore` (§6.1, SEC-16); storage and updater admin `core:system.manage` | closed |
| UI hints | `/api/core/v1/me` returns `permissions` (concrete, expanded) and `patterns`; `@mediacore/shell` `can(scope)` implements §4.4 in TS. **Advisory only**: the server is authoritative. Matching vectors shared in `core/rbac/testdata/match_vectors.json`, run by Go and vitest | n/a |

### 4.11 Interplay with auth modes

- Auth disabled: the principal's checker is `authz.AuthDisabled(catalog)`: every concrete scope
  that is neither `AuthRequired` nor `Sensitive`, **`admin=false`, no implication from
  `core:admin`**, non-interactive (05-authn §4.11, SEC-2). `core:admin`, `core:system.manage`,
  `core:backup.restore` and `books:admin.debug` are `AuthRequired`, so every route behind them —
  store wipes, debug edits, credential surfaces — answers 401 with auth disabled, exactly today's
  always-authenticated class (`wire_admin_role.go:28-38`). Read-only admin introspection stays
  reachable because it maps to non-`AuthRequired` admin-only scopes (§6.2), matching today's
  `adminRole` pass-through. The one deliberate exception is the `settings.manage` system detail and
  log routes, whose successors need `core:system.manage` (§6.2, SEC2-21).
- Zero users: anonymous; only `Public` routes (05-authn §4.2). Today's `HoldsPermission` first-run
  bypass (`internal/server/middleware/auth.go:498-504`) is removed.

## 5. Lifecycle

1. **P1** (06 §6.5.1) checks only the static namespace rules of every manifest's permission names;
   no `rbac` service exists yet. **P4d step 1**: during the compatibility window the kernel rebuilds
   `c/rbac/role/…` from the legacy rows (§6.3); after a restore, **P4d step 5** runs rbac's and
   audit's restore hooks (§7.1) as package functions.
2. **P5**: the kernel creates `rbac.Service` (over the `c/rbac/` handle issued in P4c) and
   `audit.Store` (R4: L4, same layer as `auth`) (COR2-7).
3. Core declares its permissions, seeds and (when enabled) the ABO legacy alias table; then each
   enabled module's manifest is declared via `Declarer` (06 A1: permissions and roles are manifest
   data). Disabled modules declare nothing; their scopes stay stored in roles but are inert (not in
   catalog → never `Has`), and reappear when the module is re-enabled.
4. `Freeze`: validate (§4.9), compute closures, run seed merge (§4.8) in one storage batch with its
   audit records, bump generation.
5. `httpapi.Build` validates route requirements against the frozen catalog.
6. Runtime: role writes bump `Generation()` and call `Deps.OnChange`, through which the kernel
   publishes `core.rbac.changed.v1`; caches key on generation, so the next request sees the change.
7. `audit` starts its async writer goroutine; the kernel registers `core.audit.prune` on
   `tasks.ScheduleFromConfig("core.audit.schedules.prune")`, a declared `tasks.ScheduleConfig` key in
   audit's own namespace (resolution R2-7, SIMP2-1). On shutdown the async queue is drained (bounded
   5 s), remaining events counted as dropped.

## 6. Migration from ABO (atoms, roles, stored data)

### 6.1 The 13 atoms → new permissions

09-abo-on-core §9.2.6 fixed the invariant (allow/deny identical for all 572 routes × 4 seed roles,
`TestLegacyRoleReachUnchanged`, extended here to scoped API keys and auth-off mode, §12) and
proposed names; this table is the normative mapping. It equals 09's proposal except where noted.

| Legacy atom | Routes (`raw/09` §1.3) | Canonical expansion | Reverse (mirror) rule | Difference from 09 proposal |
|---|---|---|---|---|
| `library.view` | 147 | `books:view`, `core:tasks.view` | iff `books:view` | 09 also lists `core:events.subscribe`: dropped — the event stream is `AnyAuthenticated` with per-topic permissions (05-realtime §4.4), so no subscribe permission exists |
| `library.edit_metadata` | 111 | `books:edit` (implies `books:view`) | iff `books:edit` | — |
| `library.delete` | 22 | `books:delete` (implies `books:view`) | iff held | — |
| `library.organize` | 9 | `books:organize` (implies `books:view`) | iff held | — |
| `scan.trigger` | 15 | `books:scan` | iff held | 09 also lists `core:tasks.run`: dropped — task start is the definition's `Permissions.Start` (05-tasks §5T.3.7), there is no global run permission |
| `integrations.manage` | 12 | `core:integrations.manage` (implies `core:integrations.read`), `books:integrations.manage` | iff **both** | adds the implied read scope used by the integrations registry's list/get routes (05-metadata) |
| `users.manage` | 20 | `core:users.manage` (implies `core:users.view`), `core:roles.view` | iff `core:users.manage` | adds `core:roles.view` so holders can still see role names when assigning. HEAD's in-handler `users.manage` gates on user-identifying text (run error text and current-item labels, `opsv3/events.go:609-616`, `view.go:59-64`) map to `core:users.view`, the read-side successor (COR2-2, 05-realtime §4.7) |
| `settings.manage` | 83 (and old op-def default) | `core:settings.manage`, `books:settings.manage`, `core:tasks.manage`, `core:system.view` | iff **all four** | adds `core:system.view`: today `settings.manage` gates system status (`wire_system_routes.go:28`) |
| `playlists.create` | 0 | `books:playlists.create` | iff held | now enforced on playlist write routes (among the 36 permission-less routes, `raw/09` §4) |
| `collections.manage` | 6 | `books:collections.manage` | iff held | — |
| `requests.create` | 0 | `books:request` | iff held | — |
| `requests.approve` | 0 | `books:request.approve` (implies `books:request`) | iff held | — |
| `fingerprint.worker` | 5 | `books:fingerprint.worker` (`Confining`) | iff held | — |
| role-name check `admin` | 20 `RequireAdmin` + handlers | see §6.2 | role `admin` unchanged | see §6.2 |

New permissions with no legacy atom: `core:admin` (`AuthRequired`), `core:self`,
`core:roles.view` (`ClassView`), `core:roles.manage` (`Sensitive`), `core:users.view` (`ClassView`),
`core:audit.view` (`ClassView`), `core:system.status` (`ClassView`; readiness tokens),
`core:system.view` (`ClassView`; status detail, sysinfo and cache introspection — held by default
only through `core:admin` and the `settings.manage` expansion), `core:system.manage`
(`AuthRequired`; resets, logs, storage admin), `core:backup.restore` (`Sensitive`, `AuthRequired`,
SEC-16), `core:integrations.read` (`ClassView`; list and read integration instances, never secret
values; implied by `core:integrations.manage`, otherwise held by default only through `core:admin`),
`core:tasks.view` (`ClassView`), `core:tasks.manage`, `core:metrics.read` (`ClassView`),
`core:library.manage` and `core:library.browse` (declared by 05-library §5L.3; held by default only through
`core:admin`; see §14 item 7),
`books:reading` (own progress/state: the reading-state and position routes among the 36),
`books:admin.view` (`ClassView`; read-only admin introspection), `books:admin.debug`
(`AuthRequired`, `Sensitive`). `books:view` is `ClassView`.

### 6.2 Seed roles and admin-gated routes

| Legacy role (ID kept) | After migration |
|---|---|
| `admin` | core seed, locked, `core:admin` |
| `editor` | core seed `editor`; books' `DefaultGrants` supply the books permissions (§4.8 table) |
| `viewer` | core seed `viewer`; books' `DefaultGrants` supply `books:view, books:request, books:reading` |
| `fp-worker` | books module seed role with legacy ID `fp-worker` (declared in books' `LegacySurfaces`), `books:fingerprint.worker` only — no `core:self` |

The 20 `RequireAdmin` routes keep today's two kinds (`wire_admin_role.go:19-57`, decision table in
`docs/decisions/2026-10-30-admin-routes-auth-off.md`), so both the role reach and the auth-off reach
stay identical:

| Today's kind | Routes (all under `/api/v1`) | Canonical permission | With auth off |
|---|---|---|---|
| read-only (`adminRole`) | `GET /cache/stats/keys` | `core:system.view` | open (not `AuthRequired`) |
| read-only (`adminRole`) | the four `GET /admin/debug/*` | `books:admin.view` | open |
| read-only (`adminRole`) + `users.manage` | master-list routes | `AllOf(core:users.manage, books:admin.view)` | open |
| always-authenticated | `PATCH /admin/debug/books/:id`, `PATCH /admin/debug/book-files/:id`, `POST /admin/debug/edits/:edit_id/undo` | `books:admin.debug` | 401 |
| always-authenticated | `POST /system/reset`, `POST /system/factory-reset` | `core:system.manage` | 401 |
| always-authenticated | retired 410 stubs `/maintenance/wipe`, `/cache/invalidate`, `/admin/recompact-digests` | `core:admin` (stub keeps auth-first ordering) | 401 |
| always-authenticated | guest service tokens list, plan, revoke | `core:admin` | 401 |

v1.0.0 mapped the read-only debug and master-list routes to `books:admin.debug`, which is
`AuthRequired` and would have closed them with auth off, contrary to today's pass-through. Only
`admin` holds the new permissions by default, so every seed role's allow/deny with auth on is
unchanged.

**`settings.manage` system routes: one deliberate auth-off tightening (SEC2-21).** HEAD's `s.perm`
passes every request with auth off (`wire_admin_role.go:18-22`), so the `settings.manage` system
routes are open then. Their successors split:

| Today (`/api/v1`, `settings.manage`) | Successor (05-health, 05-logging) | With auth off |
|---|---|---|
| `GET /system/status` | `GET /api/core/v1/system/status`, `core:system.status` (readiness tokens only) | open, as today |
| `GET /system/status` detail, `GET /system/storage`, system info | `GET /api/core/v1/health`, `/system/info`: `core:system.manage` | **401** (tightened) |
| `GET /system/logs`, `/system/activity-log` journal | 05-logging journal query and live tail: `core:system.manage` | **401** (tightened) |
| diagnostics bundle (new) | `core:system.manage` + `Guards.Interactive` | 401 |

`core:system.manage` is `AuthRequired` on purpose: detail text, versions, host data, log lines (file
paths, user names, provider errors) and bundles stay behind a credential (SEC-8). The tightening is
recorded in 09 §9.2.6 and these aliases are excluded from the auth-off half of
`TestLegacyRoleReachUnchanged`; every other route keeps its auth-off reach.

### 6.3 Stored-data migration and compatibility

Data movement follows cross-cutting resolution 5 of the round-1 dispositions and 09 §9.2.3
(DATA-3, SEC-1, SEC-19):

- **Legacy rows stay authoritative for the window.** `role:` and `apikey:` (with their `idx:`
  rows, DATA-5) are the source of truth; the core copies `c/rbac/role/…` and `c/auth/apikey/…` are
  **rebuilt from legacy at every boot** (families are small; over the cap the rebuild falls back to
  the resumable path of 05-storage). A deactivation or revocation made by an older build is
  therefore always honoured; there is no field merge.
- **Native writes** (role CRUD, key creation) write the legacy row, its `idx:` rows and the core
  row in **one batch**; a failed legacy write fails the change. This includes **every revocation**
  — a role unassigned, narrowed or deleted, interactively, by journal re-application or by the
  post-restore hook (§7.1): it reaches the authoritative legacy row in the same batch as the native
  one (resolution R2-2), so the next boot's rebuild keeps it and a rollback build sees it.
- **Legacy atoms that the legacy build enforces more weakly (SEC2-2; resolution R2-1).** "Never
  widen" is checked per atom, but HEAD enforces `users.manage` with no dominance and no `CanAssign`
  (temp-login for anyone, `PUT /auth/me/password {user_id}`, assigning `admin`) and lets
  `settings.manage` change every sign-in field (`oauth_default_role=admin`, "admin would mint
  admins"). S4 makes roles editable, so an operator may create `custom.helpdesk =
  {core:users.manage}` for a non-admin, which dominance keeps safe natively; mirrored, that role
  holds `users.manage`. The fix is in the rollback target, not the mirror: the **identity Teach
  build** (05-authn §11.1), below which no build can open the store after S4, bounds `users.manage`
  by dominance and `CanAssign` and `settings.manage`'s protected fields by legacy-admin equivalence
  in its legacy handlers, so a rollback cannot turn a bounded custom role into admin power. The same
  build bounds the legacy write path of integration URLs and secrets (the `settings.manage` config
  update; legacy `integrations.manage` routes write neither) by the native 05-metadata M1.3 rule:
  secrets cleared on a host change, private targets admin-only (05-authn §11.1 item 3; §14 item 6).
  `TestTeachBuild_UsersManageDominance` / `TestTeachBuild_SettingsManageProtectedFields` /
  `TestTeachBuild_IntegrationURLBound` (05-authn §10) drive the real legacy handlers against a
  mirrored store.
- **Native-only data** (canonical scopes with no exact legacy form, a seed role's
  `Added`/`Removed`/`PendingSeed`, `Origin=custom`, tombstones) lives in core **side records**
  keyed by the legacy ID (`c/rbac/side/role/<id>`, `c/auth/side/apikey/<id>`) and is merged in at
  rebuild. A side record whose legacy row was deleted, or revoked/disabled by the previous build, is
  dropped — **except** a row the mirror itself wrote restricted (`SideRecord.Restriction == RestrictionDisabled`) that is
  still `Fresh` (05-storage §5S.3.11): that is a native-only key and stays active natively, so a narrow
  native API key survives every boot of the new build; once the previous build rewrites the row,
  legacy wins and the side record is dropped.
- **Roles** translate legacy `Permissions` through §6.1 into `SeedPermissions` for seed roles (diff
  empty — ABO never allowed edits) or `Added` for any non-seed row (09 probe P2: any atom outside
  §6.1 makes boot refuse naming the role — fail-closed, owner maps it). The mirrored legacy row
  holds the reverse-mapped atoms; dropping an atom from a role only narrows it (an empty legacy role
  grants nothing).
- **API keys never widen on rollback (SEC-1).** In the legacy binary an empty scope list means
  "every permission of the owner's roles" (`middleware/auth.go:405-408`). A native key is mirrored
  with legacy atoms **only when the atoms re-expand to exactly its canonical scopes**; otherwise the
  legacy row is written **disabled** (`LegacyProjection{Restrict: RestrictionDisabled, Reason: "no exact legacy
  scope set"}`, recorded as `SideRecord.Restriction`, 05-storage §5S.3.11) and the canonical scopes live
  in the side record, so the old build can never read the key as unscoped, while the new build keeps
  serving it natively. The exact legacy form of that disabled row (`status: "revoked"`, `revoked_at`,
  `mirror_reason`, sentinel scope `mirror.disabled`), the Teach build's refusal to change its status,
  the side record's `Restriction` and the most-restrictive rebuild are specified in 05-authn §11 and
  §11.1 (SEC2-1, DATA2-3). A legacy key with empty scopes is adopted as `LegacyFullScope=true` and
  mirrored back unchanged — only if it has no `mirror_reason` and no side record.
  Property test `TestMirror_NarrowKeyNeverBecomesEmptyScope`: for random native scope sets, the
  legacy effective set is ⊆ the native effective set, and empty only for `LegacyFullScope`; and after
  any legacy rewrite reachable through the Teach build's API, re-adoption is never `LegacyFullScope`.
- **Compatibility window.** The window is the one defined in 09 §9.2.1 and recorded in the `compat`
  ledger (09 §9.2.9) under surface `rbac_atom`; the legacy alias table is enabled by the
  compile-time `app.Options.LegacyPermissionAliases` (no runtime flag, SIMP-7). During the window:
  - key-creation and role-write APIs accept legacy atoms, store the canonical expansion, count
    `mediacore_compat_legacy_use_total{surface="rbac_atom"}`, and answer with `Deprecation` and a
    `deprecated_permissions` field;
  - `/api/v1/auth/me` (legacy alias) returns both `permissions` (canonical) and
    `legacy_permissions` (reverse-mapped) until the UI reads canonical names;
  - from 09 S4 the legacy mount's `s.perm(atom)` and in-handler `auth.Can(ctx, atom)` map through
    `Catalog.Legacy` and require **all** of the expansion, preserving the old meaning.
- Contract (09 C4, gated by `TestNoContractBeforeSunset` and the usage counter): the alias table is
  removed by a PR, legacy strings are refused with 400 `unknown_permission` naming the canonical
  permission, mirror writes stop, side records fold into the core rows.

## 7. Audit log (`core/audit`, G13)

### 7.1 What is recorded

Actions follow 03 §3.6: `<ns>.<noun>.<verb>`, exactly three segments, each `[a-z][a-z0-9_]*`, the verb
in the **present tense** (`core.apikey.create`; COR2-19). Success and failure of one action share its
name and differ in `Outcome` (a failed login is `core.auth.login` with `Outcome: failure`). This table
is normative; other files use these names.

| Noun (prefix) | Write path | Actions |
|---|---|---|
| `core.auth.*` | login success **sync**; failure/throttle **async** | `login` (success/failure; `Reason` `throttled` when the throttle refused), `logout`, `setup` |
| `core.bootstrap.*`, `core.identity.*`, `core.templogin.*`, `core.invite.*` | sync | `bootstrap.redeem`; `identity.link`, `identity.unlink`, `identity.displace` (email displacement); `templogin.mint`, `templogin.consume`; `invite.create`, `invite.accept` |
| `core.session.*`, `core.apikey.*` | sync (in batch) | `session.revoke`; `apikey.create`, `apikey.revoke`, `apikey.set_status`, `apikey.require_confirmation` |
| `core.user.*` | sync (in batch) | `create`, `disable`, `enable`, `delete`, `reset_password`, `set_roles` |
| `core.role.*` | sync (in batch) | `create`, `update`, `delete`, `reset`, `update_seed`, `accept_seed` |
| `core.permission.deny` | async, coalesced | every 403 from httpapi stage 13, from `rbac.Require` (including the legacy mount's `s.perm` adapter from 09 S4), every 403 `dominance_required` and every 403 `cross_origin_rejected` (from 09 S4; before that the access log's `security_event`, 05-http-api §4.2) |
| `core.task.*` | sync | `approve`, `reject`, `cancel` (by another actor) |
| `core.settings.*` | sync (in batch) | `update`: section + changed key names (values of secret fields never) |
| `core.oidc_provider.*` | sync (in batch) | `create`, `update`, `delete` (issuer, `link_existing_by_email`) |
| `core.integration.*` | sync (in batch) | `create`, `update`, `change_url` (secrets cleared, SEC-15), `write_secret`, `delete`, `test` |
| `core.secret.*` | sync | `rotate` |
| `core.backup.*`, `core.restore.*`, `core.system.*`, `core.update.*`, `core.diagnostics.*` | sync | `backup.create`, `backup.download`, `backup.import`, `restore.stage`, `restore.apply`, `restore.revoke` (05-authn §4.14 counts), `system.factory_reset`, `update.apply`, `update.rollback`, `diagnostics.download` |
| `core.audit.*` | sync | `export`, `prune` |
| `<module>.*` | module-defined, via `audit.Logger` | ABO: `books.user.merge`, `books.guest_token.mint`, `books.guest_token.revoke`, `books.admin_debug.edit`, `books.admin_debug.undo` |

Routine successful reads are **not** audited (that is the access log's job); downloads of
diagnostics, backups and the audit export are, because each copies sensitive data off the host.

**Revocations journaled by rbac (SEC2-10).** One `Subject` cannot say what a role removal removed,
so `journal.Revocation` carries `Object entity.Ref` and `Detail []string` (05-backup §5B.3.2), and
rbac appends, before the change commits: `role.unassigned{Subject: user, Object: role}` (a role
taken from a user, directly or by `?reassign=`), `role.narrowed{Subject: role, Detail: removed
permissions}` (an edit that removes permissions, including an accepted seed withdrawal) and
`role.deleted{Subject: role}`. The identity Teach build journals the legacy equivalents it performs
(05-authn §11.1 item 4).

**Restore hooks (SEC-17, R2-2).** The restore record is written to 05-backup's out-of-DB restore
journal (cross-cutting resolution 8). `audit` and `rbac` each provide a `journal.RestoreHook`, run by
the kernel as package functions at P4d step 5 (06 §6.5.1), after the swap, the identity rebuild and
the journal re-apply, and before listeners open; boot fails if either fails.

- **rbac's hook** re-applies, through the role mirror (legacy row + native row + side record in one
  batch, so the next boot's rebuild keeps it and a rollback build sees it): `role.unassigned` →
  remove the role from the user; `role.narrowed` → remove `Detail` from the role (from `Added`, or add
  to `Removed` for seed permissions); `role.deleted` → delete the role (seed roles: tombstone) and
  unassign it. When a re-application would remove the last active `core:admin` holder, it is
  **kept**, health reports `degraded` (`restore_last_admin_kept`) and the audit record says which
  entry was skipped — a lockout is worse than a stale admin the operator can demote. A demoted admin
  therefore stays demoted after restoring an archive taken before the demotion
  (`TestRestore_DemotedAdminStaysDemoted`).
- **audit's hook** opens `rc.Previous("core/audit")` — the read-only view of the pre-swap store
  limited to `c/audit/`. The boundary is clock-free (DATA2-7): the archive's manifest records the
  audit `Sequence` high-water mark **before** its checkpoint (`rc.AuditHighWater`), and every pre-swap
  record with a higher `seq` is copied. **Before copying** (SEC2-12, DATA2-16) the hook calls
  `Sequence.AdvancePast(ctx, H)` on the restored store's audit sequence, `H` being the highest `seq` it
  will copy (05-storage §5S.3.2): its **own** Sync commit of `hw = max(hw, H+1)`, made before the
  copy batch, so a crash between the two can only skip IDs. It then copies records and their index
  rows with their keys unchanged, so the next allocation can never overwrite a copied record; a record already present with the same
  `seq` (allocated between the high-water read and the checkpoint) is identical and rewritten
  idempotently. It then appends the staged and applied records of `rc.Journal`, the
  `core.restore.revoke` record with the counts auth's hook returned (05-authn §4.14), and
  `core.restore.apply`. When `rc.Previous` is nil (no live store before the restore) only the journal
  and restore records are appended.

### 7.2 Storage layout

Keyspace `c/audit/`: `rec/<seq:8B big-endian>` → record JSON; secondary indexes
`idx/actor/<userID>/<seq>`, `idx/action/<action>/<seq>`, `idx/target/<module>/<kind>/<id>/<seq>`
(empty values). `seq` comes from a 05-storage `Sequence`, which persists its high-water mark before
handing out IDs (DATA-19): a crash can skip IDs, never reissue one. Synchronous records committed via
`RecordIn` share the caller's batch (the change and its record commit or fail together), so a
discarded batch also leaves a gap. **`seq` is unique and increasing in allocation order, not
gap-free and not strictly commit-ordered** (COR-15); readers order by `seq` and must not treat a gap
as tampering.

### 7.3 Volume control

Denials and login failures go through `Emit` → bounded channel (`core.audit.async_buffer`, 4096) →
one writer goroutine that batches up to 256 records or 100 ms. Identical denials (same actor,
route, permission, outcome) within `core.audit.coalesce_window` (1 min) are folded into one record
with `Coalesced=n`. Full queue → drop + `mediacore_audit_dropped_total` (fail-open: availability
under a credential-stuffing burst beats completeness; the throttle and access log still see it).

### 7.4 Integrity (no hash chain)

v1.0.0 chained records with `SHA-256(PrevHash || record)`. That is dropped (SIMP-20, which also
resolves COR-15): with no secret key and no external anchor, anyone able to edit Pebble rows can
recompute the chain, and accidental corruption is already caught by Pebble block checksums. The
audit keyspace is append-only through the API (no update or delete except retention, §7.5) and is
covered by backups. There is no `Verify`, no anchor key and no verify route.

### 7.5 Retention

Delete when older than `core.audit.retention.max_age` (default 365 d) **or** beyond
`core.audit.retention.max_records` (default 1,000,000), whichever trims more; task
`core.audit.prune` (on `tasks.ScheduleFromConfig("core.audit.schedules.prune")`, default daily, §5),
`tasks.Parallelism` sequential with reason "single
ordered range delete on one keyspace". Pruning writes `core.audit.prune{from_seq,to_seq,count}` as
a record.

### 7.6 Query API

`GET /api/core/v1/audit?since=&until=&actor=&action=&outcome=&target=&cursor=&limit=`
(`core:audit.view`; one of the opt-in cursor routes of 05-http-api §4.4, cursor over `seq`
descending), `GET /api/core/v1/audit/export` (NDJSON stream, `core:audit.view`, `RateExpensive`,
audited `core.audit.export`). Records whose action is `core.auth.*` or `core.user.*` about
**another** user are visible only to `core:users.manage` holders as well (today's activity-log rule,
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
| Module grants foreign or core scope, or a restricted DefaultGrant | boot error | closed | escalation |
| Implication cycle | boot error | closed | |
| Unknown role ID on a user (role deleted out of band) | skipped, `mediacore_rbac_unknown_roles_total` | closed (grants nothing) | today's behaviour (`middleware/auth.go:436-442`) |
| Stored scope unknown to the catalog (module disabled or removed) | inert, kept in storage | closed | re-enable restores |
| Permission check with no principal | false | closed | |
| Confined principal on a requirement not naming its scope | 403 `permission_denied` | closed | worker/facade confinement |
| Sensitive scope, non-interactive principal | 403 `interactive_required` | closed | SEC-23 |
| Role write without actor holding scopes | 403 `scope_escalation` | closed | |
| Action on a user the actor does not dominate | 403 `dominance_required` | closed | SEC-4 |
| Version mismatch on role write | 412 | closed | |
| Last-admin removal | 409 `last_admin` | closed | lockout protection |
| Seed merge storage error at boot | boot error | closed | partial seed state is worse than not starting |
| Legacy rebuild of the core copy fails at boot | boot error | closed | serving a stale copy could resurrect a revoked key |
| Sync audit write fails | the change fails (same batch) | closed | attribution of security changes is required |
| Async audit queue full | drop + metric | open | availability |
| Audit query store error | 503 | closed | |
| Restore audit copy-forward fails | boot error | closed | SEC-17 |
| Restore role re-application would remove the last admin | kept; health `degraded`; audited | open (that entry) | lockout protection (SEC2-10) |
| Seed-role edit while the role mirror is active | 409 `seed_role_locked_during_window` | closed | a rollback would silently widen it (DATA2-20) |
| Prune fails | task fails, retried next run; growth alarm when > 1.5 × max_records | open | |

## 9. Concurrency contract

- `Catalog` is immutable after `Freeze`; lock-free reads.
- Compiled checkers are immutable; cached in a sharded LRU keyed by `(sorted role IDs, sorted
  scopes, interactive, generation)` (`core.rbac.cache_size`, 4,096); a generation bump makes old
  entries unreachable (no explicit purge needed).
- Role writes: one storage batch per write with optimistic `Version` check; last-admin check reads
  holders inside the same batch snapshot (05-storage snapshot reads), so two concurrent demotions
  cannot both pass (`testing/synctest` test).
- `audit`: one writer goroutine for async events (`wg.Go`, stopped by app context); sync records
  take their `seq` from the storage `Sequence` (goroutine-safe) and never wait on the async queue.
- No goroutine per check, per request or per record.

## 10. Config keys

`Protection` levels are defined by 05-config; `admin` = `ProtectAdmin` (requires `core:admin` and an
interactive method, SEC-6).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.rbac.cache_size` | int | 4096 | no | none |
| `core.audit.retention.max_age` | duration | 8760h | yes | admin |
| `core.audit.retention.max_records` | int | 1000000 | yes | admin |
| `core.audit.schedules.prune` | `tasks.ScheduleConfig` | `{enabled: true, every: 24h}` | yes (re-read every poll) | none |
| `core.audit.async_buffer` | int | 4096 | no | none |
| `core.audit.coalesce_window` | duration | 1m | yes | none |
| `core.audit.details_max_bytes` | int | 4096 | yes | none |

The v1.0.0 runtime key `core.rbac.legacy_aliases` is removed: the alias table is a compile-time app
option governed by the compat ledger (§6.3, SIMP-7).

## 11. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_rbac_denials_total` | counter | `owner` (module/core), `scope` (catalog-bounded) |
| `mediacore_rbac_checker_cache_total` | counter | `result` (`hit`,`miss`) |
| `mediacore_rbac_unknown_roles_total` | counter | — |
| `mediacore_rbac_generation` | gauge | — |
| `mediacore_rbac_roles` | gauge | `origin` |
| `mediacore_rbac_pending_seed_roles` | gauge | — |
| `mediacore_audit_records_total` | counter | `namespace`, `outcome` |
| `mediacore_audit_dropped_total` | counter | — |
| `mediacore_audit_queue_depth` | gauge | — |
| `mediacore_audit_write_errors_total` | counter | `path` (`sync`,`async`) |
| `mediacore_audit_pruned_total` | counter | — |

## 12. Tests

| Test | Asserts |
|---|---|
| `FuzzParsePermission` / `TestScopeGrammar_Table` | grammar, one sub level, lowercase |
| `TestWildcard_RolesAllowedKeysRejected` | §4.2 table |
| `TestChecker_MatchVectors` | runs `testdata/match_vectors.json` (also run by vitest in `@mediacore/shell`) |
| `TestImplies_ClosureAndCycle` | |
| `TestCompile_KeyNarrowsRoles` | incl. wildcard role ∩ concrete key |
| `TestAdmin_IsScopeAndKeyScopingApplies` | admin user's `books:view` key → 403 on `core:admin` route (regression for `RequireAdmin`) |
| `TestSensitive_EvaluatedBeforeAdmin` | bootstrap-style `["core:admin"]` API key: `Has(core:roles.manage)` false; interactive admin session: true (SEC-23) |
| `TestAuthDisabled_CheckerHasNoAdmin` | `authz.AuthDisabled` holds no `AuthRequired`/`Sensitive` scope, `admin=false`, `Has(core:system.manage)` false (SEC-2) |
| `TestAuthOff_AdminRouteKindsUnchanged` | the §6.2 table: read-only kinds open, always-authenticated kinds 401 with auth off |
| `TestConfining_WorkerKeyRefusedOnPlaylists` | `books:fingerprint.worker` key → 403 on the playlist routes and every `AnyAuthenticated` route (regression for `middleware/auth.go:289`), 200 on the worker routes |
| `TestConfining_FacadeKeyRefusedOnNative` (deferred-build: lands with `arrfacade`, SIMP2-6) | `tv:arr.read` key → 403 on every non-facade route (SEC-5) |
| `TestRestore_DemotedAdminStaysDemoted` | archive taken before an admin's demotion; restore inside the window, restart, roll back to the Teach build: the account is not admin anywhere; a re-application that would remove the last admin is kept with health `degraded` (SEC2-10, R2-2) |
| `TestAudit_CopyForwardThenWriteNoOverwrite` | after a restore with copied records, the next N records get fresh `seq`s and no copied record or index row changes (SEC2-12, DATA2-16) |
| `TestSeedRole_EditRefusedDuringWindow` | `PATCH /roles/editor` → 409 `seed_role_locked_during_window` while the role mirror is active; custom roles editable; after C4 allowed (DATA2-20) |
| `TestModuleSystemActor_CannotStartUndeclaredKind` | (in 05-tasks) the system actor's checker = own kinds' `Start` ∪ `TaskStarts`, never `core:admin`/`AuthRequired` (SEC2-15) |
| `TestAuditAction_Grammar` | every action literal in core and in this table matches `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$` (COR2-19) |
| `TestAnyAuthenticated_ModuleOnlyKeyReachesCoreEvents` | key scoped only `books:scan` passes `AnyAuthenticated` on `GET /api/core/v1/events` (SEC-3) |
| `TestManifest_RejectsForeignNamespace` / `TestManifest_RejectsCoreGrant` / `TestManifest_RejectsCoreLegacyAliasFromModule` / `TestManifest_RejectsRestrictedDefaultGrant` | §4.9 |
| `TestSeedMerge_PreservesOperatorRemoval` / `TestSeedMerge_NewSeedScopeReachesUneditedRole` / `TestSeedMerge_TombstoneNotRecreated` / `TestSeedMerge_Idempotent` | property test over random seed/edit sequences: effective = (seed ∪ added) \ removed |
| `TestSeedMerge_RestrictedAdditionHeldPending` | a release adding a `Sensitive` scope to an existing role leaves it in `PendingSeed` until `accept-seed` (SEC-25) |
| `TestRoleWrite_EscalationRefused` / `TestCanAssign_CannotGrantUnheld` / `TestCanAssign_RemovalCheckedLikeAssignment` | |
| `TestUsersManage_CannotTempLoginAdmin` / `TestUsersManage_CannotResetDominatingUser` | a custom role with `core:users.manage` only → 403 `dominance_required` (SEC-4) |
| `TestStart_ViewOnlyDefRefusedForViewer` | (in 05-tasks) a def whose `Start` is `ClassView` fails registration without `StartableByViewers` (SEC-9) |
| `TestApprove_KeyMustNameScope` | (in 05-tasks) API key approves only with the `Approve` scope named; `LegacyFullScope` and delegated sessions refused (SEC-24) |
| `TestLastAdmin_ConcurrentDemotions` | `testing/synctest`, two concurrent demotions → one 409 |
| `TestLegacyAtomMapping_Golden` | the 13 rows of §6.1 |
| `TestMigration_RolesUsersKeys` | fixture DB from ABO key families → expected `c/rbac`/`c/auth` rows and side records |
| `TestMirror_NarrowKeyNeverBecomesEmptyScope` | property test, §6.3 (SEC-1) |
| `TestRebuild_LegacyRevocationHonoured` | key revoked on the old build → core copy revoked at next boot; its side record dropped (DATA-3) |
| `TestLegacyRoleReachUnchanged` (09) | extended to scoped API keys and auth-off mode, not only the 4 seed roles |
| `TestAudit_SyncInSameBatch` | failing batch → neither change nor record |
| `TestAudit_SeqUniqueAcrossCrash` | `Sequence` reservation: after a simulated crash, no `seq` is reissued; gaps allowed |
| `TestAudit_DenialCoalescing` | `synctest` clock |
| `TestAudit_RestoreCopiesForwardRecords` | records above the manifest's audit high-water mark survive a restore (SEC-17, DATA2-7) |
| `TestAudit_RedactsSecretsByKeyAndPrefix` | |
| `TestAudit_QueueFullDropsWithoutBlocking` | |
| `TestNoGlobals` (archtest) | no package-level registries in `rbac`/`audit` (R5) |
| `archtest.NoSystemPrincipalOutsideKernel` | `core/internal/authz` privileged constructors called only from `rbac`, `auth`, the `tasks` executor and `kernel` (SEC-26) |

## 13. Gap disposition G1–G15 (all gaps from `raw/10` §8)

| Gap | Disposition | Where |
|---|---|---|
| G1 closed hard-coded vocabulary | **Fixed**: catalog of module-declared `PermissionSpec`s; `IsKnown` → `Catalog.Lookup` | §3.2, §4.9 |
| G2 no namespace | **Fixed**: `<ns>:<action>[.<sub>]`; 13 atoms mapped; stored data migrated with compat window | §4.1, §6 |
| G3 seed-only roles, boot overwrites edits, no CRUD | **Fixed**: roles as data with CRUD API; seed merge by diff; tombstones; module seeds and grants; restricted additions held | §4.7, §4.8 |
| G4 admin by role name | **Fixed**: `core:admin` scope; role-name checks removed; key scoping applies | §4.6 |
| G5 imperative route binding, 36 permission-less routes, unenforced atoms | **Fixed**: declarative `Route.Perm` (R6, boot error), `AnyAuthenticated` made explicit, confinement explicit, `playlists.create` enforced | 05-http-api §3.1, §8.2; §4.10, §6.1 |
| G6 key scopes: empty = unrestricted | **Fixed**: new keys require non-empty concrete scopes; legacy empty → `LegacyFullScope` until expiry; key-mints-key escalation closed; rollback never widens | §4.5, §6.3; 05-authn §4.4 |
| G7 no per-app membership | **Fixed by composition**: roles composed of namespaced permissions (a user holding `tv.admin` (`tv:*`) and `viewer` (books view grants)); per-user data ownership stays app-specific | §4.6 |
| G8 plugin routes unauthenticated | **Fixed**: modules only get chained `Group`s | 05-http-api §4.2 |
| G9 auth wiring inline/global | **Fixed** | 05-authn §13 |
| G10 single-binary bootstrap | **Fixed** | 05-authn §4.6 |
| G11 CF/owner machinery in auth layer | **Fixed** (extension point; policy editing stays in ABO) | 05-authn §4.8 |
| G12 secrets at rest | **Fixed**: auth credentials hashed (05-authn §4.1); config secrets encrypted via `core/secrets` (05-config); N10 | 05-authn, 05-config |
| G13 no uniform audit trail | **Fixed**: `core/audit` schema, sync/async paths, retention, query API, restore copy-forward (no hash chain, §7.4) | §7 |
| G14 throttling tied to tunnel topology | **Fixed + documented** | 05-authn §4.9 |
| G15 two auth stacks | **Fixed**: compat resolves via `auth.Service.PrincipalFor` | 05-authn §4.10 |

## 14. Open items

1. Whether `core:self` should be implicit for every active human user instead of a grant in the
   `editor`/`viewer` seeds; granted is chosen here because it keeps "a user with no roles can do
   nothing" (today's safe default, `middleware/auth.go:424-427`) and keeps worker roles out of
   self-service routes.
2. Resource-level permissions (per-library or per-root ACLs) are not designed; `rbac.Require` plus
   module-side filtering covers today's per-user ownership (playlists, reading state).
3. Audit export to an external sink (syslog/OTLP logs) — 05-logging may add a sink; not required.
4. Coordination with 09-abo-on-core §9.2.6: drop `core:events.subscribe` and `core:tasks.run` from
   its proposed expansions and add `core:system.view` to `settings.manage` (§6.1); §6.3 replaces the
   reconcile with the rebuild-from-legacy model.
5. Coordination with 06-app-sdk §6.4.2 and 08 (COR-16, SEC-23): both use `rbac.Permission`,
   `rbac.PermissionSpec` (with `DefaultGrants`, `Warn`) and `RoleName` from this file; there is no
   `rbac.Scope`, `rbac.PermissionDef`, `rbac.Grant` or `Dangerous` field. Core seed role IDs are
   `admin`/`editor`/`viewer` (not `core.viewer`).
6. **Resolved (lead decision, round-2 integration pass): `integrations.manage` on rollback.**
   SEC2-2's reviewer also named `integrations.manage`, whose legacy form keeps stored secrets across
   an integration URL change (the SEC-15 defect HEAD still has). The R2-1 Teach-build bound is
   extended to the legacy scope that path runs under on rollback: `core:integrations.manage`
   reverse-maps to `integrations.manage` (§6.1), whose legacy routes write no URL or secret, and the
   legacy URL and secret writes go through the `settings.manage` config update, so the identity Teach
   build applies 05-metadata M1.3's native rule there (secrets cleared on a scheme, host or port
   change unless re-supplied; loopback, private or CGNAT targets need legacy-admin-equivalent atoms
   and an interactive session). Rollback is never weaker than the native bound (05-authn §11.1
   item 3, `TestTeachBuild_IntegrationURLBound`).
7. **`core:library.browse` reach (found in the round-2 integration pass; owner decision before S7).** HEAD
   gates the directory picker `GET /api/v1/filesystem/browse` with `settings.manage`
   (`internal/server/wire_library_routes.go:61`); its successor needs `core:library.browse` (05-library
   §5L.3), which the §6.1 expansion of `settings.manage` does not contain, so `TestLegacyRoleReachUnchanged`
   cannot hold for that route once it moves. Either add `core:library.browse` to the expansion (the reverse
   rule then needs all five) or keep the route on the legacy mount under its legacy gate until C9.

## Objections to 03

None open. 03 v1.3.0+ R6 states that `AnyAuthenticated` never admits a confined credential (the
v1.1.0 amendment), and §3.6 has the role-ID and audit-action rows.

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
3. **`users.manage` reaches every account's credentials** (`internal/server/auth_reset_password.go:18-60`,
   `auth_temp_login.go:225-227`): latent for the same reason; fixed by design in §4.7 rule 5.

## Review history

Round 1 (2026-11-04), findings applied to this file:

- SEC-1 — §6.3: keys mirrored with legacy atoms only on exact re-expansion, else revoked; side records; property test.
- SEC-2 — `core:admin` is `AuthRequired`; auth-disabled checker has no admin flag, no Sensitive/AuthRequired scope (§4.6, §4.11).
- SEC-3 — `Confining` flag; `AnyAuthenticated` = `core:self` or any non-Confining scope; namespace heuristic removed (§3.2, §4.10).
- SEC-4 — escalation rule 5 (dominance) and removal checked like assignment (§4.7); `Dominates`.
- SEC-5 — `<m>:arr.*` scopes are `Confining`; facade confinement is rbac's (§4.10).
- SEC-6 — `ProtectAdmin` referenced for settings keys; Protection column (§4.10, §10).
- SEC-8 — `core:system.view` dropped from editor/viewer seeds; `core:system.status` added; `settings.manage` expansion gains `core:system.view` (§4.8, §6.1).
- SEC-9 — `PermissionSpec.Class`; task `Start` not satisfiable by a view-class scope; legacy floor kept (§4.10).
- SEC-16 — `core:backup.restore` (Sensitive, AuthRequired) added (§6.1, §4.10).
- Cross-cluster (R-F) — `core:integrations.read` declared, implied by `core:integrations.manage` (§6.1).
- SEC-17 — integration, secret, OIDC-provider, diagnostics, backup and export actions; restore copy-forward; bridge denials audited (§7.1).
- SEC-23 — `Sensitive` evaluated before `core:admin` (§4.4); `Warn` field; 08 names aligned (§14 item 5).
- SEC-24 — approval rule: session, key naming `Approve` explicitly, or verified extension; LegacyFullScope and delegated refused (§4.10).
- SEC-25 — restricted `DefaultGrants`; restricted seed additions held in `PendingSeed` (§4.8, §4.9).
- SEC-26 — `rbac.All` removed; privileged checkers in `core/internal/authz` plus archtest (§3.4).
- OPS-4 — same fix as SEC-1.
- DATA-3 — legacy authoritative, rebuild at every boot, side records (§6.3).
- COR-15 — resolved by SIMP-20; `seq` from `Sequence`, gaps allowed (§7.2).
- COR-16 — owner names stated for 06/08; role ID objection closed (03 v1.3.0) (§14, Objections).
- Cross-cluster (R-A) — `rbac.ReservedNamespaces()`; core routes registered from `internal/rbachttp`/`internal/audithttp` (§3).
- Cross-cluster (R-C) — `core.rbac.changed.v1` published through an injected `OnChange` callback; `rbac` does not import `events` (§3, §3.4, §5).
- COR-17 — `audit.Event.Target` is `entity.Ref` (§3.5).
- COR-19 — layer numbers per 03 v1.2.0 (§3).
- COR-20 — `*metrics.Scope` in `Deps`; bus dependency replaced by a callback (§3.4).
- SIMP-6 — audit prune on `tasks.Schedule` bound to a config key (§5, §7.5).
- SIMP-7 — legacy alias window references 09 §9.2.1 / compat ledger; runtime flag removed (§6.3, §10).
- SIMP-20 — audit hash chain, anchor and verify route dropped (§7.4).
- Correction found while applying SEC-2 — read-only admin routes mapped to non-AuthRequired scopes so auth-off reach is unchanged (§6.2; evidence `wire_admin_role.go:19-57`).

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-B request (SEC-17) — restore copy-forward is audit's `backup.RestoreHook`, reading the pre-swap `c/audit/` through `RestoreContext.Previous`; rbac's hook re-applies `role.removed`; `Deps.Revocations backup.RevocationJournal`.
- integration pass: R-H request (SEC-1, DATA-3) — §6.3: mirror-disabled keys that are still `Fresh` keep their side record; the legacy row is written **disabled** (`LegacyProjection.Disabled` / `SideRecord.MirrorDisabled`), matching 05-storage's terms.
- integration pass: Stale objection to 03 (R6 confinement) removed: applied in 03.
- integration pass: Verified already present (R-F request): `core:integrations.read`, implied by `core:integrations.manage`.

Round 2 (2026-11-04), findings applied to this file:

- R2-1 / SEC2-2 — legacy `users.manage`/`settings.manage` bounded by the identity Teach build (05-authn §11.1), the rollback floor; the reviewer's omit-from-mirror alternative not applied (the resolution wins); `integrations.manage` surfaced as open item 6 (§6.3, §14).
- R2-2 / SEC2-3 / DATA2-4 — role revocations (interactive, re-applied, post-restore) staged through the role mirror in one batch (§3.4, §6.3, §7.1).
- SEC2-1 / DATA2-3 — mirror-disabled key projection and monotone rebuild referenced from 05-authn §11; `LegacyFullScope` only for never-mirrored rows; property test extended (§6.3).
- SEC2-10 — role kinds `role.unassigned`/`role.narrowed`/`role.deleted` with `Object`/`Detail`; re-application per kind; last-admin kept with health `degraded` (§3.4, §7.1, §8, §12).
- SEC2-12 / DATA2-16 / DATA2-7 — audit copy-forward bounded by the manifest's pre-checkpoint `Sequence` mark; sequence raised before copying (§7.1, §12).
- SEC2-8 — backup route permissions per 05-backup's table (§4.10).
- SEC2-15 — module system actor = own kinds' `Start` ∪ `Manifest.TaskStarts`, never `core:admin`/`AuthRequired` (§3.4, §12).
- SEC2-17 — `Delegated` only on admitted `OwnAuth` routes (§3.1).
- SEC2-21 — deliberate auth-off tightening of the system detail/log successors recorded; reach table (§4.11, §6.2).
- DATA2-20 — seed roles read-only while the role mirror is active (`ErrSeedRoleLocked`) (§3.4, §4.8, §8, §12).
- COR2-2 — HEAD's `users.manage` user-text gate → `core:users.view` (§6.1).
- COR2-7 — `rbac.New` and `Declarer` at P5; P1 static checks only; hooks at P4d (§5).
- COR2-9 — leaf types land at S2 with `httpapi`; journal types from `backup/journal` (§3).
- COR2-12 — `RecordIn(ctx, b *storage.Batch, e)`; `storage` in the audit import block (§3.5).
- COR2-19 — present-tense `<ns>.<noun>.<verb>` audit actions; normative table; grammar test (§3.5, §7.1, §7.5, §12).
- SIMP2-1 — `core.audit.prune_schedule` (`tasks.ScheduleConfig`) replaces `prune_interval` (§5, §7.5, §10).
- SIMP2-6 — facade confinement test marked deferred-build (§12).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): Open item 6 resolved by the lead decision (Teach-build integration-URL bound, 05-authn §11.1 item 3); §6.3 references it; open item 7 added (`core:library.browse` reach, `wire_library_routes.go:61`); `core:library.*` listed among new permissions.
- integration pass (round 2): §7.1: `update.rollback` added; audit hook uses `Sequence.AdvancePast` as its own commit; action names `core.role.update_seed`/`accept_seed`, `core.audit.export` aligned with the table.
- integration pass (round 2): `Deps.Revocations` Append(ctx, b, r); `Deps.MirrorView`; `Restriction` terms; schedule key `core.audit.schedules.prune`.
