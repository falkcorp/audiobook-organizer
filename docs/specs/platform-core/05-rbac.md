<!-- file: docs/specs/platform-core/05-rbac.md -->
<!-- version: 1.5.1 -->
<!-- guid: c07c6c34-fabe-451c-ae99-262b2c83232f -->
<!-- last-edited: 2026-11-04 -->

# 05 — Authorization (`core/rbac`) and audit (`core/audit`)

Evidence commit `748fd787a`. Raw inputs: [`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md)
(13 atoms, 4 seed roles, gaps G1–G15 — all dispositioned in §13),
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §1.3, §4,
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §9,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §8.
Identity: [05-authn](05-authn.md). Route enforcement: [05-http-api](05-http-api.md) §4.2.
Layers (L0–L9), `entity.Ref`, naming and the compatibility window are from
[03-architecture](03-architecture.md) and [09-abo-on-core](09-abo-on-core.md) §9.2.1. Stored roles follow
round-4 resolution R4-1 (refining R3-1): `rbac.Store` is an SDK interface; core ships a native
implementation over `c/rbac/`, and an app with pre-core roles (ABO) passes its own implementation,
written in the app over the app's rows, with native-only data in an app-owned extension family that
older builds never read (§3.4, §6.3). Core holds no app key name or row format.

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
(L6, imported only by `kernel`, 03 §3.3). Roles are read and written through the SDK interface
`rbac.Store` (§3.4, R4-1): the native implementation `core/internal/rbacstore` keeps them in the
core keyspace `c/rbac/`; ABO's `books` module supplies a legacy implementation over its own role rows
(§6.3), so no storage grant is involved during the extraction. The compiled checker and the two
privileged checker constructors live in `core/internal/authz` (layer L4, the implementation of
`rbac`), so apps cannot reach them (§3.4, SEC-26). `authz` imports `rbac`; `rbac` imports nothing
from `authz`: the kernel injects `authz.Compile` as `rbac.Deps.Compile` (COR4-12). `rbac` also imports the leaf
`backup/journal` (L4; `Revocation`, `RevocationJournal`, `RestoreContext`), not `backup`, which lands
at 09 S8 (COR2-9). Package `github.com/falkcorp/mediacore/audit` (layer L4; imports `entity`,
`storage`, `log`, `metrics`; never `tasks` — the kernel registers its prune job).

**Landing (COR2-9, COR4-11).** The leaf types `Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`,
`Public`, `AnyAuthenticated`, `Delegated` (§3.1), `RoleName` (§3.3) and the `Checker` interface (§3.4;
it names nothing beyond these leaf types) land at 09 S2 with `httpapi`, whose
`Route.Perm`/`LegacyRoute.Perm` need them, whose S2 manifest annotates 36 routes `AnyAuthenticated`,
and whose `auth` leaf (`Principal.Perms`, `User.Roles`) names `Checker` and `RoleName`; this list is
the one 04 §8.1, 06 §6.4.10 and 05-http-api §3 repeat. The catalog, roles, `Store`, `Service`, the
checker implementation and `audit` land at S4.

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
	New []Permission // the canonical expansion (§6.1 column 3): what a stored atom translates to
	// Reach is the native scope set equivalent to the atom's FULL legacy reach
	// on the snapshot release (§6.3, SEC4-4): a legacy route or in-handler check
	// gated by Old requires AllOf(Reach...) (resolution R4-3), and the row rule
	// writes Old only into a role whose checker satisfies it. Reach equals New
	// for every atom whose expansion dominates its reach; it is {core:admin}
	// for users.manage and settings.manage (§6.1 column 4).
	Reach []Permission
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
func (c *Catalog) Legacy(old string) ([]Permission, bool)          // LegacyAlias.New
func (c *Catalog) LegacyReach(old string) (Requirement, bool)      // AllOf(LegacyAlias.Reach...), R4-3
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
	// Seed bookkeeping (Origin == OriginSeed): the last seed applied (§4.8).
	SeedPermissions []Permission
	SeedDigest      string       // sha256 of the sorted SeedPermissions
	PendingSeed     []Permission // restricted seed additions held for admin acceptance (§4.8, SEC-25)
	Added           []Permission // custom roles: the role's whole set (patterns allowed); on seed roles the operator diff is deferred-build (§3.6)
	Locked          bool         // "admin": immutable, undeletable, outside the seed merge (§4.8)
	// Suspended: the store found the role's native-only data stale against its
	// stored row (an older build changed the row, §6.3); while set, the role
	// grants only what the stored row grants on an older build, until an admin
	// re-confirms or discards it (ReconfirmRole). Reported by the Store.
	Suspended       bool
	CreatedAt, UpdatedAt time.Time
	Version         int64
	// Removed and Tombstone are deferred-build members (§3.6).
}

// Permissions returns the role's effective patterns: SeedPermissions for a
// seed role, Added for a custom role; for a Suspended role, the translation of
// the stored legacy atoms (§6.3). PendingSeed is not effective until accepted.
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
	Store   Store            // native core/internal/rbacstore over c/rbac/, or the app's legacy store from module.Manifest.LegacyStores.Roles (§6.3, R4-1)
	Compile func(CheckerInput, *Catalog) Checker // injected by the kernel: authz.Compile (COR4-12)
	Audit   audit.Logger
	OnChange func(generation uint64) // kernel publishes core.rbac.changed.v1 on the bus; rbac never imports events
	Revocations journal.RevocationJournal // Append(ctx, b, r) of role.unassigned / role.narrowed / role.deleted into the change's batch b before it commits (§7.1, 05-backup §5B.3.6)
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
func (s *Service) AcceptSeed(ctx context.Context, actor Checker, id RoleName, ifVersion int64) (*Role, error) // apply PendingSeed; allowed on seed roles (§4.8)
func (s *Service) ReconfirmRole(ctx context.Context, actor Checker, id RoleName, accept bool, ifVersion int64) (*Role, error) // clear Suspended: re-apply (accept) or discard the stale native-only data (§6.3)
// ResetRole is a deferred-build member (§3.6).

// CanAssign reports whether actor may give or take away roles (actor must hold
// every scope of every role named, or core:admin). Removal is checked like
// assignment (§4.7 rule 2).
func (s *Service) CanAssign(actor Checker, roles []RoleName) error

// Dominates reports whether actor may act on a user whose roles are target
// (actor holds every scope target holds, or core:admin). auth.Service calls it
// for every action on another user's credentials, status or roles (§4.7 rule 5).
func (s *Service) Dominates(actor Checker, target []RoleName) error

// Compile builds a principal's checker (cached by role-set + scopes +
// interactive + generation) through Deps.Compile. Unknown role IDs are
// skipped and counted.
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
	ErrSeedRoleLocked   = errors.New("rbac: seed roles are read-only until the role cutover")
	ErrConfiningNoLegacyAtom = errors.New("rbac: a Confining scope without a legacy atom cannot be put in a role before the role cutover")
	ErrUndecodableRole  = errors.New("rbac: stored role cannot be decoded") // per row, from Store.ScanRoles (§6.3)
)

// Store is the role store (resolution R4-1). It is SDK, frozen by the
// API-stability gate. Core's native implementation, core/internal/rbacstore,
// keeps roles in c/rbac/ (hello, coretest and every app without pre-core
// roles; §6.3 "Native store"). An app with pre-core roles passes its own
// implementation through module.Manifest.LegacyStores.Roles (06 §6.4.2), written
// in the app over the app's own rows: core holds no app key name or row
// format. Core enforces its invariants above the interface (escalation,
// last admin, row rule input via Catalog.LegacyReach); rbactest.StoreSuite is
// the conformance suite every implementation runs. Every write takes the
// caller's *storage.Batch, never a copy (COR2-12), and stages an Expect on
// the role's Version.
type Store interface {
	GetRole(ctx context.Context, name RoleName) (*Role, error)
	ScanRoles(ctx context.Context) iter.Seq2[*Role, error] // an undecodable stored role yields ErrUndecodableRole for that role only
	PutRole(b *storage.Batch, r *Role, legacyAtoms []string) error // legacyAtoms: what the row rule admits (§6.3); a native store ignores it
	DeleteRole(b *storage.Batch, r *Role) error
	Reconfirm(b *storage.Batch, r *Role, accept bool) error
}
```

**Privileged checkers are not SDK (SEC-26).** The two checkers that bypass role resolution are
constructed only in `core/internal/authz`: `authz.AuthDisabled(cat *rbac.Catalog) rbac.Checker`
(§4.11) and `authz.ForSystem(perms []rbac.Permission) rbac.Checker` (the `_system` principal of
background tasks, built from the task definition's declared permissions, never `core:admin` by
default). Go's `internal/` rule keeps them out of app code; within core,
`archtest.NoSystemPrincipalOutsideKernel` allows calls to these two constructors only from `auth`,
the `tasks` executor and `kernel` (`rbac` reaches `authz` only through the injected `Deps.Compile`,
COR4-12). The public `rbac.All` of v1.0.0 is removed.

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

### 3.6 Deferred-build members

Deferral applies to features inside built packages (SIMP2-6, 03 §3.4). These members of `rbac` are
specified so the role cutover and 08 can rely on them, and are **not built** during the extraction;
the archtest `archtest.DeferredMembersAbsent` (03 §3.4) checks their absence by package-qualified
identifier, and the PR that builds one deletes that member's entry (COR4-19, SIMP4-1):

| Member | Lands with | Contract |
|---|---|---|
| the operator diff on seed roles: `Added` on a seed role, `rbac.Role.Removed`, `rbac.Role.Tombstone`; the merge rows "digest differs" with operator edits and "tombstone" (§4.8) | the role cutover (§6.3) | effective = `(SeedPermissions ∪ Added) \ Removed`; an operator removal survives upgrades; a deleted seed role is not recreated (SIMP4-6) |
| `rbac.Service.ResetRole` and `POST /roles/{name}/reset` | the role cutover | drop a seed role's operator diff |
| `rbac.Service.UpdateRole` / `DeleteRole` on seed roles (`ErrSeedRoleLocked` lifted) | the role cutover | §4.8 |
| tests `TestSeedMerge_PreservesOperatorRemoval`, `TestSeedMerge_TombstoneNotRecreated`, `TestSeedRole_ResetClearsDiff` | the role cutover | §4.8 |
| `TestConfining_FacadeKeyRefusedOnNative` | `arrfacade` (08) | SEC-5 |

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
- Legacy keys stored with empty scopes are read as `LegacyFullScope=true`: effective = role
  permissions (today's semantics), shown in the UI as "legacy full-access key, expires <date>", never
  creatable; their rows keep the empty list as written (05-authn §11). All keys expire within 365 days (`raw/10` §2.3), so this state ends by itself; no
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
| `POST /roles/{name}/accept-seed` (apply `PendingSeed`; allowed on seed roles, §4.8) | `core:roles.manage` |
| `POST /roles/{name}/reconfirm` `{accept}` (clear `Suspended`, §6.3) | `core:roles.manage` |
| `DELETE /roles/{name}` (custom: delete if unassigned or `?reassign=`) | `core:roles.manage` |
| `POST /roles/{name}/reset`; `PATCH`/`DELETE` of a seed role | deferred-build (§3.6); 409 `seed_role_locked` until the role cutover |
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

### 4.8 Seed merge (G3)

Seed input for a role = core's seed for that role ∪ every enabled module's `PermissionSpec.DefaultGrants`
naming it ∪ (for module roles) the module's `RoleSeed.Permissions`. Each seed role stores
`SeedPermissions` (the seed input last applied), `SeedDigest` and `PendingSeed`. The rules differ
between the extraction and the role cutover (SIMP4-6).

**During the extraction (built).** At `Freeze`, for every seed role:

| Stored state | Action |
|---|---|
| the locked role `admin` | **outside the merge** (DATA4-11): always written as `{core:admin}`, with the legacy atoms the row rule admits for it (all of them, §6.3); never `PendingSeed` |
| absent | create with `SeedPermissions = seed` |
| present, `SeedDigest` equal | nothing |
| present, digest differs, or no `SeedDigest` (written by an older build, which re-seeds every boot, `internal/auth/seed.go:80-129`) | set `SeedPermissions = new seed` minus any **restricted** addition (below); put restricted additions in `PendingSeed`; log + audit `core.role.update_seed` with the permissions the release added, held or withdrew. A role without `SeedDigest` counts as last seeded with the translation of its stored atoms (§6.1) |

A *restricted* addition is a scope that is `Sensitive`, `AuthRequired` or `Confining`, or any
`*:settings.manage` / `*:integrations.manage`, added to a role that already existed, **unless the
legacy reach of the role's stored atoms already implies it** (DATA4-11: compared against the union of
`LegacyAlias.Reach` over those atoms, §3.2 — `core:admin` is implied for HEAD's `admin` — so re-seeding
a role from HEAD-written rows never parks a scope the role already held in substance). It becomes effective only when an admin accepts it
(`POST /roles/{name}/accept-seed`, audited `core.role.accept_seed`); the Roles page shows the pending
set (SEC-25). Roles created fresh from the seed take the full seed. Effective = `SeedPermissions`;
today's behaviour (overwrite every boot, `internal/auth/seed.go:117-127`) plus the restricted hold.
06-app-sdk §6.4.2 describes `DefaultGrants` as "seeded on first boot of the module only; admin edits
win afterwards"; that is this merge once the cutover adds the operator diff.

**Seed roles are read-only except for `accept-seed` (DATA2-20, R3-1, SIMP4-6).** An older build
rewrites every seed row with its own seed on every boot (`internal/auth/seed.go:80-129`, called at
`internal/server/server_lifecycle.go:1102`), so an operator's narrowing of `editor` made here would be
silently widened for the whole rollback. `UpdateRole` and `DeleteRole` on a seed role therefore answer
409 `seed_role_locked` (`ErrSeedRoleLocked`) until the role cutover; `ResetRole` does not exist yet
(§3.6). `AcceptSeed` **is allowed**: accepting adds a scope the older build does not know, and that
build's re-seed overwrites the row with its own, narrower seed — fail-closed — so the restricted hold
is never a dead end (a release adding a restricted scope to an existing seed role becomes effective
once an admin accepts it, as HEAD applies it on the next boot). `ErrLockedRole` keeps covering `admin`
on every path. Custom roles stay fully editable — older builds never rewrite non-seed rows — and the
Roles page offers "duplicate as custom role". This is no regression: HEAD has no seed-role edits at
all (G3). Of the reviewer's two options in round 3, refusal is chosen over silently cloning a seed
role into a custom role on edit (DATA3-16, SEC3-17).

**Deferred-build (lands with the role cutover, §3.6).** The operator diff `Added`/`Removed` on seed
roles, `Tombstone` (a deleted seed role is not recreated), `ResetRole` and
`POST /roles/{name}/reset`: then effective = `(SeedPermissions ∪ Added) \ Removed`, an unrestricted
permission a new release grants to `editor` reaches every `editor` role the operator did not strip of
it, an operator's removal survives upgrades, a withdrawn permission disappears unless added
explicitly, and `Removed` entries no longer in the seed are dropped.

Core seed roles (IDs unchanged from ABO, so stored `User.Roles` and `Invite.RoleID` need no rewrite,
09-abo-on-core §9.2.6):

| Role | Core seed permissions | Typical module grants |
|---|---|---|
| `admin` (locked) | `core:admin` | — (implies everything) |
| `editor` | `core:self`, `core:tasks.view`, `core:system.status`, `core:metrics.read` | `books:view, books:edit, books:delete, books:organize, books:scan, books:playlists.create, books:request, books:request.approve, books:reading` |
| `viewer` | `core:self`, `core:tasks.view`, `core:system.status`, `core:metrics.read` | `books:view, books:request, books:reading` |

`core:system.view` is **not** seeded to `editor`/`viewer` (SEC-8): today system status and logs need
`settings.manage` (`wire_system_routes.go:28,31`), which only `admin` holds. `core:system.status`
(readiness and maintenance-banner tokens only, no logs, paths or component detail) is what the
shell's banner needs. `core:self` (own profile, own sessions and keys) is the explicit grant behind
self-service routes; a role without it (a worker role) cannot use them.

`core:metrics.read` is seeded to `editor` and `viewer` only (SEC3-13), which keeps `/metrics`
reachable for every interactive seed role as today (any credential reads it at HEAD,
`server_lifecycle.go:1736-1747`). It is never seeded to a role holding a `Confining` scope: `fp-worker`
would then hold a non-`Confining` scope, stop being `Confined()` and lose SEC-3's confinement
everywhere (`TestSeed_NoNonConfiningScopeOnConfinedRole`). A scoped legacy key whose atoms do not
expand to `core:metrics.read` loses `/metrics`; the change is recorded in 09 §9.2.6.

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
| Storage/backup/updater admin ops | core routes; backups per 05-backup's route table (SEC2-8): list `core:system.view`; verify and create `core:system.manage` (verify opens a checkpoint and hashes the archive, so it is not a view-class start, SEC3-12); delete and import `core:system.manage` + `Guards.Interactive` (import audited); **download `core:admin` + `Guards.Interactive` + `Guards.Owner`** (with 05-authn §4.8's fallback; audited) — an archive holds every credential verifier, so it is credential-equivalent and never behind a grantable non-admin scope (SEC3-7); restore `core:backup.restore` (§6.1, SEC-16); storage and updater admin `core:system.manage` | closed |
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
- Legacy routes with auth disabled: a legacy `s.perm(atom)` route or in-handler atom check requires
  `Catalog.LegacyReach(atom)` from a role-based principal (R4-3, §6.3), but the auth-disabled
  principal is judged against the atom's expansion (`Catalog.Legacy`), exactly as before R4-3: HEAD's
  `s.perm` passes every request with auth off (`server_lifecycle.go:1693-1698`), R4-3 guards against a
  bounded custom role and the auth-disabled principal is not one, and the `{core:admin}` reach of
  `settings.manage` would otherwise close all 83 of its legacy routes with auth off — an unrecorded
  change to the auth-off half of `TestLegacyRoleReachUnchanged`. Interactive guards still refuse it
  (`credGuard`, 05-authn §4.11).
