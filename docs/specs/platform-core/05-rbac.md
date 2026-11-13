<!-- file: docs/specs/platform-core/05-rbac.md -->
<!-- version: 1.13.12 -->
<!-- guid: c07c6c34-fabe-451c-ae99-262b2c83232f -->
<!-- last-edited: 2026-11-13 -->

# 05 — Authorization (`core/rbac`) and audit (`core/audit`)

Evidence commit `748fd787a`. Raw inputs: [`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md)
(13 atoms, 4 seed roles, gaps G1–G15 — all dispositioned in §13),
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §1.3, §4,
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §9,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §8.
Identity: [05-authn](05-authn.md). Route enforcement: [05-http-api](05-http-api.md) §4.2.
Layers (L0–L9), `entity.Ref` and naming are from [03-architecture](03-architecture.md); release keys
(R0–R3) and R2's flips (F2–F9) are 03 §3.8's, and [09-abo-on-core](09-abo-on-core.md) owns their content. `rbac.Store` is an SDK interface with one
implementation, core's native store over `c/rbac/`, which every app uses. An app whose roles and key
scopes predate core (ABO) supplies no store of its own: its identity cutover maps its permission strings
to native scopes once, through an app-owned table, and writes the result through `rbac.Importer` and
`auth.Importer` (ABO: step M3 of R2; §6). Core holds no app key name, row format or permission string.

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
`rbac.Store` (§3.4, R4-1), whose one implementation, `core/internal/rbacstore`, keeps them in the core
keyspace `c/rbac/` for every app; the same package implements `rbac.Importer` for an app's cutover step
(§3.4, §6.3). The compiled checker and the two privileged checker constructors live in
`core/internal/authz` (layer L4, the implementation of `rbac`), so apps cannot reach them (§3.4,
SEC-26). `authz` imports `rbac`; `rbac` imports nothing from `authz`: the kernel injects
`authz.Compile` as `rbac.Deps.Compile` (COR4-12). `rbac` also imports the leaf `backup/journal` (L4;
`Revocation`, `RevocationJournal`, `RestoreContext`), not `backup` (COR2-9). Package
`github.com/falkcorp/mediacore/audit` (layer L4; imports `entity`, `storage`, `log`, `metrics`; never
`tasks` — the kernel registers its prune job).

**Landing (COR2-9, COR4-11, COR6-1).** The leaf types `Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`,
`Public`, `AnyAuthenticated`, `Delegated` (§3.1), `RoleName` (§3.3) and the `Checker` interface (§3.4;
it names nothing beyond these leaf types) land in release R1 with `httpapi`, whose `Route.Perm`/`LegacyRoute.Perm`
need them, whose manifest annotates 36 routes `AnyAuthenticated` and records, for every books
`s.perm(atom)` route, the requirement books' atom table gives (§6.1), built from these leaf
constructors with no catalog (05-http-api §8.1), and whose `auth` leaf (`Principal.Perms`, `User.Roles`)
names `Checker` and `RoleName`; this list is the one 04 §8.1, 06 §6.4.10 and 05-http-api §3 repeat.
**F2, R2's foundation PRs (03 §3.8; IMPL-1–IMPL-5, XFILE-4),** land `audit` (the store and the whole action table of §7.1,
`core.storage.*` and `core.settings.update` included, built before P4b so F3's settings commits and the
cutover's step records stage through `RecordIn`, §5), the complete scope catalog (every `core:*`
`PermissionSpec` any flip uses, §5 step 3) and the config sections `core.rbac` and `core.audit` (§10),
dark. Roles, `Store`, `Importer`, `Service` and the checker implementation land dark during R2, and all
of it serves from R2's identity flip (F4), whose `Freeze` rejects a manifest scope the catalog does not
hold.

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

// RequirementKind is the kind a Requirement was built with. Need(s) reports
// KindAllOf with one scope. The zero value KindInvalid is the zero Requirement's.
type RequirementKind uint8

const (
	KindInvalid RequirementKind = iota
	KindPublic
	KindAuthenticated
	KindAllOf
	KindAnyOf
	KindDelegated
)

var (
	Public           Requirement // no principal needed
	AnyAuthenticated Requirement // a principal holding core:self or any non-Confining scope (§4.10)
)

func Need(s Permission) Requirement
func AllOf(s ...Permission) Requirement
func AnyOf(s ...Permission) Requirement

// Delegated marks a gin-mount route whose permission is enforced inside the
// mounted handler by its own compat authenticator (05-http-api §8.2,
// LegacyRoute.OwnAuth). Accepted only by httpapi.MountLegacy, and only on an
// OwnAuth route that names a compat method its module registered with
// Registrar.AuthMethod (SEC2-17); never by Handle.
func Delegated(reason string) Requirement

func (r Requirement) IsZero() bool
func (r Requirement) Kind() RequirementKind // so authz, httpapi and tests outside rbac can tell AllOf from AnyOf
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
	Name        RoleName     // "<ns>.<name>"; an un-namespaced ID only if listed in the module's Manifest.ImportedRoleIDs (books: "fp-worker"; 06 §6.4.2, COR5-5)
	Title       string
	Description string
	Permissions []Permission // patterns allowed (roles may hold wildcards), own namespace only
}

// Declarer is what the kernel uses to feed a module's manifest into the catalog.
type Declarer interface {
	Permissions(specs ...PermissionSpec) error
	Roles(seeds ...RoleSeed) error
}

type Catalog struct{ /* frozen after kernel validation */ }

func (c *Catalog) Lookup(s Permission) (PermissionSpec, bool)
func (c *Catalog) All() iter.Seq[PermissionSpec]
func (c *Catalog) Namespace(ns string) iter.Seq[PermissionSpec]
func (c *Catalog) Expand(p Permission) []Permission // pattern -> concrete scopes currently in the catalog
```

### 3.3 Roles

```go
// RoleName identifies a role. Core seed roles keep ABO's IDs so imported
// User.Roles need no rewrite (§6.3):
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
	CreatedAt, UpdatedAt time.Time
	Version         int64
	// Removed and Tombstone are deferred-build members (§3.6).
}

// Permissions returns the role's effective patterns: SeedPermissions for a
// seed role, Added for a custom role. PendingSeed is not effective until
// accepted (§4.8).
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
	Store   Store            // core/internal/rbacstore over c/rbac/ (§6.3, R4-1)
	Compile func(CompileInput, *Catalog) Checker // injected by the kernel: authz.Compile (COR4-12)
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
	Scopes      []Permission // key scopes; nil = role permissions only (sessions)
	Interactive bool         // auth.Method.Interactive() of the credential (§4.4)
}

// CompileInput is what Service.Compile hands Deps.Compile: the caller's input
// plus the named roles' patterns, resolved by Service.Compile from its role
// cache at the current Generation (unknown role IDs already skipped and
// counted). authz.Compile never reads the store or the role cache itself.
type CompileInput struct {
	CheckerInput
	RolePatterns []Permission // union of the patterns of every known role in Roles, deduplicated (core:admin included when a role holds it)
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
	ErrSeedRoleLocked   = errors.New("rbac: seed roles are read-only until seed-role editing lands")
	ErrUndecodableRole  = errors.New("rbac: stored role cannot be decoded") // per row, from Store.ScanRoles (§6.3)
)

// Store is the role store (resolution R4-1). It is SDK, frozen by the
// API-stability gate, because other apps and coretest build on it. Core's
// implementation, core/internal/rbacstore, keeps roles in c/rbac/ (§6.3
// "Native store") and is the only implementation the kernel builds. Core
// enforces its invariants above the interface (escalation, last admin);
// rbactest.StoreSuite is the conformance suite. Every write takes the
// caller's *storage.Batch, never a copy (COR2-12), and stages an Expect on
// the role's Version.
type Store interface {
	GetRole(ctx context.Context, name RoleName) (*Role, error)
	ScanRoles(ctx context.Context) iter.Seq2[*Role, error] // an undecodable stored role yields ErrUndecodableRole for that role only
	PutRole(b *storage.Batch, r *Role) error
	DeleteRole(b *storage.Batch, r *Role) error
}

// Importer is how an app's cutover step brings the custom roles it held
// before core into the native store, once (ABO: step M3 of R2, §6.3).
// core/internal/rbacstore implements it and the kernel hands it to the step
// as part of module.Importers (03 §3.3, 06 §6.4.2); rbactest.StoreSuite
// carries its cases. The step maps the app's
// permission strings to native scopes itself (ABO: §6.1's table, books' code),
// so core holds no app permission string. ImportRole stores r as a custom
// role (OriginCustom, Name custom.<slug>) with its native permissions and
// refuses (ErrImportRejected) an unknown scope or a set no catalog role could
// hold; seed roles are not imported, P7's seed commit writes them (§4.8).
type Importer interface {
	ImportRole(b *storage.Batch, r *Role) error
}

var ErrImportRejected = errors.New("rbac: imported role breaks a role store invariant")
```

**Privileged checkers are not SDK (SEC-26).** The two checkers that bypass role resolution are
constructed only in `core/internal/authz`: `authz.AuthDisabled(cat *rbac.Catalog) rbac.Checker`
(§4.11) and `authz.ForSystem(cat *rbac.Catalog, perms []rbac.Permission) rbac.Checker` (the `_system`
principal of background tasks, built from the task definition's declared permissions, never
`core:admin` by default). `ForSystem` holds exactly the `perms` that `cat` declares, with no
implication, and every check goes through `Has` against `cat` as a non-interactive principal, so a
`Sensitive` scope it was given (a module's as well as core's) and an undeclared one are refused
(§4.4 rule 1); a nil `cat`, or an entry that is `core:admin`, `AuthRequired`, a wildcard or malformed,
yields a checker that holds nothing. Go's `internal/` rule keeps `authz` out of app code; within core,
`archtest.NoSystemPrincipalOutsideKernel` allows a non-test import of `authz` only in `auth`, the
`tasks` executor and `kernel` (`authz.Compile` mints an interactive admin checker from any role set
holding `core:admin`, so it is fenced like the two constructors), and no `//go:linkname` naming it
(`rbac` reaches `authz` only through the injected `Deps.Compile`, COR4-12). The public `rbac.All` of
v1.0.0 is removed.

**A module's system actor (SEC2-15).** When a module's worker, hook or bus subscriber starts a run
outside a request, the kernel stamps the module's system actor (05-tasks §5T.3.10). Its checker is
`authz.ForSystem` over exactly the union of `Perms.Start` of the module's own kinds plus the kinds in
`Manifest.TaskStarts` (a reviewed list of foreign or core kinds the module may start, 06); `core:admin`
and every `AuthRequired` scope are never included, so a module cannot start `core:system.manage`
kinds (diagnostics export, backup create) or another module's undeclared kinds. A `Sensitive` scope
in the union is refused by the checker itself (non-interactive, §4.4 rule 1), so a kind whose `Start`
is `Sensitive` cannot be started by a system actor.

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
specified so a later release and 08 can rely on them, and are **not built** during the extraction.
The table is planning information (resolution A-4: no archtest checks the absence of these members),
and the PR that builds one deletes that member's row:

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| `rbac.Role.Removed`, `rbac.Role.Tombstone` | the operator diff on seed roles and the merge rows "digest differs" with operator edits and "tombstone" (§4.8); tests `TestSeedMerge_PreservesOperatorRemoval`, `TestSeedMerge_TombstoneNotRecreated` | seed-role editing (not scheduled) | effective = `(SeedPermissions ∪ Added) \ Removed`; an operator removal survives upgrades; a deleted seed role is not recreated (SIMP4-6) |
| `rbac.Service.ResetRole` | `POST /roles/{name}/reset`; test `TestSeedRole_ResetClearsDiff` | seed-role editing | drop a seed role's operator diff |

Also deferred, with no identifier of its own: `TestConfining_FacadeKeyRefusedOnNative` (with
`arrfacade`, 08; SEC-5). Two restrictions on **built** members are behaviour, not absence, so they are
not in the table: `rbac.Role.Added` is built for custom roles and carries no operator diff on a seed
role, and `rbac.Service.UpdateRole`/`DeleteRole` are built for custom roles and refuse seed roles
(`ErrSeedRoleLocked`) until seed-role editing lands; `TestSeedRole_EditRefused` checks both (§4.8, SIMP5-6).

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

`Compile` builds a checker from the union of the roles' patterns (`Service.Compile` resolves them into `CompileInput.RolePatterns` before calling `Deps.Compile`; §3.4): a hash set of concrete scopes
(implication closures precomputed into it), a set of namespace wildcards, a set of action
wildcards, an `admin` flag, and the `interactive` bit. With key scopes, the checker is the
intersection (a scope is held iff the roles hold it and the key names it, after implication), and it
holds the `admin` flag **iff the roles hold `core:admin` and the key names it** (SEC-12): a key naming
`core:admin` whose owner's roles do not hold it gets no admin flag and no scope from it. **A key's confinement is decided on
that effective set** (resolution R10-2, SEC10-1), the set HEAD's confinement tests too (`workerOnly`
reads the bound permissions, role ∩ key: `internal/server/middleware/auth.go:268-297` @748fd787a, "an
API key scoped to fingerprint.worker, or a user holding only the fp-worker role"). So a key scoped
`["books:fingerprint.worker"]` is `Confined()` whoever owns it, an admin included; the owner's roles
never decide it (`TestWorkerKey_Confined`).
`Has(s)` is evaluated in this order:

1. `s` is `Sensitive` and the principal is not interactive → **false**, even with `admin`
   (SEC-23). The bootstrap key (`["core:admin"]`, method `api_key`) therefore cannot write roles.
2. `admin` → true.
3. exact ∨ `ns:*` ∨ `ns:action.*` → true.

`Confined()` is true when the concrete set is non-empty and every member is `Confining` (an
`admin` principal is never confined). Nothing is materialized per request; compiled checkers are
cached by (sorted role IDs, sorted scopes, interactive, `Generation()`).

### 4.5 API key scopes (G6)

- **Keys: scopes required, non-empty, concrete (no wildcards); empty = refused.** Recommendation
  accepted from `raw/10` G6. A key may hold `core:admin` (bootstrap key, admin automation) — that is
  explicit, not a wildcard, and §4.4 rule 1 still applies to it.
- At creation every scope must be in the catalog and held by the actor (and by the key owner when
  creating for another user, 05-authn §4.4).
- **Imported keys (D12 (b)).** A key an app imported at its cutover holds native scopes like
  any other key; ABO's `abk_` keys get them from §6.1's table under §6.3's rules, which also turn
  HEAD's empty scope list ("all role permissions") into the owner's role set at the cutover, so no
  stored key has an empty list and no key widens when its owner later gains a role.
- Per-key module binding is expressed by scopes plus the key's `Purpose` (05-authn §4.4); a facade
  key's scopes are all `<m>:arr.*`, which are `Confining`.

### 4.6 Admin is a permission (G4)

- `core:admin` is a permission held by the locked seed role `admin`. It satisfies every requirement
  except a `Sensitive` scope for a non-interactive principal (§4.4). It is `AuthRequired`, so the
  auth-disabled principal never holds it (§4.11, SEC-2). Every role-name check is removed; the
  inventory, each with its replacement and why (SEC9-9):
  - `RequireAdmin` and `adminRole` (`internal/server/middleware/auth.go:522-537`,
    `wire_master_list_routes.go:41` @748fd787a) → the permissions of §6.2;
  - `handlers/user.go:285,453` (the `adminRole` constant at `:441`) → `Can(core:admin)`: both ask
    whether the requesting actor is an admin, so key scoping must apply;
  - the user-merge actor check `mergeActor` (`internal/server/handlers/user_merge.go:62-75`, the
    `adminRole` test at `:67`) → `Can(core:admin)` (SEC8-7);
  - the repairs owner apply (`internal/server/handlers/repairs/handler.go:529`, "owner apply needs the
    admin role") → `Can(core:admin)`: it asks about the requesting actor, after the Access owner proof,
    so key scoping applies;
  - the merge survivor check (`internal/usermerge/plan.go:342`, "the surviving account is not an
    admin") → the survivor's roles compiled role-only (`Compile(CheckerInput{Roles: survivor.Roles})`)
    hold `core:admin`: it asks about an account, not the request, so no key takes part;
  - `ActorIsAdmin` (`internal/usermerge/plan.go:400`, called by the plan at `:328` and by the merge
    task, `internal/plugins/maintenance/user_merge.go:161`) → the actor's roles compiled role-only hold
    `core:admin`: the task runs after the request, and the request already passed `mergeActor`'s
    key-scoped `Can(core:admin)`.

  None of them is a key-scope bypass today (the owner apply needs the owner proof, the merge a grant),
  but a holder of `core:admin` through a custom every-atom role would fail a name test, so each moves
  with its handler (04) and checks the permission. An explicit `RoleName("admin")` test is never used. From R2 the ABO-owned merge handler that 04 carries the
  merge methods onto checks `rbac.Can(ctx, core:admin)` with key scoping, never `adminRole`. At HEAD the
  four merge POSTs are credential routes (`internal/server/wire_library_routes.go:177-181` @748fd787a),
  which refuse every API key, and they keep that guard (`Guards.Interactive`); the two reads that call
  `mergeActor` without it, `GET /users/:id/footprint` and `GET /users/merges`
  (`user_merge.go:97-99,190-192`), are what an admin's key scoped to `users.manage` reaches today, and
  the check closes them (`TestUserMerge_ScopedAdminKeyRefused`, §12). Holding the role *named* `admin` confers nothing by itself; holding `core:admin`
  (through any role) does.
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
| `DELETE /roles/{name}` (custom: delete if unassigned or `?reassign=`) | `core:roles.manage` |
| `POST /roles/{name}/reset`; `PATCH`/`DELETE` of a seed role | deferred-build (§3.6); 409 `seed_role_locked` until then |
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
   gin mount's handlers (which call it, 05-http-api §8) cannot bypass it. Refusal:
   403 `dominance_required`, audited.

### 4.8 Seed merge (G3)

Seed input for a role = core's seed for that role ∪ every enabled module's `PermissionSpec.DefaultGrants`
naming it ∪ (for module roles) the module's `RoleSeed.Permissions`. Each seed role stores
`SeedPermissions` (the seed input last applied), `SeedDigest` and `PendingSeed` (SIMP4-6).

**Built.** At `Freeze`, for every seed role:

| Stored state | Action |
|---|---|
| the locked role `admin` | **outside the merge** (DATA4-11): always written as `{core:admin}`; never `PendingSeed` |
| absent | create with `SeedPermissions = seed` |
| present, `SeedDigest` equal | nothing |
| present, digest differs | set `SeedPermissions = new seed` minus any **restricted** addition (below); put restricted additions in `PendingSeed`; log + audit `core.role.update_seed` with the permissions the release added, held or withdrew. |

A *restricted* addition is a scope that is `Sensitive`, `AuthRequired` or `Confining`, or any
`*:settings.manage` / `*:integrations.manage`, added to a role that already existed. It becomes effective only when an admin accepts it
(`POST /roles/{name}/accept-seed`, audited `core.role.accept_seed`); the Roles page shows the pending
set (SEC-25). Roles created fresh from the seed take the full seed. Effective = `SeedPermissions`;
today's behaviour (overwrite every boot, `internal/auth/seed.go:117-127`) plus the restricted hold.
The first boot after ABO's identity
cutover finds no seed row (the cutover imports none, §6.3) and creates all four from the seed.

**Seed roles are read-only except for `accept-seed` (DATA2-20, SIMP4-6).** Because the merge rewrites a
seed role from its seed at every boot that changes the seed, and the operator diff that would carry an
edit across that rewrite is deferred-build (§3.6), `UpdateRole` and `DeleteRole` on a seed role answer
409 `seed_role_locked` (`ErrSeedRoleLocked`); `ResetRole` does not exist yet. `AcceptSeed` **is
allowed**, so the restricted hold is never a dead end. `ErrLockedRole` keeps covering `admin` on every
path. Custom roles stay fully editable, and the Roles page offers "duplicate as custom role". This is no
regression: HEAD has no seed-role edits at all (G3). Refusal is chosen over silently cloning a seed role
into a custom role on edit (DATA3-16, SEC3-17).

**Deferred-build (§3.6).** The operator diff `Added`/`Removed` on seed roles, `Tombstone` (a deleted seed
role is not recreated), `ResetRole` and `POST /roles/{name}/reset`: then effective =
`(SeedPermissions ∪ Added) \ Removed`, an unrestricted permission a new release grants to `editor`
reaches every `editor` role the operator did not strip of it, an operator's removal survives upgrades, a
withdrawn permission disappears unless added explicitly, and `Removed` entries no longer in the seed are
dropped.

Core seed roles (IDs kept from ABO, so imported `User.Roles` need no rewrite, §6.3):

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
reachable for every interactive seed role. It is never seeded to a role holding a `Confining` scope:
`fp-worker` would then hold a non-`Confining` scope, stop being `Confined()` and lose SEC-3's
confinement everywhere (`TestSeed_NoNonConfiningScopeOnConfinedRole`). HEAD lets any credential read
`/metrics` (`internal/server/server_lifecycle.go:1736-1747` @748fd787a); from R2 the kernel's `Infra`
route requires `core:metrics.read`, and the identity cutover gives it to every imported key and custom
role that is not `Confined()` (§6.3), so ABO's Prometheus scrape key keeps working.

### 4.9 Module declarations and escalation validation

At `Freeze` the kernel validates every module's declarations; any failure aborts boot (errors
joined, before listeners open):

| Rule | Error |
|---|---|
| Every `PermissionSpec.Name` is concrete and `Namespace() == moduleID` | `ErrForeignNamespace` |
| `RoleSeed.Name` is `<moduleID>.<name>` (or an ID listed in the module's `Manifest.ImportedRoleIDs`); its permissions' namespaces == moduleID | `ErrForeignNamespace` |
| No module permission, seed or default grant targets `core:*` (in particular `core:admin`) | `ErrCoreGrant` |
| `DefaultGrants` name only `editor`, `viewer` or the module's own roles (never `admin`, never another module's role) | `ErrForeignNamespace` |
| `DefaultGrants` never appear on a `Sensitive`, `AuthRequired` or `Confining` spec, nor on any `*:settings.manage` / `*:integrations.manage` (SEC-25); such scopes reach roles only through a module's own `RoleSeed` or an admin edit | `ErrRestrictedGrant` |
| `Implies` stays in namespace; closure acyclic; a `Confining` scope implies nothing and is implied by nothing | `ErrForeignNamespace`, `ErrImpliesCycle` |
| A module's routes, tasks, topics, settings sections require only own-namespace permissions, `core:admin`, or `Public`/`AnyAuthenticated` (requiring *more* is not escalation; requiring another module's permission is coupling, R3) | `httpapi.ErrForeignScope` |
| Permission uniqueness across catalog | duplicate error |

**ABO's atom table is books' code, not core's (COR5-5).** HEAD's 13 permission atoms include
core-bound meanings (`users.manage`, `settings.manage`) that map to `core:*` scopes, which no module
may grant (`ErrCoreGrant`), and core may not hold an app's permission strings (03 R1). So the table of
§6.1 is Go code in `internal/books`, used in exactly two places: books' identity cutover step, which
writes native scopes through the importers (§6.3), and books' `s.perm(atom)` adapter on the gin mount,
which turns each atom into its route requirement (§6.1). Neither reaches `Freeze` as a grant:
the cutover's writes pass the importers' checks, and a route requirement of the table is an ordinary route
requirement that `Freeze` validates against the catalog. `fp-worker`, books' un-namespaced role ID, is admitted through
`Manifest.ImportedRoleIDs []rbac.RoleName` (06 §6.4.2); keeping the ID is cheaper than renaming it, which
would rewrite every user row that names it and the worker setup docs.

### 4.10 Enforcement points

| Point | Mechanism | Fail |
|---|---|---|
| HTTP routes | `Route.Perm` checked in httpapi stage 13 (05-http-api §4.2); denials audited (coalesced) | closed |
| In-handler dynamic checks | `rbac.Require(ctx, scope)` (op-def permissions, repairs fixers; books' `s.perm` adapter, §6.1) | closed |
| `AnyAuthenticated` | the principal holds `core:self` **or any non-`Confining` scope**. Core routes that serve module resources (task start, event stream) add per-resource dynamic checks; they do not test the route owner's namespace (SEC-3) | closed |
| Confinement | a `Confined()` principal satisfies a requirement only when it **names** one of the principal's scopes literally: never `AnyAuthenticated`, never a requirement that the scope merely implies. `books:fingerprint.worker` and every `<m>:arr.*` scope are `Confining`, which reproduces today's worker path confinement (`middleware/auth.go:265-298`) and 08's facade-key rule without a path list | closed |
| Tasks (05-tasks §5T.3.7) | definition declares `Permissions{Start, Cancel, Approve, View}`; `Start` **required** (registration fails like R6) and **not satisfiable by a `ClassView` scope** unless the definition sets `StartableByViewers` with a reviewed reason (SEC-9; the conversion of an op def declaring only `library.view` becomes `AllOf(books:scan, books:view)`, which keeps today's floor, `opsv3/handler.go:236-246`); `Cancel` zero = `Start`, the run's own actor may always cancel, `core:tasks.manage` may cancel any; `Approve` required iff the definition needs approval; `View` zero = `core:tasks.view`. **Approval** (SEC-24, today's rule kept, `opsv3/runs.go:395-410`): by a `session`, an API key, or a verified identity extension (CF Access); an API key must name the `Approve` scope **explicitly** in its scopes (not through implication or `core:admin`); `session_delegated` is refused | closed |
| Realtime topics (05-realtime) | the topic's permission from the one events topic catalog (05-events, SIMP-5) checked per subscriber per event; principal revalidated on heartbeat and invalidation | closed |
| Settings sections (05-config) | each section declares `ReadPerm`, `WritePerm` (default write `<ns>:settings.manage`, core sections `core:settings.manage`); keys carry a `Protection` level (05-config), and keys whose change could mint admins, spoof client addresses or apply updates are `ProtectAdmin` (needs `core:admin` + an interactive method, SEC-6; the per-key column is in 05-authn §8 and 05-http-api §10); secret fields are write-only regardless | closed |
| Storage/backup/updater admin ops | core routes; backups per 05-backup's route table (SEC2-8): list `core:system.view`; verify and create `core:system.manage` (verify opens a checkpoint and hashes the archive, so it is not a view-class start, SEC3-12); delete and import `core:system.manage` + `Guards.Interactive` (import audited); **download `core:admin` + `Guards.Interactive` + `Guards.Owner`** (with 05-authn §4.8's fallback; audited) — an archive holds every credential verifier, so it is credential-equivalent and never behind a grantable non-admin scope (SEC3-7); restore `core:backup.restore` (§6.1, SEC-16); **factory reset** `POST /api/core/v1/system/factory-reset` (in `internal/storagehttp`; it only stages, through the kernel's staging function handed as `storagehttp.Stager`, never `*storage.Admin`, 05-storage §5S.3.15, COR8-2; request `{"confirm": "RESET"}`, 422 `confirm_required` otherwise; library files under `RootDir` are deleted only with `"delete_library_files": true`, confirmed by `RootDir` and the file count, §6.2, resolution A-17): `core:admin` + `Guards.Interactive` + `Guards.Owner` (with 05-authn §4.8's fallback; audited `core.system.factory_reset`) — it sets aside every credential, role and secret record of the install, so like the archive download it is never behind a grantable non-admin scope (DATA6-6); HEAD's `POST /api/v1/system/factory-reset` and `POST /api/v1/system/reset` are deleted in release R1, whose only reset path is the offline `<app> storage reset` (05-storage §5S.3.15); the route arrives with R2's identity flip (F4), and its `kind` covers both (§6.2, resolution R8-1); other storage and updater admin `core:system.manage` | closed |
| UI hints | `/api/core/v1/me` returns `permissions` (concrete, expanded) and `patterns`; `@mediacore/shell` `can(scope)` implements §4.4 in TS. **Advisory only**: the server is authoritative. Matching vectors shared in `core/rbac/testdata/match_vectors.json`, run by Go and vitest | n/a |

### 4.11 Interplay with auth modes

- Auth disabled: the principal's checker is `authz.AuthDisabled(catalog)`: every concrete scope
  that is neither `AuthRequired` nor `Sensitive`, **`admin=false`, no implication from
  `core:admin`**, non-interactive (05-authn §4.11, SEC-2). `core:admin`, `core:system.manage`,
  `core:backup.restore`, `core:audit.view` and `books:admin.debug` are `AuthRequired`, so every route behind them —
  store wipes, debug edits, credential surfaces — answers 401 with auth disabled, exactly today's
  always-authenticated class (`wire_admin_role.go:28-38`). Read-only admin introspection stays
  reachable because it maps to non-`AuthRequired` admin-only scopes (§6.2), matching today's
  `adminRole` pass-through.
- **Books' gin routes with auth disabled (COR5-4, SEC5-9).** Books' `s.perm(atom)` routes and its tasks
  declaring an atom's requirement keep HEAD's auth-off outcome, as HEAD's `s.perm`
  (`server_lifecycle.go:1693-1698`) and op checks (`wire_ops_v3_routes.go:96-98`,
  `operations_v2.go:781-783`) passed every request with auth off. **The auth-off evaluation rule:**
  when the auth-disabled checker evaluates an `AllOf` requirement, it drops the `AuthRequired` members
  and requires the rest; a requirement every one of whose scopes is `AuthRequired` (a `Need` or `AllOf`
  of `core:admin`, `core:system.manage`, `core:backup.restore`, `core:audit.view` or
  `books:admin.debug`) is still refused with 401. So `settings.manage`'s requirement, `AllOf` its eight
  scopes with `core:system.manage` among them (§6.1, XFILE-5), passes with auth off on its seven others, and
  settings stay usable; every real principal still needs the full `AllOf`. Only `authz.AuthDisabled`
  applies the rule (`TestAuthOff_SettingsManageKeepsReach`, §12). Books' in-handler atom checks keep
  HEAD's auth-off **denial**, because HEAD's `auth.Can` returns false when no permission set is bound
  (`internal/auth/context.go:171-178`), for example other users' trash progress
  (`handlers/audiobooks/handler.go:986`): books' in-handler adapter refuses the auth-disabled principal,
  except at the sites whose HEAD code bypasses the check with auth off (`wire_ops_v3_routes.go:96`,
  `wire_repairs_routes.go:79,99`, `maintenance_dispatcher.go:91`).
- **Auth-off tightenings (SEC2-21, SEC5-8).** These change HEAD's auth-off reach on purpose; R2's
  release note states each (09), and the identity cutover oracle lists them as expected differences
  (§12):
  - (a) every `Guards.Interactive` route (keys, temp-login, sessions of others, password, backup
    restore): HEAD's credential guard is a pass-through with auth off
    (`internal/server/credential_routes.go:79-82`), the auth-disabled principal is non-interactive
    (05-authn §4.11);
  - (b) the `settings.manage` system detail and log routes, whose successors need
    `core:system.manage` (§6.2);
  - (c) the backup routes: create, delete, verify and import (`core:system.manage`) and restore
    (`core:backup.restore`), both `AuthRequired`, where HEAD's `s.perm` opened them;
  - (d) the credential-metadata and audit routes (05-authn §4.11, §4.13; §7.6): the auth-disabled
    principal has no user and `core:audit.view` is `AuthRequired` (SEC-3).
- Zero users: anonymous; only `Public` routes (05-authn §4.2). Today's `HoldsPermission` first-run
  bypass (`internal/server/middleware/auth.go:498-504`) is removed.

## 5. Lifecycle

1. **P1** (06 §6.5.1) checks only the static namespace rules of every manifest's permission names;
   no `rbac` service exists yet. **P4**: on a store that still holds an app's pre-core roles, the app's
   cutover step writes them through `rbac.Importer` (ABO: M3 of R2, §6.3). **P4c** builds the role
   `Store`, `core/internal/rbacstore`, for every app. **P4d**: role kinds of the revocation-journal
   re-apply write through the `Store` (no rebuild step); on a boot that applied a restore or found a
   replaced store, P4d's post-restore step runs rbac's and audit's restore hooks (§7.1) as package
   functions, after auth's, and after the keyring and config overrides (COR3-2). **HEAD's per-boot role
   seeding (COR6-3):** until R2's identity flip, books' start hook keeps running HEAD's `auth.SeedRoles`
   over books' own code, so every intermediate `main` boots with the four seed roles; the flip PR deletes
   that call in the change that turns on P7's seed commit (05-authn §5 lists the whole
   `seedRolesAndTokens` sequence).
2. **Before P4b** the kernel builds `audit.Store` (R4: L4, same layer as `auth`), as it builds the
   cutover's importers (05-storage §5S.3.13 "Reach"), so a cutover step's `core.storage.cutover_step`
   record and F3's `core.settings.update` stage through `RecordIn` in their own batches (IMPL-3); its
   async writer starts in step 7. **P5**: the kernel creates `rbac.Service` (over the `Store` built in
   P4c, with `Deps.Compile = authz.Compile`) (COR2-7, COR4-12).
3. Core declares its permissions and seeds: from F2 the kernel declares **one fixed list**, every
   `core:*` `PermissionSpec` this file names (§3.3, §4.7, §6.1, §6.2, §7.6) with its class and flags,
   whichever core subsystems are wired yet (IMPL-4), so F4's seed roles, M3's writes and F4's
   manifest requirements never name a scope the catalog lacks (`TestCoreCatalog_FixedFromF2`, §12);
   then each enabled module's manifest is declared via
   `Declarer` (06 A1: permissions and roles are manifest data). Disabled modules declare nothing; their
   scopes stay stored in roles but are inert (not in catalog → never `Has`), and reappear when the
   module is re-enabled.
4. `Freeze`: validate (§4.9), compute closures and the seed merge (§4.8), bump generation. The seed
   merge's batch (seed rows with their audit records) is committed in P7 as 06's "seeded grants"
   writer, so P5 writes nothing.
5. `httpapi.Build` validates route requirements against the frozen catalog.
6. Runtime: role writes bump `Generation()` and call `Deps.OnChange`, through which the kernel
   publishes `core.rbac.changed.v1`; caches key on generation, so the next request sees the change.
7. `audit` starts its async writer goroutine; the kernel registers `core.audit.prune` on
   `tasks.ScheduleFromConfig("core.audit.schedules.prune")`, a declared `tasks.ScheduleConfig` key in
   audit's own namespace (resolution R2-7, SIMP2-1); until R2's tasks flip is on `main` nothing prunes,
   which retention tolerates (COR4-13). On shutdown the async queue is drained (bounded 5 s), remaining
   events counted as dropped.

## 6. Migration from ABO (atoms, roles, stored data)

### 6.1 The 13 atoms → native scopes

This is ABO's atom-to-scope table, the one normative mapping (books' code, §4.9). Two things use it,
and only these: R2's identity cutover, which writes each atom's **native scopes** into the imported keys
and custom roles (§6.3), and books' `s.perm(atom)` adapter on the gin mount, which requires the atom's
**route requirement** on every books route HEAD gated by the atom (05-http-api §8.1, §8.3, which cites
this column) and on every books task definition that declared it (05-tasks). No row stores an atom after
the cutover. **Every row's route requirement is `AllOf` all of its native scopes, always** (XFILE-5;
05-http-api §8.3 wins as the stricter): the atom's **full** HEAD reach, so a role holding part of that
reach (a native custom role made after R2 holding only `books:settings.manage`, or only
`books:integrations.manage`) passes no route the atom gated. A single-scope row's requirement is
trivially its one scope, and `playlists.create` has none because no route enforces it. For
`library.view` the rule adds `core:tasks.view` to its 147 routes; every principal the cutover gives
`library.view` holds it, and the `editor` and `viewer` seeds carry it (§4.8), so no R0 reach is lost.
For `integrations.manage` it requires both integration scopes, which the cutover always writes
together. The read-side entry at in-handler sites (`users.manage`'s `core:users.view`, below) is a
separate entry of the atom table, not the row's route requirement.

| HEAD atom | Routes (`raw/09` §1.3) | Native scopes (written by the cutover) | Route requirement | Notes |
|---|---|---|---|---|
| `library.view` | 147 | `books:view`, `core:tasks.view` | `AllOf(books:view, core:tasks.view)` | 09's `core:events.subscribe` dropped: the event stream is `AnyAuthenticated` with per-topic permissions (05-realtime §4.4) |
| `library.edit_metadata` | 111 | `books:edit` (implies `books:view`) | `books:edit` | — |
| `library.delete` | 22 | `books:delete` (implies `books:view`) | `books:delete` | — |
| `library.organize` | 9 | `books:organize` (implies `books:view`) | `books:organize` | — |
| `scan.trigger` | 15 | `books:scan` | `books:scan` | 09's `core:tasks.run` dropped: task start is the definition's `Permissions.Start` (05-tasks §5T.3.7) |
| `integrations.manage` | 12 | `core:integrations.manage` (implies `core:integrations.read`), `books:integrations.manage` | `AllOf(core:integrations.manage, books:integrations.manage)` | the implied read scope serves the integrations registry's list/get routes (05-metadata) |
| `users.manage` | 20 | `core:users.manage` (implies `core:users.view`), `core:roles.view` | `AllOf(core:users.manage, core:roles.view)`; at in-handler sites that only show user-identifying text, `core:users.view`, the read-side successor (COR2-2, SEC5-14; e.g. trash progress, `handlers/audiobooks/handler.go:986`; run error text through `tasks.RedactRunView`, 05-realtime §4.7) | `core:roles.view` lets holders see role names when assigning. Natively the scope is bounded by dominance and `CanAssign` (§4.7), which HEAD's atom was not (`auth_temp_login.go:225-227`, `auth_reset_password.go:18-60`, `handlers/user.go:155-190`) |
| `settings.manage` | 83 (and old op-def default) | `core:settings.manage`, `books:settings.manage`, `core:tasks.manage`, `core:system.view`, `core:system.manage`, `core:library.manage`, `core:library.browse`, `core:integrations.manage` | `AllOf` of these eight scopes (the auth-disabled principal is judged on the seven non-`AuthRequired` ones, §4.11) | its HEAD reach includes system status (`wire_system_routes.go:28`), backup create and delete, logs, storage, filesystem browse and import paths (`wire_library_routes.go:60-66`, `wire_media_routes.go:62,69-70,81`), whose successors need the core scopes listed (SEC3-1). Restore (`core:backup.restore`, `Sensitive`) and every `ProtectAdmin` setting stay out of a key's reach natively, as HEAD's credential guard kept restore from keys |
| `playlists.create` | 0 | `books:playlists.create` | — (not enforced) | HEAD gates no route with it; the playlist write routes stay `AnyAuthenticated` (among the 36 permission-less routes, `raw/09` §4; `wire_library_routes.go:102-109`), so viewers and keys keep their own-playlist writes, as at HEAD (SEC-4; the oracle shows no loss except for a key whose owner's roles hold no ambient scope, §6.3) |
| `collections.manage` | 6 | `books:collections.manage` | `books:collections.manage` | — |
| `requests.create` | 0 | `books:request` | `books:request` | — |
| `requests.approve` | 0 | `books:request.approve` (implies `books:request`) | `books:request.approve` | — |
| `fingerprint.worker` | 5 | `books:fingerprint.worker` (`Confining`) | `books:fingerprint.worker` | — |
| **every** atom, for HEAD's `admin` seed (`internal/auth/seed.go:44-46`) and for a key whose owner holds HEAD's `admin` role (the bootstrap key, `internal/server/bootstrap.go:402` @748fd787a, and an admin's every-atom or empty-list key) | — | `core:admin` plus every native scope of the 13 rows above | — | with early fix 4 such a key is HEAD's admin-equivalent credential, less the `Sensitive` scopes (`core:roles.manage`, `core:backup.restore`, `books:admin.debug`) (05-authn §11.1); the explicit scopes keep the reach that `core:admin` alone does not give a key: approving runs, whose `Approve` scope a key must name (§4.10). Any other holder of every atom — a custom role, or a key owned by a non-admin — gets the union of the 13 rows (a key then ∩ its owner's role set), never `core:admin`, because HEAD's admin checks were role-name tests it never passed (§4.6; SEC-12) |
| role-name check `admin` | 20 `RequireAdmin` + handlers | see §6.2 | see §6.2 | see §6.2 |

A set of atoms maps to the union of its rows, after the implication closure; only the holders the
every-atom row names map to it. `TestAtomTable_Golden` pins the table, and the reach oracle (§12) is
what proves it: every route a principal reached on R0 it reaches after the cutover, and no other.

**In-handler sites (XFILE-12).** This section owns books' in-handler site list: every HEAD
`auth.Can(ctx, atom)` call in the gin mount, with its atom, whether it is judged by the read-side entry
(`core:users.view` for `users.manage`) and whether HEAD's code bypasses it with auth off (§4.11), plus the
role-name sites of §4.6. It is generated from HEAD's code into books' atom-table package, checked against
the mount by the ABO archtest that finds no raw `auth.Can` left, and pinned by `TestAtomTable_Golden`;
05-http-api §8.3's `legacyauth.Can` reads it.

New permissions with no atom: `core:admin` (`AuthRequired`), `core:self`,
`core:roles.view` (`ClassView`), `core:roles.manage` (`Sensitive`), `core:users.view` (`ClassView`),
`core:audit.view` (`ClassView`, `AuthRequired`: the audit trail holds user names, addresses and key IDs, SEC-3), `core:system.status` (`ClassView`; readiness tokens),
`core:system.view` (`ClassView`; status detail and sysinfo — held by default
only through `core:admin` and the `settings.manage` row), `core:system.manage`
(`AuthRequired`; resets, logs, storage admin), `core:backup.restore` (`Sensitive`, `AuthRequired`,
SEC-16), `core:integrations.read` (`ClassView`; list and read integration instances, never secret
values; implied by `core:integrations.manage`, otherwise held by default only through `core:admin`),
`core:tasks.view` (`ClassView`), `core:tasks.manage`, `core:metrics.read` (`ClassView`),
`core:library.manage` and `core:library.browse` (declared by 05-library §5L.3; held by default only
through `core:admin` and the `settings.manage` row; see §14 item 7),
`books:reading` (own progress/state: the reading-state and position routes among the 36),
`books:admin.view` (`ClassView`; read-only admin introspection: the debug reads and ABO's cache key listing), `books:admin.debug`
(`AuthRequired`, `Sensitive`). `books:view` is `ClassView`.

### 6.2 Seed roles and admin-gated routes

| HEAD role (ID kept) | After migration |
|---|---|
| `admin` | core seed, locked, `core:admin` |
| `editor` | core seed `editor`; books' `DefaultGrants` supply the books permissions (§4.8 table) |
| `viewer` | core seed `viewer`; books' `DefaultGrants` supply `books:view, books:request, books:reading` |
| `fp-worker` | books module seed role with ID `fp-worker` (admitted by books' `Manifest.ImportedRoleIDs`, §4.9), `books:fingerprint.worker` only — no `core:self` |

The 20 `RequireAdmin` routes keep today's two kinds (`wire_admin_role.go:19-57`, decision table in
`docs/decisions/2026-10-30-admin-routes-auth-off.md`), so both the role reach and the auth-off reach
stay identical:

| Today's kind | Routes (all under `/api/v1` at HEAD) | Native permission | With auth off |
|---|---|---|---|
| read-only (`adminRole`) | `GET /cache/stats/keys` | `books:admin.view` | open (not `AuthRequired`) |
| read-only (`adminRole`) | the four `GET /admin/debug/*` | `books:admin.view` | open |
| read-only (`adminRole`) + `users.manage` | master-list routes | `AllOf(core:users.manage, books:admin.view)` | open |
| always-authenticated | `PATCH /admin/debug/books/:id`, `PATCH /admin/debug/book-files/:id`, `POST /admin/debug/edits/:edit_id/undo` | `books:admin.debug` | 401 |
| always-authenticated (and owner-guarded at HEAD, 05-authn §4.8) | `POST /system/reset` and `POST /system/factory-reset` | deleted in release R1, whose only reset is the offline `storage reset` (A-17); from F4 the one native route `POST /api/core/v1/system/factory-reset` replaces both, with `core:admin` + `Guards.Interactive` + `Guards.Owner` (§4.10); only `admin` holds `core:admin`, so the reach equals HEAD's `RequireAdmin` | 401 |
| always-authenticated | retired 410 stubs `/maintenance/wipe`, `/cache/invalidate`, `/admin/recompact-digests` | `core:admin` (stub keeps auth-first ordering) | 401 |
| always-authenticated | guest service tokens list, plan, revoke | `core:admin` | 401 |

`books:admin.debug` is `Sensitive`: the three debug edit routes are interactive-only from R2. An admin's API key, the
bootstrap key included, no longer reaches them (§4.4 rule 1); HEAD's `RequireAdmin` let an admin's every-atom or
empty-list key through. The four debug reads are unchanged.

v1.8.1 mapped `POST /system/reset` to `core:system.manage`, a grantable non-admin scope without the
interactive and owner guards. HEAD wires it with the same `credRoute` + always-authenticated admin group +
`ownerGateWhenOwnerSet` chain as the factory reset (`internal/server/wire_system_routes.go:45-48`
@748fd787a), so that mapping widened a whole-store wipe (COR8-1).

**The native reset route (resolutions R8-1, R9-6, A-17; COR8-1, COR8-2, SEC7-4, SEC9-4).** Both of HEAD's
whole-store wipes become one route over core's staged swap (`KindReset` of `restoreswap`, 05-storage
§5S.3.15): `POST /api/core/v1/system/factory-reset` in `internal/storagehttp` (L6, imported only by
`kernel`, 03 §3.3), audited `core.system.factory_reset` (detail `kind`). `storagehttp` receives the
kernel's staging function as a storagehttp-local `Stager` interface, never `*storage.Admin` (COR8-2).
Nothing destructive runs before the restart.

- **One confirmation contract: `storage.ResetRequest`.** 05-storage owns the struct and its rules; this
  route states only its guards and wire shape. The request carries
  `{kind, confirm: "RESET", delete_library_files?, library_root?, library_file_count?}`: `kind` is
  `store` (HEAD's `ResetSystem`: store and settings only, no file effect) or `factory` (adds books'
  post-reset hook). A missing or wrong `confirm` answers 422 `confirm_required` and stages nothing. The
  route deletes the library files under `RootDir` only when `delete_library_files` is true; without it
  the reset keeps every library file (resolution A-17). With it, `library_root` and
  `library_file_count` must match the configured `RootDir` and a fresh preflight count taken before
  anything is staged, which `kernel.stageReset` compares (05-storage defines what is counted: the
  top-level entries the hook would remove, one directory read); otherwise the route answers 422
  `library_files_confirm_required`, stages nothing, and its problem body carries the current
  `library_root` and `library_file_count`, which the UI's confirmation dialog shows ("delete <count>
  files under <RootDir>") before the second request. Success answers 200 `{"reset_id", "message"}`, plus
  the confirmed `library_root` and `library_file_count` when files will be deleted; the restart follows
  (05-storage §5S.3.15). `kind: store` refuses the flag (422), because it has no file effect.
- **HEAD's side effects.** For `factory` only, the gap-fill config file reset and the Open Library data,
  and, when `delete_library_files` is true, the library `RootDir` wipe
  (`internal/server/handlers/system/handler.go:538-589` @748fd787a), run in books' post-reset hook at the
  reset boot. They are stated as not undoable. A dangerous `RootDir` is refused at staging with 400 and
  nothing is staged, as HEAD refuses it (`handler.go:565-570`); without `delete_library_files` no file
  under `RootDir` is recorded or touched, so no dangerous-root check runs (SEC9-4). The hook never fails
  boot: per-entry errors are logged, audited and reported as `factory_reset_cleanup_incomplete`, and
  `hook_done` is always recorded. HEAD's count-less wipe (a factory reset confirmed by `RESET` alone) is not
  carried over: release R1's SPA hides its reset control, and F4's PR moves the dialog to the native
  route (09).
- **Exclusion.** One intent slot, `restore/pending.json` with its `kind`, serves restores and both reset
  kinds, so a staged reset and a staged restore exclude each other (`storage.ErrSwapPending`); 05-storage
  §5S.3.15 owns the rules.
- **Tests.** `TestFactoryResetCoreRouteRecordsHookArgs` and `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault`
  (§12).

v1.0.0 mapped the read-only debug and master-list routes to `books:admin.debug`, which is
`AuthRequired` and would have closed them with auth off, contrary to today's pass-through. Only
`admin` holds the new permissions by default, so every seed role's allow/deny with auth on is
unchanged.

**`settings.manage` system routes: a deliberate auth-off tightening (SEC2-21; the full list is §4.11).**
HEAD's `s.perm` passes every request with auth off (`wire_admin_role.go:18-22`), so the `settings.manage`
system routes are open then. Their successors split:

| Today (`/api/v1`, `settings.manage`) | Successor (05-health, 05-logging) | With auth off |
|---|---|---|
| `GET /system/status` | `GET /api/core/v1/system/status`, `core:system.status` (readiness tokens only) | open, as today |
| `GET /system/status` detail, `GET /system/storage`, system info | `GET /api/core/v1/health`, `/system/info`: `core:system.manage` | **401** (tightened) |
| `GET /system/logs`, `/system/activity-log` journal | 05-logging journal query and live tail: `core:system.manage` | **401** (tightened) |
| diagnostics bundle (new) | `core:system.manage` + `Guards.Interactive` | 401 |

`core:system.manage` is `AuthRequired` on purpose: detail text, versions, host data, log lines (file
paths, user names, provider errors) and bundles stay behind a credential (SEC-8). R2's release note
states the tightening, and the identity cutover oracle lists it (§12); every other route keeps its
auth-off reach.

### 6.3 Stored data and the identity cutover (R2)

There is no copy of the roles, no mirror and no rebuild at boot: `rbac` reaches roles only through
`rbac.Store` (§3.4), and core holds no app key name, row format or permission string.

**Native store (every app; SIMP4-3).** `core/internal/rbacstore`, used by `hello`, `coretest`, ABO and
every other app:

| Key | Value |
|---|---|
| `c/rbac/role/<name>` | `codec.JSON[Role](1)` (every field of §3.3) |

User role assignments live in auth's user records (05-authn §3.4). `TestNativeStore_RoundTrip`
(rbac) runs `rbactest.StoreSuite` over it.

**ABO's roles and key scopes at the cutover (R2, step M3; 05-authn §11).** Books' identity cutover step
moves HEAD's `role:` rows (`Role{ID, Name, Description, Permissions []string, IsSeed, Version}`,
`internal/database/store.go:966-977`) and the scope lists of HEAD's `apikey:` rows into native scopes,
in the same step and chunks as the users and keys, through §6.1's table. The hardened design lands
directly; no record holds an atom afterwards.

- **Seed rows** (`admin`, `editor`, `viewer`, `fp-worker`) are deleted, not imported: P7's seed commit
  creates them from the native seed at the same boot (§4.8), with their IDs, so every imported user's
  role list stays valid. HEAD rewrites them from its own seed at every boot
  (`internal/auth/seed.go:80-129`), so no operator content is lost.
- **Any other role row** (HEAD has no role CRUD route, so such a row exists only if written out of band;
  the fixture's hand-built overlay supplies them, 09 §9.3, IMPL-8) is imported through `rbac.Importer` as the custom role `custom.<slug of its ID>` with the native scopes
  of its atoms (a role holding every atom gets the union of the 13 rows, never `core:admin`, §6.1), plus
  the ambient scopes below when it is not `Confined()`; the imported users' role lists
  are rewritten to the new name in the same step. An atom outside §6.1 grants nothing, and the role is
  reported in the step's dry-run report and in health naming it (SEC4-13); a role whose every atom is
  unknown is quarantined with the role list entries that name it.
- **API keys.** Each imported key's scopes are its **effective set** at the cutover: the native scopes
  of its atoms (an empty list, HEAD's "all role permissions", meaning the owner's role set) ∩ the
  owner's native role set, as a role-only checker compiles it (§4.4), plus the ambient scopes below when
  that set is not `Confined()`. A key that HEAD admitted nowhere because its atoms lie outside its
  owner's roles is written only the ambient scopes, so a key never ends with an empty list (05-authn §4.4).
  At runtime it keeps the authenticated-only reach HEAD gave it only where its owner's roles hold those
  scopes: an `editor`, `viewer` or `admin` owner's key does. A key whose owner's roles hold none of them (the
  `fp-worker` seed role, an imported `Confined()` custom role, an owner left with no role) compiles to an
  empty checker and reaches no route. HEAD let such a key through every authenticated-only route because
  `workerOnly` reads an empty effective set as not worker-only (`internal/server/middleware/auth.go:271-274`
  @v0.223.0), although the guard confines "a user holding only the fp-worker role". An admin could mint one,
  since HEAD's create check reads the caller's set, not the owner's. R2 closes that gap on purpose, and the
  reach oracle lists it (§12). **Only when the owner's roles hold `core:admin`**
  (HEAD's `admin` role; SEC-12) does a key holding every atom, or with an empty list, get §6.1's
  every-atom row: `core:admin` plus every other row's native scopes, so it still approves every run HEAD
  let it approve (approval names its scope explicitly, §4.10) and the reach oracle shows no loss for it except the three debug edit routes, whose `books:admin.debug` is
  `Sensitive` (§6.2).
  An every-atom key of any other owner (an admin could mint one for an editor at HEAD,
  `handlers/apikeys.go:199-209`) gets the intersection, as every key does: the 13 rows' union ∩ its
  owner's role set, with no `core:admin`, so no key of a non-admin carries an admin flag (§4.4).
- **The ambient scopes (resolution R10-2, SEC9-8, SEC10-1).** HEAD gated its self-service routes
  (`/api/v1/auth/me`, own sessions and keys), `/metrics` (any credential,
  `internal/server/server_lifecycle.go:1736-1747`) and own reading state by authentication alone, and
  their successors require `core:self`, `core:metrics.read` and `books:reading`. The cutover writes these
  three into every imported key and custom role that is not `Confined()`, deciding a key on its
  effective set, never on its owner (HEAD's `workerOnly` input, `internal/server/middleware/auth.go:268-297`
  @748fd787a). So the Prometheus scrape key, scripts' keys and own-profile reach keep working, and the
  remote fingerprint worker's key, an `fp-worker` user's key whose effective set is `books:fingerprint.worker` and an admin-owned key scoped
  `["fingerprint.worker"]` stay `Confined()` with none of them. At runtime a key's checker is still
  role ∩ key (§4.4), so an ambient scope counts only where the owner's role holds it too: the seed
  `editor` and `viewer` hold all three, `admin` holds them through `core:admin`, `fp-worker` holds none, so the
  ambient scopes written into an `fp-worker` user's key scoped outside the worker scope grant nothing (keys
  bullet above).
- **One write per record.** Each chunk imports its roles, users and keys and deletes their HEAD rows in
  one batch with its audit record (`core.role.create` for an imported custom role, detail
  `imported: true`), so no role exists in both forms.

**The reach oracle (PC-10-06).** `TestIdentityCutover_ReachOracle` (§12) proves the table: on the
fixture corpus the R0 binary wrote, with its labelled hand-built overlay (09 §9.3), every route and books
task × every principal kind (each seed role's session, keys of every scope shape, the auth-disabled
principal) gets the same allow or deny **on the R0 binary** (HEAD plus the six early fixes, 05-authn
§11.1; SEC-13) and after the cutover, except the listed expected differences (§12). It runs on the cutover PR and stays as
the atom table's regression test.
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
| `core.session.*`, `core.apikey.*` | sync (in batch) | `session.revoke` (05-authn §4.3); `apikey.create`, `apikey.revoke`, `apikey.set_status`, `apikey.require_confirmation` (05-authn §4.4, §4.14) |
| `core.user.*` | sync (in batch) | `create`, `disable`, `enable`, `delete`, `reset_password`, `set_roles` |
| `core.role.*` | sync (in batch) | `create`, `update`, `delete`, `update_seed`, `accept_seed`, `review_admin` (§7.1); `reset` with seed-role editing (§3.6) |
| `core.permission.deny` | async, coalesced | every 403 from httpapi stage 13, from `rbac.Require` (including books' `s.perm` adapter), every 403 `dominance_required` and every 403 `cross_origin_rejected` (until R2's identity flip, the access log's `security_event`, 05-http-api §4.2) |
| `core.task.*` | sync | `approve`, `reject`, `cancel` (by another actor) |
| `core.settings.*` | sync (in batch) | `update`: section + changed key names (values of secret fields never); from F3, through the store F2 lands (§3) |
| `core.storage.*` | sync (in batch) | `cutover_step` (one per cutover step, with its read, imported, quarantined and deleted counts, staged in the step's last chunk; 05-storage §5S.3.13), `quarantine_purge` (`<app> storage quarantine purge`: step, count, sensitive count; never a value), `cutover_blocker_fix` and `cutover_blocker_delete` (`<app> storage cutover-blockers fix` / `delete`: step, key, action, IDs, reason; never a value), `checkpoint_deleted` (the checkpoint sweeper or `storage prune-checkpoints`: name, reason) (IMPL-3; 05-storage §5S.3.12, §5S.3.13) |
| `core.oidc_provider.*` | sync (in batch) | `create`, `update`, `delete` (issuer, `link_existing_by_email`) |
| `core.integration.*` | sync (in batch) | `create`, `update`, `change_url` (secrets cleared, SEC-15), `write_secret`, `delete`, `test`, `local_migrated` (M6 only: instance, stored origin; staged in M6's chunk batch; 05-metadata M1.10) |
| `core.secret.*` | sync | `rotate` |
| `core.backup.*`, `core.restore.*`, `core.system.*`, `core.update.*`, `core.diagnostics.*` | sync | `backup.create`, `backup.download`, `backup.import`, `restore.stage`, `restore.apply`, `restore.revoke` (05-authn §4.14 counts), `system.factory_reset`, `update.apply`, `update.rollback`, `diagnostics.download` |
| `core.audit.*` | sync | `export`, `prune` |
| `core.outbox.*` | sync | `dead_retry`, `dead_discard` (consumer, seq, topic), `backlog_purge` (consumer, messages advanced past) (05-events §5E.11) |
| `<module>.*` | module-defined, via `audit.Logger` | ABO: `books.user.merge`, `books.guest_token.mint`, `books.guest_token.revoke`, `books.admin_debug.edit`, `books.admin_debug.undo` |

Routine successful reads are **not** audited (that is the access log's job); downloads of
diagnostics, backups and the audit export are, because each copies sensitive data off the host.

**Revocations journaled by rbac (SEC2-10).** One `Subject` cannot say what a role removal removed,
so `journal.Revocation` carries `Object entity.Ref` and `Detail []string` (05-backup §5B.3.2), and
rbac appends, before the change commits: `role.unassigned{Subject: user, Object: role}` (a role
taken from a user, directly or by `?reassign=`), `role.narrowed{Subject: role, Detail: removed
permissions}` (an edit that removes permissions, including an accepted seed withdrawal) and
`role.deleted{Subject: role}`. A store changed without the journal is handled by the replaced-store
rule of 05-authn §4.14.

**Restore hooks (SEC-17).** The restore record is written to 05-backup's out-of-DB restore
journal (cross-cutting resolution 8). `audit` and `rbac` each provide a `journal.RestoreHook`, run by
the kernel as package functions in P4d's post-restore step (06 §6.5.1), after the swap, the keyring,
the config overrides and the journal re-apply (COR3-2) and after auth's hook, and before listeners
open; boot fails if either fails. They also run on a replaced-store boot (05-authn §4.14).

- **rbac's hook** re-applies, writing through `rbac.Store` and auth's user store (one stored format,
  R4-1):
  `role.unassigned` → remove the role from the user; `role.narrowed` → remove `Detail` from the role's
  permissions; `role.deleted` → delete the role
  and unassign it (a seed role is re-seeded at the next `Freeze`). When a re-application would remove
  the last active `core:admin` holder, the role is **kept** — a lockout is worse than a stale admin —
  but the account is restricted without being removed (SEC3-15, SEC4-12): its `password_hash` is
  blanked with `password_reset_required` set (05-authn §4.14 step 1), its API keys are set
  `needs_confirmation`, its external-identity links are deleted (journaled `identity.unlinked`), and
  its sign-in is restricted (`User.SignInRestricted`, 05-authn §4.14): OIDC and identity extensions
  refuse it as password sign-in does, until an owner-verified reset link or the offline
  `<app> users reset-password` clears it (opened through `kernel.OpenOffline`; 05-authn §4.14,
  DATA7-11). The residual, Cloudflare Access resolving users by allow-listed e-mail, is stated in the
  restore report. So the demoted, possibly compromised,
  account cannot sign in with its restored password, keys or SSO links here;
  health reports `degraded` (`restore_last_admin_kept`, on authenticated endpoints only, never the unauthenticated `/health`, 05-health §5H.3.1) naming the recovery path (a reset link through
  the owner verifier, or the offline `<app> users reset-password`), and the audit record says which
  entry was kept. A demoted admin therefore stays demoted after restoring an archive taken before the
  demotion (`TestRestore_DemotedAdminStaysDemoted`).
  **Pre-swap restrictions win whenever there is a pre-swap store (SEC5-10, SEC6-4).** A demotion whose
  `Append` failed yields a `gap` line, so `rc.Revocations` is nil, and re-applying the journal alone
  would hand the role back. So whenever the kernel can open the pre-swap store (a staged
  restore: a read-only `rbac.Store` over the pre-swap store, built by calling the live store's
  constructor once more over the handles `storage.Admin.OpenReadOnly` issues for the pre-swap directory,
  whose writes fail `ErrReadOnly`, 05-backup §5B.3.6, 05-storage, 05-authn §3.2 `RestoreDeps.Previous`;
  COR7-8), whatever `rc.Revocations` holds, rbac's hook takes the **most restrictive** of the
  restored and the pre-swap state for every role row and every user's role list: an assignment absent
  in the pre-swap store is removed, a role's permissions are the intersection, a role deleted there is
  deleted, and the last-admin rule above still applies. With full coverage this equals the journal
  re-apply for journaled kinds, so it costs nothing on the common path. A staged restore's own swap
  appends no `gap` line and keeps its coverage (05-backup, DATA6-2), so a restore made after a clean
  run re-applies the journal **and** keeps every pre-swap restriction. 05-authn §4.14 step 3 applies
  the same rule to keys, user status, password hashes and identity links. For a replaced store there is no pre-swap store, and
  the residual is this one: a demotion made after the archive whose journal line is lost
  (the journal broken, unavailable or not covering the interval) can come back with the restored role
  rows until an admin re-checks the accounts holding `core:admin`. The restriction applied to them meanwhile is this file's: the hook lists every
  `core:admin` holder in the restore report and health reports `degraded` (`restore_admins_review`, authenticated endpoints only, 05-health §5H.3.1)
  naming them until an interactive `core:admin` session acknowledges each one (audited
  `core.role.review_admin`), and auth's fallback has already bumped their epochs (every session gone)
  and restricted their keys (`needs_confirmation`), so nothing they held before the restore is usable without a
  fresh sign-in. §14 item 8 records the stronger option (sign-in restriction) and its cost.
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
route, permission, outcome, target and reason) within `core.audit.coalesce_window` (1 min) are
folded into one record with `Coalesced=n`; the target is in the key because a route is a pattern,
so without it a caller walking many targets inside the window would show only the first one. The
open folds are bounded by `core.audit.async_buffer`: at the cap the oldest fold is written early,
as it stands. Full queue → drop + `mediacore_audit_dropped_total` (fail-open: availability
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
audited `core.audit.export`). Visibility is default-deny: `core/audit` classifies every action of the
§7.1 table (`audit.VisibilityOf`) as `VisibleToAll` or `UserScoped`. A `core:users.manage` holder
sees every record; any other `core:audit.view` holder sees only the `VisibleToAll` ones, and a
record whose action is `UserScoped` (everything about an identity, a credential, a session, a
one-time link, a sign-in provider or a security decision: `core.auth.*`, `core.user.*`,
`core.apikey.*`, `core.session.*`, `core.invite.*`, `core.templogin.*`, `core.identity.*`,
`core.bootstrap.*`, `core.oidc_provider.*`, `core.permission.deny`, `core.secret.rotate`,
`core.restore.revoke`, `core.integration.write_secret`, `core.integration.change_url`, and `core.integration.local_migrated`, whose details name the destination a Local grant was made for) or whose action
the table does not list (a module's own action, or a core action added without a classification)
is withheld whole, never partly masked. This is stricter than today's activity-log rule
(`internal/activity/user_data.go:78-90`, `core.auth.*` and `core.user.*` about another user), which
let a user see rows about themselves and every other prefix. `TestCoreActions_EveryActionIsClassified`
fails a table entry nobody classified. A row shown to a viewer who does not hold `core:users.manage`
also lacks `actor.credential_id` and `actor.client_addr` (which key or session did it, and from
where, is for user managers). The store filters by one action prefix only, so the query reads and
drops a non-manager's withheld records itself: one request reads at most 10,000 records and then
answers with the records it has (possibly none) and a `next_cursor` to go on from; a client follows
cursors until there is none.

### 7.7 Redaction

`Details` keys matching `(?i)(password|secret|token|key|cookie|authorization)` are replaced with
`"[redacted]"`; string values matching core credential prefixes (`mcs_`, `mck_`, `mcb_`, `mci_`,
`mct_`, and HEAD's `abk_`/`abbs_`, which imported keys and old archives still carry) are redacted regardless of key. Usernames are kept (needed for an
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
| A HEAD role or key row at the cutover names an atom outside §6.1 | the atom grants nothing; the record is reported in health and the dry-run report; the step continues (a role with no known atom is quarantined) | closed (that atom) | one bad row never crash-loops the unit (SEC4-13) |
| `ImportRole` with an unknown scope or a seed or non-`custom.` name | `ErrImportRejected`; the cutover quarantines the row | closed | the importer holds the store's invariants |
| Sync audit write fails | the change fails (same batch) | closed | attribution of security changes is required |
| Async audit queue full | drop + metric | open | availability |
| Audit query store error | 503 | closed | |
| Restore audit copy-forward fails | boot error | closed | SEC-17 |
| Restore role re-application would remove the last admin | role kept, account restricted (password reset required, keys `needs_confirmation`, `SignInRestricted`); health `degraded`; audited | open (that entry) | lockout protection (SEC2-10, SEC3-15) |
| Staged restore (a pre-swap store exists), with or without journal coverage | role rows and assignments: most restrictive of restored and pre-swap state | closed | an unjournaled or undetected demotion is not undone (SEC5-10, SEC6-4) |
| Seed-role edit or delete (`accept-seed` excepted) | 409 `seed_role_locked` | closed | the seed merge would silently undo it, because the operator diff is deferred-build (§3.6, DATA2-20) |
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

The v1.0.0 runtime key `core.rbac.legacy_aliases` is removed: ABO's atom table is books' code (§4.9,
§6.1; SIMP-7).

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

Names are shown under core's default metric namespace `mediacore`; an app passes its own namespace
(`app.Options.MetricNamespace`, 05-metrics).

## 12. Tests

| Test | Asserts |
|---|---|
| `FuzzParsePermission` / `TestScopeGrammar_Table` | grammar, one sub level, lowercase |
| `TestWildcard_RolesAllowedKeysRejected` | §4.2 table |
| `TestChecker_MatchVectors` | runs `testdata/match_vectors.json` (also run by vitest in `@mediacore/shell`) |
| `TestImplies_ClosureAndCycle` | |
| `TestCompile_KeyNarrowsRoles` | incl. wildcard role ∩ concrete key |
| `TestCompile_ResolvesRolePatterns` | `Deps.Compile` receives the union of the known roles' patterns; an unknown role ID is skipped and counted; a role edit bumps `Generation` and the next `Compile` sees the new patterns |
| `TestRequirement_Kind` | `Public`, `AnyAuthenticated`, `Need`, `AllOf`, `AnyOf`, `Delegated` and the zero value report their kinds |
| `TestAdmin_IsScopeAndKeyScopingApplies` | admin user's `books:view` key → 403 on `core:admin` route (regression for `RequireAdmin`) |
| `TestAdminChecks_HonourKeyScopes` | **property test, early fix 4 ported (05-authn §11.1):** for random key scopes and random owners (an admin, an editor, a viewer; imported every-atom keys and `["core:admin"]` included), every `core:admin` check — stage 13 on admin routes, `Can(core:admin)` at every site of §4.6's inventory, the merge actor check — admits iff the owner's roles hold `core:admin` and the key names it; an editor-owned key naming `core:admin` or every atom passes none (SEC-12); the survivor and task-actor checks read the account's roles, never the request's key; a holder of `core:admin` through a custom role passes them (SEC4-11, SEC8-7, SEC9-9) |
| `TestUserMerge_ScopedAdminKeyRefused` | HEAD's code (early fix 4) then core: an admin's API key scoped to user management passes the route guard of the footprint and merge-journal reads and is refused by the merge actor check (403); it is refused on every merge POST (credential route); the admin's session reads and merges (§4.6, SEC8-7) |
| `TestSensitive_EvaluatedBeforeAdmin` | bootstrap-style `["core:admin"]` API key: `Has(core:roles.manage)` false; interactive admin session: true (SEC-23) |
| `TestAuthDisabled_CheckerHasNoAdmin` | `authz.AuthDisabled` holds no `AuthRequired`/`Sensitive` scope, `admin=false`, `Has(core:system.manage)` and `Has(core:audit.view)` false (SEC-2, SEC-3) |
| `TestCoreCatalog_FixedFromF2` | (IMPL-4) a kernel with no optional core subsystem wired declares every `core:*` scope this file names, with its class and flags; every scope §6.1's table and the seed roles write is in it |
| `TestAuthOff_AdminRouteKindsUnchanged` | the §6.2 table: read-only kinds open, always-authenticated kinds 401 with auth off |
| `TestAuthOff_BooksGatesAndInHandlerDenial` | (ABO) auth off: a books `s.perm("library.view")` route and a started books task declaring `scan.trigger` → allowed; an in-handler atom site → denied, except the HEAD bypass sites (§4.11, COR5-4, SEC5-9) |
| `TestAuthOff_SettingsManageKeepsReach` | (ABO) auth off: a books `s.perm("settings.manage")` route and a books task declaring it → allowed (the `AuthRequired` `core:system.manage` dropped from the `AllOf`); a requirement made only of `AuthRequired` scopes (logs, backup create, the audit query) → 401; a real principal holding seven of the eight scopes → 403; no core route opens for the auth-disabled principal by the rule (§4.11) |
| `TestFactoryResetCoreRouteRecordsHookArgs` | the intent staged through `POST /api/core/v1/system/factory-reset` carries `kind` and `hook_args`, written through the kernel's `Stager` from one `storage.ResetRequest` (§6.2, COR8-2, R9-6) |
| `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault` | without `delete_library_files` the intent keeps every library file and a dangerous `RootDir` is not refused; with it but without, or with a stale, `library_root`/`library_file_count` → 422 `library_files_confirm_required` carrying the current root and count, and no intent is staged (no `restore/pending.json`); with matching values 200 naming the root and count, and the hook deletes only under that root; `kind: store` with the flag → 422; a missing or wrong `confirm` → 422, nothing staged (resolutions A-17, R9-6; SEC9-4) |
| `TestConfining_WorkerKeyRefusedOnPlaylists` | `books:fingerprint.worker` key → 403 on the playlist routes and every `AnyAuthenticated` route (regression for `middleware/auth.go:289`), 200 on the worker routes |
| `TestWorkerKey_Confined` | an admin-owned key scoped `["books:fingerprint.worker"]`, an `fp-worker` user's key scoped `["fingerprint.worker"]` and an `fp-worker` session: `Confined()` is true; each gets 403 on `/api/core/v1/me`, own sessions, `/metrics` and a reading-state route and reaches the worker routes, as HEAD's `workerOnly` decides (`internal/server/middleware/auth.go:268-297`; R10-2, SEC10-1) |
| `TestConfining_FacadeKeyRefusedOnNative` (deferred-build: lands with `arrfacade`, SIMP2-6) | `tv:arr.read` key → 403 on every non-facade route (SEC-5) |
| `TestRestore_DemotedAdminStaysDemoted` | archive taken before an admin's demotion; restore, restart: the account is not admin; a re-application that would remove the last admin keeps the role, blanks the password, sets the keys `needs_confirmation` and reports health `degraded` (SEC2-10, SEC3-15) |
| `TestAudit_CopyForwardThenWriteNoOverwrite` | after a restore with copied records, the next N records get fresh `seq`s and no copied record or index row changes (SEC2-12, DATA2-16) |
| `TestSeedRole_EditRefused` | `PATCH /roles/editor` and `DELETE /roles/editor` → 409 `seed_role_locked`; `accept-seed` → 200; custom roles editable (DATA2-20, SIMP4-6) |
| `TestSeed_NoNonConfiningScopeOnConfinedRole` | no seed merge gives a role holding a `Confining` scope a non-`Confining` one; `fp-worker` stays `Confined()`; `editor`/`viewer` hold `core:metrics.read` (SEC3-13) |
| `TestAtomTable_Golden` | (ABO) the 13 rows and the every-atom row of §6.1: native scopes and route requirement per atom; every requirement is `AllOf` all of its row's native scopes (a single-scope row's is that scope), no single-scope requirement is `AuthRequired`, `playlists.create` has none; every scope the table writes is in the core catalog of §5 step 3 or books' manifest; the in-handler site list matches the mount (XFILE-5, IMPL-4, XFILE-12) |
| `TestIdentityCutover_RolesAndKeyScopes` | (ABO, over the R0-written fixture of 05-authn §10 and its overlay) the four seed rows are gone and P7 recreated them natively; an out-of-band role row is imported (one holding every atom without `core:admin`) as `custom.<slug>` with its atoms' scopes and the users that held it hold the new name; an unknown atom grants nothing and is reported (SEC4-13); every imported key's scopes are its effective set (an empty list → the owner's role set; an admin owner's every-atom or empty-list key → §6.1's every-atom row, `core:admin` plus every other row's scopes; a non-admin's every-atom key → the intersection, no `core:admin`) plus the ambient scopes iff not `Confined()`; no key or role has an empty list; no atom string is left in any role or key record (§6.3) |
| `TestIdentityCutover_ReachOracle` | (ABO, PC-10-06) on the R0-written fixture with its hand-built overlay (09 §9.3), against the **R0 binary** as baseline (HEAD plus the six early fixes, 05-authn §11.1; SEC-13): for every route of the HEAD router dump (572 at `748fd787a`), every core route (the auth-disabled principal included, SEC-3) and every books task, and every principal kind — a session of each seed role, keys scoped `["library.view"]`, `["settings.manage"]`, `["users.manage"]`, `["fingerprint.worker"]`, every atom and an empty list, owned by an admin, an editor, a viewer and an `fp-worker` user, an editor-owned every-atom key (SEC-12), a key whose atoms lie outside its owner's roles, a custom role holding every atom, and the auth-disabled principal — the allow or deny after the cutover equals R0's, mapped to each route's successor. Expected differences, each asserted as such: §4.11's auth-off tightenings (a)–(d), the credential-metadata and audit routes' 401 for the auth-disabled principal among them; §6.2's tightened `settings.manage` system routes; the `core:system.status` readiness reach of `editor` and `viewer`; dominance and `CanAssign` on `core:users.manage`; the deleted HEAD reset routes (§6.2); the authenticated-only routes for a key whose atoms lie outside its owner's roles when those roles hold no ambient scope (an `fp-worker` user's keys scoped `["library.view"]`, `["settings.manage"]`, `["users.manage"]`; §6.3); and the debug edit routes for an admin's every-atom or empty-list key (`books:admin.debug` is `Sensitive`, §6.2). Viewer sessions and every key whose compiled checker holds a non-`Confining` scope keep their playlist writes (SEC-4). No principal loses an allow outside these: an every-atom or empty-list key of an admin still approves every run it approved on R0 (§6.3) |
| `TestUnknownAtom_ReportedNotGranted` | an imported role or key naming an atom outside §6.1: the atom grants nothing, health and the dry-run report name the record, the step continues (SEC4-13) |
| `TestNativeStore_RoundTrip` / `rbactest.StoreSuite` | the native store passes `StoreSuite` (SIMP4-3, R4-1) |
| `TestImporter_RejectsBrokenRole` | `ImportRole` with an unknown scope, a non-`custom.` name or a seed name → `ErrImportRejected`, nothing staged |
| `TestModuleSystemActor_CannotStartUndeclaredKind` | (in 05-tasks) the system actor's checker = own kinds' `Start` ∪ `TaskStarts`, never `core:admin`/`AuthRequired` (SEC2-15) |
| `TestAuditAction_Grammar` | every action literal in core and in this table matches `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$` (COR2-19) |
| `TestAnyAuthenticated_ModuleOnlyKeyReachesCoreEvents` | key scoped only `books:scan` passes `AnyAuthenticated` on `GET /api/core/v1/events` (SEC-3) |
| `TestManifest_RejectsForeignNamespace` / `TestManifest_RejectsCoreGrant` / `TestManifest_RejectsRestrictedDefaultGrant` | §4.9 |
| `TestSeedMerge_NewSeedScopeReachesRole` / `TestSeedMerge_Idempotent` / `TestSeedMerge_AdminOutsideMerge` | property test over random seed sequences: effective = the last applied seed; `admin` is always `{core:admin}` (DATA4-11). The operator-diff tests are deferred-build (§3.6) |
| `TestSeedMerge_RestrictedAdditionHeldPending` | a release adding a `Sensitive` scope to an existing role leaves it in `PendingSeed` until `accept-seed`, which succeeds on a seed role (SEC-25, SIMP4-6) |
| `TestRoleWrite_EscalationRefused` / `TestCanAssign_CannotGrantUnheld` / `TestCanAssign_RemovalCheckedLikeAssignment` | |
| `TestUsersManage_CannotTempLoginAdmin` / `TestUsersManage_CannotResetDominatingUser` | a custom role with `core:users.manage` only → 403 `dominance_required` (SEC-4) |
| `TestStart_ViewOnlyDefRefusedAtRegister` | (in 05-tasks) a def whose `Start` is `ClassView` fails registration without `StartableByViewers` (SEC-9) |
| `TestApprove_RequiresVerifiedMethodAndPermission` | (in 05-tasks) among its cells: an API key approves only with the `Approve` scope named; delegated sessions refused (SEC-24) |
| `TestLastAdmin_ConcurrentDemotions` | `testing/synctest`, two concurrent demotions → one 409 |
| `TestRestore_UnjournaledDemotionHeldByPreSwapStore` | staged restore of an archive taken before a demotion whose `Append` failed, with nil coverage: the role stays unassigned and narrowed, deleted roles stay deleted, the last-admin rule still holds (SEC5-10, SEC6-4) |
| `TestAudit_SyncInSameBatch` | failing batch → neither change nor record |
| `TestAudit_SeqUniqueAcrossCrash` | `Sequence` reservation: after a simulated crash, no `seq` is reissued; gaps allowed |
| `TestAudit_DenialCoalescing` | `synctest` clock |
| `TestAudit_RestoreCopiesForwardRecords` | records above the manifest's audit high-water mark survive a restore (SEC-17, DATA2-7) |
| `TestAudit_RedactsSecretsByKeyAndPrefix` | |
| `TestAudit_QueueFullDropsWithoutBlocking` | |
| `TestNoGlobals` (archtest) | no package-level registries in `rbac`/`audit` (R5) |
| `archtest.NoSystemPrincipalOutsideKernel` | no non-test file outside `auth`, the `tasks` executor and `kernel` imports `core/internal/authz` (alias, dot and blank imports included), and none names it in a `//go:linkname`; `rbac` imports nothing from `authz`, test files included (SEC-26, COR4-12) |

## 13. Gap disposition G1–G15 (all gaps from `raw/10` §8)

| Gap | Disposition | Where |
|---|---|---|
| G1 closed hard-coded vocabulary | **Fixed**: catalog of module-declared `PermissionSpec`s; `IsKnown` → `Catalog.Lookup` | §3.2, §4.9 |
| G2 no namespace | **Fixed**: `<ns>:<action>[.<sub>]`; 13 atoms mapped; stored data migrated once at R2's identity cutover | §4.1, §6 |
| G3 seed-only roles, boot overwrites edits, no CRUD | **Fixed**: roles as data with CRUD API; module seeds and grants; restricted additions held and acceptable; the seed-role operator diff and tombstones are deferred-build (§3.6) | §4.7, §4.8 |
| G4 admin by role name | **Fixed**: `core:admin` scope; role-name checks removed; key scoping applies | §4.6 |
| G5 imperative route binding, 36 permission-less routes, unenforced atoms | **Fixed**: declarative `Route.Perm` (R6, boot error), `AnyAuthenticated` made explicit, confinement explicit; `playlists.create` stays unenforced, so playlist writes keep HEAD's reach (SEC-4) | 05-http-api §3.1, §8.2; §4.10, §6.1 |
| G6 key scopes: empty = unrestricted | **Fixed**: keys require non-empty concrete scopes; HEAD's empty lists become the owner's role set at the cutover; key-mints-key escalation closed | §4.5, §6.3; 05-authn §4.4 |
| G7 no per-app membership | **Fixed by composition**: roles composed of namespaced permissions (a user holding `tv.admin` (`tv:*`) and `viewer` (books view grants)); per-user data ownership stays app-specific | §4.6 |
| G8 plugin routes unauthenticated | **Fixed**: modules only get chained `Group`s | 05-http-api §4.2 |
| G9 auth wiring inline/global | **Fixed** | 05-authn §13 |
| G10 single-binary bootstrap | **Fixed** | 05-authn §4.6 |
| G11 CF/owner machinery in auth layer | **Fixed** (extension point; policy editing stays in ABO) | 05-authn §4.8 |
| G12 secrets at rest | **Fixed**: auth credentials hashed (05-authn §4.1); config secrets encrypted via `core/secrets` (05-config); N10. ABO's rows move at R2's identity and config cutovers (05-authn §11, 05-config) | 05-authn, 05-config |
| G13 no uniform audit trail | **Fixed**: `core/audit` schema, sync/async paths, retention, query API, restore copy-forward (no hash chain, §7.4) | §7 |
| G14 throttling tied to tunnel topology | **Fixed + documented** | 05-authn §4.9 |
| G15 two auth stacks | **Fixed**: compat authenticates only through the method-bound `auth.Compat` handle (05-authn §3.1) | 05-authn §4.10 |

## 14. Open items

1. Whether `core:self` should be implicit for every active human user instead of a grant in the
   `editor`/`viewer` seeds; granted is chosen here because it keeps "a user with no roles can do
   nothing" (today's safe default, `middleware/auth.go:424-427`) and keeps worker roles out of
   self-service routes. ABO's imported keys and custom roles get it by the cutover's ambient rule
   (§6.3), which keeps HEAD's reach: HEAD's self-service routes need authentication only.
2. Resource-level permissions (per-library or per-root ACLs) are not designed; `rbac.Require` plus
   module-side filtering covers today's per-user ownership (playlists, reading state).
3. Audit export to an external sink (syslog/OTLP logs) — 05-logging may add a sink; not required.
4. **Resolved (D10–D12): 09 §9.2.6.** 09 states the policy only and cites §6.1's table, which drops
   `core:events.subscribe` and `core:tasks.run` and adds `core:system.view` to `settings.manage`; §6.3 is
   the store interface (R4-1) and the one-way identity cutover.
5. Coordination with 06-app-sdk §6.4.2 and 08 (COR-16, SEC-23): both use `rbac.Permission`,
   `rbac.PermissionSpec` (with `DefaultGrants`, `Warn`) and `RoleName` from this file; there is no
   `rbac.Scope`, `rbac.PermissionDef`, `rbac.Grant` or `Dangerous` field. Core seed role IDs are
   `admin`/`editor`/`viewer` (not `core.viewer`).
6. **Resolved (round 3, R3-1): `integrations.manage` reach.** HEAD's integration URL and secret writes
   go through the `settings.manage` config update, and the `integrations.manage` atom's own routes
   write no URL or secret (`deluge_integration.go:141-146`, `server_lifecycle.go:2088-2092`), so the
   atom's native scopes (§6.1) stay inside the native bound (05-metadata M1.3). SEC3-4's bypass is
   moot.
7. **Resolved (round 4, R4-3): `core:library.browse` reach.** HEAD gates the directory picker
   `GET /api/v1/filesystem/browse` with `settings.manage` (`internal/server/wire_library_routes.go:61`).
   Its R2 successor requires `core:library.browse`, which `admin` holds through `core:admin` and the
   `settings.manage` row of §6.1 grants an imported key; only `admin` holds `settings.manage` among
   HEAD's seed roles, so the reach oracle holds across the move (§12).

8. **Resolved (round 5, lead decision): replaced-store restores and unjournaled demotions take the
   weaker rule (SEC5-10).** A replaced-store restore (a file copy, a host-loss restore) has no pre-swap
   store, so a demotion whose journal line is lost can come back. This file applies the **weaker rule**, which the lead accepted: after such a
   restore every `core:admin` holder is listed for review in the restore report and in health
   (`restore_admins_review`) until an interactive admin acknowledges each one, with auth's fallback
   having already bumped their epochs and set their keys `needs_confirmation` (§7.1); what remains is
   §7.1's stated residual. The stronger option, recorded and not taken, is to set
   `SignInRestricted` on every `core:admin` holder except the bootstrap path after such a restore,
   until an owner-verified reset; its cost is that every such restore locks every admin out until a
   reset link or the offline `<app> users reset-password`. Kept here only so a
   later decision can revisit it; nothing in the extraction depends on it.

## Objections to 03

None open. 03 v1.3.0+ R6 states that `AnyAuthenticated` never admits a confined credential (the
v1.1.0 amendment), §3.6 has the role-ID and audit-action rows, and §3.3's store-interface rows (native
implementations plus an importer an app's cutover step calls) are what §3.4 and §6.3 rely on.

## Out-of-scope defects found

1. **`RequireAdmin` ignores API-key scopes** (`internal/server/middleware/auth.go:522-537` reads
   `user.Roles`; `handleAPIKeyAuth` binds the full user at `:384-386`): a key an admin scoped to
   `library.view` passes every admin-gated route not also behind `credGuard` (7 `admin/debug`
   routes including `PATCH …/books/:id` and `…/edits/:edit_id/undo`, `raw/09` §3.1 rows 241-247).
   The three in-handler role-name checks (`internal/server/handlers/user.go:285,453`,
   `slices.Contains(actor.Roles, "admin")`, and the user-merge actor check `mergeActor`,
   `internal/server/handlers/user_merge.go:67`, SEC8-7) ignore key scopes the same way: a key an admin
   scoped to `users.manage` passes `s.perm(users.manage)` and `mergeActor` on the two merge reads that are
   not credential routes (`GET /users/:id/footprint`, masked sign-ins; `GET /users/merges`, every merge
   journal; `user_merge.go:97-99,190-192`), although the handler's own comment says `users.manage` is not
   enough. The merge POSTs are credential routes (`wire_library_routes.go:177-181`), so the key cannot
   run a merge. Hotfix in ABO (05-authn §11.1 item 4):
   refuse an `api_key` method whose non-empty scopes do not include every atom, in `RequireAdmin`, in
   `adminRole` and in the three handler checks. Requiring `auth.Can(users.manage)` in
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

Round 5 (2026-11-04), findings applied to this file:

- COR5-4 — one requirement value `rbac.LegacyAtom(atom)` for legacy `s.perm` routes and adapted op definitions (roles via `LegacyReach`, the auth-disabled principal via the expansion), plus `LegacyCheck` for in-handler sites; op starts get no auth-off tightening (§3.1, §4.11, §6.3, §12).
- SEC5-7 — a key without an extension record keeps HEAD's role ∩ key reach on legacy surfaces (`CheckerInput.LegacyKey`/`LegacyAtoms`); scoped-key cells (§3.1, §3.4, §4.5, §6.3, §12).
- SEC5-8 — auth-off claims corrected (HEAD's credential guard is a pass-through); recorded tightenings listed: `Guards.Interactive` routes from S4, system detail/logs, backup routes from S8 (§4.11, §6.2, §12).
- SEC5-9 — in-handler checks deny the auth-disabled principal as HEAD's `auth.Can` does; snapshot column `auth_off` with response-content cells (§3.1, §4.11, §6.3).
- SEC5-14 — in-handler `users.manage` sites: per-site `read_side` in the snapshot's in-handler list; §6.1 and §6.3 cite it (§3.1, §6.1, §6.3).
- COR5-5 — the legacy alias table is `app.Options.LegacyPermissionAliases`, declared by the kernel as app-level aliases (may target `core:*`, validated at `Freeze`); `core/rbac/legacy_abo.go` deleted; `Manifest.LegacyRoleIDs` replaces `LegacySurfaces` (§3.2, §4.9, §5, §6.2, §6.3, §12).
- MOD5-17 — `internal/books/rbacreach/` is a data directory read by the test, not a Go package (§6.3).
- SEC5-10 — rbac's restore hook takes the most restrictive of restored and pre-swap state when coverage is nil; replaced-store residual matches 14 K22, with the interim restriction (admin holders listed, health `restore_admins_review`, audited `core.role.review_admin`); stronger option recorded as §14 item 8; `TestRestore_UnjournaledDemotionHeldByPreSwapStore` (§7.1, §8, §12, §14).
- SEC5-12 / DATA5-16 (R5-6) — the `role:` guarded projection covers `id`, `permissions`, `is_seed` only; `legacy_ref.fp`; field classes stated (§6.3). DATA5-2's "roles hash the whole row" is superseded by R5-6 (security-relevant fields only).
- SEC5-1 (R5-6) — `LegacyFullScope` only for an empty key row without an extension record; never-empty rule names the reserved no-scope string (§4.5, §6.3).
- SEC5-13 / OPS5-5 — `/metrics` requires `core:metrics.read` from S4 r2 through the legacy manifest entry (option a); `TestLegacyMetricsRoute_RequiresScopeFromS4` (§4.8).
- SIMP5-4 — `rbac.Store` holds the native contract; `LegacyAtomWriter` and `Reconfirmer` are optional interfaces detected at P4c (§3.4, §5, §6.3, §12).
- SIMP5-6 / COR5-16 — deferred-members table: identifier-only first column; `UpdateRole`/`DeleteRole` and `Added` on seed roles moved to prose (§3.6).
- SIMP5-9 — `TestNativeBackend_RoundTrip` renamed `TestNativeStore_RoundTrip` (§6.3, §12).
- COR5-15 — the S4 r1 census is P4d step 1c (§5).
- DATA5-4 (via 05-authn) — audit action `core.apikey.confirm` added (§7.1).
- integration pass (round 5): §14 item 8 marked resolved — the lead accepted the weaker rule (admin holders listed for review, `restore_admins_review`, after an older-build restore; residual in 14 K22); the stronger sign-in restriction kept as a recorded, untaken option.

Round 6 (2026-11-04), findings applied to this file:

- COR6-1 — `LegacyAtom`/`LegacyCheck` are S2 leaf constructors that only record the atom; evaluation is the S4 checker's; `Freeze` refuses an unaliased atom; S2 manifest carries them (Landing, §3.1, §6.3, §12).
- COR6-3 — S4 r1: books' start hook keeps HEAD's `auth.SeedRoles`; the r2 flip PR deletes it as P7 seeding starts (§5).
- SEC6-2 — one scope-source predicate (`scopes_source: canonical`); restore/stale-stamp records are `scopes_source: legacy` and keep legacy semantics; empty-list case added (§3.1, §3.4 `CheckerInput`, §4.5, §6.3).
- SEC6-4 — rbac's restore hook applies "pre-swap restrictions win" whenever a pre-swap store exists, not only at nil coverage (§7.1, §8, §12).
- DATA6-2 — a staged restore's own swap appends no `gap` and keeps coverage (§7.1).
- DATA6-6 — factory-reset route: from S2b the handler stages core's factory reset (§6.2).
- SIMP6-4 — `rbac.Reconfirmer` deleted; re-confirmation through the shared `ExtConfirmer` (`"role/"+name`), discard as a write of the row's translation in the same batch (§3.3, §3.4, §6.3).
- SIMP6-6 — role-store capability cases move to one `legacytest.RBACStoreSuite` (§3.4, §6.3, §12).
- SIMP6-7 — `Role.Suspended` becomes `Role.Legacy *RoleLegacyState{Suspended}` (nil from a native store) with a `Role.Suspended()` reader, per 03's one rule for legacy-only record state (§3.3, §6.3).
- R6-3 / SIMP6-2 — role-store capabilities live in the `legacy` package outside the API promise (`rbac.LegacyAtomWriter` → `legacy.AtomWriter`); the kernel detects them at P4c and `rbac` names them through unexported twins checked by `export_test.go`; cutover removal is not a v1 break (§3, §3.4, §5, §6.3, Objections).
- integration pass (round 6): §4.10 and §6.2: `POST /api/core/v1/system/factory-reset` is `core:admin` + `Guards.Interactive` + `Guards.Owner` (05-authn §4.8 fallback; audited `core.system.factory_reset`), and the legacy `POST /system/factory-reset` row, split from `/system/reset`, carries the same guards (reach equal to HEAD's `RequireAdmin`; DATA6-6).

Round 7 (2026-11-04), findings applied to this file:

- COR7-1 / SEC7-4 / SEC7-5 / DATA7-6 (R7-2) — factory reset by stage: S2b–S4 r1 books' gin handler, behind HEAD's chain unchanged, calls `legacy.Maintenance.StageFactoryReset` (no core route, no alias); from S4 r2 the core route lands in `internal/storagehttp` and the legacy path becomes its `Alias`; HEAD's side effects run in a books post-reset hook after `applied`, not undoable (Q-K); the handler writes no default and deletes nothing (§4.10, §6.2).
- SEC7-4 — `TestLegacyRoleReachUnchanged` gains S2b and S3 cells for the legacy factory-reset path (§6.2, §12).
- COR7-8 (R7-4) — the pre-swap `rbac.Store` is built by calling the constructor once more over `storage.Admin.OpenReadOnly`'s handles (§7.1).
- DATA7-11 (R7-4) — the offline `users reset-password` opens through `kernel.OpenOffline` (§7.1).
- COR7-2 / SIMP7-1 (R7-7) — the mirror test is 03 §3.3's: `export_test.go` exports aliases, `legacy_mirror_test.go` in `package rbac_test` asserts both directions (§3.4).
- SIMP7-5 — the kernel asserts only the capabilities it drives; rbac's service and restore hook detect the role-store capabilities through their mirrors (§3.4, §5).
- SIMP7-8 — `legacy.AtomWriter` renamed `legacy.RBACAtomWriter` (owner-specific identifiers take the former package's name) (§3.4, §5, §6.3).
- integration pass (round 7): SIMP7-8 sweep: `RBACAtomWriter` comment cites 05-storage for former names; `legacytest.RBACStoreSuite` row names `RBACAtomWriter`.

Round 8 (2026-11-04), findings applied to this file:

- COR8-1 (R8-1) — §6.2: `POST /system/reset` mapped to the factory-reset guards (`core:admin` + Interactive + Owner) instead of `core:system.manage`; it stages core's `ResetStore` reset from S2b through `legacy.Maintenance` behind HEAD's chain and is an `Alias` of the storagehttp route from S4 r2; the factory reset is `ResetFactory`; `TestLegacyRoleReachUnchanged` covers both paths (§4.10, §6.2, §12).
- COR8-2 (R8-1) — §4.10, §6.2: storagehttp receives the kernel's staging function as `storagehttp.Stager`, never `*storage.Admin`; request `{"confirm": "RESET"}` (422 otherwise); both aliases keep HEAD's `{data: {message}}` through `LegacyError` + `Shape`; the reset alias picks `ResetStore` through a path value no native request carries; `TestFactoryResetCoreRouteRecordsHookArgs`, `TestFactoryResetAliasKeepsConfirmAndShape` (§12).
- COR8-4 / DATA8-1 / OPS8-5 / SEC8-5 (R8-1) — §6.2: the dangerous-`RootDir` refusal is at staging (400), the post-reset hook never fails boot and always records `hook_done`; one intent slot, `storage.ErrSwapPending`, `restore_pending`/`restore_inflight` (05-storage owns the rules).
- SIMP8-4 — §6.2: books' two gin handlers are deleted with the aliases in the S4 r2 PR.
- SEC8-7 — §4.6, "Out-of-scope defects" item 1: `mergeActor` (`user_merge.go:67`) is the third in-handler role-name check; from S4 the merge handler checks `Can(core:admin)` with key scoping; the reachable bypass is the two merge reads, since the merge POSTs are credential routes at HEAD; `TestUserMerge_ScopedAdminKeyRefused` (§12).
- SIMP8-3 — §3.4: "the capabilities it drives" replaced by "the capabilities 03 §3.3 lists".
- integration pass (round 8): §12 cites 05-tasks' tests by the names 05-tasks defines (`TestStart_ViewOnlyDefRefusedAtRegister`, `TestApprove_RequiresVerifiedMethodAndPermission`) (sweep (h)).

Advisor (2026-11-04), resolutions and findings applied to this file:

- A-2 (ADV-3, ADV-S1) — header, §3.1, §3.3, §3.4, §4.5, §4.7 rule 6, §4.8, §6.3, §7.1, §8, §12, §13: ABO's roles and keys keep HEAD's representation through S10 (legacy atoms only); core-namespaced scopes are an API-level read/write mapping over the app's alias table (every atom ↔ `core:admin`), so D7 holds; unrepresentable writes refused with 422 `no_legacy_atom` (`ErrNoLegacyAtom`, replacing `ErrConfiningNoLegacyAtom`); `RoleLegacyState` deleted (the legacy store returns the mapped set; `legacy.RBACAtomWriter` confirmed for writes, R-B request); the role extension record, its fingerprint, suspension, `ReconfirmRole`, `POST /roles/{name}/reconfirm`, `rbac_role_suspended`, `core.role.reconfirm`, `core.apikey.confirm`, `core.session.revoke_after_rollback`, the shared confirm capability and their tests are deleted; ABO's seed rows are HEAD's atoms with no `SeedDigest`/`PendingSeed`; `app.Options.LegacyAmbientScopes` (`core:self`, `core:metrics.read`, `books:reading`) keeps HEAD's authenticated-only reach; the row rule reduces to HEAD's own check on ABO (`TestLegacyAtomEqualsHEADCheck`, R-C request); the restricted last admin and restored keys use `disabled` on a legacy-backed store; the role cutover is part of 05-authn §11.2.
- A-5 (ADV-S4) — §4.8, §11: `/metrics` stays `AnyAuthenticated` on ABO (ambient `core:metrics.read`; the S4 r2 re-annotation and `TestLegacyMetricsRoute_RequiresScopeFromS4` deleted); metric names under the app's namespace.
- A-17 — §4.10, §6.2, §12: the native factory reset deletes library files only with `delete_library_files: true` and a matching `library_root`/`library_file_count` (422 `library_files_confirm_required` names them; success names them too); the legacy alias passes the flag and keeps HEAD's confirmation; `TestFactoryReset_LibraryFilesNeedCountedConfirm`.
- A-14 (ADV-S8) — §6.3: the atom-input contract waits for the ledger dates and the owner's check of the process-local counter.
- A-4 (ADV-S3) — §3.6: no deferred-members archtest; the table is planning information.
- ADV-11 — §12: the 572-route count in `TestLegacyRoleReachUnchanged` is informative and generated at the stage's base commit.
- integration pass (advisor): the native factory-reset test is `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault`, the name 09 S4 cites; compat counter under `<namespace>_`; no `needs_confirmation` stamp confirmed.

Round 9 (2026-11-04), findings applied to this file:

- COR9-3 (R9-4) — §3.4 `CheckerInput.Ambient`; §4.4: `Compile` decides `Confined()` before the ambient scopes and adds them after the key intersection; §6.3: the role-read injection and the "minus the ambient scopes" write rule deleted, ambient scopes come only from the principal and are never grantable; `TestLegacyAmbientScopes` gains a `LegacyFullScope` key, a scoped key and an empty-set key on `/api/v1/auth/me`, `/me/api-keys` and `/metrics` (§12).
- COR9-4 / SEC9-4 (R9-6) — §6.2: the factory reset uses 05-storage's one `storage.ResetRequest` (422 `library_files_confirm_required` against a fresh preflight count, nothing staged), carried unchanged through `Stager.StageReset`, `kernel.stageReset` and `ResetArgs`; the alias sets path values only; the dangerous-root check runs only when files will be deleted; tests restated (§12).
- SEC9-9 — §4.6, §6.3: the role-name inventory adds `repairs/handler.go:529`, `usermerge/plan.go:342,400` and `adminRole`, each with its replacement and reason; the reach snapshot's in-handler list names them; `TestRoleNameChecksUseCoreAdmin`.
- SEC9-8 — §6.3 deferred cutover: H1 writes the ambient scopes into every non-confined role and key before `LegacyAmbientScopes` goes.
- integration pass (round 9): §6.2 reset contract: `storage.ResetRequest`'s `LegacyConfirm` (`json:"-"`) and the comparison in `kernel.stageReset` (top-level entries, one directory read).

Round 10:

- SEC10-1 (R10-2) — §3.4 `CheckerInput.Ambient`, §4.4, §6.3: a key's confinement is decided on its effective set (role set ∩ key scopes, `admin` only when the key names `core:admin`), HEAD's `workerOnly` input, so a worker-confined key never gains the ambient scopes at any stage, whoever owns it; §6.3 states H1's carry-over once (roles iff not `Confined()`, keys iff their effective set is not `Confined()`, native key scopes derived from the effective set, never from the owner), with its one narrowing; `TestWorkerKey_NoAmbientScopes` added, `TestLegacyAmbientScopes` extended, `TestH1_ConfinedKeyStaysConfined` cited.

D10–D12 revision (2026-11-04):

- ABO's roles and key scopes move once at R2's identity cutover (step M3). Rewritten: intro, §3 intro and landing (R1/R2), §3.2 (no alias types), §3.4 (native store only; new `rbac.Importer` and `ErrImportRejected`; `CheckerInput` without legacy and ambient fields), §3.6, §4.4 (confinement on the key's effective set), §4.5 (imported keys), §4.8 (native merge only; seed-role edits refused because the operator diff is deferred), §4.9 (the atom table is books' code), §4.10–§4.11 (books' gates keep HEAD's auth-off reach; in-handler denial kept; tightenings listed in the oracle), §5, §6.1 (the atom-to-scope table: native scopes written by the cutover and the gate books' `s.perm` adapter requires), §6.2 (HEAD's two reset routes deleted; one native route), §6.3 (stored data and the cutover: seed rows recreated natively, out-of-band roles imported as custom roles, key scopes = effective set plus the ambient scopes iff not confined, the reach oracle), §7.1, §8, §10–§14 and objections. Deleted: `LegacyAtom`, `LegacyCheck`, `LegacyAlias`, `Catalog.Legacy`/`LegacyReach`, `Declarer.LegacyAliases`, `ErrNoLegacyAtom`, `legacy.RBACAtomWriter`, `LegacyAmbientScopes`, `LegacyFullScope`, the row rule and reach snapshot, the compatibility window and the deferred role cutover. Consistency pass: an every-atom or admin empty-list key maps to `core:admin` plus every other row's scopes, so the reach oracle shows no loss (approval included); `Manifest.ImportedRoleIDs`; HEAD's reset routes deleted in release R1, the native route at F4; stale 06 and 14 citations dropped; the §3.2 code fence closed.

Round 13 (2026-11-04), by round-13 ruling: R1/IMPL-3/IMPL-4 (F2 lands audit, the action table with `core.storage.*`, the fixed core catalog and the config sections), R8 (role rows keep quarantine; identity rows stop, 05-authn §11), R13/IMPL-8 (overlay), R16/XFILE-5 ("Route requirement", `AllOf` for `settings.manage` and `users.manage`; auth-off tightening (d)), R18/SEC-1 and R20/SEC-3, SEC-4, R22 SEC-7, SEC-12, SEC-13, XFILE-12 (d) (in-handler site list). Follow-up: the auth-off evaluation rule (`AllOf` drops `AuthRequired` members for the auth-disabled principal only) replaces tightening (d); `TestAuthOff_SettingsManageKeepsReach`. Follow-up: §7.1 `core.storage.*` adds `cutover_blocker_fix`, `cutover_blocker_delete` and `checkpoint_deleted` (05-storage's names).

Round 14 (2026-11-05): V15 (§6.1: every row's route requirement is `AllOf` all of its native scopes, always; `library.view` and `integrations.manage` rows fixed; `TestAtomTable_Golden` says "all of its row's native scopes"), V27 (§7.1: `core.integration.local_migrated`, M6 only), V23 (bare round-13 ruling citations in §3, §4.11, §6.1 and §12 replaced by their finding IDs; the round-13 history entry names its IDs as round-13 rulings).

Round 17 (2026-11-05): brief-sync gap fixes G18 (§3.4 `CompileInput` with `RolePatterns` resolved by `Service.Compile`; `Deps.Compile` takes it; §4.4 cross-reference; `TestCompile_ResolvesRolePatterns`), G19 (§3.1 `Requirement.Kind()` and `RequirementKind`; `TestRequirement_Kind`), G21 (§7.1 `core.outbox.*` row for 05-events §5E.11's audited routes).

Round 18 (2026-11-10, F4-09 review): §3.4 `authz.ForSystem` takes the catalog (`ForSystem(cat, perms)`) so a background actor's `Sensitive` and undeclared scopes are refused through `Has` (§4.4 rule 1 had no enforcement point for a module's `Sensitive` scope); `archtest.NoSystemPrincipalOutsideKernel` counts any non-test import of `authz` outside `auth`, the tasks executor and `kernel` (covering `authz.Compile`) and any `//go:linkname` naming it (§3.4, §12).