- Zero users: anonymous; only `Public` routes (05-authn §4.2). Today's `HoldsPermission` first-run
  bypass (`internal/server/middleware/auth.go:498-504`) is removed.

## 5. Lifecycle

1. **P1** (06 §6.5.1) checks only the static namespace rules of every manifest's permission names;
   no `rbac` service exists yet. **P4c** builds the role `Store`: the app's legacy store from
   `module.Manifest.LegacyStores.Roles` when given (ABO), else the native `core/internal/rbacstore`; a
   build whose choice differs from the claim ledger's `backends` record fails boot with
   `ErrLegacyStoreMissing` (05-storage). **P4d**, from 09 S4 r2: role kinds of the revocation-journal
   re-apply write through the `Store` (one stored format; no rebuild step); on a boot that applied a
   restore or found a replaced store, P4d's post-restore step runs rbac's and audit's restore hooks
   (§7.1) as package functions, after auth's, and after the keyring and config overrides (COR3-2).
   **At S4 r1 (dark)** the kernel only runs the read-only decode census of `Store.ScanRoles` at P4d
   (after step 1) — counted, logged, nothing written — and skips P4d's role writes and P7's seed
   commit (COR4-4).
2. **P5**: the kernel creates `rbac.Service` (over the `Store` built in P4c, with `Deps.Compile =
   authz.Compile`) and `audit.Store` (R4: L4, same layer as `auth`) (COR2-7, COR4-12).
3. Core declares its permissions, seeds and (when enabled) the ABO legacy alias table; then each
   enabled module's manifest is declared via `Declarer` (06 A1: permissions and roles are manifest
   data). Disabled modules declare nothing; their scopes stay stored in roles but are inert (not in
   catalog → never `Has`), and reappear when the module is re-enabled.
4. `Freeze`: validate (§4.9), compute closures and the seed merge (§4.8), bump generation. The seed
   merge's batch (seed rows with their audit records) is committed in P7 as 06's "seeded grants"
   writer, from S4 r2, so P5 writes nothing.
5. `httpapi.Build` validates route requirements against the frozen catalog.
6. Runtime: role writes bump `Generation()` and call `Deps.OnChange`, through which the kernel
   publishes `core.rbac.changed.v1`; caches key on generation, so the next request sees the change.
7. `audit` starts its async writer goroutine; the kernel registers `core.audit.prune` on
   `tasks.ScheduleFromConfig("core.audit.schedules.prune")`, a declared `tasks.ScheduleConfig` key in
   audit's own namespace (resolution R2-7, SIMP2-1), **from 09 S5**, when `tasks` lands (COR4-13); at
   S4 nothing prunes, which retention tolerates (one stage of records, far below
   `core.audit.retention.max_records`). On shutdown the async queue is drained (bounded 5 s), remaining
   events counted as dropped.

## 6. Migration from ABO (atoms, roles, stored data)

### 6.1 The 13 atoms → new permissions

09-abo-on-core §9.2.6 fixed the invariant (allow/deny identical for all 572 routes × 4 seed roles,
`TestLegacyRoleReachUnchanged`, extended here to scoped API keys and auth-off mode, §12) and
proposed names; this table is the normative mapping. It equals 09's proposal except where noted.

| Legacy atom | Routes (`raw/09` §1.3) | Canonical expansion | Written into a role row iff (row rule, §6.3) | Difference from 09 proposal |
|---|---|---|---|---|
| `library.view` | 147 | `books:view`, `core:tasks.view` | iff `books:view` | 09 also lists `core:events.subscribe`: dropped — the event stream is `AnyAuthenticated` with per-topic permissions (05-realtime §4.4), so no subscribe permission exists |
| `library.edit_metadata` | 111 | `books:edit` (implies `books:view`) | iff `books:edit` | — |
| `library.delete` | 22 | `books:delete` (implies `books:view`) | iff held | — |
| `library.organize` | 9 | `books:organize` (implies `books:view`) | iff held | — |
| `scan.trigger` | 15 | `books:scan` | iff held | 09 also lists `core:tasks.run`: dropped — task start is the definition's `Permissions.Start` (05-tasks §5T.3.7), there is no global run permission |
| `integrations.manage` | 12 | `core:integrations.manage` (implies `core:integrations.read`), `books:integrations.manage` | iff **both** | adds the implied read scope used by the integrations registry's list/get routes (05-metadata) |
| `users.manage` | 20 | `core:users.manage` (implies `core:users.view`), `core:roles.view` | iff `core:admin` — the legacy build enforces it with no dominance and no `CanAssign` (temp-login, reset and set-password for any user, invites into any role, `auth_temp_login.go:225-227`, `auth_reset_password.go:18-60`, `handlers/user.go:155-190`), and gates the master-list routes (natively `AllOf(core:users.manage, books:admin.view)`), so only `core:admin` dominates its reach | adds `core:roles.view` so holders can still see role names when assigning. HEAD's in-handler `users.manage` gates on user-identifying text (run error text and current-item labels, `opsv3/events.go:609-616`, `view.go:59-64`) map to `core:users.view`, the read-side successor (COR2-2, 05-realtime §4.7) |
| `settings.manage` | 83 (and old op-def default) | `core:settings.manage`, `books:settings.manage`, `core:tasks.manage`, `core:system.view` | iff `core:admin` — its legacy reach includes every sign-in field (`oauth_default_role`, "admin would mint admins"), restore into the live DB (`wire_system_routes.go:65`, `handlers/system/handler.go:840-862`), backup create/delete, logs, storage, diagnostics export, filesystem browse and import paths (`wire_library_routes.go:60-66`, `wire_media_routes.go:62,69-70,81`), whose successors need `core:system.manage`, `core:backup.restore`, `core:library.manage`, `core:library.browse` and `core:integrations.manage` (SEC3-1) | adds `core:system.view`: today `settings.manage` gates system status (`wire_system_routes.go:28`) |
| `playlists.create` | 0 | `books:playlists.create` | iff held | now enforced on playlist write routes (among the 36 permission-less routes, `raw/09` §4) |
| `collections.manage` | 6 | `books:collections.manage` | iff held | — |
| `requests.create` | 0 | `books:request` | iff held | — |
| `requests.approve` | 0 | `books:request.approve` (implies `books:request`) | iff held | — |
| `fingerprint.worker` | 5 | `books:fingerprint.worker` (`Confining`) | iff held | — |
| role-name check `admin` | 20 `RequireAdmin` + handlers | see §6.2 | role `admin` unchanged | see §6.2 |

**Reach (R4-3, SEC4-4).** Each row's `LegacyAlias.Reach` (§3.2) is the native scope set equivalent
to the atom's whole legacy reach on the snapshot release (§6.3): `{core:admin}` for `users.manage`
and `settings.manage` (column 4), and the canonical expansion (column 3) for every other atom, whose
expansion dominates its reach — `TestRowRuleDominatesLegacyReach` checks both claims against the
snapshot. Legacy routes and in-handler checks gated by an atom require `AllOf(Reach)`; column 4 is
the row rule over the same table.

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

### 6.3 Stored data and compatibility (store interface, R4-1)

Resolution R4-1 refines R3-1 for roles: there is no copy of the roles, no mirror, no rebuild at boot,
no claim hand-over and **no storage grant** during the extraction. `rbac` reaches roles only through
`rbac.Store` (§3.4). Core holds no app key name or row format.

**Native store (apps without pre-core roles; SIMP4-3).** `core/internal/rbacstore`, landing with
`rbac` at 09 S4, used by `hello`, `coretest` and every app that passes no legacy store:

| Key | Value |
|---|---|
| `c/rbac/role/<name>` | `codec.JSON[Role](1)` (every field of §3.3, `Suspended` always false) |

User role assignments live in auth's user records (05-authn §3.4). `TestNativeBackend_RoundTrip`
(rbac) runs `rbactest.StoreSuite` over it.

**ABO's legacy store (books-supplied).** The `books` module passes `module.Manifest.LegacyStores.Roles`
(06 §6.4.2, §6.11.6): ABO code, over ABO's existing role rows and their name index in their legacy format
(`Role{ID, Name, Description, Permissions []string, IsSeed, Version}`,
`internal/database/store.go:966-977`), reached through books' own claims. User role assignments stay
in the legacy user rows' role list (05-authn §11). Write exclusivity on those families is an ABO
archtest (only the legacy-store package writes them), not a storage grant; grants for these families
are part of the deferred cutover (05-storage). It runs `rbactest.StoreSuite` with HEAD fixtures.

- **Legacy row content: legacy atoms only (SEC4-3).** A role row this build writes holds, in its
  `Permissions` list, **only** the legacy atoms the row rule admits (`Store.PutRole`'s `legacyAtoms`)
  — never a namespaced string. HEAD's permission set is the union of the role rows' raw strings, and
  worker confinement is a "nothing else" test: `workerOnly` holds only if every string is
  `fingerprint.worker` (`internal/server/middleware/auth.go:271-297`), so one extra string in a
  confining role's row would un-confine it on an older build. A natively `Confined()` role is
  therefore written as exactly its atoms (`fingerprint.worker`), which keeps it worker-only there
  (`TestRowRule_ConfinedRoleStaysWorkerOnlyOnPrev`); a `Confining` scope with no legacy atom (the
  deferred `<m>:arr.*`) cannot be put in a role before the role cutover
  (`ErrConfiningNoLegacyAtom`). Exact-match checks grant nothing for an unknown string, so the
  premise "an older build ignores namespaced strings" held only for positive checks; the one negative
  check is handled by never writing such strings.
- **Native-only data in books' extension family.** The role's canonical permissions (patterns
  included), `Title`, `Origin`, `SeedPermissions`, `SeedDigest` and `PendingSeed` are kept by the
  legacy store in a **books-owned extension record** keyed by the role's ID, in a family no older build
  reads, written in the same batch as the row. The record never repeats a field the row holds and
  carries `legacy_ref`, the SHA-256 of the role row's bytes it was written with (the extension-record
  rule of 05-storage §5S.3.11).
- **Suspension on roll-forward.** When the row's hash differs from `legacy_ref` — an older build
  changed the row while rolled back, or the row was restored or edited by hand — the extension is
  **suspended**: the store reports `Role.Suspended`, and the role grants only the translation of the
  row's atoms through `LegacyAlias.New` (§6.1), which is never wider than what the older build
  granted (no silent widening). A custom role stays suspended until an admin re-confirms it
  (`POST /roles/{name}/reconfirm`, accept or discard; audited `core.role.reconfirm`); health reports
  `degraded` (`rbac_role_suspended`) naming the roles. Seed roles and `admin` carry no operator data
  in their extension: on a mismatch the next `Freeze` re-derives it from the seed (§4.8), and a
  restricted addition an admin had accepted returns to `PendingSeed`. A missing row makes the
  extension an orphan, ignored and deleted by the next write of that key.
- **Row rule (never widen on rollback; SEC3-1, SEC4-4).** A legacy atom is written into a role row
  only if the role's native checker satisfies `Catalog.LegacyReach(atom)` — `AllOf` the native scope
  set equivalent to **every** legacy route and in-handler check that atom gates on the **snapshot
  release**. The input is a frozen reach snapshot of the last pre-S4 release, produced once by the
  real-router dump of `raw/09` §0 plus a reviewed list of in-handler atom and role-name checks with
  file:line (`auth.Can`, `slices.Contains(…, "admin")`), committed in the S4 r1 PR and never
  regenerated from later builds, whose own manifests shrink as routes leave the mount while the
  rollback target keeps them (SEC4-4). **The snapshot is app data, supplied by the app** (lead
  decision, round 4): it names the app's legacy atoms and routes, so for ABO it lives in the books
  module (`internal/books/rbacreach/legacy_reach_v<release>.json`, name open; ABO), never under
  `core/`. Core `rbac` holds only the mechanism and the types: `LegacyAlias.Reach`,
  `Catalog.LegacyReach`, the snapshot type `rbactest.ReachSnapshot` and the property-test helper
  `rbactest.RowRuleSuite(t, *Catalog, ReachSnapshot)`, which core runs over a synthetic fixture
  (`core/rbac/testdata/legacy_reach_synthetic.json`, invented atoms) and ABO runs over books'
  snapshot. `LegacyAlias.Reach` (§3.2) is the snapshot's legacy-atom → native `AllOf` table, frozen
  with it at S4: R4-3's legacy-route enforcement, the row rule and 14 K21 all cite this one table, and
  ABO's `TestRowRuleDominatesLegacyReach` fails if the compiled `Reach` and books' snapshot differ.
  Reach on releases older than the snapshot is K21's territory (the runbooks name the
  oldest supported rollback target). §6.1's fourth column states the outcome; for `users.manage` and
  `settings.manage`, whose reach includes acting on any account and admin-minting sign-in fields, only
  `core:admin` qualifies. A role that fails the rule simply lacks the atom on an older build:
  narrower, never wider. Property test `TestRowRuleDominatesLegacyReach` reads the snapshot (random
  native roles; for each legacy route, a request the older build would admit from the row's atoms is
  one the native role admits on the route's successor).
- **Legacy routes require the full reach (R4-3, SEC4-2).** From 09 S4 r2 the legacy mount's
  `s.perm(atom)`, its manifest `Perm` (05-http-api §8.2) and in-handler `auth.Can(ctx, atom)` require
  `Catalog.LegacyReach(atom)`, not the atom's expansion: a custom role holding the four
  `settings.manage` expansion scopes — a natural "settings admin" — passes no `settings.manage` gin
  route (backup restore and create, logs, storage, diagnostics, filesystem browse and import paths,
  tool installs, AI model pulls, plugin settings, op definitions declaring `settings.manage`), so it
  cannot restore an archive from before its own demotion. Seed-role reach is unchanged (only `admin`
  holds those atoms, and it holds `core:admin`); auth-off reach is unchanged (§4.11).
  `TestLegacyRoleReachUnchanged` gains a custom-role half and
  `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor` checks the two atoms.
- **Reading a row.** A row whose extension is current is read as the extension's canonical
  permissions. A row with no extension (written by an older build, or never touched) is translated
  through §6.1's expansion. An atom outside §6.1 grants nothing (fail-closed), the role is reported
  `degraded` naming it, and the S4 probe P2 lists it; boot continues, so one bad row never
  crash-loops the unit (SEC4-13, 09 §9.2.3 rule 3). The S4 r1 decode census reports such rows before
  the flip.
- **One write, every build.** A role write — create, edit, delete, and every revocation (a role
  unassigned, narrowed or deleted, interactively, by journal re-application or by the post-restore
  hook, §7.1) — rewrites that row, its extension record and the affected user rows in one batch with
  its audit record and journal line. Narrowing removes the atoms the rule no longer admits in the same
  row write, so the restriction holds for an older build after a rollback with nothing else to keep
  in step.
- **Seed roles** are re-seeded by every build at boot (HEAD: every boot, `internal/auth/seed.go:80-129`).
  An older build's rewrite after a rollback changes the row, so this build's next `Freeze` re-derives
  the extension (§4.8; a row without a current extension counts as last seeded with the translation
  of its atoms). Operator edits of seed roles are refused until the cutover (§4.8).
- **Custom roles** (`custom.<slug>`) are new rows older builds never rewrite; on an older build they
  grant only the atoms the row rule admitted.
- **API keys never widen (SEC-1).** Keys keep their own scope lists; new keys never store an empty
  list and never a namespaced string in the legacy row, legacy keys keep what they were created with,
  and an empty legacy list is read as `LegacyFullScope` (§4.5; 05-authn §4.4, §11).
  `TestAPIKey_StoredScopesNeverEmpty` (05-authn §10).
- **Compatibility window.** The window is the one defined in 09 §9.2.1 and recorded in the `compat`
  ledger (09 §9.2.9) under surface `rbac_atom`; the legacy alias table is enabled by the
  compile-time `app.Options.LegacyPermissionAliases` (no runtime flag, SIMP-7). During the window:
  - key-creation and role-write APIs accept legacy atoms, store the canonical expansion, count
    `mediacore_compat_legacy_use_total{surface="rbac_atom"}`, and answer with `Deprecation` and a
    `deprecated_permissions` field;
  - `/api/v1/auth/me` (legacy alias) keeps the legacy wire shape (FE3-7): `permissions` holds the
    legacy atoms whose `Catalog.LegacyReach` the principal's checker satisfies (the test the bridge's
    `s.perm` applies), so a previous-release tab shows exactly the controls its user may use
    (`web/src/hooks/useLibraryPermission.ts:9,11`), and the alias adds `canonical_permissions`. The
    native `/api/core/v1/me` returns canonical names only (§4.10).
- Contract (09, gated by `TestNoContractBeforeSunset` and the usage counter): the alias table is
  removed by a PR and legacy strings are refused with 400 `unknown_permission` naming the canonical
  permission. It writes no stored data.
- **Deferred: cutover.** Moving ABO's roles to the native store (and with it lifting
  `ErrSeedRoleLocked`, the operator diff of §3.6, and the storage grant the move needs) is not part of
  the extraction: a later, one-way, checkpointed step behind the per-owner level guard, decided on
  measured data (02 decision 13), whose floor is the cutover release itself.

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
| `core.session.*`, `core.apikey.*` | sync (in batch) | `session.revoke`, `session.revoke_after_rollback` (05-authn §4.3); `apikey.create`, `apikey.revoke`, `apikey.set_status`, `apikey.require_confirmation` |
| `core.user.*` | sync (in batch) | `create`, `disable`, `enable`, `delete`, `reset_password`, `set_roles` |
| `core.role.*` | sync (in batch) | `create`, `update`, `delete`, `update_seed`, `accept_seed`, `reconfirm` (§6.3); `reset` with the role cutover (§3.6) |
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
`role.deleted{Subject: role}`. Older builds journal nothing; a store they changed is handled by the
replaced-store rule of 05-authn §4.14.

**Restore hooks (SEC-17).** The restore record is written to 05-backup's out-of-DB restore
journal (cross-cutting resolution 8). `audit` and `rbac` each provide a `journal.RestoreHook`, run by
the kernel as package functions in P4d's post-restore step (06 §6.5.1), after the swap, the keyring,
the config overrides and the journal re-apply (COR3-2) and after auth's hook, and before listeners
open; boot fails if either fails. They also run on a replaced-store boot (05-authn §4.14).

- **rbac's hook** re-applies, writing through `rbac.Store` and auth's user store (one stored format,
  R4-1):
  `role.unassigned` → remove the role from the user; `role.narrowed` → remove `Detail` from the role's
  permissions (and the atoms the row rule no longer admits, §6.3); `role.deleted` → delete the role
  and unassign it (a seed role is re-seeded at the next `Freeze`). When a re-application would remove
  the last active `core:admin` holder, the role is **kept** — a lockout is worse than a stale admin —
  but the account is restricted without being removed (SEC3-15, SEC4-12): its `password_hash` is
  blanked with `password_reset_required` set (05-authn §4.14 step 1), its API keys are set
  `needs_confirmation`, and its sign-in through identity links is suspended (`User.SignInRestricted`,
  05-authn §4.14): OIDC and identity extensions refuse it as password sign-in does, until an
  owner-verified reset link or the offline `<app> users reset-password` clears it. On an older build
  the blanked hash plus the deletion of its external-identity links (journaled `identity.unlinked`)
  hold; the residual is Cloudflare Access sign-in on an older build, which resolves users by
  allow-listed e-mail and is stated in the restore report. So the demoted, possibly compromised,
  account cannot sign in with its restored password, keys or SSO links here;
  health reports `degraded` (`restore_last_admin_kept`) naming the recovery path (a reset link through
  the owner verifier, or the offline `<app> users reset-password`), and the audit record says which
  entry was kept. A demoted admin therefore stays demoted after restoring an archive taken before the
  demotion (`TestRestore_DemotedAdminStaysDemoted`).
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
| A legacy-only role row holds an atom outside §6.1 | the atom grants nothing; the role is reported `degraded` naming it; P2 lists it; boot continues (09 §9.2.3 rule 3) | closed (that atom) | one bad row never crash-loops the unit (SEC4-13) |
| A role's extension is stale against its row (an older build changed it) | `Suspended`: the role grants only the translation of the row's atoms until an admin re-confirms; seed roles re-derived at `Freeze`; health `degraded` (`rbac_role_suspended`) | closed | no silent widening on roll-forward (R4-1) |
| A `Confining` scope with no legacy atom in a role write | 400 `confining_no_legacy_atom` (`ErrConfiningNoLegacyAtom`) | closed | its row could not stay confined on an older build (SEC4-3) |
| The role store's backend differs from the claim ledger's record | boot error `ErrLegacyStoreMissing` (05-storage) | closed | a build without the app's legacy store must not serve a store that has one |
| Sync audit write fails | the change fails (same batch) | closed | attribution of security changes is required |
| Async audit queue full | drop + metric | open | availability |
| Audit query store error | 503 | closed | |
| Restore audit copy-forward fails | boot error | closed | SEC-17 |
| Restore role re-application would remove the last admin | role kept, account restricted (password reset required, keys `needs_confirmation`); health `degraded`; audited | open (that entry) | lockout protection (SEC2-10, SEC3-15) |
| Seed-role edit or delete before the role cutover (`accept-seed` excepted) | 409 `seed_role_locked` | closed | an older build's per-boot re-seed would silently widen it after a rollback (DATA2-20) |
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
| `TestRestore_DemotedAdminStaysDemoted` | archive taken before an admin's demotion; restore, restart, roll back to the previous release: the account is not admin anywhere; a re-application that would remove the last admin keeps the role, blanks the password, sets the keys `needs_confirmation` and reports health `degraded` (SEC2-10, SEC3-15) |
| `TestAudit_CopyForwardThenWriteNoOverwrite` | after a restore with copied records, the next N records get fresh `seq`s and no copied record or index row changes (SEC2-12, DATA2-16) |
| `TestSeedRole_EditRefused` | `PATCH /roles/editor` and `DELETE /roles/editor` → 409 `seed_role_locked`; `accept-seed` → 200; custom roles editable (DATA2-20, SIMP4-6) |
| `TestSeed_NoNonConfiningScopeOnConfinedRole` | no seed merge gives a role holding a `Confining` scope a non-`Confining` one; `fp-worker` stays `Confined()`; `editor`/`viewer` hold `core:metrics.read` (SEC3-13) |
| `TestRowRuleDominatesLegacyReach` **(ABO)**, over core's `rbactest.RowRuleSuite` (core runs the suite over its synthetic fixture `core/rbac/testdata/legacy_reach_synthetic.json`) | property test of §6.3's row rule over the frozen reach snapshot books supplies (`internal/books/rbacreach/legacy_reach_v<release>.json`, name open): for random native roles, every legacy route the snapshot release admits from the row's atoms is admitted natively on its successor; `users.manage`/`settings.manage` only with `core:admin`; every other atom's `Reach` equals its expansion (SEC3-1, SEC4-4) |
| `TestRoleRow_PrevBuildSeesAdmittedAtomsOnly` | a custom role written by this build holds only admitted atoms in its row, never a namespaced string; read by the previous release's real binary it grants exactly those atoms; a narrowing here is narrower there at once (R4-1, SEC4-3) |
| `TestRowRule_ConfinedRoleStaysWorkerOnlyOnPrev` | a custom role `{books:fingerprint.worker}` is written as `["fingerprint.worker"]`; HEAD's `workerOnly` over the row is true; the S4 R-test cell "a custom confining role created on cur is confined on prev" (SEC4-3) |
| `TestRoleExtension_SuspendedAfterPrevWrite` | a custom role's row changed by the previous release → on roll-forward `Suspended`, grants only the row's translated atoms, health `rbac_role_suspended`, `reconfirm` restores or discards; a seed role's extension is re-derived at `Freeze` (R4-1) |
| `TestReseedFromHEADRows_AdminKeepsCoreAdmin` | a store seeded by HEAD (rows without `SeedDigest`): after the first S4 r2 boot `admin` holds `core:admin`, `PendingSeed` is empty for every role whose stored atoms' reach implies the addition (DATA4-11) |
| `TestUnknownAtom_RoleDegradedBootContinues` | a legacy-only row with an atom outside §6.1 → the atom grants nothing, health `degraded` names the role, boot continues (SEC4-13) |
| `TestNativeBackend_RoundTrip` / `rbactest.StoreSuite` | the native store and ABO's legacy store pass the same suite (ABO's with HEAD fixtures) (SIMP4-3, R4-1) |
| `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor` | (in 05-http-api) the two `{core:admin}`-reach atoms gate their routes with `core:admin`; a role holding the four `settings.manage` expansion scopes gets 403 on restore and the other admin-equivalent routes (R4-3, SEC4-2) |
| `TestMeAlias_LegacyShape` | `/api/v1/auth/me` `permissions` holds legacy atoms, `canonical_permissions` the canonical set; a previous-release tab shows the delete button for a `library.delete` holder; `/api/core/v1/me` returns canonical names only (FE3-7) |
| `TestModuleSystemActor_CannotStartUndeclaredKind` | (in 05-tasks) the system actor's checker = own kinds' `Start` ∪ `TaskStarts`, never `core:admin`/`AuthRequired` (SEC2-15) |
| `TestAuditAction_Grammar` | every action literal in core and in this table matches `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$` (COR2-19) |
| `TestAnyAuthenticated_ModuleOnlyKeyReachesCoreEvents` | key scoped only `books:scan` passes `AnyAuthenticated` on `GET /api/core/v1/events` (SEC-3) |
| `TestManifest_RejectsForeignNamespace` / `TestManifest_RejectsCoreGrant` / `TestManifest_RejectsCoreLegacyAliasFromModule` / `TestManifest_RejectsRestrictedDefaultGrant` | §4.9 |
| `TestSeedMerge_NewSeedScopeReachesRole` / `TestSeedMerge_Idempotent` / `TestSeedMerge_AdminOutsideMerge` | property test over random seed sequences: effective = the last applied seed; `admin` is always `{core:admin}` (DATA4-11). The operator-diff tests are deferred-build (§3.6) |
| `TestSeedMerge_RestrictedAdditionHeldPending` | a release adding a `Sensitive` scope to an existing role leaves it in `PendingSeed` until `accept-seed`, which succeeds on a seed role during the extraction (SEC-25, SIMP4-6) |
| `TestRoleWrite_EscalationRefused` / `TestCanAssign_CannotGrantUnheld` / `TestCanAssign_RemovalCheckedLikeAssignment` | |
| `TestUsersManage_CannotTempLoginAdmin` / `TestUsersManage_CannotResetDominatingUser` | a custom role with `core:users.manage` only → 403 `dominance_required` (SEC-4) |
| `TestStart_ViewOnlyDefRefusedForViewer` | (in 05-tasks) a def whose `Start` is `ClassView` fails registration without `StartableByViewers` (SEC-9) |
| `TestApprove_KeyMustNameScope` | (in 05-tasks) API key approves only with the `Approve` scope named; `LegacyFullScope` and delegated sessions refused (SEC-24) |
| `TestLastAdmin_ConcurrentDemotions` | `testing/synctest`, two concurrent demotions → one 409 |
| `TestLegacyAtomMapping_Golden` | the 13 rows of §6.1 |
| `TestLegacyRows_ReadInPlace` | (ABO, through books' legacy stores) fixture DB from ABO's role, user and key families → expected roles, checkers and keys, with no row written by the read (R4-1) |
| `TestLegacyRevocationHonoured` | key revoked or user deactivated on the previous release → refused here at the next request, with no rebuild (R3-1) |
| `TestLegacyRoleReachUnchanged` (09) | extended to scoped API keys, auth-off mode and random custom roles (stage 13 admits a legacy route only if its successor admits, SEC4-2), not only the 4 seed roles; runs on a HEAD-seeded store, not only a fresh one (DATA4-11) |
| `TestAudit_SyncInSameBatch` | failing batch → neither change nor record |
| `TestAudit_SeqUniqueAcrossCrash` | `Sequence` reservation: after a simulated crash, no `seq` is reissued; gaps allowed |
| `TestAudit_DenialCoalescing` | `synctest` clock |
| `TestAudit_RestoreCopiesForwardRecords` | records above the manifest's audit high-water mark survive a restore (SEC-17, DATA2-7) |
| `TestAudit_RedactsSecretsByKeyAndPrefix` | |
| `TestAudit_QueueFullDropsWithoutBlocking` | |
| `TestNoGlobals` (archtest) | no package-level registries in `rbac`/`audit` (R5) |
| `archtest.NoSystemPrincipalOutsideKernel` | `core/internal/authz` privileged constructors called only from `auth`, the `tasks` executor and `kernel`; `rbac` imports nothing from `authz` (SEC-26, COR4-12) |

## 13. Gap disposition G1–G15 (all gaps from `raw/10` §8)

| Gap | Disposition | Where |
|---|---|---|
| G1 closed hard-coded vocabulary | **Fixed**: catalog of module-declared `PermissionSpec`s; `IsKnown` → `Catalog.Lookup` | §3.2, §4.9 |
| G2 no namespace | **Fixed**: `<ns>:<action>[.<sub>]`; 13 atoms mapped; stored data migrated with compat window | §4.1, §6 |
| G3 seed-only roles, boot overwrites edits, no CRUD | **Fixed**: roles as data with CRUD API; module seeds and grants; restricted additions held and acceptable; the seed-role operator diff and tombstones land with the role cutover (§3.6) | §4.7, §4.8 |
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
   reconcile with the store interface (R4-1), the row rule and the full-reach requirement of legacy
   routes (R4-3).
5. Coordination with 06-app-sdk §6.4.2 and 08 (COR-16, SEC-23): both use `rbac.Permission`,
   `rbac.PermissionSpec` (with `DefaultGrants`, `Warn`) and `RoleName` from this file; there is no
   `rbac.Scope`, `rbac.PermissionDef`, `rbac.Grant` or `Dangerous` field. Core seed role IDs are
   `admin`/`editor`/`viewer` (not `core.viewer`).
6. **Resolved (round 3, R3-1): `integrations.manage` on rollback.** The round-2 lead decision bounded
   the legacy integration-URL write path in the identity Teach build; that build is gone. The legacy
   URL and secret writes go through the `settings.manage` config update, and §6.1's row rule writes
   `settings.manage` only into `core:admin` roles, so on an older build only an admin can move an
   integration URL. `core:integrations.manage` maps to legacy `integrations.manage`, whose legacy
   routes write no URL or secret (`deluge_integration.go:141-146`, `server_lifecycle.go:2088-2092`), so
   that atom's reach stays inside the native bound (05-metadata M1.3). SEC3-4's bypass of the Teach
   bound is moot.
7. **Resolved (round 4, R4-3): `core:library.browse` reach.** HEAD gates the directory picker
   `GET /api/v1/filesystem/browse` with `settings.manage` (`internal/server/wire_library_routes.go:61`).
   While it is a gin route it requires `Catalog.LegacyReach("settings.manage")` = `core:admin` (§6.3),
   and its S7 successor requires `core:library.browse`, which `admin` holds; only `admin` holds
   `settings.manage` among the seed roles, so `TestLegacyRoleReachUnchanged` holds before and after the
   move. The round-3 option "keep the route on the legacy mount under its legacy gate" is deleted: the
   legacy gate is exactly the widening R4-3 removes (SEC4-2).

## Objections to 03

None open. 03 v1.3.0+ R6 states that `AnyAuthenticated` never admits a confined credential (the
v1.1.0 amendment), and §3.6 has the role-ID and audit-action rows.

## Out-of-scope defects found

1. **`RequireAdmin` ignores API-key scopes** (`internal/server/middleware/auth.go:522-537` reads
   `user.Roles`; `handleAPIKeyAuth` binds the full user at `:384-386`): a key an admin scoped to
   `library.view` passes every admin-gated route not also behind `credGuard` (7 `admin/debug`
   routes including `PATCH …/books/:id` and `…/edits/:edit_id/undo`, `raw/09` §3.1 rows 241-247).
   The in-handler role-name checks (`internal/server/handlers/user.go:285,453`,
   `slices.Contains(actor.Roles, "admin")`) ignore key scopes the same way. Hotfix in ABO (05-authn
   §11.1 item 4): refuse an `api_key` method whose non-empty scopes do not include every atom, in
   `RequireAdmin`, in `adminRole` and in the two handler checks. Requiring `auth.Can(users.manage)` in
   addition is **not** enough — an admin's key scoped to `users.manage` passes it and keeps the
   debug-edit bypass (SEC4-11). Fixed by design in §4.6.
2. **Invite role not bounded by the creator's permissions** (`internal/server/handlers/user.go:155-190`):
   latent today because only the seed `admin` role holds `users.manage`, but any future custom role
   with `users.manage` could invite itself an admin. Fixed by design in §4.7 rule 2.
3. **`users.manage` reaches every account's credentials** (`internal/server/auth_reset_password.go:18-60`,
   `auth_temp_login.go:225-227`): latent for the same reason; fixed by design in §4.7 rule 5.

## Review history

Round 1 (2026-11-04), findings applied to this file:

- SEC-1 — §6.3: keys mirrored with legacy atoms only on exact re-expansion, else revoked; side records; property test. (superseded in round 3 by R3-1)
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
- OPS-4 — same fix as SEC-1. (superseded in round 3 by R3-1)
- DATA-3 — legacy authoritative, rebuild at every boot, side records (§6.3). (superseded in round 3 by R3-1)
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
- integration pass: R-H request (SEC-1, DATA-3) — §6.3: mirror-disabled keys that are still `Fresh` keep their side record; the legacy row is written **disabled** (`LegacyProjection.Disabled` / `SideRecord.MirrorDisabled`), matching 05-storage's terms. (superseded in round 3 by R3-1)
- integration pass: Stale objection to 03 (R6 confinement) removed: applied in 03.
- integration pass: Verified already present (R-F request): `core:integrations.read`, implied by `core:integrations.manage`.

Round 2 (2026-11-04), findings applied to this file:

- R2-1 / SEC2-2 — legacy `users.manage`/`settings.manage` bounded by the identity Teach build (05-authn §11.1), the rollback floor; the reviewer's omit-from-mirror alternative not applied (the resolution wins); `integrations.manage` surfaced as open item 6 (§6.3, §14). (superseded in round 3 by R3-1 and the §6.3 row rule)
- R2-2 / SEC2-3 / DATA2-4 — role revocations (interactive, re-applied, post-restore) staged through the role mirror in one batch (§3.4, §6.3, §7.1). (superseded in round 3 by R3-1: one stored format)
- SEC2-1 / DATA2-3 — mirror-disabled key projection and monotone rebuild referenced from 05-authn §11; `LegacyFullScope` only for never-mirrored rows; property test extended (§6.3). (superseded in round 3 by R3-1)
- SEC2-10 — role kinds `role.unassigned`/`role.narrowed`/`role.deleted` with `Object`/`Detail`; re-application per kind; last-admin kept with health `degraded` (§3.4, §7.1, §8, §12). (last-admin handling refined in round 3 by SEC3-15)
- SEC2-12 / DATA2-16 / DATA2-7 — audit copy-forward bounded by the manifest's pre-checkpoint `Sequence` mark; sequence raised before copying (§7.1, §12).
- SEC2-8 — backup route permissions per 05-backup's table (§4.10). (refined in round 3 by SEC3-7 / SEC3-12)
- SEC2-15 — module system actor = own kinds' `Start` ∪ `Manifest.TaskStarts`, never `core:admin`/`AuthRequired` (§3.4, §12).
- SEC2-17 — `Delegated` only on admitted `OwnAuth` routes (§3.1).
- SEC2-21 — deliberate auth-off tightening of the system detail/log successors recorded; reach table (§4.11, §6.2).
- DATA2-20 — seed roles read-only while the role mirror is active (`ErrSeedRoleLocked`) (§3.4, §4.8, §8, §12). (refined in round 3: read-only until the role cutover, R3-1)
- COR2-2 — HEAD's `users.manage` user-text gate → `core:users.view` (§6.1).
- COR2-7 — `rbac.New` and `Declarer` at P5; P1 static checks only; hooks at P4d (§5).
- COR2-9 — leaf types land at S2 with `httpapi`; journal types from `backup/journal` (§3).
- COR2-12 — `RecordIn(ctx, b *storage.Batch, e)`; `storage` in the audit import block (§3.5).
- COR2-19 — present-tense `<ns>.<noun>.<verb>` audit actions; normative table; grammar test (§3.5, §7.1, §7.5, §12).
- SIMP2-1 — `core.audit.prune_schedule` (`tasks.ScheduleConfig`) replaces `prune_interval` (§5, §7.5, §10). (superseded in round 2 by R2-7: the key is `core.audit.schedules.prune`)
- SIMP2-6 — facade confinement test marked deferred-build (§12).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): Open item 6 resolved by the lead decision (Teach-build integration-URL bound, 05-authn §11.1 item 3); §6.3 references it; open item 7 added (`core:library.browse` reach, `wire_library_routes.go:61`); `core:library.*` listed among new permissions. (superseded in round 3 by R3-1; open item 6 re-resolved)
- integration pass (round 2): §7.1: `update.rollback` added; audit hook uses `Sequence.AdvancePast` as its own commit; action names `core.role.update_seed`/`accept_seed`, `core.audit.export` aligned with the table.
- integration pass (round 2): `Deps.Revocations` Append(ctx, b, r); `Deps.MirrorView`; `Restriction` terms; schedule key `core.audit.schedules.prune`. (`Deps.MirrorView` and `Restriction` superseded in round 3 by R3-1)

Round 3 (2026-11-04), findings applied to this file:

- R3-1 — one stored format for roles: legacy-format adapter over the granted `role:` rows, canonical permissions stored additively as namespaced strings, extra members for title/origin/seed bookkeeping; `Deps.Mirror`/`MirrorView`, side records, the per-boot rebuild and the Teach-build bound deleted; seed edits refused until the role cutover; cutover deferred (header, §3, §3.4, §4.5, §4.8, §5, §6.3, §7.1, §8, §12, §14).
- SEC3-1 — §6.1's reverse column replaced by the generated row rule (an atom is written only if the native role dominates its whole legacy reach); `users.manage`/`settings.manage` only with `core:admin`; `TestRowRuleDominatesLegacyReach` (§6.1, §6.3, §12).
- SEC3-2 — resolved by R3-1 (no narrowed mirror rows; on an older build only `core:admin` roles hold `users.manage`); legacy invite accept re-checks `CanAssign` (05-authn §4.13).
- SEC3-4 — moot after R3-1; open item 6 re-resolved (§14).
- SEC3-7 — backup download needs `core:admin` + Interactive + Owner (§4.10).
- SEC3-12 — backup verify needs `core:system.manage` (§4.10).
- SEC3-13 — `core:metrics.read` seeded to `editor` and `viewer` only, never to a confined role; `TestSeed_NoNonConfiningScopeOnConfinedRole` (§4.8, §12).
- SEC3-15 — last admin kept by a restore is restricted (password reset required, keys `needs_confirmation`), recovery path surfaced (§7.1, §8, §12).
- SEC3-17 / DATA3-16 — seed-role refusal is the only option; Teach-build tests removed (§4.8, §12).
- COR3-2 — restore hooks run after the keyring, config overrides and journal re-apply (§5, §7.1).
- FE3-7 — `/api/v1/auth/me` alias keeps legacy atoms in `permissions` and adds `canonical_permissions`; native `/me` canonical only (§6.3, §12).
- SIMP3-12 — round-2 SIMP2-1 history line marked superseded by R2-7 (history).

Round 4 (2026-11-04), findings applied to this file:

- R4-1 / SIMP4-2 / SIMP4-3 — `rbac.Store` SDK interface; native `core/internal/rbacstore` over `c/rbac/` (native store table) for `hello`, `coretest` and new apps; ABO's legacy store supplied by books through `module.Manifest.LegacyStores.Roles`; no storage grant (grants move to the deferred cutover); native-only role data in a books-owned extension record with `legacy_ref`, suspended (`Role.Suspended`, `ReconfirmRole`, `POST /roles/{name}/reconfirm`) when an older build changed the row; `ErrLegacyStoreMissing` (header, §3, §3.3, §3.4, §4.7, §5, §6.3, §7.1, §8, §12).
- SEC4-3 — legacy role rows hold admitted legacy atoms only, never namespaced strings; confined roles stay worker-only on an older build; `ErrConfiningNoLegacyAtom`; premise sentence corrected; `TestRowRule_ConfinedRoleStaysWorkerOnlyOnPrev` (§3.4, §6.3, §8, §12).
- R4-3 / SEC4-2 — `LegacyAlias.Reach` and `Catalog.LegacyReach`: legacy routes and in-handler atom checks require `AllOf` the full legacy reach (`core:admin` for `users.manage`/`settings.manage`); auth-off principal judged against the expansion so auth-off reach is unchanged; `/api/v1/auth/me` atoms by the same test; custom-role half of `TestLegacyRoleReachUnchanged` (§3.2, §4.11, §6.1, §6.3, §12).
- SEC4-4 — row rule and reach read a frozen reach snapshot of the last pre-S4 release (`core/rbac/testdata/legacy_reach_v<release>.json`), never regenerated from later builds (§6.3, §12).
- SEC4-13 — an unknown atom in a role row grants nothing, the role is reported degraded, boot continues (§6.3, §8, §12).
- SEC4-11 — out-of-scope defect 1's hotfix keeps only the scope test, applied to `RequireAdmin`, `adminRole` and the two handler role-name checks; the "or simply" alternative deleted (defects).
- SEC4-12 — the restricted last admin also loses SSO sign-in (`User.SignInRestricted`); older-build residual stated (§7.1).
- DATA4-11 — the locked `admin` role is outside the seed merge; restricted additions compare against the stored atoms' legacy reach; `TestReseedFromHEADRows_AdminKeepsCoreAdmin`; reach test on a HEAD-seeded store (§4.8, §12).
- SIMP4-6 — seed merge split: during the extraction seed row = seed, `AcceptSeed` allowed, update/delete refused; operator diff, `Tombstone`, `ResetRole` and the reset route are deferred-build members of the role cutover (§3.3, §3.4, §3.6, §4.7, §4.8, §12, §13).
- COR4-4 — S4 r1 runs only the read-only role census; P4d role writes and P7's seed commit start at S4 r2 (§5).
- COR4-11 — `RoleName` and the `Checker` interface join the S2 leaf types; this list is the one other files repeat (§3).
- COR4-12 — `rbac` no longer imports `authz`: the kernel injects `authz.Compile` as `Deps.Compile`; archtest caller list updated (§3, §3.4, §5, §12).
- COR4-13 — `core.audit.prune` registers from S5, when `tasks` lands (§5).
- COR4-19 / SIMP4-1 — new `Deferred-build members` heading (§3.6), read by the generated archtest list.
- §14 item 7 resolved by R4-3; item 4 updated (§14).

Integration pass (round 4):

- integration pass (round 4): §6.3, §12: the frozen legacy-reach snapshot is app data supplied by books (`internal/books/rbacreach/…`, ABO), not `core/rbac/testdata`; core holds `rbactest.ReachSnapshot` and `rbactest.RowRuleSuite` with a synthetic fixture; `TestRowRuleDominatesLegacyReach` is an ABO test (lead decision); §6.3 cites 06 §6.4.2 for `LegacyStores.Roles`.
