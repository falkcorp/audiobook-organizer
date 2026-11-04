<!-- file: docs/specs/platform-core/06-app-sdk.md -->
<!-- version: 1.12.2 -->
<!-- guid: d581911c-8f90-48bb-a307-5cd6288a9695 -->
<!-- last-edited: 2026-11-04 -->

# 06 — App SDK: `module`, `kernel`, `app`, `coretest`

Evidence commit: `748fd787a`. Raw data: [`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md),
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §7, §9,
[`raw/06-tasks-ops.md`](../../plans/platform-core/raw/06-tasks-ops.md) §1.2.
Names, layers and rules R1–R8 are from [03-architecture](03-architecture.md); gates G1–G8 from
[02-decision-record](02-decision-record.md) §2.4. Types owned by other contracts (`storage`,
`tasks`, `httpapi`, `config`, …) are shown with the shape this file needs; where the owning
`05-*` file differs in a detail, **the owning file wins** and this file is amended.

## 6.1 Purpose & scope

This file specifies how an app is assembled from core plus modules:

* what a **module** implements (`module.Module`), what it declares statically (`module.Manifest`)
  and what it registers once its dependencies exist (`module.Registrar`, `module.Deps`);
* how the **kernel** validates, migrates, constructs, starts and stops modules, and how module
  enablement works (restart-bound, gate G1);
* the composition root **`app.New` / `(*App).Run`**;
* the **`coretest`** harness (gates G6, G7) and a complete worked **hello** module;
* the **retirement** of `internal/serviceregistry` (43 registrants), `internal/plugin`,
  `pkg/plugin/sdk`, and the classification of all 111 non-test `func init()`.

Out of scope here: the contracts of the individual services a module receives (each `05-*` file),
the UI side of a module (07), the *arr facade (08), ABO's module internals (09).

**Build scope (03 promotion rule, cross-cutting resolution 1).** `acquire`, `arrfacade`,
`integrations/indexer` and the download grab/monitor pipeline are **deferred-build**: their contracts
are specified, but no S0–S10 stage (09 §9.3) builds them. The `module` package therefore cannot import them in
v1. Every SDK member that depends on one of them is marked **deferred-build** below. It is added
additively, with no change to existing fields, in the build that creates the package (§6.4.9). The
members that are built land stage by stage with their packages (09 §9.3, 04 §8.1); §6.4.10 gives the
stage of every member, and the Go blocks below show the S9 end state (COR2-9).

## 6.2 Current state at HEAD

There is no module concept. Composition is spread over five registry mechanisms, all populated
through package-level state ([raw/07 §0](../../plans/platform-core/raw/07-plugin-registry-sdk.md)):

| Mechanism | Anchor | What it does | Why it cannot be the core SDK |
|---|---|---|---|
| `serviceregistry` global factory map | `internal/serviceregistry/registry.go:38` (`var registered = map[string]ServiceDef{}`), writer `Register` `:44`, test reset `:60` | 43 `ServiceDef`s registered from 24 `init()` bodies; `Container` per instance resolves a Kahn order, `Build`/`PostInit`/`Start`/`Stop` | global unsynchronised map (raw/07 §6.1); `Build` returns `any`, consumers call `Get[T]` (`container.go:245`) and `TryGet[T]` (`:270`) with string keys — 113 non-test call sites in 31 files (grep at HEAD); audiobook keys in the generic package (`keys.go:14-35`: `KeyAudiobook`, `KeyITunes`, …) |
| `IncludeGroup` selection | `container.go:73`; production `internal/server/server.go:814-829` | services join named groups (`core`, `ai`, `scheduler`, `plugins`, `activity`); the server includes groups | membership is a string in the registrant; a missing group silently excludes the service: `batchpoller` has no `Groups` (`internal/server/batch_poller_register.go:18-20`) and is therefore never built, so `TryGet` at `internal/server/registry_wire.go:422` can only miss |
| `Override` as production seam | `server.go:815` (`Override("store", store)`), `container.go:92` ("Test-only") | injects store/config/appversion | the documented test-only seam is the production root |
| runtime plugin registry | `internal/plugin/registry.go:19` (`globalRegistry`), `Register` `:33`, `InitAllScoped` `:114`; mounted at `internal/server/plugins_init.go:42` | webhook is the only plugin (`internal/plugins/webhook/plugin.go:33`) | process singleton; the router group is outside `protected` (raw/09 §8.3) |
| operation SDK | `pkg/plugin/sdk/plugin.go:19` (`Plugin{ID,Name,Version,Register(Registry)}`), `registration.go:12` | six `internal/plugins/*` bundles of op defs | type aliases onto `internal/operations/registry`; imports `internal/database` (raw/07 §3) |
| op catalogs | `internal/server/op_registrars.go:23` (`var opRegistrars []opRegistrar`), `internal/maintenance/registry.go:19-26` (panics on duplicate, no unregister) | 48 `addOpRegistrar` calls in 33 files, 34 `maintenance.Register` | init-by-import: `TestCatalogLinkedIntoBinary` (`internal/opscatalog/catalog_link_test.go:31`) exists only to detect a package whose `init()` is never linked |

The process itself is assembled by `server.NewServer` (`internal/server/server.go:653`, ~612
lines per raw/07 §7.3) and `Server.Start` (`internal/server/server_lifecycle.go:245`;
`container.Start` at `:279`). Startup steps are classified Fatal/Degraded by `startup.Table`
(`internal/startup/steps.go`, raw/12 §7) — a reusable idea this file keeps.

Count of non-test `func init()` at HEAD: **111** (re-run of `grep -rn '^func init()'` excluding
tests and `node_modules`), classified in raw/07 §5.1 and mapped to their replacement in §6.11.4.
Every literal count in this file (111 inits, 43 registrants, 113 call sites, 258 families, 66 legacy
migrations, …) is measured at `748fd787a` and is **informative**: a done-check that needs a count
generates it at its stage's base commit (A-16, ADV-11).

## 6.3 Design decisions

| # | Decision | Rationale and cost |
|---|---|---|
| A1 | A module is `Manifest()` (pure data, validated before storage opens) **plus** `Register(*Registrar)` (runs after migrations and config load, receives typed `Deps`). **Rule: anything the kernel needs before services exist, or while the module is disabled, lives in the `Manifest`; anything that needs a service lives in `Register`.** So permissions/roles, storage spec (keyspace, legacy claims, migrations, the declarative part of derived indexes), the legacy store implementations a module supplies to core subsystems (R4-1; a `legacy.Stores` value, R6-3), library roles, settings sections, its outbox consumer names, its task-ID ledger, the foreign task kinds its system actor may start, legacy HTTP prefixes and UI descriptor IDs are manifest fields. Routes, tasks, resources, topics (the one catalog, SIMP-5), subscriptions, outbox handlers, change sinks, adapters, health/diagnostics, backup artifacts, derived-index hooks, compat auth methods, hooks and workers are registrar calls: each closes over a service built in `Register` (COR2-15). | 03 §3.7 requires validation before listeners, migrations before services, and 05-config requires sections to be registered before the config store loads, while route handlers need migrated storage and loaded config. One `Register` cannot satisfy both. Outbox consumer names and the task-ID ledger are manifest data so that a disabled module's messages are retained (COR-12) and its persisted runs resolve as held (COR-13). Cost: two places to look; mitigated by the rule being mechanical. |
| A2 | **No generic DI container in core.** Core's own ~30 services are built by hand-written, typed code in `kernel/services` (a struct with one field per service, constructed in a fixed order). A module builds its internal services with plain Go constructors inside `Register`. | Evaluated below (§6.3.1). |
| A3 | `Deps` is a **struct of typed, module-scoped handles**, never `any`, never nil. Each handle's **type** carries the module identity, so scoping is enforced by the owning package and cannot be spoofed by the caller: `events.Scope` publishes only the module's declared topics, `library.Scope` carries the module's owner, `tasks.Client` stamps the actor from ctx, `secrets.Scope` reads only `modules.<id>.` names, `*metrics.Scope` prefixes the module's metric names (03 §3.6 `MetricPrefix`, §6.4.4). A shared pointer to a whole service never appears in `Deps`. | Replaces 113 `Get[T]` string lookups with compile-time types; a scope type is the only place R3/R8 can be enforced for a handle (COR-21). |
| A4 | Registrar methods **record problems instead of returning errors** (like `testing.T`); the kernel joins every problem of every module into one `*ValidationError`. | An operator sees all misconfigurations at once, not one per restart. |
| A5 | Module code runs only through the kernel: no `init()`, no package-level mutable state, background goroutines only via `Registrar.Worker`. A disabled module's `Register`, migrations, hooks and workers are **never invoked**. The one exception is its legacy store constructors (`Manifest.LegacyStores`, a `legacy.Stores` value, §6.4.11, §6.11.6): the data they serve belongs to core subsystems (users, roles, runs), so the kernel builds them whether the module is enabled or not, and a constructor may only capture its handles (no goroutine, no write, R4-1). | This is what makes G1 a structural property rather than a per-feature check. Enforced by `archtest.NoInit` + `archtest.TestNoGlobals` over module packages, and by `coretest.AssertModuleInert`. |
| A6 | Enablement is restart-bound (`core.modules.<id>.enabled`) and fail-closed. It is resolved in P1 from `config.Store.Boot` (operator layers plus the pre-storage boot record, 05-config §1.3.5), the read that exists before storage opens and before anything that depends on enablement (P3 section registration, P4 migrations, P6 `Register`) (§6.5.3, COR-14). There are no inter-module dependencies: `DependsOn` was dropped because R3 forbids module imports and no planned module needs another (SIMP-22). | Old decision 10 (02 §2.2); `ServeMux` routes cannot be unmounted. |
| A7 | Tests use `testing/synctest` for time; core has no `Clock` interface in `Deps`. | Go 1.25+ virtualises `time` inside a bubble; a clock abstraction would be a second, weaker mechanism. |
| A8 | **Construction is acyclic: services are built before `Register`, registrations are consumed after it.** P5 builds every core service a `Deps` handle wraps, including the realtime hub (so P6 can hand out lazy projection handlers, COR2-1). P6 runs each `Register`, which only records. P7 hands the recorded registrations to their services: task definitions to the executor, topics to the catalog, subscriptions to the bus, outbox handlers to the relay, legacy projections and redactors to the realtime hub, change sinks to the task writer, adapters to the integrations registry, derived-index hooks to the storage index loader, compat auth methods to `auth`, health checks to the health registry. No service is built inside `Register`, and no registration takes effect before every module has registered. | COR-1: the acquire service was needed inside `Register` but was to be built only when `Register` declared a media type. Under A8 the trigger is manifest data (`Manifest.Acquire`, deferred-build), so P5 can build the service and P6 only attaches typed hooks. |

### 6.3.1 Does core need a generic DI container?

| Question | `serviceregistry` today | Explicit construction (chosen) |
|---|---|---|
| Ordering | runtime Kahn sort over `Needs` strings (`graph.go:19`) | statement order; using a value before it is constructed does not compile |
| Missing dependency | `ErrUnknownService` at boot, or a silent skip when a group is wrong (`batchpoller`) | compile error |
| Type safety | `Build` returns `any`; `Get[T]` panics on mismatch (`container.go:245`) | static types |
| Optional services | typed-nil returns that every consumer must nil-check (raw/07 §6.10; e.g. `metricsstore` returns `(*database.PebbleMetricsStore)(nil)`, `internal/server/registry_wire.go:283-291`) | `if cfg.AI.Enabled { … }` at the one construction site; consumers receive a non-nil value or a documented no-op |
| Cycles | `PostInit` cross-wiring after `Build` (6 implementors, e.g. `internal/dedup/lifecycle.go:76`) | explicit setter after both are built, or replace the edge by an event subscription (most of today's `PostInit`s subscribe to the event bus) |
| `IncludeGroup` | string groups `core`/`ai`/`scheduler`/`plugins`/`activity`; `activity` gated on `DatabasePath` (`server.go:824-826`) | plain `if` in `books` `Register`; the group concept disappears |
| Test seam | `Override(name, instance)` | `coretest` options replace whole core services (`coretest.WithConfig`, `WithAdapters`, fakes) and a module's own constructors take interfaces |

A container earns its keep when the composition set is open-ended and assembled from discovered
registrants. That is exactly what this design removes (N4). With ≤ 30 core services in a fixed
order and modules that own their internals, a container would re-introduce `any` and string keys for
no remaining benefit. The kernel therefore has **a typed services struct, not a container**. 03 §3.3
already says so in its `kernel/...` row, and its §3.7 diagram points at §6.5.2 for the start order.

## 6.4 Go API

All packages below are in module `github.com/falkcorp/mediacore`. `module` is SDK (R4 L7); `kernel`
is public-but-unstable (L8; apps must not import it, R2); `app` (L9) and `coretest` are SDK. `legacy`
(§6.4.11) holds every identifier that exists only to serve an app's pre-core stores; like `kernel` it
is excluded from the API-stability gate, but apps may import it (R6-3).

### 6.4.1 `module` — identity and the `Module` interface

```go
package module

import (
	"context"
	"errors"
	"log/slog"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/legacy" // Manifest.LegacyStores only (§6.4.11, R6-3)
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/ui"
	// The block is the S9 end state; each import arrives with the stage that
	// lands its package (§6.4.10). Deferred-build (§6.4.9): acquire,
	// arrfacade, integrations/indexer.
)

// ID is a module identifier: ^[a-z][a-z0-9]{1,15}$ and not in ReservedIDs().
// ID.Valid reports both.
type ID string

// Valid reports whether id matches the naming rule and is not reserved.
func (id ID) Valid() bool

// ReservedIDs returns the union of httpapi.ReservedSegments (server paths),
// ui.ReservedSegments (shell routes, 07 §7.4.3; a dependency-free leaf of
// package ui that lands at S2 with module, FE3-8) and rbac.ReservedNamespaces
// ("core", "custom") (03 §3.6). It is static, so P1 can evaluate it, and a
// test asserts the union equals 03's row from S2 on. Two app-specific inputs
// are added by the kernel in P1, both known before any MountLegacy call
// (COR2-19, FE2-11):
//   - the first path segment of every manifest's LegacyPrefixes (ABO: auth,
//     socket.io, ping, status, logout, public, login, invite): RESERVED IDs
//     ONLY. Whether a segment is also NonSPA is decided by the mount, not by
//     the prefix (FE3-1, R3-5): NonSPA = LegacyMount.TopSegments minus
//     LegacyMount.SPAShared (03 §3.6, 05-http-api §3.5, §8.2) (ABO SPAShared:
//     login, whose ABS route is POST /login while GET /login is the SPA page;
//     invite, whose legacy GET /invite/{token} 302 shares the segment with
//     the SPA page, SEC3-16). The P1/P7 split of 03 §3.6: P1 validates
//     module IDs against the static union, ReservedAPINamespaces and these
//     LegacyPrefixes first segments (all manifest data); P7, once mounts are
//     recorded, re-checks module IDs against every mount's TopSegments and
//     SPAShared and checks that each such entry is a first segment of a
//     declared LegacyPrefixes entry, still before listeners open;
//   - app.Options.ReservedAPINamespaces: second segments under /api (ABO: the
//     ABS unversioned /api/<seg> names); reserved IDs only, never NonSPA.
// A fresh slice per call (R5).
func ReservedIDs() []string

// Module is what an app hands to app.New. Implementations are plain values
// constructed by the app's main (e.g. books.Module(), hello.New(); 03 §3.7
// spells ABO's books.Module(), MOD5-11); they must not
// do work in their constructor beyond capturing options.
type Module interface {
	// Manifest returns the module's static declaration. It must be pure,
	// cheap and deterministic: the kernel and tests may call it any number of
	// times, from any goroutine, before and after Register.
	Manifest() Manifest

	// Register is called exactly once per boot, for an enabled module only,
	// after core and module migrations have run, config is loaded and every
	// core service exists (A8). It constructs the module's services from
	// r.Deps() and records routes, tasks, subscriptions, outbox handlers,
	// adapters, health checks, hooks and workers. Recording has no effect until
	// every module has registered (P7). r.Context() carries app.New's ctx.
	// Register must be side-effect-free outside the handles in Deps: no file,
	// network or subprocess I/O and no goroutine (work of that kind belongs in
	// a Hook or Worker), so app.Describe can run it on in-memory storage
	// (COR2-13). A returned error aborts boot (fail-closed).
	Register(r *Registrar) error
}
```

### 6.4.2 `module.Manifest`

```go
// Manifest is everything the kernel validates before any storage is opened,
// and everything that must stay known while the module is disabled.
type Manifest struct {
	ID          ID
	Version     string // semver, e.g. "1.12.0"; reported by GET /api/core/v1/modules
	DisplayName string // human name, e.g. "Audiobooks"

	// MinCoreVersion is the lowest core version this module needs (semver).
	// P1 compares it with version.Version, the hand-maintained constant of the
	// public L0 package github.com/falkcorp/mediacore/version (MOD-12). It is
	// never derived from debug.ReadBuildInfo: under the in-repo replace the
	// module reports v0.0.0-00010101000000-000000000000 and would always fail.
	// A CI check keeps version.Version equal to the newest core checkpoint
	// (pre-split) or to the tag being released (post-split). version.Version
	// starts at "0.1.0" when the package lands (S1), so hello's S2 skeleton
	// ("0.1.0") passes P1, and check_core_version.py also asserts that every
	// in-tree MinCoreVersion (hello, books) is <= version.Version (MOD3-19).
	// Empty = no check.
	MinCoreVersion string

	// DefaultEnabled is the value of core.modules.<id>.enabled when unset.
	DefaultEnabled bool

	// Requires lists integration kinds the module needs at runtime.
	// Required=false entries only drive the settings UI and health text.
	Requires []CapabilityRequirement

	// RBAC declarations, types owned by 05-rbac §3.2: permissions in this
	// module's namespace (each with its DefaultGrants into core seed roles
	// "editor"/"viewer" or the module's own roles), the module's own seed roles
	// ("<ID>.<name>", 05-rbac §3.3) and legacy permission-string aliases in
	// the module's OWN namespace only (05-rbac §4.9: a module never aliases a
	// string to core:*). P1 checks only their namespaces; the kernel feeds them
	// to rbac's Declarer in P5, after rbac.New has its role store (COR2-7,
	// R4-1). Aliases that target core:* (ABO's 13 legacy atoms with their
	// Reach) are app-level, app.Options.LegacyPermissionAliases (§6.4.5,
	// COR5-5). During the extraction a seed role's stored row is its seed
	// (HEAD's overwrite); a restricted addition waits in PendingSeed until an
	// admin accepts it with AcceptSeed, which is allowed; the operator-diff
	// merge (Added/Removed, Tombstone, ResetRole) is deferred-build with the
	// role cutover (05-rbac §4.8, SIMP4-6).
	Permissions   []rbac.PermissionSpec
	Roles         []rbac.RoleSeed
	LegacyAliases []rbac.LegacyAlias

	// LegacyRoleIDs are the un-namespaced legacy role IDs this module's seed
	// roles may keep as their RoleSeed.Name (books: "fp-worker", HEAD's
	// worker role, whose ID older builds and stored user rows name; 05-rbac
	// §3.3, §6.2). Every other seed role is "<ID>.<name>". P1 refuses an ID
	// listed by two manifests or equal to a core seed role (ProblemLegacyRole);
	// the kernel hands the list to rbac's Declarer with Roles (COR5-5). S4.
	LegacyRoleIDs []rbac.RoleName

	// TaskLedger lists every task kind this module has ever registered,
	// including FormerKinds ("<ID>.<name>"). It is readable while the module
	// is disabled, so 05-tasks classifies the module's persisted runs and
	// op-ID ledger lines as held instead of failing boot (5T.3.11, COR-13).
	// A registered kind missing from it is a problem (ProblemTaskLedger).
	TaskLedger []tasks.Kind

	// TaskStarts is the reviewed list of FOREIGN kinds (core.* or another
	// module's) that the module's system actor may start outside a request,
	// from a Worker, Hook or subscriber (SEC2-15). The system actor holds
	// exactly the union of Perms.Start of the module's own kinds plus the
	// Perms.Start of these kinds; core:admin and AuthRequired scopes are never
	// included (05-tasks 5T.3.10). P7 refuses a kind that is neither registered
	// (core or an enabled module) nor in ANY manifest's TaskLedger, disabled
	// modules included (ProblemTaskStartUnknown; 05-tasks 5T.3.1's rule), so
	// disabling module B never fails the boot of a module A that lists one of
	// B's kinds (G1, COR3-10). Starting a disabled module's kind is refused at
	// Start with tasks' "unknown kind" (O3).
	TaskStarts []tasks.Kind

	// OutboxConsumers names every outbox consumer the module may register
	// ("<ID>.<name>"). They are known while the module is disabled, so the
	// relay keeps retaining messages for them (05-events, COR-12); an
	// OutboxHandler whose consumer name is not listed here is a problem.
	OutboxConsumers []string

	// Storage is the module's storage declaration (05-storage §5S.3): its
	// m/<ID>/ keyspace, legacy claims (books: ABO's 258 families, held by
	// books in place for the whole extraction), versioned migrations (run
	// before Register, under the boot's single pre-migration checkpoint, G2;
	// on a fresh install a Seed step and every later step of the same owner
	// run too, §6.5.1 P4a, COR5-8, COR6-4), and the
	// DECLARATIVE part of each derived index (name,
	// version, Blocking), so the claim ledger and readiness gating know the
	// index while the module is disabled. The Snapshot/Load closures and the
	// rebuild task are registrar data (Registrar.DerivedIndex), because they
	// close over services built in Register (COR2-15). It declares no storage
	// grants: no core subsystem reads a module's rows during the extraction;
	// grants belong to the deferred per-family cutover (R4-1, 05-storage).
	Storage storage.Spec

	// LegacyStores supplies this module's implementations of core store
	// interfaces over its OWN legacy claims (R4-1, §6.11.6): books passes
	// ABO's code over ABO's keys and row formats, so core holds no audiobook
	// key name or row format (03 R1). Its type, the constructor set, lives in
	// package legacy, outside the API promise (§6.4.11, R6-3): the field stays
	// in v1, and a family's cutover deletes that family's member of
	// legacy.Stores in a minor, never a major (03 §3.3 "Legacy-only
	// identifiers"). The zero value means every subsystem uses only its native
	// implementation over c/<subsystem>/, which is what hello, coretest and
	// every new app get (SIMP4-3). Each member may be set by at most one
	// manifest (ProblemLegacyStore). The kernel calls the constructors at P4c
	// with the module's own handles, enabled or not (A5). Each member lands
	// with its subsystem (§6.4.10).
	LegacyStores legacy.Stores

	// DataMigrations are the module's large, resumable rewrites
	// (05-storage §5S.3.6). They live outside storage.Spec because
	// storage/datamigrate (L4) imports tasks and storage (L1) cannot. During
	// the extraction every one is BootBlocking: it runs in process at P4b
	// through tasks.ForEach with persisted shard cursors, with its dry run
	// (05-storage). The Online mode (a run of the kernel-synthesised module
	// kind "<ID>.datamigrate.<name>" after Run step 5, with its read adapters,
	// Writes/RewritesInPlace and conditional-write steps, COR4-5, COR4-6) has
	// no S0–S10 consumer and is deferred-build: 05-storage §5S.3.14 lists its
	// members, and it lands additively with its first consumer (SIMP5-7).
	DataMigrations []datamigrate.DataMigration

	// Library declares the library-root roles the module needs (05-library).
	Library []library.RoleRequirement

	// Settings are the typed config sections the module owns, wrapped with
	// Settings(section). Paths must be "modules.<ID>" or below. They are
	// registered with config before the config store loads (05-config §1.3.2).
	Settings []SettingsSection

	// UI lists the descriptor IDs the module's client bundle may contribute.
	// The server reports them in /api/core/v1/modules; the shell refuses
	// client entries whose IDs are not listed (G3, 07 §7.5).
	UI UIDescriptor

	// LegacyPrefixes are un-namespaced HTTP subtrees ("/api/v1/", "/public/")
	// or exact paths ("/ping", "/login") the module occupies (09); each becomes
	// a claim, and its first segment is a reserved module ID known in P1
	// (§6.4.1, FE2-11). A prefix does NOT make its segment NonSPA: that comes
	// only from the mount's LegacyMount.TopSegments, and a segment the mount
	// shares with an SPA page is listed in LegacyMount.SPAShared instead
	// (FE3-1, R3-5). httpapi.Router.Build refuses any two owners whose subtree
	// claims overlap, which also covers facade vs legacy (05-http-api §4.9,
	// narrowed by SIMP-22: no claim kinds, no YieldsTo). Only paths listed
	// here may be passed to MountLegacy. "/" is not a subtree claim: it is the
	// SPA fallback, owned either by ui.Handler or by exactly one LegacyMount
	// with ServesSPA (§6.4.3, FE-1).
	LegacyPrefixes []string

	// PublicRoutes names the native routes of this module (httpapi.Route.Name)
	// that may use Perm: rbac.Public; httpapi.Router.Build refuses rbac.Public
	// on any other native route (ErrPublicNotAllowed, 05-http-api §3), so the
	// public set is a reviewed manifest list, not a per-route say-so (COR3-3).
	// hello's S2 skeleton lists "getGreeting" until S4 gives the route
	// rbac.Need(PermRead).
	PublicRoutes []string

	// Acquire (deferred-build, §6.4.9) declares that the module contributes
	// a media type to the acquire engine. In P5 the kernel builds the one
	// acquire service when any enabled manifest sets it; in P6
	// acquire.Register[T] only attaches the module's typed hooks (COR-1).
	// Acquire *AcquireDecl
}

// CapabilityRequirement names an integration kind the module uses.
type CapabilityRequirement struct {
	Kind     integrations.Kind // "download", "metadata", "ai", "notify"; "indexer" once built (05-metadata M1)
	Required bool              // true: boot fails when no instance of this kind is configured
	Why      string            // shown in settings and in the boot error
}

// UIDescriptor lists the IDs of client-side contributions (07 §7.4.2).
// Every slot a module can fill in the shell has a list here, so G3 can drop
// an undeclared item of any kind (FE-16).
type UIDescriptor struct {
	Pages          []string // route IDs, e.g. "library", "book"
	Nav            []string // nav entry IDs
	SettingsPanels []string // settings panel IDs
	Widgets        []string // optional home/system/diagnostics widgets
	Banners        []string // layout banners rendered by the shell's main layout
	TopBarItems    []string // items in the shell's top bar
	UserPage       []string // tab and row-action IDs on the shell Users page
	ActivityPanes  []string // panes on the shell Activity page (05-logging §5)
	SetupSteps     []string // first-run wizard steps (with the module's isRequired predicate)

	// LegacyRedirects are the module's legacy page URLs and their shell paths
	// (books: ABS_REDIRECTS plus the absorbed pages, 07 §7.11). It is the ONE
	// list: manifest.gen.ts exports it and the shell's client redirects are
	// built from it (FE3-17); the kernel registers one ui_route compat entry
	// per row at P7, and ui.Handler records a full page load that matches one
	// (FE4-5). Books fills it in the URL-move release, which is the shell
	// adoption release (07 §7.19 steps 4 and 5 ship together, FE4-8, FE5-13)
	// (FE5-11). Lands with package ui at S9.
	LegacyRedirects []UIRedirect
}

// UIRedirect maps one legacy page path (a ServeMux-style pattern, e.g.
// "/library/{id}") to its shell path ("/books/library/{id}").
type UIRedirect struct{ From, To string }

// SettingsSection wraps a config.Section[T] so a Manifest can list sections
// of different T. The kernel registers each before config loads.
type SettingsSection struct{ /* unexported: path + register func(config.Registry) (any, error) */ }

// Settings wraps a declared section for Manifest.Settings.
func Settings[T any](s config.Section[T]) SettingsSection

// Setting returns the live handle of a section this module declared in its
// Manifest. Calling it for an undeclared section records ProblemSettingsUndeclared
// and returns a handle holding the section defaults (never nil).
func Setting[T any](r *Registrar, s config.Section[T]) *config.Handle[T]
```

Default grants are expressed the 05-rbac way: each `rbac.PermissionSpec` lists the seed roles that
receive it (`DefaultGrants: []rbac.RoleName{rbac.RoleViewer}`), and a module may seed its own roles
(`books.editor`). A module can never grant `core:*` (05-rbac §4.9). There is no separate `Grant` type.
A permission a facade key or automation key must exercise is never `Sensitive`, because a
non-interactive principal never satisfies a `Sensitive` scope, `core:admin` included (05-rbac, SEC-23).

### 6.4.3 `module.Registrar`

```go
// Registrar is handed to Module.Register. It is not goroutine-safe and is
// closed when Register returns; calls after that record ProblemRegistrarClosed.
// Methods record problems rather than return errors (A4); Err reports them.
// Nothing recorded here takes effect before P7 (A8).
type Registrar struct{ /* unexported */ }

// Context is derived from app.New's ctx: cancelled when app.New is cancelled
// or returns. Register uses it for its own config/keyspace reads (COR-24).
func (r *Registrar) Context() context.Context

// Deps returns the module-scoped dependency handles. Every field is non-nil.
func (r *Registrar) Deps() Deps

// HTTP returns the module's route group rooted at /api/<ID>/v1, with the full
// core chain applied (05-http-api §3.1). Register routes with
// httpapi.Handle(r.HTTP(), httpapi.Route{...}, fn). Every route needs Perm (R6).
func (r *Registrar) HTTP() *httpapi.Group

// MountLegacy mounts an http.Handler on prefixes declared in
// Manifest.LegacyPrefixes, behind core's perimeter stages (05-http-api §8.2).
// m.Owner is set by the registrar. For books this is the gin engine, mounted
// permanently (cross-cutting resolution 2: there is no gin-retirement stage).
// m.ServesSPA = true lets the mount own the "/" SPA fallback; the kernel then
// does not mount ui.Handler. At most one mount per app may set it, and
// app.Options.UI must be the zero Bundle while one does (FE-1).
// MountLegacy cannot skip core authentication on its own say-so (SEC2-17):
//   - m.Auth = httpapi.LegacyAuthGin (stages 12-13 off for the mount) is
//     accepted only for the module named by app.Options.LegacyAuthGinUntil
//     (ABO: books, from S2 until the S4 PR clears the field; an archtest
//     fails if it is set after S4); any other module records
//     ProblemLegacyAuth;
//   - a route with OwnAuth = true is accepted only when it names a compat
//     method registered with AuthMethod; the kernel records every OwnAuth
//     route in a golden (TestOwnAuthRoutesGolden, 05-http-api §8.2). Both
//     MountLegacy and AuthMethod exist from S2, so this admission holds from
//     S2 for ABO's ABS routes (SEC3-14).
// m.BuildID (05-http-api §8.2) is the UI build ID embedded in the mount's
// own SPA (ABO: the <meta name="mediacore-build"> of its embedded web/dist
// index.html); httpapi stage 5 writes it as X-App-Build until S9. It is the
// only UI-build-ID source before S9: there is no linked value and no boot
// comparison (FE4-1). It is never the binary's identity (app.BinaryID,
// §6.4.5, R5-7).
// Metrics (R10-3, R11-1; 05-http-api §4.2 stage 2 owns the rule): books'
// gin engine keeps otelgin, configured as at HEAD, so the mount is the one
// recorder of every request that reaches gin, with HEAD's http_route
// (gin's c.FullPath(); none for NoRoute and SPA loads), server_address,
// scope and buckets; core's stage 2 never records such a request, and
// nothing carries a route out of gin. The mount's wrapper clears r.Pattern
// before it calls gin (otelgin would record it as http_route on a gin
// miss); the SEC2-22 post-match check reads httpapi.MatchedPattern(ctx).
// A request a core stage answers before the mount (401, 413, 429, a CORS
// preflight on a legacy path) is recorded by stage 2 with no http_route:
// 09 §9.2.1 row 12, with its release note. Stage 2 records into the same
// OTel instruments as otelgin (Options.HTTPMetricsScope below), so a label
// set both produce is one series and no duplicate family can reach a
// scrape. HEAD's WithoutScopeInfo stays.
func (r *Registrar) MountLegacy(m httpapi.LegacyMount)

// AuthMethod registers a compat authentication method of this module, named
// "<ID>.<name>" (books: books.abs, 05-authn §3.3 AddMethod). The kernel
// refuses (ProblemAuthMethod) any spec that is Interactive, sets
// MayChangeCredentials, or whose authenticator implements OwnerVerifier: only
// app.Options.Authenticators may carry those (SEC2-16). From S2 to S3 it only
// RECORDS the declaration (auth.MethodSpec is a leaf type that lands at S2
// beside rbac's), so P7's OwnAuth admission works before the auth service
// exists; from S4 P7 also hands it to auth (SEC3-14, COR3-3).
func (r *Registrar) AuthMethod(spec auth.MethodSpec)

// Tasks registers task definitions; kinds must be "<ID>.<name>". Schedules are
// part of each definition (tasks.Common.Schedule, bound with
// tasks.ScheduleFromConfig to a declared key in the module's own namespace,
// modules.<ID>.<subsystem>.schedules.<name>, no hyphens; the one
// schedule-key rule <owner>.<subsystem>.schedules.<name>, R2-7, 05-tasks
// 5T.3.8); there is
// no second timer API.
func (r *Registrar) Tasks(defs ...tasks.Definition)

// Resources declares the resource kinds this module owns (G8, 05-tasks 5T.3.4).
func (r *Registrar) Resources(kinds ...string)

// Lanes declares remote work lanes (05-tasks 5T.3.9).
func (r *Registrar) Lanes(l ...tasks.Lane)

// ChangeSink registers the module's change journal (books: the operation-change
// journal and undo); tasks.Writer records every gated write's
// changes into it (05-tasks 5T.3.5). At most one per module.
func (r *Registrar) ChangeSink(s tasks.ChangeSink)

// Topics declares the module's entries in the ONE topic catalog (05-events
// §5E.3.1, SIMP-5): every bus or durable topic it publishes, with its schema,
// its read Permission, optional Visible filter, and Durable / Notify /
// Realtime metadata. Build entries with events.NewTopic[T](name).Spec(...)
// or the outbox topic's Spec. Names are "<ID>.<noun>.<verb-past>.v<N>".
// notify and realtime read this catalog; there is no OutboxTopics,
// NotificationTopic or realtime-only declaration.
func (r *Registrar) Topics(t ...events.TopicSpec)

// Subscribe declares bus subscriptions built with events.On (own or core.*
// topics, typed) or events.OnRaw (another module's topic by name, R3). They
// are frozen when Register returns; delivery begins at Bus.Open (§6.5.2,
// COR-8).
func (r *Registrar) Subscribe(s ...events.Subscription)

// OutboxHandler registers outbox consumers built with outbox.Consume,
// ConsumeTx, ConsumeIntoTask, or the raw forms ConsumeRaw/ConsumeIntoTaskRaw
// that consume ANOTHER module's durable topic by name without importing it
// (R3, COR-11; payload validated against the catalog schema). Each consumer
// name must be listed in Manifest.OutboxConsumers; each topic must be a
// Durable catalog entry. Delivery begins when the relay starts (§6.5.2).
func (r *Registrar) OutboxHandler(h ...outbox.Handler)

// Realtime returns the module's realtime registrar (05-realtime §3). Catalog
// topics with Realtime metadata are streamed without any call here, and
// redaction is not registered here either: it is the topic's catalog
// TopicSpec.Redact, declared by the topic's owner, core topics included
// (05-events, COR2-2). The registrar has one method:
//   - Projection(p realtime.LegacyProjection) (http.Handler, error) records a
//     legacy framing and returns a lazy handler bound to the hub, which P5 has
//     built; the hub's catalog subscriptions are attached in P7, and the
//     handler answers 503 + Retry-After until the router is built (P8).
//     Errors (duplicate name, unknown topic, topic without Realtime) abort
//     boot. Books serves it from its gin manifest at
//     /api/events, /api/v1/operations/events and /api/v3/ops/events (OwnAuth
//     false, Perm rbac.AnyAuthenticated as for the native stream; per-topic
//     permissions are applied per event inside the hub, 05-realtime §4.4).
//     A projection is a legacy manifest route, not an httpapi.Alias
//     (COR2-1, SEC2-25).
// It never declares a topic name or permission (SIMP-5).
func (r *Registrar) Realtime() realtime.Registrar

// Adapter registers an integration adapter the module ships (05-metadata M1.3);
// its ID prefix must equal the module ID. Core adapters are enabled in
// app.Options.Adapters instead.
func (r *Registrar) Adapter(d integrations.Descriptor)

// AI declarations, signatures owned by 05-ai.
func (r *Registrar) AICapability(spec ai.CapabilitySpec) ai.Capability // zero handle on error
// There is no AIBatchHandler: a module applies AI batch results in its own
// task kind "<ID>.ai.batch-apply.<name>" (05-ai's spelling), built with
// ai.BatchApplyTask(cap, h, effects) and registered with Tasks;
// core.ai.batch-poll starts it through the tasks.Stager the kernel gives core
// services (Stager.StartIn, 05-tasks; never the Executor) instead of calling
// module code inline, so the writes run under the module's Effects (COR2-11,
// COR3-8).

// Logging contributions (05-logging §3, §5). LogPathRules adds the module's
// token-bearing path shapes to the kernel's Redactor, built before any
// listener (the invite shape is a core default and needs no rule).
// LogOtherUserData registers the predicate that classifies a system-journal
// record, legacy syslog row or native record alike, as naming another user's
// data; the journal query and the live tail return such a record only to a
// principal that also holds core:users.view and blank its user_id otherwise
// (HEAD's activity.NamesOthersUserLog gate). At most one predicate per module;
// type owned by 05-logging. Both only record; consumed in P7.
func (r *Registrar) LogPathRules(p ...log.PathRule)
func (r *Registrar) LogOtherUserData(pred log.OtherUserPredicate)

// Health, startup steps and diagnostics (05-health). Health lands at S5 with
// the readiness core of health that task dispatch waits on (COR4-8);
// StartupSteps and Diagnostics land at S8 with the rest of health (§6.4.10). ABO's
// internal/startup stays in ABO (it does not move to kernel). Its serve runs
// HEAD's seven steps (cmd/root.go:274-391 @748fd787a) through startup.Steps
// before app.New at S2 only; each then has one successor (R4-6, COR4-1,
// MOD4-17):
//   - initializeStore: the kernel's P2 from S2b (one Pebble open);
//   - InitOTEL: app.InstallProcessDefaults from S2 (05-logging);
//   - EnsureSecureStateDir: app.ResolveStateDir from S2 (§6.4.5, OPS4-8);
//   - guardAgainstKeyRegeneration, initEncryption, loadConfigFromDB (with
//     syncConfigFromEnv) and config.Validate: books' legacy config store
//     from S2b, whose Open (legacy.ConfigOpener) the kernel calls first in
//     P4d step 1 (§6.5.1; a capability the kernel drives, COR7-3); a
//     failure fails boot exactly where HEAD's step is Fatal, so AppConfig
//     never runs on defaults and no defaults are written over the stored
//     blob. At S3 the AppConfig façade (05-config §1.12.1) replaces the
//     settings load; the key guard and the encryption init stay in the
//     store's Open until the settings family's cutover (05-config §1.12.2),
//     and secrets.Open, which runs after Open, covers core's own records: it
//     records the key Open loaded as k0-legacy (05-config §2.4) (COR5-2).
//     The store keeps reading and writing the blob in its own format.
// So cmd's last use of internal/startup goes at S2b; the package is deleted
// when internal/server's own steps no longer need it (04 §8.3). Degraded
// steps of the owning services become health checks at S8.
func (r *Registrar) Health(c ...health.Check)
func (r *Registrar) StartupSteps(s ...health.StartupStep)
func (r *Registrar) Diagnostics(c health.Contributor)

// Backup declares module-owned artifacts outside storage (05-backup). They are
// registrar calls, not manifest data, because their Snapshot/Restore hooks
// close over services built in Register.
func (r *Registrar) Backup(a ...backup.Artifact)

// DerivedIndex supplies the hooks of a derived index whose declarative part
// is in Manifest.Storage (COR2-15). name is module-relative ("hnsw", not
// "books.hnsw"). h carries Snapshot and Load only (storage.DerivedHooks;
// storage, L1, cannot name a tasks type). build is the rebuild task, an
// ordinary tasks.Definition built with datamigrate.DerivedBuildTask (package
// storage/datamigrate, L4; 05-storage §5S.3.6) or with tasks.Task: its kind must be
// "<ID>.derived.<name>.build" and, like every definition, it sets Title,
// Effects (ReadOnly over the named module resources it reads), Perms.Start,
// Resume, Parallelism and a Timeout sized for a library-scale rebuild (24 h
// recommended, as for Batch); P7 refuses a wrong kind name or a missing field
// like any other definition (COR3-7). The version/generation check, Load and
// the c/storage/derived/<module>/<name> record run at Run start step (3a), after the
// module's hooks have started and before Bus.Open, never inside app.New, so
// Describe never calls them and Load can use objects a start hook builds
// (§6.5.2). A declared index without hooks, or hooks for an undeclared index,
// records ProblemDerivedIndex.
func (r *Registrar) DerivedIndex(name string, h storage.DerivedHooks, build tasks.Definition)

// Hook registers a Start/Stop pair. Hooks of one module run in registration
// order on start and reverse order on stop.
func (r *Registrar) Hook(h Hook)

// Worker registers a supervised long-running goroutine, started after all
// hooks have started and stopped (ctx cancelled) before any hook stops.
func (r *Registrar) Worker(w Worker)

// Err returns the problems recorded so far, joined; nil if none.
func (r *Registrar) Err() error

// Deferred-build registrar methods (§6.4.9), added with their packages:
//   func (r *Registrar) ArrFacade(p arrfacade.Provider)   // 08; at most one per module
//   func (r *Registrar) DownloadWatch(w ...download.Watch) // 05-download phase 2 monitor pipeline

type Hook struct {
	Name  string                          // "<ID>.<name>", unique
	Start func(ctx context.Context) error // may be nil
	Stop  func(ctx context.Context) error // may be nil; ctx carries the stop deadline

	// ExtendsStart marks a Start that makes measured progress and calls
	// Deps.ExtendStart per batch: core.lifecycle.start_timeout then bounds
	// the time between two calls, not the whole Start (§6.5.2). Books'
	// post-reset hook sets it, because wiping a large library can exceed
	// 60 s (§6.5.1 P0a; COR8-4, R8-1).
	ExtendsStart bool
}

type WorkerPolicy int

const (
	// DegradeOnExit: an early return or panic marks health "<ID>.<worker>"
	// unhealthy and is logged; the app keeps serving.
	DegradeOnExit WorkerPolicy = iota + 1
	// StopAppOnExit: an early return or panic begins a graceful shutdown and
	// App.Run returns the worker's error. Only for workers whose loss would
	// make continued operation unsafe for data.
	StopAppOnExit
)

type Worker struct {
	Name   string
	Run    func(ctx context.Context) error // must return promptly after ctx is done
	Policy WorkerPolicy                    // zero value refused: the module must say which
}
```

### 6.4.4 `module.Deps`

```go
// Deps are the handles a module may use. Every handle is scoped to the module
// by its TYPE (A3) and safe for concurrent use. None is ever nil; a capability
// with no configured instance returns integrations.ErrNotConfigured (or the
// kind package's equivalent) from its lookups instead.
type Deps struct {
	Module ID

	Log     *slog.Logger   // child of the app logger with attr module=<ID> (05-logging)
	Metrics *metrics.Scope // module series under app.Options.MetricPrefix on the App's registry, never the global one (05-metrics, 03 §3.6); until S8 that registry's exporter registers on app.Options.PromRegisterer (MOD4-3)

	// ExtendStart is health.Supervisor.ExtendStart as a plain func: call it right
	// before a startup step that makes real progress (a store open, a migration
	// step, a search-index open); never from a timer; a no-op without
	// NOTIFY_SOCKET (05-health §5H.3.4). Books' search code that stays in ABO
	// through S2–S7 receives it as an injected func(step string) that
	// books.Module() wires from Deps.ExtendStart: below layer 6 an ABO package
	// never imports module or app (MOD4-18, 03 §3.5.1). At S2 ABO's serve
	// opens the store and runs books' migrations BEFORE app.New (HEAD's order,
	// §6.5.1 "Stages"), where no Deps exists yet; that code calls
	// app.ExtendStart, the same stateless notify write (§6.4.5). From S2b core
	// storage extends the start timeout itself for the steps it runs.
	ExtendStart func(step string)

	Storage StorageHandles // Keyspace m/<ID>/, Legacy claims, Indexes, Aux opener, Derived generations, Maintenance (S2b to the S4 r2 PR) (05-storage)

	Tasks  tasks.Client  // start/observe runs of any kind by Kind string (R3); actor stamped by the kernel (05-tasks 5T.3.10)
	Events events.Scope  // publish only this module's catalog topics (subscriptions: Registrar.Subscribe) (05-events)
	Outbox outbox.Writer // stage durable messages into the module's own batches (05-events)
	Notify notify.Publisher // built by notify.Service.For(module); Publish(ctx, b, n) stages a notification in the caller's batch, only for the module's own catalog topics with Notify metadata (05-notify §3, COR2-3)

	Secrets secrets.Scope // names under "modules.<ID>."; values never logged (05-config part 2)
	Library library.Scope // roots, FS(rootID), Transfer/TransferIn with the module's owner implied (05-library)

	Auth      auth.Identities // read view: user lookup by ID/username, principal from ctx (05-authn §3.1)
	AuthAdmin auth.Admin      // actor-checked identity participants: StageUserMerge (05-authn §3.1, 05-storage DATA-18)
	Audit audit.Logger    // Record/RecordIn/Emit; actor from ctx (05-rbac §3.5)

	Health health.Reporters // push status for the module's own declared checks only (05-health)

	HTTP         *providerhttp.Registry // instance-scoped outbound clients (05-metadata M2)
	Integrations *integrations.Registry // the ONE integration-instance store (SIMP-4): configured
	                                    // instances of every kind; typed access via
	                                    // integrations.Lease[T] and the kind packages (05-metadata M1)
	Downloads *download.Pool  // client pool: Items, Files, Relocate (05-download; ABO's path update); Remove is phase 2 (SIMP2-17)
	AI        *ai.Router      // provider routing for the module's AI capabilities (05-ai)
	Backup    backup.Ensurer  // Ensure only: "archive unless one verified within maxAge" (05-backup)

	// Deferred-build (§6.4.9):
	//   Acquire  *acquire.Service // 10; built in P5 when an enabled manifest sets Acquire
	//   Indexers *indexer.Pool    // 05-indexer
}

// StorageHandles is what Deps.Storage carries. It lives in module (L7), not
// in storage, because it names storage/index, which imports storage (05-storage
// §5S.3.4, COR-2). Core subsystems receive the same values from the kernel
// over their own c/<subsystem>/ keyspaces; no core subsystem and no module
// ever receives another owner's rows (R3, R8). Where a core subsystem serves
// a module's legacy data, it calls the module's own store implementation
// (Manifest.LegacyStores, built over the module's own handles at P4c,
// §6.4.11, §6.11.6).
type StorageHandles struct {
	Keyspace storage.Keyspace           // m/<ID>/
	Legacy   storage.LegacyRange        // zero value unless the manifest declared claims
	Indexes  index.Opener               // full-text indexes the module declared (05-storage §5S.3.8)
	Aux      storage.AuxOpener          // auxiliary DBs under <db parent>/aux/<ID>/<name> (05-storage §5S.3.10)
	Derived  map[string]storage.Derived // generation handle per declared DerivedIndex (§5S.3.9)

	// Maintenance is the narrow kernel-built handle of books' pre-core reset
	// routes (legacy.Maintenance, §6.4.11; R7-2, COR7-1, R8-1): the staged
	// store or factory reset its two gin handlers call from S2b until S4 r2.
	// nil unless the manifest declares legacy claims, and nil in an offline
	// session (§6.4.6, SIMP8-9). S2b; deleted in the S4 r2 PR with those
	// handlers, when both legacy paths become Aliases of the core route
	// (SIMP8-4).
	Maintenance legacy.Maintenance
}

// ErrStopped is returned by any handle used after the app stopped.
var ErrStopped = errors.New("module: app stopped")
```

There is no `RBAC` field. In-handler checks use the context functions `rbac.Can`/`rbac.Require`
(05-rbac §3.4), which read the request principal's compiled checker. A `rbac.Checker` is a
per-principal value, not a module handle, so a `Deps` field could only hold the wrong principal (COR-21).

Owning types, with the file that must define each one (COR-21, COR-1). Where a type is not yet in
its owning file at the time of writing, the row says so and the request is recorded under the
reviser's cross-cluster list; the owning file wins on detail:

| Field | Type | Owner | Scoping it enforces |
|---|---|---|---|
| `Log` | `*slog.Logger` | 05-logging | attr `module=<ID>` |
| `ExtendStart` | `func(step string)` | 05-health §5H.3.4 (`Supervisor.ExtendStart`) | extends the start deadline per step only; cannot signal READY, STOPPING or the watchdog |
| `Metrics` | `*metrics.Scope` | 05-metrics §7 (defined) | module series named `<MetricPrefix>_*` while the app **ships** one module (the module list passed to `app.New`), `<MetricPrefix>_<ID>_*` with two or more; enablement never changes a series name, so disabling one module of two does not rename the other's (SIMP2-13); `MetricPrefix` is `app.Options.MetricPrefix` (03 §3.6) |
| `Storage` | `module.StorageHandles` | this file (above); field types 05-storage §5S.3.4, §5S.3.8–§5S.3.10 (defined); `Maintenance` is `legacy.Maintenance` (§6.4.11) | reads and writes outside `m/<ID>/` and the module's claims refused (R8); the module's `legacy.Stores` constructors receive the same `Legacy` range (`legacy.Handles`) and nothing wider (§6.4.11, §6.11.6); `Maintenance` exists only for the module that declares legacy claims, S2b to the S4 r2 PR, and carries only the staged reset (`kernel.stageReset`), never `storage.Admin` itself (R7-2, SIMP8-4) |
| `Tasks` | `tasks.Client` | 05-tasks 5T.3.10 (defined, with `RunView`) | the kernel overwrites `StartRequest.Actor` with the ctx principal, or with the module's system actor outside a request, so a module cannot start a run as another actor (05-tasks 5T.3.10, defined). The system actor's permission set is exactly the `Perms.Start` of the module's own kinds plus those of `Manifest.TaskStarts`, never `core:admin` or an `AuthRequired` scope (SEC2-15). Books' five legacy operations routes (`GET /api/v1/operations/{id}/{activity,changes,result,results}`, `GET /api/v1/operations/v2/{id}`) stay books' gin routes at every stage, behind HEAD's `library.view`; from T0+T1 their handlers read runs through this client (`Get`), and the v2 handler renders HEAD's v2 shape from it by T8, when `opsbridge` goes. No operations route or alias moves to core: core's run route is `GET /api/core/v1/tasks/runs/{id}` (05-tasks 5T.3.10), a different path, so no conflict set spans two owners (R10-1, COR10-1) |
| `Events` | `events.Scope` | 05-events §5E.3.2 (defined: `Bus.Scope(moduleID)`, kernel-only) | `Publish` refuses topics outside this module's catalog entries |
| `Outbox` | `outbox.Writer` | 05-events §5E.4 (defined) | stages only into topics this module declared `Durable` |
| `Notify` | `notify.Publisher` | 05-notify §3 (`notify.Service.For(module)`, the scoped interface over `Publish(ctx, b *storage.Batch, n)`) | refuses topics outside this module's catalog entries and topics without `Notify` metadata; outbox-backed, so a notification commits with the state change; a topic's `Notify` metadata alone produces no notification (COR2-3) |
| `Secrets` | `secrets.Scope` | 05-config §2.3 (defined, SEC-29) | prefix `modules.<ID>.`; integration-instance secrets are reachable only through `integrations.Env.Secrets`, never through a module scope |
| `Library` | `library.Scope` | 05-library (defined, incl. `TransferIn(b, …)` and `PendingTransfers`) | owner is the module, so "role written by its owner only" cannot be spoofed |
| `Auth` | `auth.Identities` | 05-authn §3.1 (defined: `User`, `UserByUsername`, `Users`, `PrincipalFrom`) | no credential or session mutation |
| `AuthAdmin` | `auth.Admin` | 05-authn §3.1 (defined: `StageUserMerge`) | every method re-checks the ctx principal (`core:users.manage`, interactive, dominance, owner rule); joins the caller's batch, never commits |
| `Audit` | `audit.Logger` | 05-rbac §3.5 (defined) | actor from ctx; action namespace `<ID>.` checked |
| `Health` | `health.Reporters` | 05-health §5H.3.1 (defined: `Reporter(id)`, `ErrNotOwned`) | cannot report another component's status |
| `HTTP` | `*providerhttp.Registry` | 05-metadata (defined) | instance-scoped throttles and quotas (R5) |
| `Integrations` | `*integrations.Registry` | 05-metadata M1 (defined) | adapter IDs outside the module prefix refused at registration |
| `Downloads` | `*download.Pool` | 05-download (defined) | grab/monitor methods and `Remove` are phase 2 / deferred-build (SIMP-12, SIMP2-17) |
| `AI` | `*ai.Router` | 05-ai (defined) | only capabilities the module declared |
| `Backup` | `backup.Ensurer` | 05-backup §5B.3.2 (defined) | no restore or prune |

Settings are not in `Deps`: a module reads its own sections through `module.Setting(r, section)`
handles obtained in `Register`, and core's public settings through the core service handles that
need them. `var ErrStopped` is an exported package-level variable of interface type `error`; R5
forbids exported map/slice/func/pointer variables, not sentinel errors, and `TestNoGlobals`
allowlists `var Err… = errors.New(…)`.

### 6.4.5 `app`

```go
package app

// Options configure one process. Zero values are valid defaults unless noted.
type Options struct {
	AppID       string // app identifier, [a-z][a-z0-9]{1,15} (03 §3.6), e.g. "abo"
	MetricPrefix string // [a-z][a-z0-9_]*; default AppID; ABO sets "audiobook_organizer" so its existing series keep their names (03 §3.6)
	// MetricNamespace is the namespace of core's OWN series,
	// <MetricNamespace>_<subsystem>_<what> ([a-z][a-z0-9_]*; default
	// "mediacore", the name every table of this spec shows). ABO passes
	// "audiobook_organizer", so every series ABO emits at HEAD keeps its name
	// when its emitter moves into a core subsystem, through S10 and after
	// (A-5, ADV-S4; 05-metrics owns the naming rule). S2, with MetricPrefix.
	MetricNamespace string
	// ServiceName is the OTel resource's service.name, merged with the SDK's
	// resource.Default() as HEAD does (cmd/root.go:296,
	// internal/telemetry/resource.go:36-50 @748fd787a; 05-metrics §4 owns the
	// attribute set), so target_info and every OTLP-pushed series keep HEAD's
	// identity; it is also the fixed server.address of the HTTP instruments
	// core's stage 2 records, which is never taken from the request's Host
	// header, so no client can mint a series per Host value (05-http-api §4.2
	// stage 2; MOD10-2, MOD10-11). The kernel passes this one value as
	// metrics.Options.ServiceName and as httpapi.Options.ServerName. Default AppID, which cannot spell HEAD's
	// name (no hyphen); ABO passes "audiobook-organizer", which is also the
	// server name HEAD's otelgin passes (internal/server/server.go:1673
	// @748fd787a). S2, with MetricPrefix.
	ServiceName string
	// MetricViews are the app's histogram views: instrument name to explicit
	// bucket boundaries, which the metrics Provider installs as one
	// explicit-bucket view per entry when it builds its MeterProvider (an SDK
	// view can be set only then), beside the views core's own Scopes declare
	// (R10-3, MOD10-6). ABO passes HEAD's telemetry.HistogramBuckets()
	// (internal/telemetry/views.go:69 @748fd787a, the table HEAD's Views()
	// installs), so every le value stays HEAD's for the instruments ABO
	// creates outside any Scope (ai_dispatch.*, ai.*, ops.* until S5) and for
	// the http.server.* instruments of the legacy mount's otelgin. The value is
	// numbers keyed by names the app supplies, so core holds no app name
	// (03 R1). 05-metrics §3 owns the install rule; ABO's
	// TestHistogramBucketsPinned is part of every stage's series check. nil =
	// core's own views only. S2 (metrics.Options carries the same table from
	// S1, when ABO's façade first builds core's Provider).
	MetricViews map[string][]float64
	// ServedMeterProvider is an S2 r1–r2 bridge (05-metrics §3 rule 2, §6;
	// MOD10-5): at S2 r1–r2 the App's registry is private and unserved, so the
	// kernel hands httpapi's stage 2 this MeterProvider (ABO: the one its S1
	// façade serves on /metrics) instead of its own Provider's, and no release
	// records a series nobody scrapes. nil = the App's Provider. Deleted in the
	// S2 r3 PR, with the façade's exporter, when the App's Provider is served.
	ServedMeterProvider metric.MeterProvider // go.opentelemetry.io/otel/metric, as Provider.MeterProvider() returns
	// HTTPMetricsScope is the instrumentation scope in which httpapi's stage 2
	// creates its three http.server.* instruments (05-http-api §3.5
	// Options.HTTPMetricsScope, §4.2 stage 2 (b); resolution R11-1, VER11-1).
	// ABO passes otelgin's ScopeName and Version constants as data
	// ({"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin",
	// "0.72.0"}, moved with the otelgin pin), so stage 2 and the legacy
	// mount's otelgin write into one instrument set on the served provider
	// and core imports nothing of otelgin. At S2 r1–r2 ABO's
	// ServedMeterProvider is the provider its façade installs as the OTel
	// global, which otelgin reads. Zero value = core's own "httpapi" scope.
	// S2.
	HTTPMetricsScope httpapi.InstrumentScope
	// ReservedAPINamespaces are the second segments under /api that the app's
	// compatibility surfaces occupy (ABO: the ABS unversioned /api/<seg>
	// names); the kernel adds them to the reserved IDs in P1 so no module can
	// take them (03 §3.6). Compat TOP-level segments come from
	// Manifest.LegacyPrefixes instead (§6.4.1, FE2-11).
	ReservedAPINamespaces []string
	DisplayName string // e.g. "Audiobook Organizer"
	Version     string // the app's release version (updater, /api/core/v1/modules)

	// There is no BuildID option (FE4-1, MOD4-9). Two identities, never
	// confused (R5-7, FE5-3):
	//   - the UI build ID has one source, the embedded bundle's
	//     <meta name="mediacore-build">, which `npm run build` itself stamps
	//     on every build path (07 §7.6.3): httpapi stage 5 writes UI.BuildID()
	//     from S9 and the ServesSPA mount's LegacyMount.BuildID before S9,
	//     otherwise "dev" (05-http-api §4.2). It serves X-App-Build,
	//     ui_build_id and the reload rule only. Nothing is linked with ldflags
	//     and nothing is compared at boot;
	//   - the binary ID (BinaryID below) is the running binary's identity,
	//     which the updater, storage and backup use.

	// EnvPrefix is the prefix of every env name (05-config §1.3,
	// [A-Z][A-Z0-9]{1,15}); default strings.ToUpper(AppID) (ABO: "ABO") (COR2-8).
	EnvPrefix string
	// StateDir is the compiled default of core.paths.state_dir (05-config).
	// core.paths.state_dir is a PRE-BOOT key (there is no core.paths.data_dir,
	// 05-config §1.7), resolved ONCE per process, before P0a, by
	// config.ResolveStateDir (05-config §1.4 step 0), whose result P0a, P0
	// and every consumer share (and P−1 once built, A-6). It keeps HEAD's rule
	// (internal/config/state_dir.go:118-155 @748fd787a; R3-4, OPS3-1):
	// an explicit flag or env value, which never falls back; else this
	// default when it exists or can be created; else
	// filepath.Dir(core.storage.path) as resolved from flags, env and the
	// operator YAML file (the file's path is itself a pre-boot input,
	// --config or <APP>_CONFIG; only that one key is read from it here,
	// HEAD's AppConfig.DatabasePath rule, internal/config/state_dir.go:143-153
	// @748fd787a, OPS5-13) (core.storage.path is a kernel pre-boot key from
	// S2, adopted by storage's section at S2b, COR4-20), with HEAD's WARN. Docker
	// and launchd installs, which set no state directory and cannot create
	// the default, therefore keep the keyring, the revocation journal and the
	// restore journal on the database's persistent volume, where HEAD already
	// keeps .encryption_key. core.paths.state_dir itself is never read from
	// the config file or the DB, and a config file that cannot be read adds
	// nothing to the fallback, so a config that P0 refuses cannot hide a
	// staged update or restore
	// (§6.5.1, OPS2-3). The state directory holds state files only (boot
	// record, keyring, updates/, restore journal); the store opens at
	// core.storage.path, which may be elsewhere (DATA2-19). From S2 ABO's
	// legacy code (the .encryption_key reads, the restore intent ABO stages)
	// uses the directory app.ResolveStateDir returns instead of HEAD's
	// EnsureSecureStateDir, so one resolver serves the whole process (OPS4-8).
	StateDir string
	// ConfigFile is optional; layered under env/flags/DB overrides (05-config).
	ConfigFile string
	// Args/Env feed the flag and env layers; nil means os.Args[1:]/os.Environ().
	Args []string
	Env  []string

	// UI is the app's embedded SPA (07 §7.6); lands with package ui at S9.
	// The zero Bundle serves the API-only placeholder, unless a module's
	// LegacyMount sets ServesSPA, in which case "/" belongs to that mount and
	// ui.Handler is not mounted (FE-1). A non-zero UI together with a
	// ServesSPA mount is a boot error.
	UI ui.Bundle

	// UIOptions carries the app's own contributions into the ui.Handler the
	// kernel builds in P8 (FE3-4): page-load middleware, extra connect-src
	// hosts and extra CSP directives (ui.AppOptions, 07 §7.6.1). It is a
	// compile-time value like Authenticators, built in main before New, so a
	// func whose inputs exist only after boot reads an atomic.Pointer that
	// the module fills in its Start hook and is a no-op (or returns an empty
	// list) until then; P8 only wraps the funcs (FE4-5). ABO (S9): Cloudflare
	// service-token recovery from internal/cfaccess as PageMiddleware and its
	// Access team host as ConnectSrcFrom, both reading pointers books fills
	// in its Start hook. ui_route recording is not the app's: the kernel
	// registers one compat entry per UIDescriptor.LegacyRedirects row at P7
	// and ui.Handler records a full page load that matches one (07 §7.6.1).
	// The kernel merges UIOptions into the ui.Options it builds (NonSPA,
	// Config, Metrics and the redirect recorder stay the kernel's). The zero
	// value adds nothing. S9, with ui.
	UIOptions ui.AppOptions

	// Authenticators are the app's identity extensions (05-authn §3.3;
	// ABO: internal/cfaccess), wired by the kernel with
	// auth.Registrar.AddAuthenticator. This compile-time list is the ONLY
	// place a MethodSpec may be Interactive or set MayChangeCredentials, and
	// the only place an OwnerVerifier may come from; modules register compat
	// methods with Registrar.AuthMethod, which refuses all three (SEC2-16).
	Authenticators []auth.Extension // {Authenticator, Spec auth.MethodSpec, Order}

	// Auth is the app's compile-time auth configuration (05-authn §3.2
	// auth.Options): legacy API-key prefixes, the invite-link form and the
	// defaults of the app's auth keys. ABO sets LegacyKeyPrefixes ["abk_"],
	// InviteLinkPath (S4 to S9), cookie name "session_id" without the
	// __Host- prefix, and DefaultDisabledAllowRemote true (SEC4-8: every
	// Docker install listens on a non-loopback address, so auth-off
	// installs would otherwise refuse to boot at S4 r2). Zero value for
	// hello and new apps.
	Auth auth.Options

	// LegacyConfig carries the app's legacy NAMES of core config keys
	// (config.Legacy, 05-config owns its shape: the aliases of core keys for
	// the file, env and flag layers). It holds no row name, codec or file of
	// the legacy settings storage: those are books' legacy config store
	// (legacy.Stores.Config, §6.4.11, R4-1), which stays until the settings
	// family's cutover (COR4-3). nil = an app with no legacy names. S2.
	LegacyConfig *config.Legacy

	// LegacyConfigStore is the S2 bridge only (S2 has no kernel storage):
	// books' legacy config store, built by ABO's main over the store its serve
	// opened before New. config's DB layer attaches over it at P3, so a
	// UI-saved value (the rate limiter, CORS origins, the log level) is
	// honoured at boot with HEAD's precedence for the app's
	// legacy keys (R5-1), and the store's Changed (the optional
	// legacy.ConfigExternalWriter, S2 to the S3 PR, SIMP5-5) fires after every blob
	// write by ABO's legacy config path, so it is honoured at runtime as HEAD
	// does (COR3-1). HEAD's file-only keys (the peer allow-list and the tunnel
	// connector addresses, json:"-" at internal/config/config.go:1708,1719
	// @748fd787a) are never in the blob and keep HEAD's storage, the operator
	// file and env only (05-config's LegacyYAML/LegacyBlob split, R9-5,
	// DATA9-6). The config.OverrideStore interface lands here with its
	// read members only, naming no storage or secrets type (COR5-14).
	// Deleted in the S2b PR, when legacy.Stores.Config takes over over
	// the kernel's one store handle (SIMP4-11). nil = none.
	LegacyConfigStore config.OverrideStore

	// PromRegisterer is where the App's Prometheus exporter registers from S2
	// to S8, so the legacy gin /metrics keeps serving one registry with both
	// the legacy client_golang families and the App's series. ABO sets it to
	// prometheus.DefaultRegisterer in S2 r3, the release that deletes the S1
	// façade's exporter, and leaves it nil (the App's own registry, unserved)
	// in S2 r1 and r2, so two exporters never share one registry (OPS5-14).
	// Removed at S8, when the kernel mounts the metrics provider's handler at
	// /metrics and the legacy families move to its registerer in the same PR
	// (MOD4-3, 05-metrics). It is a *metrics.Shared (05-metrics §3, R9-3):
	// ABO passes metrics.NewShared(prometheus.DefaultRegisterer) and registers
	// its legacy client_golang families through the same value, so a family
	// the Provider also holds is refused (ErrFamilyTaken). HEAD's three
	// http_server_* families stay HEAD's at every release: the legacy gin
	// mount keeps otelgin, configured as at HEAD, as the one recorder of every
	// request that reaches gin, and core's stage 2 records only core and
	// module routes and never a request the mount records, but both write
	// into one instrument set in otelgin's scope (HTTPMetricsScope below), so
	// the family has one owner and a label set both produce is one series;
	// HEAD's WithoutScopeInfo stays, and a pre-mount refusal carries no route
	// (R10-3 as corrected, R11-1; 05-http-api §4.2 stage 2 and 05-metrics §3
	// own the rule).
	// nil = the App's own registry.
	PromRegisterer *metrics.Shared

	// CompatLedger is the app's compat ledger (09 §9.2.9; ABO: //go:embed of
	// a generated copy of docs/compat-surfaces.md under the books module,
	// freshness-checked in CI, so no embed input sits in an ignored path,
	// R4-4). compat merges it with core's own ledger fragment
	// (core/compat/core-ledger.md, embedded by compat) and fails boot on a row
	// ID present in both; any alias or deprecated surface without a row fails
	// with ErrNoLedgerRow. That ledger-row check starts at S2b with compat; at
	// S2 aliases register without it (COR4-20). An app with no legacy
	// surfaces passes nil (SIMP3-3). S2b, with compat.
	CompatLedger []byte

	// LegacyPermissionAliases are the app's legacy permission strings that
	// map to core:* (ABO: its 13 legacy atoms, each with its frozen Reach,
	// filled by ABO's main from books' reach-snapshot data; 05-rbac §6.3). The
	// app's main is the trust root, as for every Options field, so these
	// app-level aliases may target core:*, which a module's
	// Manifest.LegacyAliases may not (05-rbac §4.9). The kernel declares them
	// in P5 and rbac validates them at Freeze: every target in the catalog,
	// Reach dominating New. Core holds no app's atom names (03 R1; there is no
	// core/rbac/legacy_abo.go) (COR5-5). nil = none. S4.
	LegacyPermissionAliases []rbac.LegacyAlias

	// LegacyAmbientScopes are the core-namespaced scopes that every
	// AnyAuthenticated principal holds on an instance whose role store is
	// legacy-backed (05-rbac §6.3, A-2): HEAD gated their routes by
	// authentication alone, and a legacy role or key row cannot hold a
	// core-only scope, so this keeps HEAD's authenticated-only reach for ABO
	// principals that hold atoms only. ABO: core:self, core:metrics.read,
	// books:reading (own profile, sessions and keys; /metrics, which HEAD
	// serves to any credential that is not worker-confined; own reading
	// state). /metrics is Router.Infra(path, "core:metrics.read", h) on every
	// app, so on ABO every non-Confined() principal reaches it through this
	// option and a worker-confined one does not, as at HEAD (COR10-8). They belong to the
	// principal, never to a role: rbac passes them as CheckerInput.Ambient,
	// Compile decides Confined() on the set before ambient, and a principal
	// that is not confined gets them AFTER the API-key scope intersection, so
	// a scoped abk_ key keeps HEAD's reach on /api/v1/auth/me, its own keys
	// and /metrics (R9-4, COR9-3). No role read adds them and no role write
	// subtracts them, so role editors and /me's Concrete() show stored atoms
	// only. A Confined() principal never holds them. rbac's Freeze refuses
	// the option on a native role store and refuses a listed scope that is
	// Sensitive, AuthRequired, Confining or a
	// *:settings.manage/*:integrations.manage scope (TestLegacyAmbientScopes,
	// which also covers a LegacyFullScope key and a key scoped
	// ["library.view"]). The identity hardening cutover H1 retires it. H1
	// writes these scopes into a role iff the role is not Confined(), and into
	// a key iff the key's EFFECTIVE set before ambient is not Confined(): the
	// owner's role atoms intersected with the expansion of the key's atoms (an
	// empty list meaning the role set), never the owner alone. That is the
	// runtime rule above and HEAD's (internal/server/middleware/auth.go:266-297
	// @748fd787a), so an admin's ["fingerprint.worker"] key stays confined and
	// every principal's Has() is unchanged across H1 (R10-2, SEC10-1; 05-rbac
	// §6.3 states the rule once; TestWorkerKey_NoAmbientScopes at every stage,
	// TestH1_ConfinedKeyStaysConfined). nil = none (hello, TV, Movies). S4.
	LegacyAmbientScopes []rbac.Permission

	// LegacyTaskAliases are the app's former task kinds of CORE-owned kinds
	// (ABO: {"core.diagnostics.export": ["diagnostics.export"]}, filled by
	// ABO's main from books' op-ID ledger, internal/server/testdata/
	// op_ids.golden). A core definition never spells an app's op ID
	// (05-tasks 5T.8.2, 03 R1(c)): the app supplies it here, as for
	// LegacyPermissionAliases. At P7 the kernel adds each value to that core
	// kind's former kinds, before the op-ledger gate, so HEAD's stored runs
	// resolve; a key that is not a registered core.* kind, or a value that a
	// module registers or lists in its TaskLedger, records
	// ProblemLegacyTaskAlias (MOD6-6). nil = none. Lands at S5, in T4, the
	// release with the first batch of core.* kinds that have ABO former IDs
	// (05-tasks 5T.8.2); core.diagnostics.export (05-health) joins at S8.
	LegacyTaskAliases map[tasks.Kind][]tasks.Kind

	// LegacyAuthGinUntil names the one module whose LegacyMount may use
	// httpapi.LegacyAuthGin (ABO: "books" from S2; the S4 PR clears it and an
	// archtest fails if it is set after S4). "" = no module may (SEC2-17).
	LegacyAuthGinUntil module.ID

	// Adapters enables core adapters (e.g. deluge.Descriptor(), webhook.Descriptor());
	// only enabled adapters may be configured; instances live in the one
	// integration-instance store (05-metadata M1.3, SIMP-4).
	Adapters []integrations.Descriptor

	// Logger overrides the default slog handler construction (tests); nil = from config.
	Logger *slog.Logger

	// Updater configures core/updater (release source, asset naming; 05-updater).
	// nil = the app ships no updater (e.g. a container-only app).
	Updater *updater.Options
}

type App struct{ /* unexported: kernel.Kernel */ }

// New runs boot phases P0a to P8 (§6.5.1; P−1, the updater boot check, is
// deferred-build, A-6): pending restore swap, config boot view, manifest validation and enablement,
// storage open, config load, checkpoint and migrations with the every-boot
// reconcilers, core services, Register of each enabled module, consumption
// of registrations, and the HTTP handler.
// It starts no module code that runs on its own (hooks, workers), no task
// executor or scheduler, no bus delivery, and no listener. Core services'
// internal goroutines started while building them (Pebble, log taps, the
// audit writer, the realtime coalescer, the rate-limiter sweeper) are
// stopped, and storage closed, in reverse order on any error (COR-24).
// On error nothing is left open.
func New(ctx context.Context, opts Options, mods ...module.Module) (*App, error)

// Run starts the app in the order of §6.5.2; blocks until ctx is done or a
// StopAppOnExit worker exits; then shuts down in the reverse order within
// core.shutdown.budget (05-health §5H.4). A clean shutdown after ctx cancellation
// returns nil.
func (a *App) Run(ctx context.Context) error

// Handler is the fully assembled root handler (for coretest and embedding in
// another server). Valid after New.
func (a *App) Handler() http.Handler

// BinaryID returns the running binary's identity: opts.Version, "+", and the
// lowercase hex SHA-256 of the executable. It is buildid.Binary(opts.Version)
// and adds nothing to it (FE6-3): the computation lives in core/buildid, a
// dependency-free L0 leaf that lands at S0 beside storage/keys, so ABO's S0
// guard (the install marker's and the served ledger's build fields) and its
// --version-json compute the same value with main.version from S0, before
// any kernel exists, and the first S2b boot compares like with like
// (05-storage "Binary ID"). The leaf reads os.Executable once per call: no
// ldflags, no package state (R5). New computes it once, before P0a, and hands
// the one value to the updater (Self.BinaryID, 05-updater), storage
// (c/storage/selfserved/<binary id>, the served ledger's build) and backup
// (StagedByBuild, Source); the boot check that would compare it with a
// marker's to_build is deferred-build with P−1 (§6.5.1, A-6). Two binaries of one commit with different
// code, or two API-only builds, therefore never share an identity, which the
// UI build ID ("dev" without an embedded bundle) cannot guarantee (R5-7,
// FE5-3).
//
// --version-json has one field list, which 09 S0 and 05-updater step 3's
// self-test use verbatim (FE6-4): {app, version, commit, ui_build_id,
// binary_id}. app is Options.AppID, version is the release version, binary_id
// is this value, ui_build_id is the value X-App-Build reports: the embedded
// bundle's meta, legacy or shell, or "dev" without one (07 §7.6.3); it is
// not /api/core/v1/modules' ui_build_id, which is the served shell bundle's
// only ("" while a legacy mount owns /, 07 §7.5.1; FE7-13). commit is
// buildid.Commit(): debug.ReadBuildInfo's vcs.revision, suffixed "+dirty"
// when vcs.modified is set (Go's definition; core/web's .gitignore keeps
// the untracked node_modules and dist outputs of `make web-deps` from
// setting it, 07 §7.3.1, FE7-1), or "" when absent (a Docker build has no
// .git in its context). It is never linked: goreleaser's
// dead "-X main.commit" and "-X main.date", which name variables ABO's main
// does not declare (.goreleaser.yml:33-34, main.go:20-21 @748fd787a), are
// deleted in the S0 PR. A consumer that needs a commit (09 P10's PREV_REF)
// strips a trailing "+dirty", and falls back to the g<hex> part of a
// describe-form version, then to a manual ref, never to a default (09 P10
// owns the order, FE7-2).
func BinaryID(opts Options) (string, error)

// ResolveStateDir returns the state directory New will use for opts: the
// same config.ResolveStateDir call over the same flags, env and
// Options.StateDir (05-config §1.7). An app that must touch state files
// before New (ABO's S2 serve: the key guard and encryption init) calls it
// instead of a resolver of its own (OPS4-8).
func ResolveStateDir(opts Options) (string, error)

// OpenOffline is kernel.OpenOffline under the app's identity (§6.4.6;
// R7-4, DATA7-11): the guard-aware open an app's offline subcommands use
// instead of opening the store themselves, so the next serve boot reads no
// foreign open from this session and still owes every duty an earlier
// foreign open caused (R8-2). readOnly opens Pebble read-only and writes
// nothing. S2b.
func OpenOffline(ctx context.Context, opts Options, readOnly bool, mods ...module.Module) (*Offline, error)

// Offline is one offline open. Storage returns a module's handles as P4c
// builds them, except that Maintenance is nil: there is no serving process
// to restart for a staged reset (SIMP8-9). Close commits the close batch of
// §6.4.6 (owed entries first, then the promotion) and closes storage.
type Offline struct{ /* unexported: kernel.Offline */ }

func (o *Offline) Storage(id module.ID) (module.StorageHandles, error)
func (o *Offline) Close() error

// The staging marker (A-10; 05-storage §5S.3.7 "Staging marker" owns the
// record: the core key c/storage/staging and the sidecar <db>.staging, each
// holding the archive's SHA-256, a nonce and the binary_id of the binary
// that wrote it, R10-11, DATA10-5). c/storage/ is a core keyspace
// that no module handle reaches (ErrOutsideKeyspace), so an app's staging
// subcommands use these and nothing else (R9-9, COR9-6). S2b.
//
// StagingMarker reads both halves in this session; ok is true only when
// both exist and agree on the hash, the nonce and binary_id.
// WriteStagingMarker (read-write sessions only) stages the key into the
// close batch with a fresh nonce and storage.StagingMarker.BinaryID = the
// running binary's buildid.Binary (the value app.BinaryID returns), which
// staging-restore has just verified equals the archive manifest's
// binary_id: the marker's binary_id and the manifest's must agree, or
// staging-restore writes no marker, so the marker carries the archive
// writer's identity to staging-scrub, which takes no archive; Close commits the key and then
// writes the sidecar, so a crash between them leaves a key without a
// sidecar, which staging-scrub refuses.
func (o *Offline) StagingMarker() (m storage.StagingMarker, ok bool, err error)
func (o *Offline) WriteStagingMarker(archiveSHA256 string) error

// ReadStagingMarker reads the sidecar and then the key through a read-only
// open that writes nothing, not even an OPTIONS-<n> (staging-scrub's check
// before any read-write open). ok as for Offline.StagingMarker; a missing
// half or a disagreement is ok == false with err naming it. S2b.
func ReadStagingMarker(ctx context.Context, opts Options) (m storage.StagingMarker, ok bool, err error)

// OfflineCommands returns core's offline commands for the app's own CLI,
// which wraps each one (ABO: a cobra command per entry; no file of cmd/
// moves into app, MOD2-4): storage restore-checkpoint (05-storage §5S.3.12,
// from S2b), secrets export-keyring, secrets rotate-derive and secrets
// set-install-id (05-config §2.3, §2.4, from S3, with the keyring) and users
// reset-password (05-authn, from S4). secrets export-keyring opens no
// store: it reads the state directory, and the install ID from the keyring
// header, so it works while the server runs (SEC10-9). Every other Run
// opens the store itself, through kernel.OpenOffline (rotate-derive,
// users reset-password, read-write; set-install-id read-only, for the
// install marker) or, for restore-checkpoint, the kernel's own swap and
// close-batch path (§6.4.6), and needs no import of kernel or
// core/internal/restoreswap. A command whose owner has not landed is absent
// (R9-9, COR9-6; ABO's TestCLIExposesCoreOfflineCommands).
func OfflineCommands(opts Options, mods ...module.Module) []Command

type Command struct {
	Name  string // space-separated path, e.g. "users reset-password"
	Short string // one-line help
	Long  string // full help, including the "service stopped" requirement
	Run   func(ctx context.Context, args []string, stdout io.Writer) error
}

// ExtendStart asks the service manager for more start time before a startup
// step that makes real progress, for app code that runs BEFORE New (ABO's S2
// serve: store open and books' migrations, §6.5.1 "Stages"). It is the same
// write as 05-health's Supervisor.ExtendStart (EXTEND_TIMEOUT_USEC with the
// per-step budget), stateless (it reads NOTIFY_SOCKET per call; no package
// state, R5), never called from a timer, a no-op without NOTIFY_SOCKET, and a
// failed write is logged, never returned.
func ExtendStart(step string)

// Modules reports each module's resolved state (enabled, disabled, reason).
func (a *App) Modules() []ModuleState

type ModuleState struct {
	ID              module.ID
	Version         string
	Enabled         bool
	Disabled        string // reason when !Enabled: "config" (with the layer that set it)
	RestartRequired bool   // stored enablement differs from the running state (§6.5.3)
}

// Describe runs phases P0–P7 against vfs.NewMem storage, a temp state
// directory and defaults only: no P−1, no P0a, no operator config file,
// every module treated as enabled, no Run and no listener; it then calls
// Router.Build without serving (COR2-13). It returns the OpenAPI 3.1
// documents and each module's UI manifest. Register's side-effect rule
// (§6.4.1) is what makes this safe. Used by an app's "openapi" subcommand
// (05-http-api §7, 07 §7.8) and by contract tests.
func Describe(ctx context.Context, opts Options, mods ...module.Module) (Description, error)

type Description struct {
	Core        []byte            // core's OpenAPI document; emitted ONLY by core/examples/hello's "openapi" subcommand (FE-20)
	OpenAPI     map[string][]byte // per module ID, JSON (05-http-api Router.OpenAPI)
	UIManifests []ui.ModuleManifest // per module: ID, Version, DisplayName, UI IDs, permission names (07 §7.6.1); S9, with package ui
}
```

Signal handling stays in `main` (`signal.NotifyContext`), so `Run` is testable and a host that embeds
the app controls its own lifecycle. A `package main` is a process owner like
`app.InstallProcessDefaults`, so R5's ban on `signal.Notify` and `signal.NotifyContext` exempts it:
`TestNoGlobals` skips main packages under `core/examples/*/cmd/` and flags both calls in every other
core package (COR6-11). ABO keeps its cobra command tree: no file of `cmd/` moves into
`app` (MOD2-4); it wraps each entry of `app.OfflineCommands` as a cobra command, so core's offline
commands are reachable from ABO's CLI without ABO importing `kernel` (R9-9, COR9-6). Its `serve` command replaces its OTel and slog bootstrap with calls to `app.New`,
`app.InstallProcessDefaults` (05-logging) and `Run`, and its flags map onto `app.Options` (§6.11). At
S2 it still opens the store, runs books' migrations and loads the settings blob before `app.New`, in
HEAD's order and through ABO's own `internal/startup` (§6.4.3, MOD3-1); from S2b the kernel opens the
store at P2, the settings load, key guard and encryption init run inside books' legacy config store at
P4d step 1, and `serve` runs no startup step of its own (§6.4.3, §6.5.1 "Stages", R4-6).
`core.json` has a single producer so that apps cannot emit diverging copies: hello's `openapi`
subcommand writes `Description.Core` to `core/openapi/core.json`, freshness-gated in core CI. It is
producible from S2, when the hello skeleton lands (§6.11.1), and a PR that adds a core route
regenerates it in the same PR (MOD2-10, FE2-9). The file lives outside `core/web` because the npm
workspace that consumes it is first created at UI-0, after S2; UI-0's api-client generator reads it
from there (MOD3-4, 07 §7.8). `Describe(...).Core` needs `httpapi`'s OpenAPI builder
(`core/internal/openapi`, imported by `httpapi`, never the reverse) at S2 (04 §8.1). An app's subcommand writes
only its modules' documents (07 §7.8). Books meets `Register`'s side-effect rule only once its server
construction (`server.NewServer`, which loads the HNSW snapshot) has moved into start hooks
(§6.11.1 step 4); until then ABO's `openapi` subcommand does not call `Describe` over books and emits
no books document. A books route group becomes native only when rewritten for its own reasons
(09 §9.2, 05-http-api §8.4), and the first such rewrite carries that move.

### 6.4.6 `kernel` (shape only; apps never import it)

| Package | Responsibility |
|---|---|
| `kernel/catalog` | manifest validation, enablement resolution (P1, from `config.Store.Boot`), collision checks across modules and core (IDs against `module.ReservedIDs()`, `Options.ReservedAPINamespaces` and the first segments of every `LegacyPrefixes` entry; permission namespaces, legacy atoms, legacy role IDs (`LegacyRoleIDs`, COR5-5), keyspace and legacy claims, legacy store supply (each `legacy.Stores` member of `Manifest.LegacyStores` set by at most one manifest, R4-1, R6-3), config sections, task kinds, topic names, outbox consumer names, legacy path prefixes, UI IDs; facade URL bases when `arrfacade` is built), `MinCoreVersion` against `version.Version` |
| `kernel/services` | `type Services struct{ Storage *storage.DB; Tasks tasks.Executor; … }` built by `func Build(ctx, opts, cfg) (*Services, error)` in a fixed, hand-written order; `Close` in reverse |
| `kernel/supervisor` | start/stop ordering of §6.5.2, hook start/stop, worker supervision (`sync.WaitGroup.Go`), panic recovery, timeouts, shutdown budget |
| `kernel` | `Kernel` = catalog + services + supervisor; `Registrar`/`Deps` construction per module (the scope types of §6.4.4); hands registrations to services in P7; mounts core capability routes; `OpenOffline`, the one guard-aware open outside a serve boot (below) |

Core capability routes (`/api/core/v1/tasks`, `/roles`, `/audit`, `/events`, `/outbox`, …) live in
the per-capability route packages `internal/<capability>http` of 03 §3.3 (e.g. `internal/taskhttp`,
`internal/rbachttp`, `internal/audithttp`) at R4 L6. Each imports its capability and `httpapi`, and
is wired onto `Router.CoreGroup()` only by the kernel (COR-18). The module catalog's own
route, `GET /api/core/v1/modules` (07 §7.5.1), is one of them: package `internal/modulehttp` (L6,
FE3-16). It imports nothing above L6: the kernel passes it a read-only `modulehttp.ModulesView`
interface (the catalog snapshot, `restart_required`, the `ui_build_id` source and `version.Version`),
implemented by `kernel/catalog`, so the L6 → L8 import never exists (03 §3.3 and 05-http-api §5 list
the package). `POST /api/core/v1/ui/diagnostics` and `POST /api/core/v1/ui/csp-report`
live in `internal/uihttp`, a capability route package like the others, so the `CoreGroup` owner rule
("used only by `internal/<capability>http`", 05-http-api §3) holds without an exception for `ui`, and
the `ui` package (S9) registers no route (COR2-19). `internal/modulehttp` and `internal/uihttp`
ship at S4, beside `GET /api/core/v1/permissions`, so a shell build is never served by a binary that
lacks them (FE2-25). The core reset route `POST /api/core/v1/system/factory-reset` lives in
`internal/storagehttp` (L6, imported only by `kernel`), which lands at S4 r2 with the other native
admin routes. The kernel hands it `kernel.stageReset` (§6.5.1 P0a) as the storagehttp-local `Stager`
interface (`StageReset(ctx, requestedBy, storage.ResetRequest)`), never `*storage.Admin`, the way
`modulehttp` receives `ModulesView`, so an intent staged through the route carries the hook arguments
exactly as one staged through the legacy handlers (COR8-2). It is the package's one route.

**One request contract (R9-6, COR9-4, SEC9-4).** The request is `storage.ResetRequest`, which
05-storage §5S.3.15 owns and 05-rbac §6.2 (guards, answers and HEAD's `{data: {message}}` shape on
the aliases), 05-config and 05-http-api cite: `{kind, confirm: "RESET", delete_library_files?,
library_root?, library_file_count?}`, plus `LegacyConfirm` (`json:"-"`), which no body can set. The same value flows unchanged through `Stager.StageReset`,
`kernel.stageReset` and `legacy.ConfigResetArgs.ResetArgs` (§6.4.11), so the route binds the wipe to
exactly what the client confirmed and no layer adds or drops a field. A request without the confirm
token answers 422 and stages nothing. When `delete_library_files` is true, `ResetArgs` takes a
fresh preflight count of the library `RootDir` (05-storage defines the count: the top-level entries
the hook would remove, one directory read), and `kernel.stageReset` compares it with the request:
on the native route, when `library_root` or `library_file_count` is absent or differs, it returns
the error the route answers as **422 `library_files_confirm_required`**, naming the current root and
count in the problem body; nothing is staged, and the client resends them once the operator has
confirmed them (07 §7.11). A dangerous `RootDir` is refused only when the
wipe is requested. When the built config store has no `legacy.ConfigResetArgs` (hello, TV, Movies),
no hook will ever wipe a file, so `kernel.stageReset` refuses `delete_library_files: true` with
`storage.ErrResetRequestInvalid` (422) and stages nothing (COR10-9 (a)). From S4 r2 both legacy paths are the route's `Alias`es, and each sets only path
values through its `LegacyPath.Request`, never a body field: each alias's `Selector` lists `kind`,
`delete_library_files`, `library_root` and `library_file_count`, so those body fields are dropped
before the strict decode and the path values alone set `Kind`, `DeleteLibraryFiles` and
`LegacyConfirm`, as HEAD's `ShouldBindJSON` read only `confirm` (COR10-9 (b)); `/system/reset` sets `reset_kind=store`
(kind `restorejournal.ResetStore`), and `/system/factory-reset` sets `delete_library_files=true` and
`library_confirm=legacy`, the legacy-confirmation marker of `storage.ResetRequest` that no native
request can carry, so the legacy path keeps HEAD's `RootDir` wipe with no count check, as HEAD
counted nothing (A-17, 14 Q-K). A `ResetFactory` staged without `delete_library_files` still resets
the store, the settings, the auxiliary DBs, the Open Library data and the gap-fill file, and deletes
no file under `RootDir`. Before S4 r2 no core route exists, and books' two gin handlers call
`legacy.Maintenance.StageFactoryReset` behind HEAD's own chain with a `storage.ResetRequest` of their
kind, carrying the legacy marker and, for `ResetFactory`, `delete_library_files` (R7-2, COR7-1,
COR8-1, SEC7-4).

**Offline open (R7-4, DATA7-11, R8-2).** `kernel.OpenOffline` is the one guard-aware open of the store
outside a serve boot; an app reaches it as `app.OpenOffline` (§6.4.5), because apps never import
`kernel`. It runs the checks of P2 and P3 of §6.5.1 (storage's steps 1–4, 05-storage §5S.4) and refuses
on `CheckReader`'s swap tokens, without acting on them (SIMP8-9): it swaps
nothing, and refuses while a swap of any kind is pending or in flight (`restore_pending`,
`restore_inflight`); it resolves config, validates the manifests, and opens storage with the store
guard, the upgrade-floor check and the claim validation; and it refuses when P4 would run a migration
step (`ErrOfflinePendingSteps`, naming the steps; 05-storage §5S.4 "Offline open"), so it never migrates.
It hands out each module's `StorageHandles` (with `Maintenance` nil) and builds the core subsystem
stores as P4c does.

**It never launders a foreign open (R8-2, DATA8-3, SEC8-4).** It computes the boot verdicts of
storage's step 2 as a serve boot does. When it finds a foreign open (a stamp or sidecar mismatch), a
missing or unreadable record, or a **behind** lineage, its close batch first stages the owed entries a
serve boot would stage (`since` = the core `last.at`, zero when there is none; `detected` = now; an
entry already owed keeps its values), and only then promotes. At a clean `Close` that one batch promotes
its own Pebble open stamp (`OPTIONS-<n>`) and sidecar identity as the confirmed records, keeps the served
ledger's `last` record and the boot lineage unchanged, and records an owed `journal` duty when it
committed a revoking write without journaling it. So the next serve boot reads no foreign open from this
session and still owes every duty an earlier foreign open caused: the `gap` line and the `gen` rebuild
(05-storage §5S.4 "Offline open" owns the mechanics,
`TestOfflineOpenIsNotForeign` and `TestOfflineOpenAfterForeignOpenKeepsEvidence`). A crash before
`Close` is a foreign open, as any other binary's open is. A read-only open opens Pebble read-only and
writes nothing. An open checkpoint's offline finish commits this close batch over the restored store
with the step-e identities standing in (§6.5.1 "Stages", DATA8-5). It does not do so through
`OpenOffline`, which refuses while the checkpoint restore's own `inflight-checkpoint.json` exists:
the `storage restore-checkpoint` command calls the kernel's internal close-batch function
(`kernel.offlineCloseBatch(store, identities)`) with the step-e identities, the one function both
paths use, and then journals `applied_offline` (COR9-15).

**Users (DATA8-4, R8-3).** Core's offline commands that write the store (`<app> users
reset-password`; the app's CLI reaches it, `storage restore-checkpoint` and `secrets
set-install-id` through `app.OfflineCommands`, §6.4.5) and, from S2b, every ABO subcommand that writes it (`scan`, `playlist`, `tag`,
`organize`, `diagnostics cleanup-invalid`, `staging-scrub`) use it read-write; `diagnostics query` and
the `bench`-tagged `dedup-bench` open read-only; ABO's `Isolate` child mode (`cmd/child_mode.go:71`
@748fd787a) opens through it until it is deleted with its caller. `seed` populates a fresh database:
on an empty store every `Seed` step is pending, so `ErrOfflinePendingSteps` refuses it, and its help text
and `docs/` say "start `serve` once first". **Staging subcommands (A-10, A-12; 09 §9.3 owns the
procedure).** Both are offline ABO subcommands from S0 and run with the binary that wrote the
archive, which has no pending step on the copy: that binary is identified by the archive manifest's
`binary_id`, and `staging-restore` refuses an archive whose `binary_id` is not the running binary's
`app.BinaryID`, and **a pre-S0 archive cannot be staged**: it has no `binary_id` and no install
marker, so `staging-restore` refuses it before extracting anything and tells the operator to take a
fresh archive on a release ≥ S0 (09 §9.3 owns where the value is recorded; 05-backup, 05-storage;
R9-9, OPS9-8, OPS9-9).
`staging-restore --archive <file> --db <empty path>` reads and verifies the archive offline, extracts
it into an empty target, refuses a non-empty one and never writes `config.yaml`; it then writes the
**staging marker** (05-storage owns the record): the key `c/storage/staging` first, then the sidecar
`<db>.staging`, both holding the archive's SHA-256, a fresh nonce and its own `binary_id`, which it
has just verified equals the manifest's (R10-11, DATA10-5). From S2b it does so through
`app.OpenOffline` read-write and `Offline.WriteStagingMarker`, whose `Close` commits the key and then
writes the sidecar (§6.4.5); before S2b through S0's guard-aware open. `staging-scrub` reads the
marker through `app.ReadStagingMarker` (from S2b; before it, S0's read-only path) before any
read-write open, and refuses unless both halves exist and agree on the hash, the nonce and
`binary_id`, that `binary_id` equals its own `app.BinaryID`, and `--confirm-destroy-credentials`
is given, so the scrub runs with the archive writer's list of credential families (R6-7); S0's pre-S2b
path writes and checks the same field; a refused scrub writes nothing, not even an `OPTIONS-<n>` (R9-9, COR9-6). A production store never has the
sidecar, so the scrub never runs against one (`TestStagingScrubRefusesUnmarkedStore`, which also
covers a marked copy and another binary: refused, nothing written;
`TestStagingRestoreWritesMarker`).
ABO's `pebble-inject-skip`, `pid-census` and `diagnostics reserved-prefs` keep opening Pebble directly
and are documented as foreign opens that cost one round of fallbacks. `reserved-prefs` is HEAD's
break-glass tool for a store the guard refuses, so it must not go through the guard; from S2b it also
lists and rewrites core's reserved records (`c/storage/level/<owner>`, `c/storage/ks/<owner>/version`
and its applied records) and the sidecars, with HEAD's confirm-and-log rules (ABO's
`TestCLISubcommandsUseOfflineOpen`).

### 6.4.7 `module.ValidationError`

```go
// ValidationError lists every problem found in one boot, across modules.
type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string           // one line per problem
func (e *ValidationError) Unwrap() []error           // each Problem as an error, for errors.Is

type Problem struct {
	Module ID       // "" for app-level problems
	Kind   ProblemKind
	Detail string   // what, where, and how to fix
}

func (p Problem) Error() string
func (p Problem) Is(target error) bool // matches the ProblemKind sentinel

type ProblemKind int

const (
	ProblemInvalidID ProblemKind = iota + 1
	ProblemReservedID
	ProblemDuplicateID
	ProblemCoreTooOld
	ProblemPermissionNamespace
	ProblemDuplicatePermission
	ProblemUndeclaredPermission
	ProblemKeyspaceOverlap
	ProblemMigrationOrder
	ProblemSettingsNamespace
	ProblemSettingsUndeclared
	ProblemTaskKindNamespace
	ProblemDuplicateTaskKind
	ProblemTopicNamespace
	ProblemTopicUndeclared        // outbox handler or raw subscription names a topic no module declares (or not Durable)
	ProblemTaskLedger             // a registered kind (or FormerKind) is missing from Manifest.TaskLedger
	ProblemOutboxConsumerUndeclared // consumer name not in Manifest.OutboxConsumers
	ProblemRouteOutsidePrefix
	ProblemRouteWithoutPermission
	ProblemLegacyPrefixCollision
	ProblemSPAOwner               // two ServesSPA mounts, or a ServesSPA mount with a non-zero app.Options.UI
	ProblemUIDescriptor
	ProblemCapabilityMissing
	ProblemUnknownModuleInConfig
	ProblemRegistrarClosed
	ProblemWorkerPolicy
	ProblemAdapter
	ProblemChangeSinkDuplicate
	ProblemTaskStartUnknown       // Manifest.TaskStarts names a kind that is neither registered nor in any manifest's TaskLedger (SEC2-15, COR3-10)
	ProblemAuthMethod             // AuthMethod spec interactive, credential-changing, owner-verifying, or not "<ID>.<name>" (SEC2-16)
	ProblemLegacyAuth             // LegacyAuthGin by a module other than Options.LegacyAuthGinUntil, or OwnAuth without a registered compat method (SEC2-17)
	ProblemDerivedIndex           // derived index declared without hooks, or hooks for an undeclared index (COR2-15)
	ProblemSPASegmentCollision    // a ui.ReservedSegments() entry is also a NonSPA segment (P8, FE2-7); the ONE name for this check (FE3-9)
	ProblemLegacyStore            // a legacy.Stores field set by two suppliers (P1), or a store backend that differs from the one the store records (P2: legacy supplied or not; P4c: the full value, COR7-4; ErrLegacyStoreMissing, 05-storage) (R4-1); stays in module because the P2 check outlives the legacy stores (§6.4.11, R6-3)
	ProblemLegacyRole             // a Manifest.LegacyRoleIDs entry listed by two manifests or equal to a core seed role ID (P1, COR5-5); appended, so no built kind is renumbered
	ProblemLegacyTaskAlias        // an app.Options.LegacyTaskAliases key that is not a registered core.* kind, or a value a module registers or lists in its TaskLedger (P7, MOD6-6); appended at S5 (T4)
	ProblemUIAppIDMismatch        // a non-zero app.Options.UI whose <meta name="mediacore-app"> (ui.Bundle.AppID) differs from Options.AppID (P8, 07 §7.6.1, FE8-7); an app-ID check, not a build-ID check (FE4-1); appended at S9

	// deferred-build (08, a design sketch, A-15): appended when arrfacade
	// lands, so adding them never renumbers a built kind (SIMP3-11; §6.4.9):
	//   ProblemFacadeIncomplete
	//   ProblemFacadeURLBaseCollision
)

// Sentinels for errors.Is(err, module.ErrCoreTooOld) etc.
var (
	ErrCoreTooOld = problemSentinel(ProblemCoreTooOld)
	// ... one per ProblemKind
)
```

The dependency problem kinds of 1.0.0 (`UnknownDependency`, `DependencyCycle`, `DependencyDisabled`)
are gone with `DependsOn` (SIMP-22).

### 6.4.8 `coretest`

```go
package coretest

// Harness is an in-process App: in-memory Pebble (vfs.NewMem), a temp
// state directory (Options.StateDir), no listeners; requests go straight to
// App.Handler().
type Harness struct{ /* unexported */ }

// New boots core with the given options. With no WithModules option it boots
// core with ZERO modules (gate G7). It registers t.Cleanup for shutdown and
// fails the test on any boot error.
func New(t testing.TB, opts ...Option) *Harness

type Option func(*settings)

func WithModules(mods ...module.Module) Option
func WithDisabled(ids ...module.ID) Option                   // sets core.modules.<id>.enabled=false in the boot record
func WithConfig(kv map[string]any) Option                    // config overrides layer
func WithAdapters(d ...integrations.Descriptor) Option       // e.g. fakes from coretest/fakes
func WithUser(name string, perms ...rbac.Permission) Option  // pre-seeded principal holding exactly perms
func WithRuns(kind tasks.Kind, n int) Option                 // seeds n persisted queued runs of kind before boot

// Boot is New without failing the test: it returns the boot error, for
// validation tests that expect a *module.ValidationError.
func Boot(t testing.TB, opts ...Option) (*Harness, error)

// MetricsProvider boots one zero-module coretest harness (G7's) with opts
// (MetricNamespace, MetricPrefix, ServiceName, MetricViews and, until S8,
// PromRegisterer) and returns
// that boot's metrics.Provider, so a series contract test scrapes
// Provider.Handler(), never promhttp.Handler(). It boots because the
// kernel's own families (<MetricNamespace>_kernel_boot_phase_seconds,
// <MetricNamespace>_compat_*, <MetricNamespace>_storage_level,
// <MetricNamespace>_storage_checkpoint_bytes) are written by kernel code
// during a boot, and a family with no sample exports nothing (05-metrics §6
// seeding row; OPS8-10). ABO's internal/telemetrypolicy uses it from S2 r3
// with ABO's own MetricNamespace and MetricPrefix, so it checks that every
// series name HEAD exports is unchanged (A-5; 05-metrics, OPS7-1); from S5
// ABO's TestABOMetricsGatherNoDuplicateOrMissing also builds ABO's real
// recorders on this Provider, gathers, and checks the full family, label
// and series set (label names and values, le included, so MetricViews is
// checked) against a HEAD golden, so two emitters of one family fail CI,
// not the scrape (R9-3, MOD9-1, MOD9-2, R10-3).
// Shutdown is registered with t.Cleanup.
func MetricsProvider(t testing.TB, opts app.Options) *metrics.Provider

func (h *Harness) App() *app.App
func (h *Harness) As(user string) *Client          // authenticated client for a seeded user
func (h *Harness) Anonymous() *Client
func (h *Harness) RunTask(t testing.TB, kind tasks.Kind, params any) RunResult // enqueue as core:admin and wait
func (h *Harness) Storage() storage.Inspector      // read-only view of every keyspace
func (h *Harness) FS() *RecordingFS                // records every mutation under library roots

// RunResult is coretest's own summary of a finished run, so tests do not
// depend on the shape of tasks.RunView.
type RunResult struct {
	ID        tasks.RunID
	State     string          // terminal state name as stored by 05-tasks, e.g. "succeeded", "failed", "timed_out"
	Succeeded bool
	Result    json.RawMessage // the run's encoded result (R of the TaskSpec)
	Error     string          // the run's error text when !Succeeded
}

type Client struct{ /* unexported */ }

func (c *Client) Do(req *http.Request) *http.Response
func (c *Client) GetJSON(t testing.TB, path string, out any) int // returns status

// AssertPermission checks that path answers 403 for a user lacking perm and
// does not answer 401/403 for a user holding exactly perm (R6 per route).
func AssertPermission(t testing.TB, h *Harness, method, path string, perm rbac.Permission)

// AssertRoutesHavePermissions walks the built router (R6, archtest twin).
func AssertRoutesHavePermissions(t testing.TB, h *Harness)

// AssertModuleInert proves gate G1 for a disabled module: no route answers
// under /api/<id>/ or its legacy prefixes (404, not 403), no task kind or
// schedule with prefix "<id>." is registered, no worker or hook ran, the
// module is absent from GET /api/core/v1/modules, RecordingFS saw no
// mutation attributed to it, its persisted runs (see WithRuns) are listed as
// held and none was dispatched, its outbox backlog is retained, and boot did
// not trip the op-ledger gate (COR-13). Its keyspace data is unchanged.
func AssertModuleInert(t testing.TB, h *Harness, id module.ID)
```

Store conformance suites are test-support packages beside `coretest`: one `<pkg>/<pkg>test` per
store interface (`authtest`, `rbactest`, `configtest`, `logtest`, `taskstest`, `aitest`,
`providerhttptest`, `integrationstest`, `notifytest`, `storage/index/indextest`; COR6-9), each exporting
`<Interface>Suite` and the fakes of its interface, named by the 05-storage §5S.3.11 rule
(`<pkg>test.<Interface>Suite`, `TestNative<Interface>_RoundTrip`, SIMP5-9). They are SDK (an app's
legacy stores run them, and the API-stability gate freezes them), may import any core package, and
no non-test core package imports them; 03 §3.3 lists them (MOD5-6). Each lands with its interface.
The cases for legacy-only capabilities and for `legacy.LibraryRootSource` (formerly `librarytest`) are not
SDK: they live in `legacy/legacytest`, outside the API promise with `legacy` (§6.4.11, R6-3).

**`coretest/crashmatrix` (R9-7, COR9-5).** The boot crash-matrix harness is a public test kit, not a
test inside `core/internal/kernel`, because a core package test can import neither an app's modules
(03 R1) nor build an app's previous release. 05-storage §5S.3.16 owns the invariants it asserts and
the state diagrams; this file owns the writer table it walks (§6.5.1 "Boot writers").

```go
package crashmatrix

// Scenario is one boot to break: Build boots the current binary in process
// through inj.New(ctx, opts, mods...), the kit's wrapper of app.New that
// wires the fault-injecting Pebble vfs and the labelled state-directory FS
// (an app passes its own modules and Options, so its boot writers run; no
// core/internal type is exposed); Prev is the path of the previous
// release's binary, which reboots every crashed state (core's instance
// builds hello from the PR's merge base; an empty Prev checks the current
// build only, COR10-7); Fixtures are the
// starting stores and state directories (an empty store, a staged restore
// without hooks, a staged restore with hooks from S4 r2, each reset kind,
// an open checkpoint, ...).
type Scenario struct {
	Build    func(t testing.TB, inj *Injector) error
	Prev     string
	Fixtures []Fixture
}

// Run breaks the boot at every write, rename and fsync that Pebble's vfs
// error injection and the labelled state-directory FS observe, in two
// modes: a kill, and an error return (ENOSPC, then EACCES) from that
// operation, so a non-crash failure, such as a failed state-file copy
// inside the swap, must also leave a legal state (R10-6, DATA10-4). It
// reboots with the current build and with Prev, and asserts 05-storage
// §5S.3.16's invariants. It fails on any durable write it observes that
// carries no writer-table label, and, after each boot, diffs the DB parent
// and state-directory trees against its label log, so an unlabelled new or
// changed file written past both seams fails the run too
// (TestBootCrashMatrixCoversEveryWriter, COR10-7).
func Run(t *testing.T, s Scenario)
```

It is SDK test support like the store suites: it may import any core package, and no non-test core
package imports it. Core runs it over the kernel's writers with hello (`TestBootCrashMatrix`, core,
S2b). ABO runs the same kit over its full boot (`TestBootCrashMatrix_ABO`, root module,
`extraction-gates`, from S2b), with books and HEAD fixtures and `Prev` built from `PREV_REF`, so books'
P4d step 1 `Open`, its start hooks, the post-reset hook and `auth.BootWrites` over books' store are
broken at every write. Books' boot-time writes outside Pebble (the legacy key file, the gap-fill
file, the post-reset hook's deletions and, until S4 r2, HEAD's `.bootstrap-token` and `.readonly-key`
files, COR10-7) go through the labelled state FS the kernel hands books'
legacy config store (05-config's `legacy.ConfigOpenEnv`), so the kit sees and labels them.

`coretest/fakes` provides in-memory implementations of every built capability contract (download,
metadata, ai, notify; indexer once built) so module tests never need a network. Time-dependent tests run inside
`synctest.Test(t, func(t *testing.T) { h := coretest.New(t, …) … })`; the harness starts no real
timers outside the bubble.

### 6.4.9 Deferred-build members

Added additively in the build that creates the package (03 §3.3 "public, later"; cross-cutting
resolution 1). Until then the `module` package does not import these packages, and no core v1
symbol refers to them. The packages they wait for are design sketches (non-normative; A-15): 10,
05-indexer, 08 and the deferred parts of 05-download, 05-ai and 05-notify. The v1 SDK and the
API-stability gate freeze none of these members; the build that creates a package writes its
contract then, with the sketch as input. The table is a reader's index, not the input of a check.

| Member | Waits for | Purpose |
|---|---|---|
| `module.Manifest.Acquire` | `acquire` (10) | manifest trigger for building the acquire service in P5 (A8) |
| `module.AcquireDecl` | `acquire` (10) | `{Vocabulary, Categories}`, the type of `Manifest.Acquire` |
| `module.Deps.Acquire` | `acquire` (10) | `acquire.Register[T](d.Acquire, mediaType)` in `Register` attaches typed hooks only |
| `module.Deps.Indexers` | `integrations/indexer` (05-indexer) | `SearchAll`, `RSS`, `Fetch`, `Caps` for module search tasks |
| `module.Registrar.ArrFacade` | `arrfacade` (08) | at most one per module; checks in P7 |
| `module.ProblemFacadeIncomplete` | `arrfacade` (08) | appended to the `ProblemKind` block, so no built kind is renumbered (SIMP3-11) |
| `module.ProblemFacadeURLBaseCollision` | `arrfacade` (08) | as above |
| `module.Registrar.DownloadWatch` | `integrations/download` monitor (05-download phase 2) | the grab/monitor pipeline, built with `acquire`; ABO does not use it (SIMP-12); listed here only, 05-download cites this row (SIMP6-8) |

Facade-only members of other built packages are deferred the same way and listed under their owners'
headings: `auth.KeySpec.Purpose`/`Module`, `auth.PurposeFacade` and query-string key extraction
(05-authn §3.5), `httpapi.Router.FacadeGroup`, `httpapi.RouteKindFacade` and the stage-13 facade
clause (05-http-api §3.6), all landing with `arrfacade` (08 §8.0, SIMP2-6), so S2 and S4 do not build
them. Storage grants (`storage.Spec.Grants` and their modes) and the `Online` data-migration members
(`datamigrate.Online`, `storage.ReadAdapter`, `storage.Spec.ReadAdapters`, …) are 05-storage's
(§5S.3.14, R4-1, SIMP5-7); with the latter go the kernel-synthesised `<ID>.datamigrate.<name>`
definition and its implicit `TaskLedger` entry (COR4-6) and their tests, whose one name is
05-storage's `TestModuleOnlineMigrationRunsAsModuleKind`, run over a test-module fixture rather than
hello (COR5-17). A grant problem kind is appended to the `ProblemKind` block only with the grants;
the seed-role operator diff is deferred-build with the role cutover (05-rbac §4.8, SIMP4-6).

### 6.4.10 Landing stage of each SDK member (COR2-9, MOD2-10)

A member exists from the 09 stage that lands the package it names (04 §8.1); before that it is
absent, not stubbed, and the kernel phase that consumes it is skipped (§6.5.1). Each later member is
added additively. `rbac`'s leaf types land at S2 so `httpapi.Route.Perm` and the `auth` leaf compile
there: `Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`, `Public`, `AnyAuthenticated`,
`Delegated`, plus the `Checker` interface and `RoleName` that `auth.Principal` and `auth.User` name,
and the legacy requirements `LegacyAtom(atom)` and `LegacyCheck(atom, readSide)` as values that only
carry the atom, so every `s.perm` entry of books' S2 legacy route manifest records
`rbac.LegacyAtom(atom)` from the PR that creates the manifest; their evaluation (`Catalog.LegacyReach`,
`Catalog.Legacy`) and `Freeze`'s refusal of an unknown atom land with the checker at S4, and the
manifest is consulted only from S4 r2, when stage 13 runs for the mount (COR6-1). 05-rbac §3.1 decides
the list and 04 §8.1 and 05-http-api §3 repeat it (COR2-9, MOD4-11, COR4-11). The `auth` leaf is
`MethodSpec`, `Principal`, `User`, `UserID`, `UserStatus` and `Assurance`. A `legacy.Stores` field
(§6.4.11) lands with its subsystem; until then that subsystem does not exist, and books' own code
serves those rows exactly as at HEAD.

| Stage | `module.Manifest` (and `legacy`, §6.4.11) | `module.Registrar` | `module.Deps` | `app` |
|---|---|---|---|---|
| S2 | `ID`, `Version`, `DisplayName`, `MinCoreVersion`, `DefaultEnabled`, `Settings`, `UI` (IDs only), `LegacyPrefixes`, `PublicRoutes`; package `legacy` with `ConfigExternalWriter` only | `Context`, `Deps`, `HTTP`, `MountLegacy`, `AuthMethod` (recording only; the `auth` leaf types land at S2, SEC3-14), `LogPathRules`, `LogOtherUserData` (recorded from S2, consumed when the journal query lands, 05-logging), `Hook`, `Worker`, `Err` | `Module`, `Log`, `Metrics`, `ExtendStart` | `Options` fields `AppID`, `MetricPrefix`, `MetricNamespace` (A-5), `ServiceName` (MOD10-2, MOD10-11; the kernel passes it as both `metrics.Options.ServiceName` and `httpapi.Options.ServerName`), `MetricViews` (R10-3, MOD10-6), `ServedMeterProvider` (S2 r1–r2 only, deleted in the S2 r3 PR; MOD10-5), `HTTPMetricsScope` (R11-1), `ReservedAPINamespaces`, `DisplayName`, `Version`, `EnvPrefix`, `StateDir`, `ConfigFile`, `Args`, `Env`, `LegacyConfig`, `LegacyConfigStore` (the `config.OverrideStore` interface lands here with its read members, naming no `storage` or `secrets` type; its `Changed` is the optional `legacy.ConfigExternalWriter`, S2 to the S3 PR; COR5-14, SIMP5-5), `PromRegisterer` (set from S2 r3, OPS5-14), `LegacyAuthGinUntil`, `Logger`; `New`, `Run`, `Handler`, `Modules`, `ResolveStateDir`, `ExtendStart`, `Describe` (`Core`, `OpenAPI`) (COR3-3) |
| S2b | `Storage`; `LegacyStores` (type `legacy.Stores`, with `legacy.Handles`) with its member `Config` and the native `config.OverrideStore` (read only: `Load`, plus the legacy store's optional `legacy.ConfigOpener` (`Open`, P4d step 1; it lands here, and at S2 config calls no `Open`, COR6-8) and `legacy.ConfigExternalWriter` (`Changed`); config writes nothing to the store before S3; COR5-14, SIMP5-4); `legacy.Maintenance` (`StageFactoryReset` with a `storage.ResetRequest`; R7-2, R8-1, SIMP8-4, R9-6), `legacy.ConfigResetArgs` (`ResetArgs`, over the same `storage.ResetRequest`, which lands here in 05-storage), `legacy.ConfigResetPlan` (with its hook directories, recorded in the intent as `hook_paths`, R10-6, COR10-4), `legacy.ConfigResetRun`, the `legacy.ConfigOpenEnv.Reset` field, and `legacy.ConfigOpenEnv.Files` with its type `legacy.StateFiles` (the labelled state filesystem; COR9-5, COR10-10) (05-storage's one list; COR8-9, SIMP8-7); the test kit `coretest/crashmatrix` (§6.4.8, R9-7) | — | `Storage` (`Keyspace`, `Legacy`, `Aux`, and `Maintenance` for the module that declares legacy claims, R7-2) | `OpenOffline`, `Offline` (R7-4) with `StagingMarker` and `WriteStagingMarker`, `ReadStagingMarker`, `OfflineCommands` and `Command` (each command present from its owner's stage; R9-9, COR9-6); `Options.CompatLedger`; `BinaryID`, which delegates to `buildid.Binary` (the leaf lands at S0, FE6-3; its first kernel consumer is storage's self-served record, R5-7); `Options.LegacyConfigStore` deleted (SIMP4-11) |
| S3 | `legacy.Stores.Config` also written by core config (the native write member `Stage`, and the legacy store's optional `legacy.ConfigSecrets` (`Secrets`, the legacy secrets for `secrets.Open`, in HEAD's representation, A-2) and the post-commit step the store registers on each batch it stages into with `storage.OnCommitted` (the gap-fill write, which `storage.CommitLocked` runs before it releases the locks; there is no `legacy.ConfigCommitObserver`, R10-5); `legacy.WriteLock` on books' config store, with `legacy.LockConfigSettings` (HEAD's `configBlobSaveMu`; COR10-10), resolved by the kernel into config's `SettingsLocks`; 05-config §1.3 owns the members and 05-storage §5S.3.11 "Legacy capabilities" the list; `legacy.ConfigExternalWriter` deleted in the S3 PR, SIMP5-5; COR5-14) | — | `Secrets` | — (`LegacyConfig` stays: it carries names, not the codec, COR4-3) |
| S4 | `Permissions`, `Roles`, `LegacyAliases` (own namespace), `LegacyRoleIDs`; `PublicRoutes` emptied for hello; `legacy.Stores.Auth`, `legacy.Stores.Roles` (the `rbac.LegacyAtom`/`LegacyCheck` values exist from S2, their evaluation lands here, COR6-1); `legacy.WriteLock` on books' identity store (`WriteLocks` over 05-authn §3.4's lock names: HEAD's `userWriteMu`, `sessionWriteMu`, `apiKeyMu`, `inviteMu`, `bootstrapMu` and `userMergeApplyMu`; not `counterMu`; the kernel calls it and hands `auth` the resolved `auth.Deps.Locks` and `auth.RestoreDeps.Locks`, COR10-5), with `storage.CommitLocked` and `storage.LocksHeld(b, l)`, held by every core commit of a batch that store staged into, from S4 r2, the first release in which core writes identity rows (R9-1, COR9-1, DATA9-1) | `AuthMethod` also handed to `auth` | `Auth`, `AuthAdmin`, `Audit` | `Options.Authenticators`, `Options.Auth`, `Options.LegacyPermissionAliases` (COR5-5), `Options.LegacyAmbientScopes` (05-rbac §6.3, A-2); `LegacyAuthGinUntil` cleared by the release in which core auth decides requests (§6.5.1 "Stages"); the kernel declares the restart-bound `core.backup.dir` (alias `backup_dir`) in its own core-section list, so `backup/journal` imports no `config`, and registers `backup/journal`'s `storage.Spec` for the core keyspace `c/backup/` (owner `backup`; `Serialized: ["journal_head", "served_boot"]`, prefixes `journal_gap/` and `restore_hooked/`; `served_boot` is `Serialized` because the journaling boot writes it with `GetForUpdate`, 05-backup, COR6-6), so a revocation's journal head and gap line commit from S4 r2; `backup` adopts both at S8 (COR4-10, COR5-6); at S4 r2 the kernel mounts `internal/storagehttp`'s reset route, books' two legacy paths become its `Alias`es, and `legacy.Maintenance` and `Storage.Maintenance` are deleted in that PR with books' two gin handlers (§6.4.6, R7-2, R8-1, SIMP8-4) |
| S5 | `TaskLedger`, `TaskStarts`, `DataMigrations` (`BootBlocking` only; `Online` deferred, SIMP5-7); `LegacyStores.Tasks`, `LegacyStores.Logs` (`legacy.Stores` members), `legacy.TasksRecoverer`, `legacy.TasksScheduleTicks` (the v3 loop's tick reports into core's scheduler instruments until T7; 05-tasks 5T.3.13, R9-3), `legacy.WriteLock` on the run store (HEAD's `opsMu` behind `legacy.LockRuns`, asserted by the kernel on the built `Stores.Tasks` value, which resolves it and hands the executor `RunLocks`, 03 §3.3, COR10-5; `Clocks`, `Pause` and `StandDown` share its hold; `Executor.WriteRuns(ctx, open, stage)` and `Stager.WriteRuns` are uses of `storage.CommitLocked`, and a nested `WriteRuns` fails with `ErrWriteRunsNested`; R9-1, COR9-8) | `Tasks`, `Resources`, `Lanes`, `ChangeSink`, `Topics`, `Subscribe`, `Health` | `Tasks`, `Events`, `Health` (readiness reporters: the `health` readiness core, `ComponentID`, `Reporter` and the `Ready` view that dispatch waits on, lands with `tasks`, COR4-8) | `Options.LegacyTaskAliases` (by T4, the first `core.*` kinds with ABO former IDs, MOD6-6) |
| S6 | `OutboxConsumers` | `OutboxHandler`, `Realtime` | `Outbox` | — |
| S7 | `Requires`, `Library`; `legacy.Stores.Roots` (with `legacy.LibraryRootSource`), `.AIJobs`, `.Gates`, `.Integrations`, `.NotifyRules` (books' dirty set reaches `storage/index` per index through `index.Spec.Dirty`, not the manifest, SIMP5-2) | `Adapter`, `AICapability`, `DerivedIndex` | `Storage.Indexes`, `Storage.Derived`, `Library`, `HTTP`, `Integrations`, `Downloads`, `AI`, `Notify` (with `integrations/notify`, SIMP3-5) | `Options.Adapters` |
| S8 | — | `StartupSteps`, `Diagnostics`, `Backup` | `Backup` (the rest of `health` lands behind the S5 `Health` handles) | `Options.Updater` (ABO adds `core.diagnostics.export` to `Options.LegacyTaskAliases`, MOD6-6); `Options.PromRegisterer` removed (MOD4-3) |
| S9 | `UI.LegacyRedirects` (books fills it in the release that moves its URLs to `/books/...`, 07 §7.19 step 5, which ships with step 4, so `ui_route` recording starts there, FE4-8, FE5-13) | — | — | `Options.UI`, `Options.UIOptions`, `Description.UIManifests` |

Package `ui` exists from S2 with one dependency-free leaf holding `ReservedSegments()` and the plain
types `ModuleManifest`, `UIIDs` and `Redirect`, so `module.ReservedIDs()` and 03's equality test hold
from S2 and `internal/modulehttp` (L6, S4) has a type for `modules[].ui`; the rest of `ui` lands at S9
(FE3-8, FE4-6, COR3-3; 04 §8.1).

`examples/hello` grows with the same stages (MOD2-10): S2 skeleton (module, `app`, a greeting route
without the stored count and with `Perm: rbac.Public` until core authentication exists, declared in
`Manifest.PublicRoutes` as `getGreeting`, and the `openapi` subcommand emitting `core/openapi/core.json`), storage and its seed migration 1 at S2b (COR5-8), its permissions and the
route's `rbac.Need(PermRead)` at S4, `hello.count` at S5, and `ui_embed.go`/`ui_nonembed.go` with its web package at S9. hello sets no
`LegacyStores`, so from S4 it and `coretest` run on core's native auth and role stores and from S5 on
the native run store, each landing with its subsystem (SIMP4-3, 03 §3.4 rule (b)). §6.12 shows the S9 state. G6
and G7 are proven from S5, the first stage at which every import of the full example exists
(§6.10).

### 6.4.11 `legacy`: the one package for legacy stores (R6-3)

```go
// Package legacy (github.com/falkcorp/mediacore/legacy) holds every
// identifier that exists only to serve an app's pre-core stores: the
// Stores constructor set an app sets on module.Manifest.LegacyStores, the Handles a
// constructor receives, the optional legacy-only capability interfaces,
// the legacy-only root source and, from S2b to the S4 r2 PR, the pre-core
// Maintenance handle (R6-3, SIMP6-2, SIMP6-3, R7-2, SIMP8-4). Like kernel/ it is
// excluded from the API-stability gate (12 SC1 and §12.4.2); unlike kernel/
// an app may import it (archtest.NoKernelImports does not name it), though
// examples/hello may not (archtest.SDKOnly). So a family's cutover deletes
// that family's member and capabilities here in a minor release, never a
// major. R4: L5 (it imports only the L0-L4 packages its signatures name;
// only module, kernel and apps import it; 03 §3.3 "Legacy-only identifiers").
package legacy

// Handles is what one constructor receives: the supplier's own claims,
// nothing wider (R8). It is 05-storage's former storage.LegacyHandles. It
// carries no extension family and no side keyspace: a legacy store reads and
// writes only its own legacy claims (A-2). S2b.
type Handles struct {
	Owner  storage.Owner       // the supplying module ("books")
	Legacy storage.LegacyRange // its claims, exactly as the module receives them
}

// Stores lists the core store interfaces a module may implement over its own
// legacy claims (R4-1, §6.11.6). Two models (SIMP5-1, 05-storage §5S.3.11
// "Adapter selection"): for an EXCLUSIVE store (Auth, Roles, Tasks, Logs,
// AIJobs) a set field replaces the native implementation; Config is
// exclusive per key: a key with an Alias.LegacyBlob field goes to the
// legacy store and every other stored key to native c/config/, so its
// backends value is legacy+native (03 §3.3, COR9-11); for a ROUTED
// store (Gates, Integrations, NotifyRules) the native store always serves,
// and the legacy one also serves the records its Covers accepts (a legacy
// half without the Covering capability covers every record, SIMP6-5). Roots
// is neither: library has no native root store, so LibraryRootSource is legacy-only
// as a whole and serves beside library's core.library.roots config entries
// (SIMP6-3 (b)). A nil field means the subsystem uses only its native
// implementation over c/<subsystem>/. ONE INSTANCE PER FAMILY (R9-1,
// DATA9-2): HEAD's locks are fields of one *PebbleStore
// (internal/database/pebble_store.go:226-244 @748fd787a), so the kernel
// calls each constructor exactly once per App (a serve process holds one
// App; a test process may hold several, each with its own store), at P4c with the
// supplier's one Handles value, and hands that same instance to every user
// (the subsystem, its route package, the boot reconcilers and hooks of P4d,
// the offline commands of an offline session); books' constructors and
// books' own store at P6 resolve to one memoised backing store per Handles
// value (05-storage §5S.9.3), so every HEAD lock is shared
// (TestLegacyStoresAreSingletons). The one other instance is P4d step 3's on
// a staged restore (Auth and Roles only), over the read-only pre-swap store,
// a different database, whose handles fail every write with
// storage.ErrReadOnly (COR7-8, §6.5.1). A constructor only captures its
// Handles (no goroutine, no I/O, no write), so it is safe for Describe. The interfaces
// it returns, and their native implementations, are owned by each
// subsystem's file and are SDK, frozen by the API-stability gate; each holds
// only the native contract. Every write method takes the caller's
// *storage.Batch (05-storage participant binding), except the methods
// 05-storage "Store interfaces" lists as committing their own batches
// (log.JournalStore; the maintenance methods Recover and Prune;
// providerhttp.GateStore, under the Registry's mutex, 05-metadata M2), none of
// which joins a multi-owner commit (COR5-12, MOD5-10, COR6-7, SIMP6-10).
// A legacy implementation is representation-neutral (A-2): it reads and
// writes HEAD's rows in HEAD's stored representation and nothing else (raw
// sess: rows and HEAD's one-time tokens, abk_ keys and roles holding legacy
// atoms only, plaintext blob secrets beside the four HEAD already encrypts),
// with no second family beside them, so every build of the extraction reads
// everything core wrote. Data with no HEAD form (native scopes, hashed
// sessions, mck_ keys, encrypted copies of plaintext secrets) exists only in
// native stores until the identity and secrets cutovers; what an interface
// member with no HEAD form does on a legacy store is its owner's
// (05-storage §5S.3.11). A legacy implementation keeps HEAD's lock
// discipline ACROSS CORE'S COMMIT (R9-1, COR9-1, DATA9-1): a store whose
// HEAD code serialises writes with an in-process mutex implements
// WriteLock (below), and core commits a batch that store staged into only
// through storage.CommitLocked, which holds the store's locks from staging
// until the commit returns; so a revoke never interleaves with a last-use
// touch (early fix 6) and an invite or bootstrap token is taken once, as at
// HEAD; HEAD's counterMu is not among those locks (nextID takes it around its
// own commit), and the one instance per family makes every allocator share
// it (05-storage §5S.3.2 item 4). Storage-checked writes (Expect,
// Serialized) bind native keyspaces and rows core newly writes only (A-3).
// The full-text dirty set is not a field: a module that keeps a pre-core dirty
// set passes it per index as index.Spec.Dirty (05-storage §5S.3.8, SIMP5-2).
// There is no result-journal store (SIMP5-3). 03 §3.3 "Store interfaces" is
// the one list (SIMP5-8). Each field lands with its subsystem (§6.4.10).
type Stores struct {
	Config       func(Handles) (config.OverrideStore, error)       // S2b: settings overrides and legacy secret copies (05-config)
	Auth         func(Handles) (auth.Store, error)                 // S4: users, API keys, OAuth links, invites, sessions (05-authn §3.4)
	Roles        func(Handles) (rbac.Store, error)                 // S4: roles (05-rbac §3.4)
	Tasks        func(Handles) (tasks.Stores, error)               // S5: runs, clocks, pause marker, scan stand-down marker (05-tasks §5T.3.13, MOD6-4)
	Logs         func(Handles) (log.JournalStore, error)           // S5: system-log rows the journal reads and prunes (05-logging)
	Roots        func(Handles) (LibraryRootSource, error)          // S7: library import roots, legacy-only (05-library, SIMP6-3)
	AIJobs       func(Handles) (ai.JobStore, error)                // S7: AI batch jobs (05-ai)
	Gates        func(Handles) (providerhttp.GateStore, error)     // S7, routed: provider holds and quota counters (05-metadata M2)
	Integrations func(Handles) (integrations.InstanceStore, error) // S7, routed: legacy-backed integration instances (05-metadata M1)
	NotifyRules  func(Handles) (notify.RuleStore, error)           // S7, routed: legacy notification rules (05-notify)
}

// LibraryRootSource is the former library.RootSource (renamed by 03 §3.3's
// naming rule, SIMP7-8): library's legacy import-root store, which has no
// native implementation (05-library §5L.3.1 owns the members and its
// record-ID helper LibraryCoverID, the former library.RouteKey;
// library.SourcedRoot is gone; a legacy-served root carries only HEAD's
// fields, the others fixed by the source, library.ErrRootFieldFixed, A-2). A legacy-served root takes no
// ReadShares (05-library, SIMP7-2). The kernel passes the built value to library as library.Options.LegacySource,
// typed any (R7-7). S7.
type LibraryRootSource interface{ /* members per 05-library §5L.3.1 */ }

// WriteLock is the pre-core write lock of a legacy store whose HEAD code
// serialises its writes with an in-process mutex (R9-1; 03 §3.3 states the
// rule, 05-storage §5S.3.2 and §5S.3.11 own the signature, storage.Lock,
// LockRank and storage.CommitLocked, and the owner files name each store's
// lock names): for ABO the identity store from S4 (HEAD's userWriteMu,
// sessionWriteMu, apiKeyMu, inviteMu, bootstrapMu and userMergeApplyMu
// behind 05-authn §3.4's LockUsers, LockSessions, LockAPIKeys, LockInvites,
// LockBootstrap and LockMerge; HEAD's counterMu is not one of them), the run
// store from S5 (opsMu behind 05-tasks 5T.3.13's LockRuns), and the config,
// integration-instance and notify-rule stores (configBlobSaveMu behind
// LockConfigSettings, one lock for the three). WriteLocks returns the
// storage.Lock values that guard the records named by names (lock names,
// never app key names, 03 R1).
//
// The kernel resolves the locks, never the owners (COR10-5): the Lock*
// names live in this package (L5), which no owner imports, so the kernel
// asserts WriteLock on every built store value at P4c (never on the Stores
// struct), calls WriteLocks with those names itself, and hands each owner,
// with the store, an owner-typed value that names no lock string:
// auth.Deps.Locks and auth.RestoreDeps.Locks (an auth.StoreLocks with one
// []storage.Lock per write kind: Users, Sessions, APIKeys, Invites,
// Bootstrap, Merge), the executor's RunLocks, and SettingsLocks for config
// (incl. secrets.Options.SettingsLocks), integrations and notify. The owner's commit path passes them to
// storage.CommitLocked(ctx, b, d, stage, locks...): it takes each distinct
// lock once, in ascending LockRank, before stage makes the batch's first
// store read, marks b with the set it holds, runs the post-commit steps the
// stores registered on b with storage.OnCommitted(b, storage.CommitStep{Key,
// Run}) (books' config store: the gap-fill file write) after Commit returns,
// and only then releases them, on every path (R10-5,
// DATA10-2); a nested CommitLocked joins the locks its ctx already holds
// (and fails with storage.ErrLockOrder if it needs a lower-ranked one).
// Every writer path HEAD covered with configBlobSaveMu goes through it.
// Executor.WriteRuns(ctx, open, stage) and Stager.WriteRuns (05-tasks; a
// nested WriteRuns fails with tasks.ErrWriteRunsNested) and auth's identity
// commits (05-authn) are uses of it.
//
// The hold lives on the batch (R10-4; COR10-2, DATA10-1): a legacy staging
// method checks storage.LocksHeld(b, l) on the batch it was handed and
// refuses a write to a guarded family when b does not hold that lock, with
// an owner-named error wrapping storage.ErrNotLocked, so
// the identity store (whose staging methods take no ctx), the run store,
// legacy.SecretsStore.Write and the config store's Stage are all checked,
// and a write staged inside a hold into another batch that commits later is
// refused too. A store method staged inside the hold never takes the lock
// again, and a writer HEAD commits without the lock keeps doing so
// (ReplaceOpRowsPrefix, stated in 05-tasks).
//
// One total order (R10-4, DATA10-3): every lock a commit path may take has
// a distinct LockRank in 05-storage §5S.3.2's one table, which extends
// HEAD's own orders; core-side mutexes (config Apply's, the integrations
// Registry's writer mutex, the rule store's and the gate Registry's) are
// taken before CommitLocked and never inside a stage, secrets' keyed mutex
// being the one exception, taken only inside a stage after its config lock;
// and no stage function calls a books method that takes a HEAD mutex of a
// family the hold covers (TestLockRanksTotalOrder). So HEAD's single-use
// invites and bootstrap tokens and early fix 6's revoke-versus-touch rule
// hold from S4 r2 (TestLegacyRevokeRacesTouch_NeverUnrevokes).
type WriteLock interface {
	WriteLocks(names []string) []storage.Lock
}

// Maintenance is the narrow kernel-built handle of books' pre-core reset
// routes until the core route (S4 r2) owns them (R7-2, COR7-1, R8-1,
// SIMP8-4). The kernel hands it as module.StorageHandles.Maintenance to the
// module that declares legacy claims, nil in an offline session (SIMP8-9);
// it is not storage.Admin, which stays kernel-only. StageFactoryReset is
// kernel.stageReset (§6.5.1 P0a, COR8-2) over the caller's
// storage.ResetRequest (R9-6): books' handlers pass their kind, the legacy
// confirmation marker and, for ResetFactory, delete_library_files (HEAD's
// wipe, A-17). It asks the built config store's ConfigResetArgs for the
// plan (an error, such as books' dangerous-RootDir refusal wrapping
// storage.ErrResetRefused, stages nothing, and the handler answers 400),
// then stages through the kernel-held restoreswap.Swapper.StageReset, which
// resolves each target's set-aside destination by 05-storage §5S.3.15's one
// rule (COR9-12; P0a moves them), pre-validates every target
// (storage.ErrRenameRefused) and creates the
// KindReset intent in the one swap slot restore/pending.json atomically
// with the live store's Pebble open stamp (05-storage §5S.3.15; nothing
// destructive before the restart), requests a restart (§6.5.2) and returns
// the swap ID. A second staging of any kind, from any process, fails with
// storage.ErrSwapPending. Books' gin handlers for POST /system/reset
// (restorejournal.ResetStore) and POST /system/factory-reset
// (restorejournal.ResetFactory) call it behind HEAD's own admin and owner
// chain and confirm interlock. S2b; deleted in the S4 r2 PR with those
// handlers. ABO's internal/backup reads books' Manifest.Storage.UpgradeFloor
// itself, so the handle carries no floors (SIMP8-4).
type Maintenance interface {
	StageFactoryReset(ctx context.Context, requestedBy string, req storage.ResetRequest) (string, error)
}

// ConfigResetArgs, ConfigResetPlan and ConfigResetRun (05-storage §5S.3.11
// and §5S.3.15 own them, and storage.ResetRequest, and win on member
// spelling; 05-config §1.3.3 owns the books side; R7-2, S2b).
// ConfigResetRun follows 03 §3.3's naming rule because only the config
// capability carries it (SIMP8-7).
//
// kernel.stageReset calls ResetArgs with the request exactly as the route,
// the alias or books' handler received it (R9-6, COR9-4, SEC9-4) on an
// app's legacy config store that implements it. It returns, for both
// kinds, the settings rows the fresh install starts with (books: HEAD's
// factoryDefaultConfig blob, root_dir empty, setup_complete false; the
// intent's fresh_rows, which the reset boot's fresh-install batch writes,
// SEC8-5), and for ResetFactory only the hook arguments (books: the Open
// Library dump directory, the gap-fill file <db parent>/config.yaml, and,
// only when req sets delete_library_files, the library RootDir; the
// intent's hook_args). With delete_library_files it runs the preflight
// BEFORE anything is staged: it resolves RootDir and counts the top-level
// entries the hook would remove (one directory read), returned as
// LibraryRoot and LibraryFiles; kernel.stageReset, not ResetArgs, compares
// them with req and, unless req.LegacyConfirm is set (HEAD's wipe with no
// count check), returns *storage.LibraryConfirmError (the route answers 422
// library_files_confirm_required with the current root and count; 05-storage
// §5S.3.15). Without delete_library_files it records no RootDir, counts
// nothing and runs no dangerous-root check. Any error refuses the reset with
// nothing staged.
//
// For a ResetFactory swap the kernel hands the applied swap back as
// legacy.ConfigOpenEnv.Reset, non-nil at Open on the reset boot (journal at
// set_aside) and on every later boot whose journal reads applied without
// hook_done (COR8-8); the store tests env.Reset != nil, and there is no
// separate ResetUnfinished flag (SIMP8-7). Delete lists the intent's
// delete entries: Recreatable auxiliary DBs the set-aside left in place
// because they sit outside the DB's filesystem, which the hook removes
// (COR8-3). DeleteLibraryFiles is the intent's delete_library_files, the
// only input that lets the hook wipe RootDir (COR9-4). Done journals
// hook_done and deletes inflight.json. For a ResetStore swap, and when the
// store does not implement both ConfigOpener and ConfigResetArgs, no Reset
// is handed and the kernel journals hook_done itself (COR8-4).
//
// Lifetime: the post-reset hook reaches books only through
// legacy.ConfigOpenEnv, which the settings cutover deletes with the legacy
// config store. The cutover must give the Open Library and RootDir wipes
// another carrier, or end them, and its release note says which
// (SIMP8-7, 14 Q-K).
type ConfigResetArgs interface {
	ResetArgs(ctx context.Context, req storage.ResetRequest) (ConfigResetPlan, error)
}
type ConfigResetPlan struct {
	Hook         []byte       // the intent's hook_args; ResetFactory only, nil for ResetStore
	Rows         []storage.KV // the intent's fresh_rows, written by the reset boot's fresh-install batch inside the module's claims
	LibraryRoot  string       // with delete_library_files: the directory the hook would wipe, as counted before staging (A-17, R9-6); "" otherwise
	LibraryFiles int          // with delete_library_files: the preflight's count of the top-level entries the hook would remove, which kernel.stageReset compares with the request; 0 otherwise
}
type ConfigResetRun struct {
	ID                 string
	Args               []byte   // ConfigResetPlan.Hook
	Delete             []string // the intent's delete entries
	DeleteLibraryFiles bool     // the intent's delete_library_files (A-17, COR9-4)
	Done               func(ctx context.Context, failed []string) error // journals hook_done with the entries it could not remove
}
```

**Legacy-only capabilities.** A member that only a pre-core store can give meaning to is an optional
interface of package `legacy`, never of the subsystem's own package (SIMP5-4, R6-3). This file spells
them `legacy.ConfigOpener` (`Open`, S2b, P4d step 1(i)), `legacy.ConfigExternalWriter` (`Changed`, S2
to the S3 PR, SIMP5-5), `legacy.ConfigSecrets` (`Secrets`, S3), `legacy.ConfigResetArgs` (`ResetArgs`, S2b, asserted and called by `kernel.stageReset`
with the `storage.ResetRequest`; R7-2, COR8-2, A-17, R9-6), `legacy.TasksRecoverer` (`Recover`, S5, run inside `Executor.Run`), `legacy.TasksScheduleTicks` (`ReportScheduleTick`, S5 until T7, asserted by the calling module on `Deps.Tasks`; 05-tasks 5T.3.13, R9-3) and `legacy.WriteLock` (`WriteLocks`; S3 on books' config store, S4 on its identity store, S5 on its run store, S7 on its integration-instance and notify-rule stores: the pre-core write lock that `storage.CommitLocked` holds from staging through the commit and the store's post-commit step, marking the batch so `storage.LocksHeld(b, l)` can refuse a write staged outside it, so `Executor.WriteRuns`, `Stager.WriteRuns` and auth's identity commits keep HEAD's mutexes across core's commit; the kernel calls `WriteLocks` and hands owners the resolved owner-typed locks; R9-1, A-3, R10-4, R10-5, COR10-5); the auth,
rbac and `Covering` capabilities follow the same rule, and `secrets.LegacyStore` is `legacy.SecretsStore`. They were
`config.LegacyOpener`, `config.ExternalWriter`, `config.LegacySecrets`, `config.CommitObserver` and
`tasks.Recoverer`; the spellings follow 03 §3.3's naming rule (an owner-specific identifier takes its
former package's name in Go initialism form, a cross-owner one none: `legacy.TasksRecoverer`,
`legacy.RBACAtomWriter`, `legacy.LibraryRootSource`; SIMP7-8), and 05-storage §5S.3.11 "Legacy
capabilities" owns the list and wins on names.

- **Detection (SIMP7-5, COR7-3).** An owner package (L1–L4) cannot import `legacy`, which imports it.
  Each capability is detected once, by the party that consumes it, and nothing records what was found.
  The kernel asserts only the capabilities 03 §3.3 lists, which are the ones it uses itself; 03 §3.3 is
  the one statement of that list and of why each is on it (SIMP8-3). In this file's phases: P4c asserts
  the driven ones, `legacy.Covering`, and `legacy.WriteLock` on every built store value, whose locks it
  resolves itself with the `legacy` lock names and hands to the store's owner as an owner-typed value
  that names no lock string (R9-1, COR10-5) (P4d step 1(i) calls `Open`, step 1(ii) passes `Secrets()` to
  `secrets.Open`, 05-config §1.3.3), and `kernel.stageReset` asserts the staging one at staging, for
  both reset kinds (COR8-2). Every other capability is called by its owner through an
  **unexported** mirror interface with the identical method set, asserted on the store value the kernel
  hands it. At S2, before P4 exists, config itself detects `legacy.ConfigExternalWriter` on
  `Options.LegacyConfigStore` when it attaches that store at P3. A native implementation implements
  none of them and has no stubs.
- **Mirror test (R7-7; 03 §3.3 states it once).** The owner's `export_test.go` (`package <owner>`)
  only exports each mirror as a test-only alias (`type CoveringMirror = covering`) and each unexported
  record-ID helper as a test-only variable. `legacy_mirror_test.go`, in the owner's **external** test
  package (`package <owner>_test`, which may import `legacy`), asserts both directions (`var _ legacy.X
  = (<owner>.XMirror)(nil)` and `var _ <owner>.XMirror = (legacy.X)(nil)`) and compares the helpers on
  a table, so a renamed or retyped member fails that owner's tests and no in-package test imports
  `legacy`, which would be an import cycle (COR7-2, SIMP7-1). For the run store's recoverer the owner
  is `internal/executor`.
- **Legacy-only values with no SDK interface (R7-7, SIMP7-4).** An owner that receives such a value
  types the option field `any`: `secrets.Options.LegacyStore` (the `legacy.SecretsStore` the kernel
  takes from `ConfigSecrets.Secrets()`) and `library.Options.LegacySource` (the built
  `legacy.LibraryRootSource`). The owner asserts its mirror at construction and returns
  `storage.ErrLegacyStoreType` on a mismatch, which fails boot, never a silent skip. The kernel only ever
  passes a value built by a typed `legacy.Stores` constructor, so the mirror test catches drift in CI,
  `legacy`'s method sets change freely (R6-3), and the field stays in v1, nil after the cutover.
- **Conformance.** The capability and `LibraryRootSource` cases live in `legacy/legacytest`, test-support
  outside the API promise like `legacy` (05-storage owns the suite pattern, SIMP6-6). An app's
  `TestLegacyStoreSuites` runs them beside the SDK interface suites `<pkg>/<pkg>test` (§6.4.8).
- **What stays outside `legacy`.** 05-storage §5S.3.11's table "Legacy-serving identifiers that stay
  in SDK packages" is the one list, with each identifier's reason and its state after the cutover
  (SIMP7-3); this file's members of it are these. Identifiers that serve legacy **surfaces** or claims
  rather than legacy stores keep their packages: `Manifest.LegacyPrefixes`, `LegacyAliases`, `LegacyRoleIDs`,
  `storage.LegacyRange`, `Registrar.MountLegacy`, `app.Options.LegacyConfig`,
  `LegacyPermissionAliases` and `LegacyTaskAliases`. `module.ProblemLegacyStore` also stays in
  `module`, because P2's backend check outlives the legacy stores: it also refuses a build that would
  supply a legacy store again over a store whose cutover recorded `native`. So does the field
  `Manifest.LegacyStores` itself: it stays in v1, and only its type's members are deleted (03 §3.3). `app.Options.LegacyConfigStore`
  is deleted in the S2b PR and `module.StorageHandles.Maintenance` in the S4 r2 PR (SIMP8-4), both long
  before any API promise.
- **Landing and lifetime.** `legacy` lands at S2 with `ConfigExternalWriter` alone (the S2 bridge,
  §6.4.5); `Handles`, `Stores` (with `Manifest.LegacyStores`), `Stores.Config`, `Maintenance`,
  `ConfigResetArgs`, `ConfigResetPlan` and `ConfigResetRun` land at S2b, and each later member with its subsystem
  (§6.4.10); `Maintenance` is deleted in the S4 r2 PR (SIMP8-4). A
  family's cutover build still sets its member, because it reads the legacy data through it. A later
  build, the first release after the cutover that no longer sets it (behind the cutover's
  `UpgradeFloor`, a minor; 12 §12.4.2, COR7-5), stops setting it, and the PR that removes the last
  setter deletes the member, its capabilities and their cases from `legacy` (§6.11.6).

## 6.5 Lifecycle

### 6.5.1 Boot phases (inside `app.New`): the one table

This table is the **only** normative statement of boot order (R2-8). 05-updater §5U.3, 05-backup
§5B.3.4, 05-config §1.4, 05-storage §5S.4, 05-authn §5, 05-rbac and 05-tasks describe what their own
step does and reference the phase named here; where one of them lists an order, this table wins.

**Write rule.** Nothing is written to the store before the pre-migration checkpoint (P4a). Before it,
the only writes are files outside the store that a pre-boot step owns (the one swap journal and its
renames, restores and resets alike, the set-aside of `storage.boot.json` by an accepted reset (COR8-8,
DATA8-13), the quarantine of an unparsable boot record), Pebble's own open (its `OPTIONS-<n>` stamp) and
in-memory buffers. Every every-boot writer of P4d (`secrets.Open`'s `c/secrets/meta`, revocation-journal
re-apply, post-restore hooks, `auth.BootWrites`) runs after the checkpoint and the migration DAG, in the
order of P4d, and before any listener (DATA2-24). The later every-boot writers, all still before
listeners (§6.5.2), are: books' start-hook repairs at Run step 2 (05-storage §5S.9.3: the journal-clock
repair, the identification-index stamp, and, until each successor lands, HEAD's `ReconcileOpsV3WithV2`
before books builds its ops registry, until S5, and HEAD's `ReconcileVerifiedEmails` and
`seedRolesAndTokens` sequence, until S4 r2, COR6-3), and, on a factory-reset boot only, books'
post-reset hook (below, P0a); the
served-ledger commit at Run step (3b) (R5-5); and the run store's start-time repair of stale run records
inside `Executor.Run` at Run step 5 (05-tasks), which is `ReconcileOpsV3WithV2`'s successor from S5
(COR5-7). Store constructors, legacy-store constructors and `Register` write nothing (DATA4-10): HEAD's
store-open writers get homes of their own, the import-path conversion as a books versioned migration
(P4b), the verified-email reconcile in books' start hook until S4 r2 and inside P4d step 4 from then;
counters need no seeding, because books reads an absent counter as HEAD's seed (05-storage §5S.9.3).
The registration-time writes (default permission grants, task definition snapshots) are P7's and are
listed in its row; the derived-index records `c/storage/derived/<module>/<name>`, and the rebuild run
they enqueue, are Run start step (3a)'s (§6.5.2): that enqueue only queues, and the run starts at Run
step 5 (COR3-15, COR4-16).

**Checked writes (A-3).** Storage-checked writes (`Expect`, `Serialized`, 05-storage's `CheckedWrites`)
in this table bind native rows only: core's `c/<subsystem>/` keyspaces (the served ledger, the owed
entries, `c/backup/`, `c/secrets/meta`, task definition snapshots, a native app's seed-role rows and
grants), a native module's `m/<ID>/` and every row core newly writes. A boot writer that writes an ABO
legacy family (P4b's books migrations, P4d step 2's revocations and step 4's writes through books'
legacy auth and role stores, P7's seed-role reconcile through books' role store, books' start-hook
repairs and post-reset hook, the reset boot's `fresh_rows`) keeps HEAD's lock discipline: keyed
mutexes, `apiKeyMu` with early fix 6, and the single-writer AST ratchet. A legacy store that stages into a
caller's batch has its HEAD mutexes held across that batch's commit: core commits the batch only through
`storage.CommitLocked` with the store's `legacy.WriteLock` locks (§6.4.11, R9-1), and every user reaches the
one instance of the store the kernel built, so the locks are shared (`TestLegacyStoresAreSingletons`). It
may also stage an `Expect` or `GetForUpdate` on a key of a `Serialized` legacy claim it read, as a
voluntary compare-and-set that storage honours but never requires (05-storage §5S.3.2). Only the shadow → strict
rollout of claim (key-ownership) checks applies to legacy ranges.

**One stored format per family; the app supplies its own legacy stores (R3-1, R4-1).** No boot phase
copies, mirrors, adopts or rebuilds a family. The data ABO already keeps for generic services (the
rows behind each store interface of 03 §3.3, the one list, SIMP5-8) stays in books' legacy claims for
the whole extraction. The core subsystem that serves it calls **books' own implementation** of the
subsystem's store interface (`Manifest.LegacyStores`, a `legacy.Stores` value, §6.4.11; the full-text dirty set per index through
`index.Spec.Dirty`, SIMP5-2), ABO code over ABO keys, built at P4c over books' own `Legacy` range:
core holds no audiobook key name or row
format (03 R1) and no core subsystem reads a module's rows, so there are no storage grants during
the extraction. An app that supplies none (hello, `coretest`, a TV app) gets core's native store over
`c/<subsystem>/`. **The extraction is representation-neutral (A-2, ADV-S1).** Books' legacy stores
keep HEAD's stored representation through S10: raw `sess:` rows and HEAD's one-time tokens, `abk_`
keys holding legacy atoms, roles holding legacy atoms only, plaintext blob secrets (the four HEAD
already encrypts stay encrypted) and HEAD's auth-enable precedence (the stored value wins). No boot
phase rewrites a legacy row into another representation, and no legacy store keeps a second family
beside HEAD's rows, so a rollback to any build of the extraction, the hotfixed HEAD included, reads
exactly what core wrote. Core-namespaced scopes are an API-level mapping over legacy atoms
(`rbac.LegacyAtom`, 05-rbac §6.3), so D7 holds, and a legacy-backed role or key cannot hold a scope
that has no legacy atom. The native stores of hello, `coretest` and the TV and Movies apps keep the
full native design (hashed sessions, `mck_` keys, native scopes, encrypted secrets). For ABO that
hardening arrives with the post-S10 identity, secrets and auth-precedence cutovers, on N15's one-way,
floored machinery (12, 14). Which implementation serves a record is fixed by the build's manifests (exclusive
stores) or by the legacy store's `Covers` (routed stores, SIMP5-1), and the choice is recorded in the
store; a build whose choice differs fails P2, or P4c when only the full value differs (`ProblemLegacyStore`, 05-storage `ErrLegacyStoreMissing`, COR7-4). Moving a family to a
native core keyspace and deleting the legacy one is the **cutover**, out of the extraction: a later,
per-family, one-way step, checkpointed by P4a and guarded by the owner's level like any format change;
storage grants exist only there (05-storage).

| Phase | Step | Store writes | On failure |
|---|---|---|---|
| P−1 | **Deferred-build (A-6, ADV-S5).** `updater.Boot` (05-updater §5U.3) with the boot-attempt counter and the automatic rollback to `.prev`: no S0–S10 build runs this phase. The S8 updater is HEAD's S0-hotfixed updater moved into `core/updater`, parameterised by release source; it keeps exit 75, the `.prev` slot, semver comparison, the level and floor checks and check-only unattended mode, and has no boot check (05-updater). When the boot check is built it runs here, before P0a, over pre-boot inputs only (the state directory `config.ResolveStateDir` resolved once, §6.4.5 `StateDir`, R3-4, OPS5-13), so it can decide a rollback before any restore swap and count every later refusal as a failed boot; 05-updater's sketch owns its rules (OPS2-3, OPS4-9, OPS5-15) | none | — |
| P0a | Pre-boot swap (05-backup §5B.3.4), with the state directory `config.ResolveStateDir` resolved once from flags, env and `Options.StateDir` with HEAD's fallback, pre-boot inputs only, never the DB (§6.4.5 `StateDir`; R3-4, OPS5-13). There is **one swap mechanism** for restores and factory resets (R8-1, SIMP8-1): one intent slot, `<state_dir>/restore/pending.json` carrying its `kind`, created atomically (a temp file `link(2)`ed into place, failing with `EEXIST`), so a second staging of any kind, from any process, fails with the one sentinel `storage.ErrSwapPending` and no staging mutex or precedence rule exists; one rejection rule for every kind (an expired `expires_at`, a `staged_by_build` naming another binary, or a recorded Pebble open stamp `OPTIONS-<n>` that differs from the live store's, because another binary opened the store since, rejects the intent, logged as `restore_rejected`); one journal, `restore/<id>/journal.json` (records of `storage/restorejournal`); and one token pair, `restore_pending`/`restore_inflight`, with one override (below). If the intent exists, swap using the **absolute targets recorded in it** (DATA2-25). The intent is renamed to `restore/<id>/inflight.json` (for a checkpoint restore, `KindCheckpoint`, `restore/<id>/inflight-checkpoint.json`, so `CheckReader` and the rollback script apply its exception from the path alone; 05-storage §5S.3.7, R7-4) **before the first rename of every kind** (R9-7, DATA9-3): for a restore after the journal reads `rechecked` and before step c's first move (`previous_moved`), for a reset at `set_aside_started`; step g then only journals `swapped`, so `CheckReader` reports `restore_inflight`, which no override passes, for every state in which a directory is half moved. **Every state has a failure exit (R10-6; DATA10-4, COR10-4).** A non-crash failure after that rename and before the first journalled move (an I/O error on a state-file copy, `EACCES`, `EBUSY` or `EXDEV` on a live rename) takes machine 1's edge out of the state it stopped in: a restore at `rechecked` runs step x, which moves back whatever c moved (rename-if-exists) and journals `swapped_back`; a reset that stopped before `set_aside_started` journals `rejected` (nothing moved) and renames `inflight.json` to `restore/<id>/rejected.json`, while one that stopped at or after `set_aside_started` first moves every moved target back, as 05-storage §5S.3.15's failure rule says (R11-5: one name for the reset state, 05-storage's); each boots the untouched store degraded (`restore_failed`, `factory_reset_failed`), so no such failure wedges every later boot behind `restore_inflight`. Step c's copies of `<state_dir>/backup.location.json` and `<state_dir>/storage.boot.json` into `restore/<id>/`, and the reset set-aside's copy of `backup.location.json`, are **copy-if-exists**: the location file has one writer, from S4 r2, so on S2b–S4 r1 and on a host-loss restore it is absent and that is not an error; step x and the manual undo restore only the copies that were saved (05-backup step c, 05-storage §5S.3.15 and §5S.3.16 own the edges; `TestRestoreWithoutLocationFileS2b`). It is not deleted, and is removed only when the restore journal reaches `applied` or `swapped_back`; P0a also resumes any journal whose state lies between `previous_moved` and `applied`, and a `KindCheckpoint` journal at `failed` that this build did not fail (`failed_by_build`; DATA8-6). From S2b it also resumes a journal that S1–S2 r3's ABO-local swap left in flight: that swap follows 05-backup §5B.3.4 exactly (the in-flight rename after `rechecked`, `applied` with no lineage write) and writes the same `restorejournal` JSON shape (09 S1 owns it; DATA10-8, `TestS2bResumesS1Journal`). The kernel decides "this is a restore boot" from that journal, never from `pending.json` alone, so a crash between the swap and P4d's hooks re-runs the (idempotent) hooks at the next boot, and `CheckReader`, `make rollback` and `deploy` refuse while such a journal exists (DATA3-6). The restore journal identifies the boot that applies it: at step e restoreswap records the full stat identity of every sidecar it rewrote (books' `.storage-format`) and the highest `OPTIONS-<n>` in the staged DB directory in `restore/<id>/journal.json` (step `sidecars`), and the kernel passes that recorded identity to storage's open at P2 (05-storage §5S.4 step 2), so storage reads its own rewrite as the restore, never as a foreign open, and never reads the journal file itself (R6-4, DATA6-2, DATA7-18). The journal's record types are storage-level. `core/internal/restoreswap` is L1 and imports `storage` and its L0 leaves, never the reverse: the staging and swap methods are not on `storage.Admin`, only the kernel imports and runs `restoreswap` (at P0a, from `kernel.stageReset` below, and for `backup`'s staging from S8), and neither storage nor restoreswap reaches L4 `backup/journal` (COR7-10, COR8-5). A swap back at step x appends a `restore_aborted {seq}` journal line, or the next boot appends it once the keyring is open (DATA6-3). P2 verifies that storage opens at the recorded DB path; if not, the swap is undone. **Factory reset (`KindReset`; 05-storage §5S.3.15 and 05-backup own the procedure; DATA6-6, R7-2, R8-1).** Two kinds, `restorejournal.ResetKind` (COR8-1): `ResetStore` for HEAD's `POST /system/reset` (store and settings only, no file effect, no auxiliary DB, no hook) and `ResetFactory` for `POST /system/factory-reset` (adds the auxiliary DBs and the post-reset hook). A reset is staged, never run, by the request: from S2b books' gin handlers for both legacy routes, behind HEAD's admin and owner chain unchanged, call `legacy.Maintenance.StageFactoryReset` with their kind (§6.4.11), and from S4 r2 both legacy paths are `Alias`es of the `internal/storagehttp` route (§6.4.6); HEAD's `PebbleStore.Reset` is deleted in the S2b PR. Staging is one kernel function, `kernel.stageReset(ctx, requestedBy, req storage.ResetRequest)` (COR8-2, A-17, R9-6; 05-storage owns the request, §6.4.6 states the one contract): it calls `legacy.ConfigResetArgs.ResetArgs(ctx, req)` with the same request when the built config store has it, which returns a `ConfigResetPlan` (the fresh settings rows for both kinds, the hook arguments for `ResetFactory`) or an error that stages nothing: with `delete_library_files` on the native route, a `library_root` or `library_file_count` that does not match a fresh preflight count (422 `library_files_confirm_required`), and, only when the wipe is requested, books' error wrapping `storage.ErrResetRefused` for a dangerous `RootDir` (400), and, when the built config store has no `legacy.ConfigResetArgs`, refuses `delete_library_files` with `storage.ErrResetRequestInvalid` (422, COR10-9), then stages through the kernel-held `restoreswap.Swapper.StageReset`, which pre-validates every target (`storage.ErrRenameRefused`, nothing staged) and writes the intent with `hook_args`, `hook_paths` (the hook's directories, which `ConfigResetPlan` returns explicitly: the Open Library dump directory and, only with `delete_library_files`, the library root; R10-6, COR10-4), `fresh_rows`, `delete` and the live open stamp, and then calls `RequestRestart` (§6.5.2, OPS8-12). An accepted reset renames the intent to `restore/<id>/inflight.json`, kept until `hook_done` (not `applied`), and sets every target aside **beside itself, on its own filesystem** (COR8-3, DATA8-1), by one rule that 05-storage §5S.3.15 owns (COR9-12): a target under `<db parent>` (the DB directory, its sidecars, the derived and index directories, this store's checkpoints and, for `ResetFactory`, the auxiliary DBs under `<db parent>/aux/<module>/<name>`) goes to `<db parent>/.mediacore-restore/<id>/previous/`; a target under the state directory (`config.boot.json`, `storage.boot.json`) to `<state_dir>/restore/<id>/previous/`; every other target, such as an auxiliary DB at a module's `LegacyPath` (books' Open Library store, by default under `RootDir`), to `<its parent>/.mediacore-restore/<id>/previous/`, whichever filesystem it is on, except that a `Recreatable` auxiliary DB outside the DB's filesystem is not moved but listed in the intent's `delete`, for the post-reset hook to remove (or the kernel, with no app hook); each `moved` record names its source and destination. P0a re-runs those renames only while the journal's last step precedes `set_aside`; from `set_aside` on, `inflight.json` only gates `CheckReader` and `legacy.ConfigOpenEnv.Reset` (DATA8-12). A set-aside rename that fails moves back what it moved, journals `rejected`, renames `inflight.json` to `restore/<id>/rejected.json` (COR9-12) and boots the untouched store `degraded` (`factory_reset_failed`). A reset journal stopped mid-set-aside (`set_aside_started` or a `moved` record) that a later boot does not resume (step a's rule: an expired intent, another binary) likewise **always moves back every recorded `moved` entry, in reverse journal order, before it journals `rejected`**, so no boot ever starts a fresh install over a half-moved store and leaves the old one for the `previous/` sweep (R9-6, DATA9-4; 05-storage §5S.3.16 owns the transition). An applied reset makes this boot a fresh install whose lineage verdict is none (a new `install_id`, the fresh-install batch of P4a, seed steps run); that batch also writes the intent's `fresh_rows` inside the declaring module's claims (books: HEAD's `factoryDefaultConfig` blob, `root_dir` empty, `setup_complete` false, the same for both kinds, rendered at staging by `ResetArgs`), so a stale `ENABLE_AUTH=false` loses to its stored `enable_auth: true` as after HEAD's reset (SEC8-5, 05-config §1.12.2). The revocation journal and the keyring header are not P0a's (05-storage §5S.3.15 places them: the journal stays at `set_aside` until P4d step 1 has rewritten the keyring header's `install_id`, then reads `applied`; DATA7-17). Nothing destructive runs before the restart, and the live settings blob is never written with defaults: the blob above is a fresh-store write after the restart. HEAD's in-request side effects of `ResetFactory` (the gap-fill file `<db parent>/config.yaml` reset, the Open Library data and, only when the staging set `delete_library_files`, the library `RootDir` wipe, which the legacy alias always sets and the native route only on request, A-17, 14 Q-K) run in books' post-reset hook, a books start hook at Run step 2. For a `ResetFactory` swap the kernel hands it `legacy.ConfigOpenEnv.Reset` (a `*legacy.ConfigResetRun` with the intent's `hook_args` and `delete`) at `Open` on the reset boot, while the journal still reads `set_aside`, and on every later boot whose journal reads `applied` without `hook_done`; the hook runs at Run step 2, after `applied` (COR8-8). The hook's deletions go through the labelled state filesystem (`legacy.ConfigOpenEnv.Files`), which the kernel bounds, without parsing `hook_args`, by the state directory, `<db parent>`, the intent's `delete` entries and its `hook_paths`, so HEAD's wipe of the Open Library data and of `RootDir`'s entries reaches paths outside the state directory and `<db parent>` and the legacy alias keeps it (A-17; 05-storage §5S.3.15 owns the bound). The hook never fails boot: per-entry errors are logged, audited and reported as `factory_reset_cleanup_incomplete`; it skips every entry that contains `<db parent>`, the state directory or a set-aside destination, and `StateFiles` applies the same kept-path rule itself, so a library root that holds the store cannot reach it; it always ends with `ConfigResetRun.Done`, which journals `hook_done` and deletes `inflight.json`, so only a crash before `Done` reruns it; and its runtime is bounded per batch and exempt from the 60 s start limit (`Hook.ExtendsStart`, §6.4.3, §6.5.2; at 05-storage's `core.storage.swap.reset_hook_timeout` it stops and records what it did not reach as failed) (COR8-4, OPS8-5). For a `ResetStore` swap, and whenever the built config store does not implement both `legacy.ConfigOpener` and `legacy.ConfigResetArgs` (no app hook: hello, a TV app, ABO after its settings cutover), the kernel journals `hook_done` itself right after `applied`, first removing the `delete` entries (COR8-4). The hook is stated as not undoable: moving `previous/` back restores the store, its secrets and its settings, not the files it deleted (DATA7-6, SEC7-5; the undo steps, including the revocation journal and the keyring header, are 05-storage §5S.3.15's). **Rollback refusal.** `CheckReader` makes `make rollback`, `deploy` and the updater refuse with `restore_pending` while `restore/pending.json` exists (any kind; the message names it) and with `restore_inflight` while a `restore/<id>/inflight.json` exists (for a reset, from the set-aside until `hook_done`). `ROLLBACK_IGNORE_RESTORE=1`, the one override of `make rollback` and `deploy`, overrides `restore_pending` only, never `restore_inflight` ("boot the current build once to finish the swap"); the updater has none (OPS7-11, OPS8-7) | none (journal and renames only) | per 05-backup: swap back; boot degraded (`restore_failed`, `restore_rejected`); a reset whose set-aside fails moves back and boots the untouched store degraded (`factory_reset_failed`) |
| P0 | `config.NewStore` with `EnvPrefix = Options.EnvPrefix`; register core sections; `Store.Boot` resolves core sections from file → env → flags and the boot record `<state_dir>/config.boot.json` (05-config §1.3.5, §1.4). A config file that sets the pre-boot key `core.paths.state_dir` is refused (05-config §1.7). A legacy env or file name decodes with HEAD's per-type semantics (`strconv.ParseBool` spellings, whitespace-split slices, Go durations, `cast` integers) and a canonical name strictly; a legacy bool on an auth or `ProtectAdmin` key that HEAD would silently read as `false` is refused here (05-config §1.3.6, A-7). Alias uses seen here (config and env window aliases, 05-config `OnLegacyUse`) are buffered in memory and handed to compat's process-local use counter when it is built (from S2b); nothing about them is persisted (A-14, OPS2-7) | none (an unparsable boot record is renamed `.bad`, fail-open, per 05-config) | fail-closed |
| P1 | `Manifest()` of **every** module, enabled or not; `kernel/catalog` validates IDs against `module.ReservedIDs()`, `Options.ReservedAPINamespaces` and the first segment of every manifest's `LegacyPrefixes` (reserved IDs only, FE3-1); `MinCoreVersion` against `version.Version`; the namespaces of permissions, settings, task kinds, outbox consumer names and the task-ID ledger; cross-module and core collisions (a disabled module's names, claims and legacy stores stay reserved, so every manifest is checked); each `legacy.Stores` member of `Manifest.LegacyStores` supplied by at most one manifest (`ProblemLegacyStore`, §6.4.11, R6-3). Static checks only: no rbac call, because `rbac` has no store yet (COR2-7). Resolve enablement from the `BootView`: `core.modules.<id>.enabled`, default `DefaultEnabled` (§6.5.3) | none | all problems joined → `*ValidationError` |
| P2 | Open storage at `core.storage.path` resolved from the `BootView` (05-storage §5S.4 step 1: absolute; `os.OpenRoot` on the DB's parent directory, which need not be under the state directory, DATA2-19, COR2-8). Store guard: the single per-owner reader-level guard and the fast-exit flag (05-storage §5S.3.7). Spec validation: keyspace and legacy claims against the persisted claim records `c/storage/claims/<owner>` (DATA-28); a claim on disk whose owner is absent from the build (a removed module) stays orphaned until a declared claim sets `Claim.TakesOver` (05-storage rule 5), never silently re-owned (DATA2-29). There are no claim hand-overs during the extraction, so no other orphan case exists (DATA3-2 is resolved by R3-1). **Upgrade floors (05-storage §5S.4 step 3):** a store whose owner's applied version is below a declared `UpgradeFloor` is refused with `ErrUpgradeFloor` (exit 78); an **empty** store is exempt, because it is the fresh install of P4a, whose batch records the steps the floor checks (R7-2, DATA7-7). **Store backends (R4-1, COR7-4):** for each core subsystem P2 compares whether the build supplies a legacy store (a `legacy.Stores` member some manifest sets) with whether the store records one; a build that would gain or lose a legacy store fails here (`ProblemLegacyStore`, 05-storage `ErrLegacyStoreMissing`: a native store would show an empty user, role or run set). The full value (`legacy` or `native` for an exclusive store; for a routed store `legacy+native`, or `legacy` when its legacy half lacks `legacy.Covering`, SIMP5-1, SIMP6-5) needs the constructed store, so P4c compares it, before any P4d write; the choice of a first boot is recorded after the checkpoint. Every release of the extraction from the stage that lands a subsystem supplies the same legacy store, so a rollback between them never trips it (DATA4-2). P0a's recorded-path check | none (read only) | fail-closed |
| P3 | Register the settings sections of every **enabled** module, then `Store.Load` from the operator layers and the boot record; at S2 only (no kernel storage), config's DB layer also attaches here over `Options.LegacyConfigStore`, books' legacy config store over the store ABO's serve opened, with DB-override precedence: unlocked keys only, and for the app's legacy keys HEAD's precedence, under which an env name HEAD treats as a default sits below the stored value and locks nothing (COR3-1, R5-1; 05-config owns the alias flag); at S2 config calls no `Open` on that store, because `legacy.ConfigOpener` lands at S2b (COR6-8). From S2b the DB layer is the config store built at P4c (books' legacy config store for ABO, native otherwise), attached at P4d step 1. `Load` seals the registry. A disabled module's sections are not registered: its stored values are retained, unvalidated, until it is re-enabled | none | fail-closed (an invalid operator-layer value) |
| P4 | In this order. **(a)** One pre-migration checkpoint for the boot (05-storage §5S.3.12); if it cannot be taken, boot halts before anything is written. A checkpoint is reused only when the same uncompleted upgrade attempt took it for the same store identity (install marker plus store generation); a completed checkpoint, or one left by a swapped-back restore, is never reused, and `storage restore-checkpoint` refuses a checkpoint older than the current upgrade attempt (DATA3-1). An **empty** store is instead stamped as a fresh install per 05-storage §5S.4 steps 5–6, in **one batch**: install marker, every owner at the build's level, and, per owner, every registered versioned step **below that owner's lowest seed step** recorded as applied (all of them for an owner with no seed step), and, for books through its version-source adapter, every legacy step 1..`LegacyCap`, because books' versioned `Migration`s are numbered from `LegacyCap+1`, contiguous with the legacy track, so the store passes the upgrade floor on every later boot (DATA2-28, DATA3-21, COR6-4, DATA7-7, `TestFreshInstallPostFloorBootsTwice`). That seed step (`storage.Migration.Seed`, allowed only with `Additive`, 05-storage §5S.3.5) and every later step of the same owner stay pending and run in (b) in version order, each an ordinary step in its own batch with its version bump, with no checkpoint, since the store held nothing; so a later step that reshapes seeded rows converts them on a fresh install too, and hello's `seed-names` and an app's default rows exist in the current shape (COR5-8, COR6-4, `TestFreshInstallRunsSeedSteps`). A seed leaves rows that already exist unchanged (hello's reads first, §6.12.1). **(b)** The cross-keyspace migration DAG (05-storage): every pending core and **enabled-module** step in DAG order. Books' legacy migrations (versions 1..`LegacyCap`, 66 at `748fd787a`) run under books' own claims, which books holds in place for the whole extraction, so no step needs another owner's rows, and no core step reads a module's rows (R4-1). A step that changes an owner's stored format raises that owner's level through the one per-owner guard (05-storage §5S.3.7): books' own format changes during the extraction, and each later per-family cutover. A step whose preconditions do not hold is deferred (05-storage §5S.3.5). `BootBlocking` data migrations run in process through `tasks.ForEach` with persisted shard cursors, not as executor runs (COR2-7); the `Online` mode is deferred-build (§6.4.2, SIMP5-7). After the DAG, storage records the claim ledger, the backends, the store generation, the owed-duty entries (`c/storage/owed/<duty>`, store-wide, one key per duty, and `c/storage/owed/gen/<owner>/<index>` per derived index; COR6-2) and the **pending** foreign-open identity of the sidecar this boot rewrote (05-storage §5S.4 step 9). On a restore boot (a restore journal between `swapped` and `applied`, P0a) storage compares the sidecar with the identity restoreswap recorded, gives the lineage verdict `restored`, never `behind`, and records owed duties only from the restored store's own evidence (its `last` lacking a duty, or its own `owed` entries), never from the swap (R6-4, DATA6-2). It does **not** replace the served ledger's `last` record, the confirmed foreign-open identity or the boot lineage (`c/storage/boot`) here (a restore boot writes its lineage in P4d step 3's `marker_written` batch, R7-4): every consumer (P4d step 2 for `journal`, the derived loader at Run (3a) for `gen`) reads the `owed` entries this boot staged through the kernel-built `storage.Duties` handle (`Owed(duty)`, `StageDone(b, duty)`, handed to each consumer at P4c/P4d; 05-storage §5S.3.7 owns the keying, one `gen` entry per derived index cleared in the batch that records that index's completed rebuild, and the rule that one clear removes a store-wide duty for every owner; COR6-2, DATA6-9), and `last` is replaced only at Run step (3b), after all of them have read (R5-5, COR5-3). **(c)** Storage issues the core subsystems' keyspace handles; the kernel then builds each core subsystem's store: for each `legacy.Stores` member some manifest's `LegacyStores` sets, it calls the constructor exactly once, with that module's one `legacy.Handles` value (its `Legacy` range), whether the module is enabled or not, and every later user of that family receives this instance (R9-1, §6.4.11); it type-asserts only the capabilities 03 §3.3 lists for it: `legacy.ConfigOpener` and `legacy.ConfigSecrets`, which it drives, `legacy.Covering` on a routed store's legacy half, and `legacy.WriteLock` on every built store value, on which it calls `WriteLocks` with the `legacy` lock names and hands the store's owner the resolved owner-typed value (`auth.Deps.Locks` and `auth.RestoreDeps.Locks`, the executor's `RunLocks`, `SettingsLocks` for config, `secrets.Options.SettingsLocks`, integrations and notify; owners never call `WriteLocks`, COR10-5) for `storage.CommitLocked`; it records nothing else (the remaining kernel-asserted capability, `legacy.ConfigResetArgs`, is asserted at staging by `kernel.stageReset`, not here; SIMP8-3); every other capability is detected once, by its owner through its mirror (§6.4.11, 05-storage §5S.3.11 "Legacy capabilities", SIMP5-4, R6-3, SIMP7-5; e.g. the executor's mirror of `legacy.TasksRecoverer` on the run store, whose `Recover` is the start-time repair at Run step 5); it then compares each subsystem's full backend value with the store's record and fails boot before P4d on a difference (`ProblemLegacyStore`, COR7-4); it builds the native store over `c/<subsystem>/` when no module supplies an exclusive store, and always for a routed one, which then serves every record the legacy store's `Covers` does not accept (a legacy half without `Covering` covers every record; R4-1, SIMP5-1, SIMP6-5); library's `legacy.LibraryRootSource` serves beside library's config-entry roots (SIMP6-3), passed to library as `library.Options.LegacySource` (typed `any`; library asserts its mirror and fails boot with `storage.ErrLegacyStoreType` on a mismatch, R7-7). It also builds the `storage.Duties` handle each consumer of an `owed` entry receives (COR6-2). **(d)** The every-boot reconcilers, each a function of its owning package over the stores from (c), with no service built yet, in this fixed order (COR3-2, DATA3-5): 1. the config store's attach, in this order (COR5-2): (i) the store's `Open` (the legacy-only `legacy.ConfigOpener`; the native store has none), to which the kernel passes `legacy.ConfigOpenEnv.Reset`, a `*legacy.ConfigResetRun` that is non-nil on the reset boot and on every later boot until books' post-reset hook has journaled `hook_done` (P0a; SIMP8-7), so no gap-fill copy of the old settings is served before the hook rewrites it (05-config, R7-2): for ABO, books' legacy config store runs ABO's key-regeneration guard and settings-encryption init, which loads the legacy key, and, at S2b (until the S3 façade), the settings blob load, env sync and `config.Validate`, over the kernel's single store handle, failing boot where HEAD's step is Fatal, so nothing ever runs on, or writes, defaults over the stored blob (R4-6, COR4-1); (ii) from S3, `secrets.Open` with the store's legacy secrets (05-config §2.3–§2.4), reached through the one legacy-secret mechanism, 05-config's `legacy.SecretsStore` (the `legacy.ConfigSecrets` capability; the kernel passes `Secrets()` as `secrets.Options.LegacyStore`, typed `any`, and secrets asserts its mirror and fails boot with `storage.ErrLegacyStoreType` on a mismatch, R7-7, and the locks it resolved at P4c for `legacy.LockConfigSettings` on that store as `secrets.Options.SettingsLocks`, through which a kept name's `Put` and `Delete` commit with `storage.CommitLocked`, 05-config §2.3, COR10-5): core's keyring and the capture of the key (i) loaded as `k0-legacy` (on the factory-reset boot the kernel sets `secrets.Options.ResetBoot`, and that boot, and only it, rewrites the keyring header's `install_id`, after which the reset journal reads `applied`, 05-config, DATA7-17). Legacy secrets stay in HEAD's rows and HEAD's representation (A-2): plaintext blob fields stay plaintext, the four HEAD already encrypts (the OpenAI and Google Books keys, the Hardcover token, the Basic password) stay encrypted, and from S7 the integration instances read theirs through the same `legacy.SecretsStore` (05-metadata M1.4); nothing is moved or re-imported, so the step's only store write is `c/secrets/meta`; step 1 is the single place secrets are opened; no derive-key retirement, which P5 decides (COR3-2); (iii) `Store.SetSecrets`; (iv) `Store.Attach` (05-config §1.4 step 5; its `Load` is read-only; there is no config import step, R3-1; COR6-10): DB overrides become visible, with HEAD's precedence for the app's legacy keys (R5-1), and an enabled module's settings are validated against them; 1c. at S4 r1 only (§6.5.1 "Stages"), the read-only decode census over `auth.Store` and `rbac.Store` scans (05-authn §5), which writes nothing (COR5-15); 2. revocation-journal tail re-apply (05-backup), with the journal located from the **effective** `core.backup.dir` (overrides attached in step 1; the key exists from S4, COR4-10; when the directory changed since the journal's last line, 05-backup first moves the journal: copy, verify the chain, rename; never on a restore boot before `applied`, whose `restore` and hook lines go to the recorded location, so the first boot after `applied` moves it, DATA8-7) and every line verified with the keyring opened in step 1. Order (SEC4-5, DATA4-7, DATA4-15): the journal's `install_id` is checked against `c/storage/install` before any line is used; read `head_before`, the store's journal head, **before** anything is appended this boot; classify the boot as a restore boot (P0a decides it), a **replaced store** or the same store exactly as 05-backup §5B.3.6 "Boot re-apply" (2) states, never by head lag alone: the evidence is 05-backup's (the content marker `c/backup/served_boot`, the store generation, the journal head anchored in `backup.location.json`, P2's lineage verdict, and this boot's owed `journal` entry or foreign-open verdict), and a relocated store is not a replaced one (DATA5-7, DATA6-4, DATA6-5); every verified revocation after `head_before` (the seq of the journal's last **revocation** line is the end; gap and restore lines are bookkeeping and never count) is written through auth's or rbac's store from (c) — on the same store a lagging head only re-applies, with no hooks; only then are the due gap line and `restore` line appended, each with its head update in its own batch; a restore boot appends no `non_journaling_build` gap for its own swap (R6-4, DATA6-2), and a `restore` line whose restore journal is `swapped_back`, or that has a `restore_aborted` line, is neither resumed nor journal evidence (DATA6-3). Step 2 then writes `c/backup/served_boot` and afterwards `backup.location.json` (generation, `served_boot` pair, journal head); on a restore boot it writes both only after step 3's hooks commit (restore journal `hooks_done`), so a hook failure that swaps back leaves the original store's marker and file as they were (DATA6-3); while a restore journal is between `swapped` and `applied`, the journal component never rewrites the committed-head anchor of `backup.location.json` (on its `Append`, its tick or at shutdown), and step x restores the copy step c saved in `restore/<id>/` (DATA7-5, R7-4); 3. post-restore hooks (05-backup `RestoreHook`, e.g. `auth.PostRestore`), on a restore boot as P0a decides it or on a replaced-store boot as step 2 classifies it (hooks with `KindExternal` and `Revocations` nil, SEC4-6), in the order auth, rbac, audit; a staged restore is not a foreign open, so its coverage is the journal's and it forces no key confirmation of its own (R6-4); on a staged restore the kernel also builds, with the same constructor as the live store (the `legacy.Stores` field's, called a second time, or the native one), a read-only `auth.Store` and a read-only `rbac.Store` over the pre-swap store, which it opens with `storage.Admin.OpenReadOnly(previous dir)`: that open's `Legacy(owner)` gives the second `legacy.Handles` (COR8-9), and every write through them fails with `ErrReadOnly` (COR7-8; `rc.Previous`, 05-backup §5B.3.6); it hands them to the hooks (`auth.RestoreDeps.Previous`, rbac's equivalent), so a nil-coverage restore keeps every restriction the pre-swap store holds; with no pre-swap store (a replaced store, host loss) they are nil (SEC5-10); after the hooks (journal `hooks_done`), one `marker_written` batch writes `c/backup/served_boot` and the boot lineage `c/storage/boot` (`n = max + 1`, a fresh nonce), then `backup.location.json` and `storage.boot.json` are rewritten, and only then does the journal read `applied`, so a restore boot that fails after `applied` (in P4d step 4, P5–P8 or Run before (3b)) is read by the next boot as the same store, never behind (R7-4, DATA7-4, `TestRestoreBootFailingAfterHooksIsNotReplaced`); 4. `auth.BootWrites` (05-authn §5: seed `_system`, stamp legacy API keys that lack an expiry, mint the bootstrap token and the optional start-up read-only key, each in its store's representation (HEAD's for books' legacy auth store, hashed for the native one, A-2), and, for ABO, books' legacy auth store's verified-email reconcile, which books' start hook runs as HEAD's `ReconcileVerifiedEmails` until this step exists at S4 r2, COR5-7; through S4 r1 books' start hook also keeps HEAD's `seedRolesAndTokens`, COR6-3), through auth's store, so P5 writes nothing (DATA3-18, COR4-13, DATA4-10); There is no compat step: alias uses are counted in process only and nothing about them is persisted (A-14). There is no tasks step: `tasks` has no post-DAG reconciler (05-tasks step 0, R3-1); the run store's start-time repair of stale records runs inside `Executor.Run` at Run step 5, before listeners, and until `tasks` lands (S5) books' start hook runs HEAD's `ReconcileOpsV3WithV2` before it builds its ops registry (`internal/database/store.go:1861-1871` @748fd787a, COR5-7). There is no identity rebuild step | yes, from (a) on | fail-closed. A failed migration step leaves earlier steps committed and the checkpoint **unconsumed** for the offline `storage restore-checkpoint`; there is no automatic restore (DATA2-19, 05-storage §5S.3.5). A failing reconciler or settings validation fails per its owning file |
| P5 | `kernel/services.Build`: every core service in fixed order (A8), including the services behind every `Deps` handle and the realtime hub; each core subsystem is built over its store from P4c; `rbac.New` and then `Declarer` per manifest, disabled ones included so their permissions stay known (§6.5.3, COR2-7); `auth` with `Options.Authenticators`; the backup service decides derive-key retirement (`secrets.RetireUnused`) now that the effective backup directory is known, never from inside `secrets.Open` (COR3-2); `notify` before `updater`, whose update notifications go through it; adapters from `Options.Adapters` (module adapters arrive in P6 and are instantiated lazily on first lookup) | none | fail-closed |
| P6 | For each enabled module in ID order: build its `Registrar` and `Deps`, call `Register(r)`, collect `r.Err()`, close the registrar. Registrations are only recorded; `Register` writes nothing outside its handles (§6.4.1) | none | all problems joined |
| P7 | Consume registrations (A8): task definitions to the executor (R7 checks, schedule keys declared under the owner's namespace, R2-7; kinds in `TaskLedger`; each `TaskStarts` kind registered or in any manifest's `TaskLedger`, COR3-10; derived rebuild kinds named `<ID>.derived.<name>.build`, COR3-7), topics to the catalog (05-events validation), subscriptions to the bus, outbox handlers to the relay (topics declared `Durable`, consumer names declared), legacy projections to the realtime hub (redaction is each catalog entry's `TopicSpec.Redact`, so nothing is registered for it), change sinks to the task writer, adapters to the integrations registry, compat auth methods to `auth` (non-interactive only; from S4), `LegacyAuthGin` and `OwnAuth` checks (SEC2-17), checks to health; one compat entry (surface `ui_route`) per `UIDescriptor.LegacyRedirects` row, for `ui.Handler`'s recorder (from S9, FE4-5). Seed roles are reconciled (05-rbac §4.8: the seed row is the seed, a restricted addition waits in `PendingSeed` for `AcceptSeed`, SIMP4-6) and default permission grants are seeded for permissions new to this boot, through rbac's store (for ABO, books' legacy role store holds legacy atoms only, so a default grant of a permission that has no legacy atom is not stored until the identity cutover, A-2; 05-rbac §6.3 owns the mapping). Post-checks: R6 over the built router, required capabilities configured, module IDs re-checked against every recorded mount's `TopSegments` and `SPAShared`, each of which must be a first segment of a declared `LegacyPrefixes` entry (03 §3.6 P1/P7 split) | seed-role rows and default permission grants; task definition snapshots `c/tasks/def/<kind>` (05-tasks 5T.4 step 1) | joined |
| P8 | Build the root `http.Handler`: core capability routes, module routes, legacy mounts, the `/` owner (either `ui.Handler`, built with `Options.UIOptions` merged into its `ui.Options` and mounted with `Router.SPA(h, httpapi.SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})`, so stage 8's Basic-gate asset exemption survives the S9 switch, 05-http-api §4.10, FE5-10; or the `ServesSPA` legacy mount with its `LegacyMount.BasicExemptAssets`, FE-1, FE3-4). Check that no `ui.ReservedSegments()` entry is in `Router.NonSPASegments()` (`TopSegments` minus `SPAShared`, 03 §3.6; `ProblemSPASegmentCollision`, FE2-7, FE3-1, FE3-9). There is no build-ID check: the only UI build ID is the `/` owner's embedded one (FE4-1) | none | fail-closed |

`New` stops core services' goroutines and closes storage and everything opened so far, in reverse
order, on any failure.

**Boot writers and the crash matrix (A-8, ADV-4, R9-7).** The table below is the complete list of
durable writes, renames and fsyncs from P0a to Run (3b), the kernel's and every app writer's (P−1
is deferred-build, A-6, and adds its row when it is built), with the state machine each advances.
05-storage draws one state diagram per durable state machine, with its legal transitions: the swap
journal, the served ledger and duties, the sidecars and the open stamp; those diagrams, not this table,
define the legal transitions. The crash-matrix kit is `coretest/crashmatrix` (§6.4.8; 05-storage
§5S.3.16 owns the invariants and the test names): with Pebble `vfs` error injection and a labelled
state-directory FS it kills the boot at every write, rename and fsync listed here, and in a second mode
returns an error from each of them instead (R10-6, DATA10-4), and asserts the
cross-mechanism invariants (no owed duty lost; the swap journal resumes or rejects; sidecars agree with
the store; both the current and the previous build boot the result). Core runs it over the kernel's
writers with hello, and ABO over its full boot with books, HEAD fixtures and `PREV_REF`, both required
from S2b (COR9-5). An app writer outside Pebble writes through the labelled state FS, so an unlabelled
durable write fails the kit, and so does a file written past both seams, which the kit's after-boot diff
of the DB parent and state-directory trees against its label log finds (`TestBootCrashMatrixCoversEveryWriter`,
COR10-7). A PR that adds a boot writer, or
moves one to another phase, adds its row here and extends the matrix in the same PR; a durable write in
this range that is not listed is a defect.

| Phase | Writer | Durable effect | State machine (05-storage diagram) |
|---|---|---|---|
| P0a | restoreswap's swap and set-aside (05-backup §5B.3.4) | `restore/<id>/journal.json` steps; `pending.json` → `inflight.json` before the first rename of any kind (R9-7); the copy-if-exists copies of `backup.location.json` and `storage.boot.json` into `restore/<id>/` (R10-6); the swap or set-aside renames and their swap-back or move-back, including the failure exits between the in-flight rename and the first journalled move (`rechecked` → `swapped_back`, `staged` → `rejected`; DATA10-4); `restore_rejected` / `rejected` records and `rejected.json`; on a restore, the sidecar and `OPTIONS-<n>` identities recorded at step e | swap journal; sidecars; open stamp |
| P0 | `config.Store.Boot` | an unparsable `config.boot.json` renamed `.bad` | — (fail-open quarantine, 05-config) |
| P2 | storage open | Pebble's `OPTIONS-<n>`; the store guard's reader-level check writes nothing | open stamp |
| P4a | checkpoint or fresh-install batch | the checkpoint directory; on an empty store one batch (install marker, levels, applied steps, the reset boot's `fresh_rows`) | — (05-storage §5S.3.12; a checkpoint is consumed or kept, DATA3-1) |
| P4b | migration DAG and post-DAG records | each step's batch with its version bump (books' legacy and versioned migrations included); `BootBlocking` shard cursors; the claim ledger, backends, store generation, owed entries and the pending sidecar identity | served ledger and duties; sidecars |
| P4d 1 (i) | books' legacy config store's `Open` (`legacy.ConfigOpener`, from S2b) | HEAD's `InitEncryption` writes the legacy key file `.encryption_key` in the state directory when none exists (a fresh install, or the first boot after a reset with no key; `internal/database/settings.go:149` @748fd787a), through the labelled state FS; no store write (COR9-5) | — |
| P4d 1 (ii) | `secrets.Open` (from S3) | `c/secrets/meta`; on the reset boot the keyring header's `install_id` | swap journal |
| P4d 1 (reset boot) | the kernel | journal `applied` after step 1; for a `ResetStore`, or with no app hook, the removal of the intent's `delete` entries, then `hook_done` and the deletion of `inflight.json` (COR8-4) | swap journal |
| P4d 2 | revocation re-apply (05-backup, from S4 r2) | revocations through auth's and rbac's stores (books' legacy stores commit through `storage.CommitLocked`); gap and `restore` lines with their head updates; `c/backup/served_boot`, `backup.location.json`; a journal move (copy, verify, rename); clearing the owed `journal` entry | served ledger and duties |
| P4d 3 (from S4 r2) | post-restore hooks | hook writes; the `marker_written` batch (`c/backup/served_boot`, `c/storage/boot`); `backup.location.json`, `storage.boot.json`; journal `hooks_done` then `applied`; `inflight.json` removed | swap journal; served ledger (lineage) |
| P4d 3 (S2b to S4 r1) | the kernel, on a restore boot of a build with no restore hooks | machine 1's one edge `opened → marker_written` (DATA9-5): a `marker_written` batch with `c/storage/boot` only (no `c/backup/` row before S4 r2), then `storage.boot.json`; journal `applied`; `inflight.json` removed | swap journal; served ledger (lineage) |
| P4d 4 | `auth.BootWrites` (from S4 r2) | `_system`, legacy-key expiry stamps, the bootstrap token (and its file, through the labelled state filesystem that `auth.RestoreDeps` receives instead of an `*os.Root`, COR10-7), the optional start-up key, the verified-email reconcile (`legacy.AuthReconciler.Reconcile`, under the locks 05-authn §3.4's table names for it, COR10-6), each committed through `storage.CommitLocked` on books' store | — |
| P7 | registration consumers | seed-role rows and default grants; task definition snapshots `c/tasks/def/<kind>` | — |
| Run 2 | books' start hooks | 05-storage §5S.9.3's repairs; HEAD's `ReconcileOpsV3WithV2` (until S5), `ReconcileVerifiedEmails` and `seedRolesAndTokens` (until S4 r2, including HEAD's `.bootstrap-token` and `.readonly-key` files, which `internal/server/bootstrap.go:121,166` @748fd787a writes with `os.WriteFile` and which go through `legacy.ConfigOpenEnv.Files` until S4 r2, so the kit labels them; COR10-7); the post-reset hook's rewrite of the gap-fill file `<db parent>/config.yaml`, its deletions (the `delete` entries, the Open Library data and, only with `delete_library_files`, the `RootDir` entries) through the labelled state FS, bounded by the state directory, `<db parent>`, the `delete` entries and the intent's `hook_paths` (R10-6, COR10-4), and `ConfigResetRun.Done` (`hook_done`, `inflight.json` deleted) | swap journal |
| Run (3a) | derived loader | `c/storage/derived/<module>/<name>`; the rebuild enqueue (an owed `gen` entry is cleared later, by the batch that records the rebuild's completion) | served ledger and duties |
| Run (3b) | served-ledger commit | each owner's `last`, the confirmed open stamp and sidecar identity, `c/storage/boot`; then `storage.boot.json` | served ledger and duties; sidecars; open stamp |

**Stages (COR2-9).** A row whose package has not landed (§6.4.10) is absent, not stubbed.

- **S2.** The kernel runs with `storage` absent: P2 and P4 are skipped and `Deps.Storage` does not
  exist. ABO's `serve` opens the store, runs books' migrations and HEAD's other startup steps
  **before** `app.New`, in HEAD's order and through its own `internal/startup` (`cmd/root.go:274-391`
  @748fd787a; MOD3-1), with the state directory from `app.ResolveStateDir` (OPS4-8). It hands the open
  store to `books.Module(store)` and passes `Options.LegacyConfigStore`, books' legacy config store over
  that store, over which config's DB layer attaches at P3 (COR3-1). There is never a second Pebble
  open, and the start-timeout extensions of that pre-`app.New` code use `app.ExtendStart`.
- **From S2b** the kernel opens the store at P2 and builds books' legacy config store at P4c from
  `Manifest.LegacyStores.Config` (a `legacy.Stores` member) over books' own range; config attaches it at P4d step 1, where it also
  runs ABO's key guard, encryption init and settings load (R4-6), so a UI-saved value is honoured at
  every stage. Its `Changed` (the optional `legacy.ConfigExternalWriter`, deleted in the S3 PR, SIMP5-5)
  fires after every blob write by ABO's legacy config path (books code, the writer until the S3
  façade), and config re-reads and republishes live keys, so a perimeter edit (rate limiter, CORS
  origins, log level) applies without a restart as at HEAD (COR4-2); the peer allow-list is file- and
  env-only at HEAD and stays so (R9-5, DATA9-6). `serve` runs no
  startup step from S2b. `secrets.Open` in P4d step 1 exists from S3, always after the store's `Open`
  (COR5-2).
- **HEAD's every-boot store repairs between S2b and their successors (COR5-7).** From S2b, when
  `serve` no longer opens the store, books' start hook (Run step 2) runs HEAD's `ReconcileOpsV3WithV2`
  before it builds its ops registry until `tasks` lands (S5), and HEAD's `ReconcileVerifiedEmails`
  until P4d step 4 exists (S4 r2); each then moves to its successor, the run store's `Recover` inside
  `Executor.Run` and books' legacy auth store inside `auth.BootWrites`. HEAD's Start-time identity
  writers, `seedRolesAndTokens` (`auth.SeedRoles`, `auth.SeedSystemUser`, the never-expiring-key stamp,
  `InitBootstrapToken` and the optional start-up read-only key; `internal/server/server_lifecycle.go:355,1098-1140`
  @748fd787a), keep running from `Server.Start` until S2b and from books' start hook (Run step 2), over
  books' own code, through S4 r1; the S4 r2 flip PR deletes them as P7's seed-role reconcile and
  `auth.BootWrites` take over (COR6-3). An S2b done-check boots a store
  with a stale opv3 record and an unstamped email row and finds both repaired (05-storage §5S.9.3).
- **P0a.** From S2b the kernel's journaled swap runs at P0a (05-backup §5B.3.4: resume, step x's swap
  back after a P2, P4b or hook failure, step h's bookkeeping until `applied` and the removal of
  `inflight.json`, and the swap that `storage restore-checkpoint` needs), over the kernel's own
  manifests' floors, and so do both reset kinds (`KindReset`, R8-1); until S8 ABO's `internal/backup`
  only stages the restore (writes the one slot `pending.json` atomically, so it and a staged reset
  exclude each other across processes, `storage.ErrSwapPending`) against the upgrade floor books
  declares in its own `Manifest.Storage.UpgradeFloor` (SIMP8-4), and from S8 05-backup stages it too
  (DATA4-6). A checkpoint restore follows 05-storage §5S.3.12's three rules (DATA7-12): an
  **open** checkpoint's offline swap is finished to `applied_offline` without hooks, after the tool has
  committed the offline close batch over the restored store with the step-e identities standing in for
  the store's records, as on a restore boot, through the kernel's internal close-batch function rather
  than the public `OpenOffline`, which refuses while that restore's in-flight file exists (§6.4.6,
  COR9-15), so the next boot reads no foreign open (DATA8-5); `CheckReader` passes a `KindCheckpoint` journal at `swapped` or `failed` for a candidate
  whose levels and floors admit the restored store and which implements the journaled swap (detected
  by its `--print-restore-journal` fast exit, S2b; any other candidate gets `restore_inflight`), whose
  P0a then resumes it as a restore boot, while the build named in a `failed` journal's
  `failed_by_build` does not resume it (DATA8-6); and a `KindCheckpoint` restore boot whose migration
  fails keeps the checkpoint and exits non-zero instead of swapping back. P4d step 3 runs the hooks from S4, when `auth`, `rbac`
  and `audit` exist, whenever either reports a restore boot (DATA3-7). P4d step 2 exists from S4, with
  `backup/journal`. **A restore boot with no restore hooks (S2b, S3 and S4 r1, where P4d steps 2–3 do
  not run)** reaches `applied` through machine 1's one legal edge `opened → marker_written` (05-storage
  §5S.3.16): the kernel's `marker_written` batch writes `c/storage/boot` only, since `c/backup/` is
  written from S4 r2, then `storage.boot.json`, then journals `applied` and removes `inflight.json`
  (R9-7, DATA9-5). The crash matrix's S2b scenario is that hookless restore; the S4 r2 PR adds the
  scenario with hooks.
- **Until S4**, P5 has no `rbac`, P7 seeds no default permission grants, and P4d step 4 does not exist.
  **Within S4**, while `Options.LegacyAuthGinUntil` is still set (the dark release in which gin's auth
  decides every request, 09 S4), the kernel builds `auth` and `rbac` over books' legacy stores for the
  read-only decode census of P4d step 1c (COR5-15) and runs none of P4d steps 2–4 or P7's seed writes:
  each is skipped, logged and counted, and **core** writes no identity or role row. Books' start hook
  meanwhile keeps HEAD's `seedRolesAndTokens` (the bullet above), so a fresh r1 store has the four seed
  roles and `_system`, and every r1 restart mints a new bootstrap token file, as at HEAD (COR6-3). They
  run from the release that clears the field (COR4-4).
- **Through S10**, P−1 is absent: neither the hotfixed HEAD updater nor the S8 `core/updater` has a
  boot check or a boot-attempt counter (deferred-build, A-6).

### 6.5.2 Run and shutdown

This section is the **one** statement of start/stop order. 05-events and 05-tasks reference it
instead of restating it (COR-8).

Start order: (1) core background services (metrics, realtime hub); (2) module hooks, modules in ID
order, hooks of one module in registration order; (3) module workers; (3a) the core components that
invoke module callbacks: first the derived-index version/generation check, each index's `Load` and its
`c/storage/derived/<module>/<name>` record (moved out of P7 so that `Load` runs after the module's start hooks
have built what it needs, and so `Describe`, which stops after P7, never calls it; COR3-7), then the
storage index reconciler and derived-index builder (05-storage §5S.3.8–§5S.3.9), the health runner of
module checks, and `OnChange` delivery for module settings sections (COR2-15); (3b) the served-ledger
commit: one batch writes each owner's `c/storage/served/<owner>/last` record (this binary's ID and
duties), the foreign-open identity (the Pebble open stamp `OPTIONS-<n>` and the sidecar identity, promoting the pending ones P4b staged; DATA8-13) and the boot lineage
`c/storage/boot` (`n = max(store n, file n) + 1`, a fresh nonce), and after it commits
`<state_dir>/storage.boot.json` is rewritten (05-storage §5S.4 step 14b, SEC5-5); a restore boot
already wrote its lineage and that file in P4d step 3's `marker_written` batch, before journal
`applied`, and (3b) does not write them again (R7-4, DATA7-4; this section is the one statement of the
order, so 05-storage §5S.4 step 14b follows it, DATA8-13); all of this happens
after every consumer of this boot's `owed` entries has read them through `storage.Duties` (P4d
step 2, the derived loader at (3a); COR6-2); each of those consumers is idempotent on re-detection (a
second `gap` line is harmless, a rebuild re-runs), so a boot that fails anywhere before (3b) leaves the previous record in place
and the next boot detects the same fallback (R5-5, COR5-3, DATA5-6; 05-storage
`TestFailedBootAfterRollbackKeepsFallbacks`); (4) `Bus.Open`: bus
delivery to the subscriptions frozen in P6 begins; (5) task executor, `Executor.Run` (the resume sweep and the run store's `Recover` run inside it; 05-tasks has no boot-time reconcile step in P4d); (6) scheduler, whose `OnStart`
schedules start the outbox relay task `core.outbox.relay`, so outbox delivery begins here;
(7) listeners; (8) readiness true. No bus handler, outbox consumer or other module callback can run
before its module's hooks have started, and the resume sweep's `core.task.*` events are delivered.

If any hook's `Start` fails or exceeds `core.lifecycle.start_timeout`, every already-started hook is
stopped in reverse order (the semantics of `serviceregistry.Container.Start` today, `container.go:164`),
and `Run` returns the joined error. A hook registered with `Hook.ExtendsStart` (§6.4.3) is exempt from
the whole-hook limit: it calls `Deps.ExtendStart` per batch of real progress, and the timeout bounds
the time between two calls instead, so its runtime is bounded by progress, never by a fixed total.
Books' post-reset hook is the one such hook; it also never returns an error (P0a; COR8-4, OPS8-5).

**Restart request (OPS8-12).** The supervisor has one restart path, `Lifecycle.RequestRestart(reason
string)`, which the kernel binds into every caller that needs the process restarted: the staging of a
reset (`kernel.stageReset`, P0a) and of a restore (05-backup step 9, the kernel's from S8), and the
updater's `Restarter` (05-updater). It logs the reason, runs the stop order below within
`core.shutdown.budget`, and then exits with `core.lifecycle.restart_exit_code` (default 75), so the
committed unit's `Restart=on-failure` restarts the service; an exec-mode restart is deferred-build
(05-updater 5U.3; R10-8), so no release execs itself. An `os.Exit(0)`
would leave a staged swap untaken under that unit while `restore_pending` blocks `deploy` (09 §9.1).
Before the kernel stages restores (S8), ABO's own restore staging exits the same way (09 S1).

Stop order on ctx cancel or `StopAppOnExit`: readiness false → listeners `Shutdown` (bounded) →
scheduler stop → executor drain (05-tasks bounded drain; the relay run is cancelled with
`ErrShutdown` and acks nothing further) → `Bus.Close` with `core.events.close_drain` → stop the
components of start step (3a) (step (3b) is a single commit and has nothing to stop) → cancel workers' ctx and wait → module hooks `Stop` in exact reverse
start order → realtime hub → storage close. No bus handler, outbox consumer or other module callback
can run after its module's Stop hook. Deadlines come from **one**
budget, `core.shutdown.budget`, sliced per phase by 05-health §5H.4 (HTTP drain, tasks drain,
`Bus.Close`, then workers, hooks and realtime share the remainder in that order); work still running
at the cap is abandoned with an ERROR log naming it, and storage is closed last regardless (Pebble's
WAL makes that safe for committed batches).

### 6.5.3 Enablement (restart-bound, G1)

* Desired state: config key `core.modules.<id>.enabled`, a `ReloadBoot` key of 05-config (§1.3.5).
  It is read in P1, the phase that decides it, through `config.Store.Boot`, before storage opens
  (COR-14). An operator-layer value (file, env, flag) locks the key as for any config key; otherwise
  the settings UI writes it through `config.Apply`, which persists it in the boot record
  `<state_dir>/config.boot.json`, not in `c/config/`. Hot reload: **no**. A change is persisted,
  `GET /api/core/v1/modules` marks `restart_required: true`, and nothing else changes until restart.
* Disabled module: manifest still read (its keyspace/legacy claims, permissions, settings section
  names, outbox consumer names and task-ID ledger stay known so nothing else can take them, and its
  data is retained); its legacy stores stay in force, because they serve core, not the module (a
  disabled books still leaves `auth`, `rbac` and `config` on ABO's user, role and settings rows,
  through books' store implementations, A5);
  migrations, `Register`, hooks, workers **not invoked**; no route, task kind,
  topic, schedule, subscription, outbox handler, adapter, health check, facade or nav entry exists.
* **Held runs (COR-13).** A persisted run whose `Kind.Module()` names a disabled manifest is
  **held**. Held is a classification computed at boot, not a stored state: the run's record is not
  rewritten. The dispatcher and the resume sweep skip held runs; `GET /api/core/v1/tasks/runs` lists
  them as held (read-only); they resume normally after re-enable. The op-ledger boot gate (05-tasks
  5T.4 step 1) treats an entry as resolved when its module prefix names a disabled manifest or its
  kind is in that manifest's `TaskLedger` (which includes former kinds), so disabling a module never
  fails boot. 05-tasks 5T.3.11 owns the mechanics.
* **Retained outbox messages (COR-12).** Messages for a disabled module's declared consumers are
  neither delivered nor dropped: retention counts the persisted cursors of every consumer declared in
  any manifest, enabled or not (05-events). `orphan_retention` applies only to consumer names that no
  manifest declares any more.
* Its permissions remain known to `rbac` (roles referencing them stay valid) and are shown as
  "module disabled" on the Roles page.
* Re-enabling runs any migrations added while disabled, then registers normally.

## 6.6 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| Invalid/duplicate/reserved module ID | fail-closed at P1, all problems joined | composition errors are programmer errors; booting a partial graph hides them |
| `MinCoreVersion` > `version.Version` | fail-closed (`ErrCoreTooOld`) | module may call APIs that do not exist |
| `core.modules.<id>.enabled` names an unknown module in an **operator** layer (file/env/flag) | fail-closed (`ProblemUnknownModuleInConfig`), message lists known IDs | a typo intended to disable a module must not leave it running, and the operator can fix the file |
| `core.modules.<id>.enabled` names an unknown module in the **boot record** (UI-written) | fail-open: retained and reported like an unknown override row (05-config §1.3.5), health `core.config` degraded | a fail-closed UI-written value would make the app unbootable with no tool to remove it (COR-14) |
| Permission not `<ID>:`-prefixed, duplicate, or route/task uses undeclared permission | fail-closed | R6 and D7 namespacing |
| Topic name outside `<ID>.`, duplicate topic, zero topic permission, outbox handler for an undeclared or non-durable topic, undeclared consumer name, registered task kind missing from `TaskLedger` | fail-closed | one topic catalog (SIMP-5); retention needs declared consumers (COR-12) |
| Keyspace or legacy-prefix overlap | fail-closed | R8; overlapping writes corrupt data |
| Two `ServesSPA` mounts, or a `ServesSPA` mount with a non-zero `Options.UI` | fail-closed (`ProblemSPAOwner`) | exactly one owner of `/` (FE-1) |
| Settings section outside `modules.<ID>` | fail-closed | config namespace collision |
| `Manifest.TaskStarts` names a kind that is neither registered nor in any manifest's `TaskLedger` (a disabled module's kind is accepted and refused at `Start` with "unknown kind") | fail-closed at P7 (`ProblemTaskStartUnknown`) | the system actor's permission set must be reviewable (SEC2-15), and disabling a module never fails another's boot (G1, COR3-10) |
| A `LegacyRoleIDs` entry listed by two manifests or equal to a core seed role | fail-closed at P1 (`ProblemLegacyRole`) | two modules would seed one legacy role row (COR5-5) |
| A `legacy.Stores` field supplied by two modules (P1), or a build whose store backend for a subsystem differs from the one the store records (P2 when it gains or loses a legacy store; P4c, before any P4d write, when only the full value differs, e.g. a routed legacy half that gained or lost `legacy.Covering`; COR7-4) | fail-closed (`ProblemLegacyStore`) | two writers of one subsystem's data, or a native store that would show an empty user, role or run set, must never boot (R4-1) |
| An owner's `any`-typed legacy option (`secrets.Options.LegacyStore`, `library.Options.LegacySource`) holds a value that does not satisfy the owner's mirror | fail-closed (`storage.ErrLegacyStoreType`, from the owner's constructor) | a silent skip would serve the native store over an empty keyspace, or drop the legacy secrets (R7-7, SIMP7-4) |
| A store whose owner's applied version is below a declared `UpgradeFloor`, unless the store is empty | fail-closed at P2 (`ErrUpgradeFloor`, exit 78) | the build no longer carries the steps the store still needs; an empty store is a fresh install and is exempt (DATA7-7) |
| An `app.Options.LegacyTaskAliases` key that is not a registered `core.*` kind, or a value a module registers or lists in its `TaskLedger` | fail-closed at P7 (`ProblemLegacyTaskAlias`) | a former kind must resolve to exactly one owner, and core never spells an app's op ID (MOD6-6) |
| A legacy store's load fails at P4d step 1 (ABO: key guard, encryption init, settings blob, `config.Validate`) | fail-closed where HEAD's step is Fatal | running on defaults would write defaults over the stored blob on the first settings save (R4-6, `cmd/root.go:360-363` @748fd787a) |
| `AuthMethod` interactive, credential-changing or owner-verifying; `LegacyAuthGin` by a module other than `Options.LegacyAuthGinUntil`; `OwnAuth` route without a registered compat method | fail-closed (`ProblemAuthMethod`, `ProblemLegacyAuth`) | a module must not decide its own authentication strength (SEC2-16, SEC2-17) |
| Derived index declared without hooks, or hooks without a declaration | fail-closed (`ProblemDerivedIndex`) | readiness gating would wait forever, or an index would be unknown while disabled (COR2-15) |
| A shell route segment is also a `NonSPA` segment (a mount's `TopSegments`; `SPAShared` segments are not `NonSPA`) | fail-closed at P8 (`ProblemSPASegmentCollision`) | a full page load of that shell page would get a 404 problem+json (FE2-7, FE3-1) |
| The embedded shell bundle's `mediacore-app` meta differs from `Options.AppID` | fail-closed at P8 (`ProblemUIAppIDMismatch`) | `storageKey()`, the theme key and the realtime channel share one app-ID namespace only if the server guarantees it (07 §7.6.1, FE8-7) |
| Settings value invalid for an **enabled** module | fail-closed | module would run with undefined config |
| Stored settings of a **disabled** module | not validated (section not registered, 05-config §1.4 step 3); retained | the module does not run; validated when it is re-enabled |
| Migration error | fail-closed; earlier steps stay committed and the checkpoint stays unconsumed for the offline `storage restore-checkpoint`; no automatic restore (DATA2-19) | G2; an automatic restore would fight the crash-loop reuse rule and non-atomic legacy steps (05-storage §5S.3.5) |
| Config file sets `core.paths.state_dir` | fail-closed at P0, naming the flag and env name to use | P0a (and P−1 once built) read it before any config file, so a file value would be silently ignored by them |
| `Register` returns error or panics | fail-closed; panic converted to error with stack in the log | partial registration leaves dangling routes/tasks |
| Registrar misuse (route outside prefix, missing permission, missing `Parallelism`, duplicate kind, second change sink, call after close) | recorded, fail-closed after P6/P7 | collected so the operator sees all at once |
| Required capability with zero configured instances | fail-closed (`ProblemCapabilityMissing`) with `Why` | a module that cannot do its job must not start; optional ones only degrade |
| Adapter descriptor invalid (ID outside module prefix, duplicate, secret field not marked) | fail-closed (`ProblemAdapter`, 05-metadata M1.4) | a bad adapter must not fail at first use |
| Persisted runs or ledger entries of a disabled module | fail-open: held, listed, not dispatched | disabling a module must not fail boot (COR-13) |
| Hook `Start` error/timeout | fail-closed: reverse-stop started hooks, `Run` returns error | a half-started module may accept work it cannot finish |
| Hook `Stop` error/timeout | fail-open: logged, joined into `Run`'s return, shutdown continues | shutdown must reach storage close |
| Worker early return/panic, `DegradeOnExit` | fail-open: health unhealthy, ERROR log, no restart | restarting a crashing loop hides the bug; readiness reflects it |
| Worker early return/panic, `StopAppOnExit` | fail-closed: graceful shutdown, `Run` returns the error | declared as essential by the module |
| `Deps` handle used after Stop | returns `module.ErrStopped` | prevents writes after storage close |
| Capability lookup with no instance | returns `ErrNotConfigured` | callers decide; never a nil handle |
| `app.New` ctx cancelled during boot | `New` returns `ctx.Err()` after reverse cleanup; `Registrar.Context()` is cancelled with it | cancellation must reach module reads in `Register` (COR-24) |

## 6.7 Concurrency contract

* `Manifest()` may be called concurrently and repeatedly (parallel `coretest` tests); it must not
  mutate shared state.
* `Register` runs once per boot, on one goroutine, for enabled modules in ID order; it must not start
  goroutines (use `Worker`) and must not block on I/O beyond reading its own config/keyspace with
  `r.Context()`. `Registrar` is not goroutine-safe and is invalid after `Register` returns.
* Every `Deps` handle is goroutine-safe. Library-scale loops inside module code use `tasks.RunItems`
  with an explicit `tasks.Parallelism` (R7, CLAUDE.md concurrency rule); hand-rolled pools in module
  code must be bounded (`errgroup.SetLimit`), and `archtest` flags `go` statements in module packages
  outside `_test.go` for review (warning list, not a hard failure, because some libraries need them).
* Hooks start sequentially (deterministic ordering, simple rollback). Boot latency is dominated by
  migrations and index opening, not hooks; parallel start is an open item, not a default.
* Workers run under `sync.WaitGroup.Go`; each gets a ctx cancelled at shutdown and must return within
  the workers' slice of `core.shutdown.budget` (05-health §5H.4).
* All shutdown waits are bounded (§6.5.2); nothing in the kernel waits without a deadline.

## 6.8 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.modules.<id>.enabled` | bool (`ReloadBoot`, boot record) | `Manifest.DefaultEnabled` | no (restart-bound; read in P1 via `Store.Boot`; UI shows restart required) |
| `core.lifecycle.start_timeout` | duration | `60s` per hook (per `ExtendStart` step for a `Hook.ExtendsStart` hook, §6.5.2) | no |
| `core.lifecycle.restart_exit_code` | int (`ProtectAdmin`) | `75` | yes (read when `RequestRestart` exits, §6.5.2; 05-updater copies it into the pending marker at apply; it replaces `core.updater.restart_exit_code`, which never shipped, so there is no alias; OPS8-12) |
| `core.shutdown.budget` | duration | `25s` total, defined by 05-health §5H.4 (which also slices it per phase) | no (restart-bound; 05-health states the same, OPS2-23) |
| `modules.<id>.*` | per module schema | per `config.Section` defaults | per field (05-config) |

## 6.9 Metrics

Names are shown with the default core namespace; each family is `<app.Options.MetricNamespace>_kernel_*`
(ABO: `audiobook_organizer_kernel_*`, A-5).

| Name | Type | Labels |
|---|---|---|
| `mediacore_kernel_module_info` | gauge (1) | `module`, `version`, `state` (`enabled`/`disabled`) |
| `mediacore_kernel_boot_phase_seconds` | gauge, set once per boot | `phase`: exactly `P-1` (only once P−1 is built, A-6), `P0a`, `P0`, `P1`, `P2`, `P3`, `P4a`, `P4b`, `P4c`, `P4d`, `P5`, `P6`, `P7`, `P8` (ASCII hyphen-minus; the one label set, which 09 and every done-check use verbatim, OPS3-16); `step` (the bounded set of migration and reconciler step names inside a phase, e.g. `books#66` in `P4b`, `revocation-reapply` in `P4d`; `""` for the phase total) (OPS2-24) |
| `mediacore_kernel_module_migration_seconds` | histogram | `module` |
| `mediacore_kernel_hook_seconds` | histogram | `module`, `hook`, `op` (`start`/`stop`) |
| `mediacore_kernel_hook_failures_total` | counter | `module`, `hook`, `op`, `reason` (`error`/`timeout`/`panic`) |
| `mediacore_kernel_workers_running` | gauge | `module` |
| `mediacore_kernel_worker_exits_total` | counter | `module`, `worker`, `reason` (`return`/`error`/`panic`/`shutdown`) |
| `mediacore_kernel_shutdown_abandoned_total` | counter | `stage` |

## 6.10 Tests

Core (`core/kernel/...`, `core/app`, `core/coretest`):

| Test | Proves |
|---|---|
| `TestBootZeroModules` | G7: core boots, serves `/api/core/v1/*`, health ready, with no module |
| `TestValidation_<ProblemKind>` (table, one row per kind) | each validation path fails closed with the right kind; `TestValidation_AllProblemsJoined` checks multiple problems are reported together |
| `TestDisabledModuleInert` | G1 via `AssertModuleInert` on hello |
| `TestDisabledModuleWithRunsBoots` | COR-13: hello disabled with `WithRuns("hello.count", 3)` and an op-ledger line, resolved through hello's `TaskLedger`; boot succeeds, runs listed held, none dispatched; re-enable resumes them |
| `TestDisabledModuleOutboxRetained` | COR-12: a message for a disabled module's declared consumer survives `orphan_retention` and another consumer's ack, and is delivered after re-enable |
| `TestUnknownModuleInConfig_OperatorFails`, `TestUnknownModuleInConfig_BootRecordWarns` | COR-14 split by layer |
| `TestEnablementFromBootRecord` | COR-14: a disable written through `config.Apply` lands in the boot record and takes effect at the next boot (read in P1); an operator env value beats it |
| `TestMigrationFailureHaltsBoot` | G2 with a failing hello migration: no later step ran, earlier steps stay committed, the checkpoint exists and is unconsumed, no automatic restore happened, and the offline `storage restore-checkpoint` brings back the pre-boot state (DATA2-19) |
| `TestBootPhaseOrder_NoWriteBeforeCheckpoint` | R2-8: an instrumented store records the first write of a boot with auth's boot writes, a revocation re-apply and a post-restore hook pending; it follows the checkpoint, the reconcilers run in the P4d order (keyring and overrides before the journal, COR3-2), and no store constructor, legacy-store constructor or `Register` writes (DATA4-10) |
| `TestP4dStep1_LegacyOpenBeforeSecrets` | COR5-2: on the first S3 boot with ABO's key file, the store's `Open` runs before `secrets.Open`, `k0-legacy` is recorded, and no legacy secret row is moved or rewritten: plaintext blob fields stay plaintext and the four secrets HEAD already encrypts stay in HEAD's rows (R5-1, A-2) |
| `TestRevocationStagesJournalHeadAtS4` | COR5-6: at S4 r2 a key revocation commits with its `c/backup/journal_head` update (the kernel registers `backup/journal`'s `c/backup/` spec), and an `Append` failure commits its `journal_gap/` line, before `backup` lands |
| `TestServedLedgerCommittedAfterConsumers` | R5-5, COR5-3: a boot that fails after P4b (a P4d step 1 refusal) or after (3a) leaves the previous served record and foreign-open identity in place, so the next boot still appends the `gap` line (with 05-storage's `TestFailedBootAfterRollbackKeepsFallbacks`); a successful boot writes them only at Run step (3b) |
| `TestJournalReapplyVerifiesWithKeyring`, `TestJournalReapplyUsesOverriddenBackupDir` | COR3-2, DATA3-5, COR4-10, from S4: with `core.backup.dir` set only in the settings blob, P4d step 2 reads the journal from that directory and verifies every line with the keyring opened in step 1; with 05-backup's `TestRollbackWithoutRestore_NoKeyConfirmation` and `TestOlderBuildRestore_DetectedDespiteGapLine`, the replaced-store test reads `head_before` before the gap line is appended (SEC4-5) |
| `TestLegacyStores` | R4-1, R6-3: a `legacy.Stores` member set by two manifests' `LegacyStores` fails P1 with `ProblemLegacyStore`; a build that gains or loses a legacy store fails P2, and one whose full value differs (a routed legacy half that gains or loses `Covering`) fails P4c before any P4d write, and adding or removing `Covering` on a recorded routed store is a backend switch (COR7-4); a disabled supplying module keeps its stores in force; a constructor receives only the supplier's own `legacy.Handles` and writes nothing; with no supplier every subsystem runs on its native `c/<subsystem>/` store; for a routed store (SIMP5-1) a record the legacy store does not `Cover` goes to the native store and the recorded backend is `legacy+native`, and a legacy half without `Covering` covers every record (SIMP6-5); the kernel type-asserts only `legacy.ConfigOpener`, `legacy.ConfigSecrets`, `legacy.Covering` and `legacy.WriteLock` (R9-1) and records no capability, every other capability being found by its owner's mirror, whose absence means "none" (SIMP5-4, SIMP7-5); on a staged restore the auth and role constructors run a second time over the read-only pre-swap store and every write through those handles fails with `ErrReadOnly` (COR7-8); no SDK package other than `module` (whose `Manifest.LegacyStores` field names `legacy.Stores`) names a `legacy` identifier; the boot copies, mirrors or rebuilds nothing |
| `TestDarkAuthReleaseWritesNothing` | COR4-4, COR6-3: while `Options.LegacyAuthGinUntil` is set at S4, auth and rbac are built over the supplied stores, P4d steps 2–4 and P7's seed writes are skipped and counted, and **core** writes no identity or role row |
| `TestRestoreBootWritesMarkersAfterHooks` | R6-4, DATA6-2, DATA6-3: a staged restore with an intact journal appends no `gap` line and needs no key confirmation; `c/backup/served_boot` and `backup.location.json` are written only after P4d step 3's hooks commit, so a failing hook that swaps back leaves the original store classified "same store" at the next boot, with no hook run and no restore resumed, and the committed-head anchor equals the original store's head after the swap-back (DATA7-5); the restore boot's lineage and `storage.boot.json` are written in the `marker_written` batch before journal `applied`, so a boot that fails at P6 after `applied` is read as the same store at the next boot, with no hook and no key confirmation (R7-4, DATA7-4; 05-storage's `TestRestoreBootFailingAfterHooksIsNotReplaced`) (with 05-backup's `TestStagedRestoreKeepsCoverage` and `TestRestoreFailureSwapsBack`) |
| `TestSwapSlotExclusive` | R8-1, SIMP8-1 (replaces `TestRestoreAndResetBothStaged`): two concurrent stagers of any kinds (restore, `ResetStore`, `ResetFactory`), in one process and across two processes, at S2b and from S8: exactly one intent is written and the other gets `storage.ErrSwapPending`; an intent of any kind whose recorded open stamp moved, that expired, or that another binary staged is rejected at P0a (`restore_rejected`); `CheckReader` reports `restore_pending` while `restore/pending.json` exists and `restore_inflight` while a `restore/<id>/inflight.json` exists, and `ROLLBACK_IGNORE_RESTORE=1` overrides only the first (COR8-8, OPS8-7) |
| `TestFactoryResetNativeAppCompletes`, `TestPostResetHookUnremovableEntryCompletes`, `TestPostResetHookSkipsKeptPaths`, `TestFactoryResetCrossDeviceTargets` | COR8-3, COR8-4, DATA8-1 (05-storage owns the last): a hookless app's reset (hello) leaves no `restore_inflight` after the reset boot (the kernel journals `hook_done`); a hookless app refuses `delete_library_files: true` with 422 `storage.ErrResetRequestInvalid` and stages nothing (COR10-9); a hook whose `RootDir` holds an entry it cannot remove still journals `hook_done`, boots, and reports `factory_reset_cleanup_incomplete`; a `<db parent>` or state directory under `RootDir` survives the wipe; targets on two filesystems (or an injected `EXDEV`) are each set aside beside themselves, and a set-aside failure moves back and boots the untouched store `factory_reset_failed` |
| `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault` (05-rbac §12 owns it; 09 S4 cites it) | A-17, R9-6: a `ResetFactory` staged through the native route without `delete_library_files` deletes no file under `RootDir`, runs no dangerous-root check and still resets the store, settings and auxiliary DBs; with `delete_library_files: true` and a `library_root` or `library_file_count` that is absent or differs from the fresh preflight count, the route answers 422 `library_files_confirm_required` naming the current root and count and stages no intent; with matching values, and through the `/system/factory-reset` alias (path values `delete_library_files=true`, `library_confirm=legacy`, no count check), HEAD's wipe runs; the one `storage.ResetRequest` reaches `ResetArgs` unchanged from the route, both aliases and books' S2b handlers |
| `TestBootCrashMatrix` (05-storage owns the invariants and the name; the kit is `coretest/crashmatrix`, §6.4.8) | A-8, R9-7, from S2b, over hello: killing the boot at every write, rename and fsync of §6.5.1's boot-writer table loses no owed duty, leaves a swap journal that resumes or rejects (a reset stopped mid-set-aside moves back before `rejected`), sidecars that agree with the store, and a store both the current and the previous build boot; a kill between `rechecked` and `swapped` followed by `prev` with `ROLLBACK_IGNORE_RESTORE=1` is still refused (`restore_inflight`); an error returned (`ENOSPC`, `EACCES`) on every operation from the in-flight rename to the first journalled move ends in `swapped_back` (restore) or `rejected` (reset) with the untouched store booted degraded (R10-6, DATA10-4); core's `Prev` is hello built from the PR's merge base (COR10-7); the S2b scenario is a staged restore without hooks, reaching `applied` through `opened → marker_written`, and the S4 r2 PR adds one with hooks; `TestBootCrashMatrixCoversEveryWriter` fails on an unlabelled durable write; every later boot writer adds its cases in its own PR |
| `TestRestartRequest_ExitsRestartCode` | OPS8-12: `RequestRestart` after a staged reset drains within the budget and exits `core.lifecycle.restart_exit_code` (75); with the committed unit (`Restart=on-failure`) the service comes back with no operator action, which the S1 and S2b done-checks repeat |
| `TestOfflineOpenIsNotForeign`, `TestOfflineOpenAfterForeignOpenKeepsEvidence` (05-storage owns both) | R7-4, DATA7-11, R8-2: an `app.OpenOffline` read-write session that commits a write and closes cleanly leaves the next serve boot with no foreign-open verdict from it, no new owed duty and the lineage unchanged; after a pre-guard (foreign) open, an offline write and then a serve boot still owe `journal` and `gen` (DATA8-3, SEC8-4); a read-only open writes nothing; an open with a pending migration step or a pending or in-flight swap is refused; `Offline.Storage` hands a nil `Maintenance` (SIMP8-9) |
| `TestLegacyConfigHonoured` | COR3-1, COR4-2, COR4-20: at S2 a value set only in the settings blob (rate limit off, a CORS origin) is effective after P3 through config's DB layer over `Options.LegacyConfigStore`; at S2 and S2b a write by ABO's legacy config path republishes it through the store's `Changed` (`legacy.ConfigExternalWriter`, SIMP5-5) without a restart; from S2b the boot value arrives through books' legacy config store at P4d step 1. The Basic gate is not asserted here: it stays gin's, reading `AppConfig`, until S3 |
| `TestBuildID_HeaderEqualsEmbeddedMeta` | FE3-2, FE4-1, FE5-1: for a `make build` binary, a Docker image and a goreleaser artefact, `X-App-Build` equals the embedded `<meta name="mediacore-build">`, and for every `embed_frontend` artefact that meta is neither `""` nor `dev` (the `npm run build` script stamped it, R5-7); no build path links an ID, and an API-only build answers `dev` |
| `TestBinaryIDDistinguishesBuilds` | R5-7, FE5-3: two `make build-api` binaries built from one commit with different code have different `app.BinaryID`s although both answer `X-App-Build: dev`; the second one's `RaisesLevel` step is deferred because `c/storage/selfserved/<binary id>` holds no record for it, and a staged-restore intent of the first is rejected by the second; `app.BinaryID(opts)` equals `buildid.Binary(opts.Version)`, so the `build` an S0 guard wrote equals the value the first S2b boot computes for the same binary (FE6-3) |
| `TestLegacyTaskAliases` | MOD6-6, from S5 (T4): with an app alias such as ABO's `core.diagnostics.export` ← `diagnostics.export` (S8), a run HEAD stored under the former ID resolves to the core kind and the op-ledger gate passes; an alias key that is not a registered core kind, or a value a module registers, fails P7 with `ProblemLegacyTaskAlias`; no non-test `core/` file spells the former kind |
| `TestStateDirFallbackMatchesHEAD` | OPS3-1: with no explicit state directory and an uncreatable default, P0a and P0 resolve the directory of `core.storage.path`, as HEAD does; an explicit value never falls back |
| `TestStateDir_OneResolverYAMLDBPath` | OPS4-8, OPS5-13 (05-config's name, the one name): with a DB path set only in ABO's YAML file and an uncreatable default, P0a, P0, the boot record, the restore intent and `.encryption_key` all resolve to that DB's directory through `app.ResolveStateDir`, as HEAD does; the update marker's `db_path` never changes the result |
| `TestBootRefusalCountsAsFailedBoot` (deferred-build with P−1, A-6) | OPS2-3, OPS5-15: with an update pending, a config refused in P0 increments the updater's attempt counter and exits with the marker's `restart_exit_code` (75), not 78, and the budget's last attempt restores `.prev` before any restore swap |
| `TestPreBootKeyInConfigFileRefused` | `core.paths.state_dir` in the config file fails P0; the same value by env boots |
| `TestDescribeInMemoryNoIO` | COR2-13: `Describe` over hello runs P0–P7 on `vfs.NewMem` with a temp state directory, touches no other path, never calls a derived-index `Load` (COR3-7), and returns the same OpenAPI document as a live boot |
| `TestStartOrder`, `TestStopOrder` | COR-8: a bus handler and an outbox consumer of hello never observe the module before its Start hook or after its Stop hook (instrumented hooks, `synctest`) |
| `TestStartRollbackReverseOrder`, `TestStopTimeoutAbandons` | hook ordering; uses `synctest` for timeouts |
| `TestWorkerPolicies` | `DegradeOnExit` vs `StopAppOnExit`, panic recovery |
| `TestRegistrarClosedAfterRegister`, `TestRegistrarContextCancelled` | late calls recorded; `r.Context()` cancelled with `app.New`'s ctx (COR-24) |
| `TestNewCleansUpCoreGoroutines` | `goleak`-style check: a boot failing in P7 leaves no core goroutine running (COR-24) |
| `TestDepsScoping` | write outside `m/hello/` refused; publish or notify to a topic outside hello's catalog entries refused; secret outside `modules.hello.` refused; `StartRequest.Actor` overwritten by the kernel; `library.Scope` writes attributed to hello; metric names under `MetricPrefix` |
| `TestModuleSystemActor_CannotStartUndeclaredKind` | SEC2-15, COR3-10: from a hello worker, starting a `core:system.manage` kind not in `TaskStarts` is refused; a declared one is allowed; `TaskStarts` naming a kind in no registration and no `TaskLedger` fails P7; naming a disabled module's ledger kind boots, and the start is refused with "unknown kind" |
| `TestModuleAuthMethod_CannotBeInteractive` | SEC2-16: `AuthMethod` with `Interactive`, `MayChangeCredentials` or an `OwnerVerifier` fails boot; `Options.Authenticators` may carry them |
| `TestLegacyAuthGinOnlyNamedModule` | SEC2-17: `LegacyAuthGin` from a module other than `Options.LegacyAuthGinUntil`, and an `OwnAuth` route without a compat method, fail boot |
| `TestRealtimeProjectionLazy` | COR2-1: a projection handler obtained in P6 answers 503 before the hub starts and streams catalog events after it |
| `TestDerivedIndexHooksAfterRegister` | COR2-15, COR3-7: `Load` runs at Run start step (3a), after the module's start hooks and before `Bus.Open`; the rebuild runs as the module's `<ID>.derived.<name>.build` definition with its own Effects, Timeout and Parallelism; a wrong kind name or a declaration without hooks fails boot |
| `TestReservedIDsIncludeLegacyTopSegments` | FE2-11: a module ID equal to the first segment of another manifest's `LegacyPrefixes`, or to an `Options.ReservedAPINamespaces` entry, fails P1 |
| `TestReservedSegmentsDisjointFromNonSPA` | FE2-7, FE3-1, FE3-9 (the one test of the P8 check): a `LegacyMount.TopSegments` entry that is a shell segment fails P8 with `ProblemSPASegmentCollision`; a `SPAShared` segment that is a shell segment (`login`, `invite`) boots, and `GET /login` serves the SPA while `POST /login` reaches the mount |
| `TestSPAOwner` | FE-1: a `ServesSPA` legacy mount owns `/` and `ui.Handler` is absent; two owners fail boot |
| `TestUIAppIDMismatch` | FE8-7: a bundle whose `mediacore-app` meta differs from `Options.AppID` fails P8 with `ProblemUIAppIDMismatch`; an equal one boots |
| `TestHelloEndToEnd` | §6.12 example: route permission, task under `-race` with `CPU()` workers, the seed migration runs once on the fresh store (so `world` is seen once, COR5-8, with 05-storage's `TestFreshInstallRunsSeedSteps`), and re-running the seed over a store that holds `name/world` leaves its count unchanged (COR6-4), settings default/override, `/api/core/v1/modules` lists nav entry |
| `TestSpecGoBlocksCompile` | COR-16: every Go block in this file marked as part of the hello example is extracted, compiled against core in CI, and **booted** with `coretest.New`, so a manifest-level refusal (an undeclared permission, a bad schedule key) fails the test, not only a compile error (COR2-14) |
| `archtest.TestNoGlobals`, `archtest.NoInit` over `core/...` and `core/examples/...` | R5, A5; `signal.Notify` and `signal.NotifyContext` both fail outside `package main`, and main packages under `core/examples/*/cmd/` are skipped as process owners, so hello's `main` passes (COR6-11) |
| `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` | MOD-7, 03 R2: hello is inside the core module path, so Go's `internal/` rule does not stop it importing `internal/...` or `kernel/...`; this check does, and `scripts/ci/core_consumer_smoke.py` builds a consumer under an unrelated module path with a negative compile check |

ABO (root module `internal/arch` or `cmd` tests):

| Test | Proves |
|---|---|
| `TestSecondModuleNoAudiobookEdit` | G6, from S5 (§6.4.10), a test of package `internal/books` (MOD5-11): `coretest.New(t, WithModules(books.Module(…), hello.New()))` boots; the test file is the only change needed — enforced by building it in CI with a `git diff --name-only` check that the hello wiring PR touched no `internal/` audiobook package (plan acceptance test) |
| `TestBooksDisabledInert` | G1 for the real module |
| `TestDarkAuthReleaseKeepsHEADSeeding` (S4 r1 done-check) | COR6-3: a fresh store booted on S4 r1 has the four seed roles and `_system`, and every r1 restart writes a new bootstrap token file, through books' start hook running HEAD's `seedRolesAndTokens`; the S4 r2 flip PR deletes that hook code |
| `TestVersionJSONFields` (S0) | FE6-4: `--version-json` prints exactly `{app, version, commit, ui_build_id, binary_id}`; `commit` is `vcs.revision` (with `+dirty` for a modified tree) or `""` in a build without VCS data; the output passes 05-updater's self-test |
| `TestFactoryResetFromLegacyRoute_S2b` (S2b done-check) | COR7-1, R7-2, SEC7-4, R8-1: books' gin route, behind HEAD's admin and owner chain, stages a `ResetFactory` through `StorageHandles.Maintenance` (intent written in the one slot with `hook_args`, live store and settings blob untouched, a restart requested); a non-admin or non-owner is refused by gin's chain; a dangerous `RootDir` answers 400 and stages nothing; the next boot is a fresh install whose blob holds HEAD's factory defaults, and books' post-reset hook removes the Open Library data only after the swap journal reads `applied`, and with `delete_library_files` the `RootDir` entries and the dump files are gone and `hook_done` lists no failure, because the intent's `hook_paths` bound the labelled filesystem (R10-6, COR10-4); an intent rejected at P0a (another binary, an expired or a moved open stamp) leaves the store and its settings as they were (05-storage's `TestFactoryResetRejectedLeavesStoreAndSettings`) |
| `TestSystemResetStagesStoreOnlyReset_S2b` (S2b done-check) | COR8-1: `POST /system/reset` behind HEAD's chain stages a `ResetStore`; the next boot is fresh and no file under `RootDir` or the Open Library dump directory is deleted |
| `TestLegacyStoresAreSingletons` (ABO, from S2b; each family from its stage) | R9-1, DATA9-2: every `legacy.Stores` constructor is called once per App; the subsystem, its route package, P4d's reconcilers and books' own P6 store all reach one backing instance per family, so each HEAD mutex (`counterMu`, `opsMu`, `apiKeyMu`, `inviteMu`) has one owner; the only other instance is P4d step 3's read-only one over the pre-swap store; with 05-authn's `TestLegacyRevokeRacesTouch_NeverUnrevokes` and `TestLegacyInviteDoubleAcceptOneWins` run through `auth.Service` and `storage.CommitLocked` over books' store |
| `TestBootCrashMatrix_ABO` (ABO root module, `extraction-gates`, from S2b) | R9-7, COR9-5: `coretest/crashmatrix` over ABO's full boot with books, HEAD fixtures and `Prev` built from `PREV_REF`, so books' P4d step 1 `Open` (the key-file write), its start hooks, the post-reset hook and `auth.BootWrites` over books' store are each broken at every write of §6.5.1's table |
| `TestCLIExposesCoreOfflineCommands` (ABO, from S2b) | R9-9, COR9-6: ABO's CLI exposes every entry of `app.OfflineCommands` the build has (`storage restore-checkpoint`; `secrets export-keyring`, `secrets rotate-derive` and `secrets set-install-id` from S3; `users reset-password` from S4) and runs it; `staging-restore` writes the marker through `Offline.WriteStagingMarker` and `staging-scrub` reads it through `app.ReadStagingMarker`, never through a module handle |
| `TestCLISubcommandsUseOfflineOpen` (ABO, from S2b; the `bench` tag included) | R7-4, DATA7-11, DATA8-4: every ABO subcommand that writes the store opens it through `app.OpenOffline`, `diagnostics query` and `dedup-bench` read-only; `seed` on an empty store is refused with "start `serve` once first"; `staging-restore` opens the store only to write the staging marker's key (A-10); only `pebble-inject-skip`, `pid-census` and `diagnostics reserved-prefs` open Pebble directly (an allow-list) |
| `TestStagingScrubRefusesUnmarkedStore`, `TestStagingRestoreWritesMarker` (ABO, from S0; 05-storage owns both) | A-10: `staging-scrub` over a store with no staging marker, a marker whose sidecar and key disagree, or no `--confirm-destroy-credentials` is refused and writes nothing, and so is a marked copy scrubbed by a binary whose `app.BinaryID` differs from the marker's `binary_id`; `staging-restore` writes both halves of the marker with the archive hash, a nonce and its own `binary_id` (R10-11, DATA10-5) |
| `TestABOManifestBootsAtS9` | FE3-1: the kernel boots ABO's real S9 manifest (books with its `TopSegments`, `SPAShared` `login`/`invite`, and the shell bundle) with no P8 problem |
| `TestS2bAppConfigFromBlob` | COR4-1, R4-6: from S2b a value set only in the blob is effective in `AppConfig` after boot, and a `PUT /api/v1/config` changing one field leaves every other non-default blob field byte-identical |
| `TestLegacyStoreSuites` (ABO; the one umbrella name, 05-storage §5S.10, 09 §9.2.3; SIMP5-9) | R4-1: books' `legacy.Stores` implementations pass core's store suites (`<pkg>test.<Interface>Suite`, §6.4.8, owned by each subsystem's file) with HEAD fixtures, and the legacy-only capability and `legacy.LibraryRootSource` cases of `legacy/legacytest` (05-storage §5S.3.11 "Legacy capabilities", R6-3) |
| `TestLegacyFamiliesWrittenOnlyByLegacyStores` | R4-1, COR5-13: an ABO archtest with an explicit allow-list per family (§6.11.6): each legacy family behind a store interface is written only from the package that implements that store (`internal/books/store`, `internal/books/taskstore`, the config-blob store package of 04 §2.4, `internal/metadata/dailyquota`) |
| `TestOnlyCompositionRootImportsModule` | R4-5, MOD4-2, MOD4-18: only `.`, `cmd` and `internal/books` (the composition root) import `mediacore/module` or `mediacore/app`, and nothing imports `internal/books` except `.` and `cmd`; it reads non-test imports only, so the G6 test inside `internal/books` is not an importer (MOD5-11) |
| `archtest.NoKernelImports` | R2 |

## 6.11 Migration from ABO

### 6.11.1 Strangler order

Named by 09 stage (MOD2-21); the old wave labels K0–K3 are retired.

1. **S2 — SDK and skeleton.** Land `module`, `kernel`, `app`, `coretest` and the hello skeleton
   (§6.4.10); core boots with zero modules. Each later stage adds its SDK members and grows hello;
   G6/G7 are proven at core level from S5.
2. **S2 — books wraps the server.** ABO's `serve` command first runs HEAD's pre-server steps through its
   own `internal/startup` (store open, books' migrations, encryption, `loadConfigFromDB`; MOD3-1), then
   calls `app.New(ctx, opts, books.Module(store))` with `Options.LegacyConfigStore`, books' legacy config
   store over that store (COR3-1). `internal/books` is the **composition root only** (R4-5, MOD4-2):
   `Module()`, the manifest and the `Registrar` calls; it imports `module`/`app` and every books package
   and is imported only by `.` and `cmd` (computed layer 6, no allowance). The shared helpers HEAD keeps
   in `pkg/plugin/sdk` (`PageBooks`), `internal/operations` (selection helpers) and `pkg/ops` (the
   books `Writer` contract), plus the task `Resource` declarations, subject resolvers and task-kind
   aliases, go to a package below it (`internal/books/contract`, name open, 04 §2.4; created at S5, when
   those sources leave, so it does not exist at S2, MOD5-9) that imports no
   `module`/`app` and nothing that imports the root, so a plugin importing a helper never forms a cycle.
   Below layer 6 no ABO package imports `module` or `app`; it receives what it needs (`ExtendStart`,
   loggers, scopes) as funcs or interfaces from the root (MOD4-18).
   `books.Register` builds today's `*server.Server` (the one recorded exception to `Register`'s
   side-effect rule until step 4, §6.4.5) and mounts its gin engine with
   `Registrar.MountLegacy` under the declared legacy prefixes (N7), permanently (cross-cutting
   resolution 2), with `ServesSPA: true` until ABO adopts the shell build (07 §7.19, FE-1). From S2b
   the kernel opens the store and those pre-server steps leave `serve`, each to the successor §6.4.3
   names (the settings load, key guard and encryption init into books' legacy config store at P4d
   step 1, R4-6; §6.5.1 "Stages"). `serviceregistry` keeps working
   **inside** books (it is app code; R5 binds core only).
3. **Books-internal, first PR after S2 — remove the global factory map:** each of the 24 registering
   files replaces `func init(){ serviceregistry.Register(def) }` with
   `func ServiceDefs() []serviceregistry.ServiceDef`; `books` passes one explicit list to
   `serviceregistry.NewContainerFrom(defs...)`; `Register`, `ResetForTest`, `IncludeAll/IncludeGroup`
   deleted. This alone removes test-isolation defects 1–4 of raw/07 §6 and the `batchpoller` class of
   bug. Cost: 24 files, mechanical, one PR.
4. **Books-internal, paced by S2b–S8 — constructors replace clusters** in `books/wire_*.go`, deleting
   their `ServiceDef` and their `Get[T]` call sites (113 sites, 31 files): each generic service goes
   when the stage that lands its core package arrives through `Deps`, then the audiobook clusters.
   The server construction that `Register` does today moves into start hooks here, which is what lets
   `Describe` run over books (§6.4.5). When the list is empty, delete `internal/serviceregistry`
   (1,157 lines incl. tests).

### 6.11.2 The 43 `serviceregistry` registrants

Rows are raw/07 §1.3. "core via Deps" means the service is replaced by a core service the kernel
builds; "books" means a plain constructor in the books module; final package placement is decided in
[04-package-mapping](04-package-mapping.md).

| Registrant(s) | Group | Destination |
|---|---|---|
| `eventbus` (`internal/plugin/register.go:14`) | core | core `events` via `Deps.Events` |
| `ophub`, `opregistry` (`internal/operations/registry/register.go:84,93`) | scheduler | core `tasks` (executor/hub in `internal/executor`) via `Deps.Tasks` |
| `updater`, `updatescheduler` (`internal/updater/register.go:60,76`) | scheduler | core `updater` (kernel-built from `app.Options.Updater`) |
| `configupdate` (`internal/config/register.go:13`) | core | core `config` (DB override layer) |
| `filesystem` (`internal/fileops/register.go:13`) | core | core `library` via `Deps.Library` |
| `system`, `dashboard` (`internal/server/registry_wire.go:97`, `internal/sysinfo/register.go:12`) | core | core `health` diagnostics + a books dashboard widget for library counts |
| `metricsstore` (`registry_wire.go:276`) | core | core `metrics` persistence if kept (05-metrics decides); else deleted |
| `searchindex` (`internal/search/register.go:106`) | scheduler | books, on core `storage/index` via `Deps.Storage.Indexes` |
| `pebble-activitystore`, `activitystore`, `activity`, `activitywriter` (`internal/activity/register.go:37,75,105,120`) | activity | core (task history + `audit`; 04 decides the split); `DatabasePath` gate disappears because core always has storage |
| `embeddingstore`, `chromemstore`, `aijobsstore`, `aiscanstore`, `pipelinemanager`, `dedup`, `embedclient`, `llmparser`, `metadatascorer`, `metadatallmscorer` (`registry_wire.go:109,133,165,298,319,179`; `internal/ai/register.go:33,105,136,158`) | ai | books (`wire_ai.go`), on core AI instances via `Deps.Integrations` (05-ai); one `if cfg.AI.Enabled` replaces per-`Build` gating |
| `batchpoller` (`internal/server/batch_poller_register.go:18`) | none (never built) | books, constructed explicitly when AI batch is enabled — **fixes** the latent defect |
| `metadatastate`, `metafetch`, `olservice` (`internal/metafetch/register.go:12,22,35`) | core | books, on core metadata adapters via `Deps.Integrations` and `Deps.HTTP` |
| `itunes` (`registry_wire.go:343`) | core | books |
| `work`, `audiobook`, `organize`, `quarantine`, `scan`, `merge`, `importpath`, `batch` (`internal/work/register.go:13`, `internal/audiobooks/register.go:12,21`, `internal/quarantine/register.go:14`, `internal/scanner/register.go:13`, `internal/merge/register.go:12`, `internal/importer/register.go:12`, `internal/batch/register.go:13`) | core | books |
| `metafetchplugin`, `itunesplugin`, `delugeplugin`, `dedupplugin`, `acoustidplugin` (`internal/plugins/*/register.go`) | plugins | books task definitions (`r.Tasks(...)`); deluge's client moves to core `integrations/download/deluge` (05-download) |

Count check by service name: eventbus 1, ophub/opregistry 2, updater 2, configupdate 1, filesystem 1,
system/dashboard 2, metricsstore 1, searchindex 1, activity 4, ai 10, batchpoller 1, metafetch 3,
itunes 1, domain 8, plugins 5 = **43**.

### 6.11.3 `internal/plugin` and `pkg/plugin/sdk`

| Today | Becomes |
|---|---|
| `internal/plugin.Plugin`, `Registry`, `Global()`, `Register` (`plugin.go:25`, `registry.go:19,33`) | deleted at S7 (04 §8.3). The only implementor, webhook (`internal/plugins/webhook/plugin.go:33`), becomes the core `integrations/notify/webhook` adapter enabled in `Options.Adapters`, subscribing through `events` |
| `plugin.EventBus` (`internal/plugin/events.go:85`, goroutine per handler with the publisher's ctx) | core `events` (05-events: bounded dispatch, bus-lifetime ctx, as design Q1 decided) |
| `plugin.PluginRouter`, `/api/v1/plugins/{id}` (`plugins_init.go:42`) | deleted; module routes go through `Registrar.HTTP()` inside the core chain. The 6 `/api/v1/plugins` routes (raw/09 §1.1) become `/api/core/v1/integrations/...` (configured adapter instances, health; 05-metadata M1.3) |
| `plugin.Capability`, vestigial `DownloadClient`/`MediaPlayer`/`Notifier` (`plugin.go:17-21,52-57`) | deleted; capabilities are `integrations.Kind` + the `integrations/*` contracts |
| `config.AppConfig.Plugins` enable flags | `core.modules.<id>.enabled` (modules) and adapter instance `enabled` (adapters); migration of the stored value in 09 |
| `pkg/plugin/sdk.Plugin`/`Registry` (`plugin.go:19`, `registration.go:12`) and its aliases | deleted after the six bundles become `tasks.Definition` lists in books; `PageBooks` (`iterate.go:41`) becomes a helper over `tasks.RunItems` in the books helper package below the composition root (`internal/books/contract`, §6.11.1 step 2, R4-5); `tools/cmd/sdkguard` replaced by `archtest` R2 |
| `pkg/ops` | promoted into core `tasks` (05-tasks, N9) with audiobook types removed |
| `internal/opscatalog` (`catalog.go:56`, `TestCatalogLinkedIntoBinary`) | deleted: explicit lists make "linked but unregistered" impossible |

### 6.11.4 The 111 non-test `func init()`

Classes from raw/07 §5.1.

| Class | Count | Replacement | Lands in |
|---|---|---|---|
| A `serviceregistry.Register` | 24 files / 43 calls | §6.11.2 (explicit list, §6.11.1 step 3, then constructors, step 4) | books / core |
| B `maintenance.Register(&job{})` (`internal/maintenance/jobs/*.go`) | 34 | each file exports its job value; `books/tasks_maintenance.go` returns the explicit list wrapped as `tasks.Definition` (the existing `maintenance_job_op.go` bridge becomes that wrapper); `maintenance.Register`/global `byID` deleted | books |
| C `addOpRegistrar(...)` (`internal/server/*_op*.go`) | 33 files / 48 calls | each file exports `func xxxTasks(d bookDeps) []tasks.Definition`; one `books/tasks.go` concatenates them; `opRegistrars` slice (`op_registrars.go:23`) deleted | books |
| D `aijobs.Register` (`internal/ai/author_dedup_job.go:132`, `dedup_review.go:242`) | 2 | an `aijobs.Registry` instance constructed in books `wire_ai.go`, both callbacks registered on it explicitly | books |
| E `plugin.Register` (webhook) | 1 | core notify adapter (§6.11.3) | core |
| F hooks: `cmd/child_mode.go:24` (`registry.ChildEnvFunc`) | 1 | `tasks` executor option `ChildEnv func() []string` set by kernel from config (05-tasks) | core |
| F `internal/metadata/google_quota.go:61` (`dailyquota.Install`) | 1 | instance-scoped quota in `providerhttp` (05-metadata) | core |
| F `internal/search/bleve_index.go:68` (writes bleve's `scorch.RegistryAsyncErrorCallbacks`) | 1 | the map is **third-party global state**: core `internal/bleveindex` registers one callback per opened index under a unique name with `sync.Once`, routes errors to that index's own logger/metric, and is the single allowlisted entry in `TestNoGlobals` with this reason | core |
| F `internal/search/field_routing.go:159` (`database.SetCollectionPerViewerClassifier`) | 1 | explicit call in books wiring (or a constructor parameter) | books |
| F `internal/server/handlers/metadata_cache_query.go:398` (fills `reviewSearchFields`) | 1 | a composite-literal package var (immutable data) | books |
| G `internal/database/embedding_store.go:60` (zstd encoder) | 1 | `sync.OnceValue` | books |
| G `internal/server/csp.go:85` (initial CSP state) | 1 | field of core `ui`/`httpapi` handler instance, computed at construction (07 §7.6) | core |
| G `pprof_debug.go:105` (env-driven pprof) | 1 | explicit call from ABO `main` under the `pprof` tag; core offers `app.Options` nothing for this (debug listener stays app-level) | ABO main |
| H cobra command/flag wiring (`cmd/*.go`, `cmd/mtls-bridge/main.go:52`, `tools/cmd/oplint/analyzer.go:96`) | 9 | unchanged in ABO (app CLI, outside R5); `oplint` moves to core with `tasks` and drops its `init` for an explicit `analysis.Analyzer` flag set | ABO / core tool |
| **Total** | **111** | | |

Test-file inits (16, raw/07 §5.2): the five `maintenance.Register` probes become tasks registered on
a `coretest` harness (no global); the eight `gin.SetMode` stay with the books legacy mount (gin is
permanent, cross-cutting resolution 2; COR2-19); `gob.Register` and the compile-time assertion stay.

### 6.11.5 Other ABO pieces

| Today | Becomes |
|---|---|
| `server.NewServer` (`server.go:653`) and `Server.Start` (`server_lifecycle.go:245`) | books `Register` (S2: wraps them; §6.11.1 step 4: dissolves them into constructors and start hooks) + kernel phases P−1 to P8 |
| `startup.Table` Fatal/Degraded steps (`internal/startup/steps.go`) | stays in ABO (MOD3-1): ABO's `serve` runs HEAD's seven steps through it at S2 only (`cmd/root.go:274`, a non-test user); from S2b each has the successor §6.4.3 names (kernel P2, `InstallProcessDefaults`, `app.ResolveStateDir`, books' legacy config store at P4d step 1), so `cmd`'s last use goes at S2b (R4-6, MOD4-17). Core's own steps are the kernel phase table from S2; Degraded steps become health checks registered by the owning service |
| HEAD's every-boot store writers (`initPebbleStore`'s import-path key migration, counter initialisation and verified-email reconcile, `internal/database/pebble_store.go:755-760`; `ReconcileOpsV3WithV2`, `internal/database/store.go:1861-1871` @748fd787a) | the import-path keys as a books versioned migration (P4b); counters need nothing, because books reads an absent counter as HEAD's seed; the verified-email reconcile in books' start hook (Run step 2) until S4 r2, then books' legacy auth store inside `auth.BootWrites` (P4d step 4); `ReconcileOpsV3WithV2` in books' start hook before it builds its ops registry until S5, then the run store's `Recover` inside `Executor.Run` (Run step 5); books' constructors write nothing (DATA4-10, COR5-7, 05-storage §5S.9.3) |
| HEAD's Start-time identity writers (`seedRolesAndTokens`: seed roles, `_system`, the never-expiring-key stamp, the bootstrap token, the optional start-up read-only key; `internal/server/server_lifecycle.go:355,1098-1140` @748fd787a) | books' start hook (Run step 2) over books' own code through S4 r1; from S4 r2 P7's seed-role reconcile and `auth.BootWrites` (P4d step 4), and the r2 flip PR deletes the hook code (COR6-3) |
| `opRegistrationGate`, `opCatalogGate` (`server_lifecycle.go:66,88`) | P6/P7 validation |
| `scanner.SetStore(store)` (`server.go:805`) and similar setters | constructor parameters in books wiring |

### 6.11.6 Books' legacy stores (R4-1)

During the extraction books keeps every one of ABO's 258 families as its own claim, held in place
(05-storage §5S.9.1 is the one ownership table), and no core subsystem reads them. Where a core
subsystem serves data ABO already keeps, books returns its own implementation of that subsystem's
store interface in `Manifest.LegacyStores`, a `legacy.Stores` value (§6.4.2, §6.4.11, R6-3), and the
kernel builds it at P4c over books' own handles (§6.5.1). Shape, as books' manifest uses it:

```go
// internal/books/manifest.go (ABO; the composition root, §6.11.1 step 2).
// A field is added in the PR of the stage that lands its subsystem
// (§6.4.10); before that the rows are served by books' own code exactly as
// at HEAD. The constructors are thin functions of this package; the
// implementations are ABO's store code over ABO's keys and row formats in
// the packages 04 §2.4 names, none of which imports internal/books back
// (R5-2, COR5-13). Every constructor returns a view over one memoised
// backing store per legacy.Handles value, the same instance books' own
// store at P6 is, so HEAD's per-instance mutexes are shared (R9-1,
// TestLegacyStoresAreSingletons): the config-blob-backed stores (Config, Integrations, the
// books-root half of Roots) in internal/books/configstore (name open in
// 04 §2.4), the task store in internal/books/taskstore, the provider
// quota counters in internal/metadata/dailyquota, and the rest in
// internal/books/store. TestLegacyFamiliesWrittenOnlyByLegacyStores allows
// writes to each family only from its implementing package (§6.10).
LegacyStores: legacy.Stores{
	Config:       newConfigStore,   // S2b (core config writes from S3); internal/books/configstore
	Auth:         newAuthStore,     // S4; internal/books/store
	Roles:        newRoleStore,     // S4; internal/books/store
	Tasks:        newTaskStores,    // S5; internal/books/taskstore
	Logs:         newJournalStore,  // S5; internal/books/store
	Roots:        newRootSource,    // S7, legacy-only (legacy.LibraryRootSource); internal/books/configstore and internal/books/store
	AIJobs:       newAIJobStore,    // S7; internal/books/store
	Gates:        newGateStore,     // S7, routed; internal/metadata/dailyquota and internal/books/store
	Integrations: newInstanceStore, // S7, routed; internal/books/configstore
	NotifyRules:  newRuleStore,     // S7, routed; internal/books/store
},
// The full-text dirty set is not a legacy.Stores member: books passes it as
// index.Spec.Dirty when its Register opens the search index (SIMP5-2).
```

What this guarantees:

- **One stored format, so any build reads the store.** Every build of the extraction, the hotfixed
  HEAD included, reads the same rows; a rollback needs no floor build, no claim hand-over and no
  rebuild, and the only per-owner level steps are real format changes (books' own migrations; later,
  each cutover).
- **Core holds no audiobook key name or row format (03 R1, SIMP4-2).** Key names, codecs and HEAD's
  store code stay in ABO behind the core interfaces; core neither re-implements them nor carries a
  second copy of the ops format. Core enforces its invariants above the interface (upgrade-on-use, the
  staging rules) and ships a store suite per interface, which books' implementations
  pass with HEAD fixtures (§6.10). The cost is the trade the simplicity review named: the store
  interfaces are SDK, frozen by the API-stability gate, and write exclusivity on ABO's ranges is held
  by package structure (the ABO archtest) and HEAD's lock discipline rather than by storage (A-3), held
  across core's commit by each store's `legacy.WriteLock` through `storage.CommitLocked`, over the one
  instance of each family the kernel builds (R9-1). Everything that exists only for the
  legacy stores (the supplier, the constructor fields, the handles, the capabilities, the legacy root
  source and their cases) sits in package `legacy`, outside the API promise, so none of it is frozen
  (§6.4.11, R6-3).
- **Representation-neutral, so a rollback costs nothing (A-2, ADV-S1).** Books' implementations read
  and write HEAD's rows in HEAD's representation only: raw `sess:` rows and HEAD's one-time tokens,
  `abk_` keys holding legacy atoms, roles holding legacy atoms only, plaintext blob secrets (the four
  HEAD already encrypts, `internal/config/persistence.go:1955-1958` @748fd787a, stay encrypted) and
  HEAD's auth-enable precedence. No secret is moved, no row gains a second representation, and no
  family exists beside HEAD's, so an older build never reads a widening and never loses a credential.
  Core-namespaced scopes are an API-level mapping over the legacy atoms (`rbac.LegacyAtom`), so D7
  holds; a legacy-backed role or key holds no scope without a legacy atom until the identity cutover.
  At-rest protection on legacy-backed instances is HEAD's (no regression, no improvement); native
  stores (hello, TV, Movies) keep the full design. The hardening ships after S10 as the identity,
  secrets and auth-precedence cutovers on N15's one-way, floored machinery (12, 14).
- **Cutover is deferred.** Moving a family to `c/<sub>/` and deleting the legacy rows is a later,
  per-family, one-way step, decided on measured data, checkpointed by P4a and behind the owner's level
  guard; its rollback floor is the cutover release itself. It is the only step in which core reads a
  module's rows, so storage grants are its deferred-build members (05-storage). The cutover build
  still reads the legacy data through the family's legacy store, so a **later** build stops setting
  the family's `legacy.Stores` member behind the cutover's `UpgradeFloor` (05-storage §5S.3.13 "After
  it", SIMP5-13), and the PR that removes the last setter deletes the member and that store's
  legacy-only capabilities from package `legacy` (`Manifest.LegacyStores` itself stays in v1). Because `legacy` is outside the API promise, that
  deletion is a minor change, never a major or a `/v2` module path (12 §12.4.2; SIMP6-2, R6-3).
  Nothing in this file schedules one.
- **The live security hotfixes** (the six early fixes of 05-authn §11.1, the single list: session-token
  exposure, unscoped child keys, revoked-key re-activation, the `RequireAdmin` scope check, the Basic
  gate's empty password and the API-key revocation race (every `apikey:` writer takes `apiKeyMu`, R6-5);
  `todo.d/2026-11-04_platform_core_followups.md`; SEC5-11, SEC6-3) ship in ABO
  independently and early. They are ordinary HEAD fixes, not a compatibility build.

An app with no legacy data (hello, a TV app) sets no `LegacyStores`, and the same core subsystems
use their native `c/<sub>/` stores (SIMP4-3). Which implementation serves a record is fixed by the
build's suppliers (exclusive stores) or by the legacy store's `Covers` (routed stores: provider gates,
integration instances, notify rules, whose native store serves every record not covered; SIMP5-1;
library's legacy-only `legacy.LibraryRootSource` serves beside its config-entry roots, SIMP6-3),
and the choice is recorded in the store, so it never changes between boots of one
store during the extraction (P2 and P4c, `ProblemLegacyStore`, COR7-4).

## 6.12 Worked example: the `hello` module

Lives at `core/examples/hello`: a **package inside the core module** (03 §3.3, MOD-7), excluded from
the API-stability gate, so both core CI and ABO's G6 test can import it with no second `require` or
`replace`. Layout:

```
core/examples/hello/
  hello.go          Module, Manifest, Register
  greeting.go       service + route
  count.go          task hello.count
  migrations.go     migration 1
  ui_embed.go       //go:build embed_frontend — embeds web/dist (sibling directory, MOD-20)
  ui_nonembed.go    //go:build !embed_frontend — zero ui.Bundle
  cmd/hello/main.go app main
  web/              hello's web package (07 §7.3.1); Vite outDir web/dist
```

`core/go.mod` carries `ignore ./examples/hello/web` beside `ignore ./web`, so `node_modules` there
never enters `./...` (MOD-10, owned by 03/13). Every Go block below compiles against the owning files
as written: 05-rbac §3.1–§3.3 (`Permission`, `PermissionSpec`, `RoleViewer`, `Need`), 05-http-api
§3.1–§3.3 (`Handle`, `Route`, `In`, `Invalid(fields ...FieldError)`), 05-tasks 5T.3.1–5T.3.7
(`Task`, `TaskSpec`, `Common`, `Permissions{Start rbac.Requirement}`, `RunItems`, `ItemsOptions`),
05-storage §5S.3.1, §5S.3.4–§5S.3.5 (`Spec`, `Keyspace`, `KV`, `Migration` with `Seed`, `MigrationTx`) and
05-config §1.3 (`Define`, `Docs`, `Handle`). `TestSpecGoBlocksCompile` (§6.10) keeps it that way.

### 6.12.1 Module, manifest, settings, migration

```go
// core/examples/hello/hello.go
package hello

import (
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

const ID module.ID = "hello"

const (
	PermRead     rbac.Permission = "hello:read"
	PermAdmin    rbac.Permission = "hello:admin"
	PermSettings rbac.Permission = "hello:settings.manage" // default read/write permission of section modules.hello (05-config §1.3.4)
)

// Settings is section modules.hello (05-config §1.3).
type Settings struct {
	Greeting   string `json:"greeting"`
	MaxNameLen int    `json:"max_name_len" schema:"minimum=1,maximum=256"`
}

type Module struct {
	settings config.Section[Settings]
}

func New() *Module {
	return &Module{
		settings: config.Define("modules.hello", Settings{Greeting: "Hello", MaxNameLen: 64},
			config.Docs(map[config.Path]string{
				"modules.hello.greeting":     "Prefix of every greeting",
				"modules.hello.max_name_len": "Longest accepted name",
			})),
	}
}

func (m *Module) Manifest() module.Manifest {
	return module.Manifest{
		ID:             ID,
		Version:        "1.0.0",
		DisplayName:    "Hello",
		MinCoreVersion: "0.1.0", // compared with version.Version (MOD-12)
		DefaultEnabled: true,
		Permissions: []rbac.PermissionSpec{
			// Default grant: the core seed role "viewer" may read greetings.
			{Name: PermRead, Title: "Read greetings", Class: rbac.ClassView,
				DefaultGrants: []rbac.RoleName{rbac.RoleViewer}},
			// No default grant: only core:admin holders run maintenance. ClassAction
			// (the zero value), so it can be a task's Perms.Start (05-rbac §4.10).
			{Name: PermAdmin, Title: "Run hello maintenance"},
			// Required because hello declares section modules.hello: 05-config
			// makes <ID>:settings.manage its default permission, and a route or
			// section naming an undeclared permission fails boot (COR2-14). No
			// default grant (SEC-25).
			{Name: PermSettings, Title: "Change hello settings"},
		},
		Storage: storage.Spec{
			Keyspace: true, // m/hello/
			Migrations: []storage.Migration{
				// Seed: runs on a fresh install too, with every later step of
				// hello; a step below it would be stamped applied there
				// without running (COR5-8, COR6-4).
				{Version: 1, Name: "seed-names", Compat: storage.Additive, Seed: true, Up: seedNames},
			},
		},
		TaskLedger: []tasks.Kind{"hello.count"},
		Settings:   []module.SettingsSection{module.Settings(m.settings)},
		UI: module.UIDescriptor{
			Pages:          []string{"greeting"},
			Nav:            []string{"hello"},
			SettingsPanels: []string{"hello"},
		},
	}
}

func (m *Module) Register(r *module.Registrar) error {
	d := r.Deps()
	svc := &service{
		names:    d.Storage.Keyspace.Sub("name"), // m/hello/name/<name> -> decimal count
		settings: module.Setting(r, m.settings),
		log:      d.Log,
	}
	r.Resources("name")
	registerGreeting(r.HTTP(), svc)
	r.Tasks(countTask(svc))
	return r.Err()
}
```

```go
// core/examples/hello/migrations.go
package hello

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
)

// seedNames writes the first name so the count task has something to count.
// Keys are relative to m/hello/; "name/world" is the key the service reads as
// Keyspace.Sub("name") + "world". A seed must leave rows that already exist
// unchanged (05-storage Migration.Seed), so it writes only an absent key and
// never resets a count (COR6-4).
func seedNames(ctx context.Context, tx *storage.MigrationTx) error {
	key := []byte("name/world")
	_, err := tx.Keyspace().Get(ctx, key)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("hello: seed: %w", err)
	}
	if err := tx.Set(key, []byte("1")); err != nil {
		return fmt.Errorf("hello: seed: %w", err)
	}
	return nil
}
```

`config.Define` declares the section; the kernel registers it before the config store loads
(05-config §1.3.2) and `module.Setting` returns the live `*config.Handle[Settings]` whose `Get()` is
one atomic load and reflects hot-reloaded fields. `storage.MigrationTx` is the batch-scoped writer of
05-storage §5S.3.5, already confined to the module's keyspace.

### 6.12.2 Route `GET /api/hello/v1/greeting`

```go
// core/examples/hello/greeting.go
package hello

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

type service struct {
	names    storage.Keyspace
	settings *config.Handle[Settings]
	log      *slog.Logger
}

type GreetingQuery struct {
	Name string `query:"name"`
}

type Greeting struct {
	Message string `json:"message"`
	Seen    int64  `json:"seen"` // how often the name was recorded; 0 if unknown
}

func registerGreeting(g *httpapi.Group, s *service) {
	httpapi.Handle(g, httpapi.Route{
		Method:  http.MethodGet,
		Path:    "/greeting",
		Name:    "getGreeting",
		Perm:    rbac.Need(PermRead),
		Summary: "Greet a name and report how often it was recorded",
	}, s.greet)
}

func (s *service) greet(ctx context.Context, in *httpapi.In[GreetingQuery]) (Greeting, error) {
	cfg := s.settings.Get()
	name := in.Req.Name
	if name == "" || utf8.RuneCountInString(name) > cfg.MaxNameLen {
		return Greeting{}, httpapi.Invalid(httpapi.Field("/query/name", "invalid_length",
			fmt.Sprintf("must be 1..%d characters", cfg.MaxNameLen)))
	}
	var seen int64
	switch v, err := s.names.Get(ctx, []byte(name)); {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return Greeting{}, fmt.Errorf("hello: read %q: %w", name, err)
	default:
		if seen, err = strconv.ParseInt(string(v), 10, 64); err != nil {
			return Greeting{}, fmt.Errorf("hello: bad count for %q: %w", name, err)
		}
	}
	return Greeting{Message: cfg.Greeting + ", " + name, Seen: seen}, nil
}
```

`httpapi.Handle` (05-http-api §3.1) decodes the query into `In.Req`, encodes the result, maps errors to
the core problem envelope (`Invalid` → 422 with `errors[]`), enforces `Perm` (R6) and contributes
operation `hello_getGreeting` to the OpenAPI document. The route resolves to `/api/hello/v1/greeting`.

### 6.12.3 Task `hello.count`

```go
// core/examples/hello/count.go
package hello

import (
	"fmt"
	"strconv"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

type CountParams struct{}

type CountResult struct {
	Names     int64 `json:"names"`
	Greetings int64 `json:"greetings"`
}

func countTask(s *service) tasks.Definition {
	return tasks.Task("hello.count", tasks.TaskSpec[CountParams, CountResult]{
		Common: tasks.Common{
			Title:   "Count recorded names",
			Help:    "Sums the per-name counters. Read-only.",
			Effects: tasks.ReadOnly(tasks.Res(string(ID), "name")),
			Perms:   tasks.Permissions{Start: rbac.Need(PermAdmin)},
			Resume:  tasks.ResumeFromZero,
		},
		Run: func(rc *tasks.RC, _ CountParams) (CountResult, error) {
			var kvs []storage.KV
			for kv, err := range s.names.Scan(rc.Context(), storage.ScanOptions{}) {
				if err != nil {
					return CountResult{}, fmt.Errorf("hello: scan: %w", err)
				}
				kvs = append(kvs, kv.Clone())
			}
			res, err := tasks.RunItems(rc, tasks.CPU(), kvs,
				func(ic *tasks.ItemRC, kv storage.KV) error {
					n, err := strconv.ParseInt(string(kv.Value), 10, 64)
					if err != nil {
						return fmt.Errorf("hello: bad count at %q: %w", kv.Key, err)
					}
					ic.Count("names", 1) // atomic, folded into res.Counters
					ic.Count("greetings", n)
					return nil
				},
				tasks.ItemsOptions[storage.KV]{
					Label: func(kv storage.KV) string { return string(kv.Key) }, // pure: item only
				})
			return CountResult{Names: res.Counters["names"], Greetings: res.Counters["greetings"]}, err
		},
	})
}
```

`tasks.CPU()` is a `tasks.Parallelism` of `runtime.NumCPU()` workers sharing the executor CPU budget;
the zero `Parallelism` is invalid, so a forgotten value fails before any item runs instead of silently
running one worker (R7, N8; the exact failure CLAUDE.md records for `RunItemsOptions.Concurrency`).
Counters go through `ItemRC.Count` and `Label` receives only the item, so there is no shared mutable
state in worker goroutines. (A library-scale source would use `tasks.RunSeq` over the scan instead of
collecting it.)

### 6.12.4 Nav entry and settings panel (client side)

```ts
// core/examples/hello/web/src/index.ts
import { defineModule } from '@mediacore/shell';
import WavingHandIcon from '@mui/icons-material/WavingHand';
import { manifest } from './manifest.gen'; // generated from Manifest() — 07 §7.4.1

export const helloModule = defineModule(manifest, {
  routes: [{ id: 'greeting', path: 'greeting', lazy: () => import('./GreetingPage') }],
  nav: [{ id: 'hello', label: 'Hello', icon: WavingHandIcon, to: 'greeting', permission: 'hello:read' }],
  settingsPanels: [{ id: 'hello', label: 'Hello', lazy: () => import('./HelloSettings') }],
  permissionsHints: { view: ['hello:read'], actions: { runCount: 'hello:admin' } },
});
```

### 6.12.5 `main.go` and the embedded UI

```go
// core/examples/hello/cmd/hello/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/examples/hello"
)

var version = "dev" // set by -ldflags

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, app.Options{
		AppID:       "hello",
		DisplayName: "Hello",
		Version:     version,
		EnvPrefix:   "HELLO",       // the default, strings.ToUpper(AppID), spelled out
		StateDir:    "hello-state", // compiled default; flags and HELLO_* env override it, never the config file
		UI:          hello.UIBundle(), // ui_embed.go (embed_frontend) or ui_nonembed.go
	}, hello.New())
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // a *module.ValidationError prints every problem
		os.Exit(2)
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

```go
//go:build embed_frontend

// core/examples/hello/ui_embed.go
package hello

import (
	"embed"

	"github.com/falkcorp/mediacore/ui"
)

// web/dist is a sibling of this file, so the pattern needs no ".." (MOD-20).
//
//go:embed all:web/dist
var webDist embed.FS

// UIBundle returns hello's built SPA (07 §7.6).
func UIBundle() ui.Bundle { return ui.MustFromFS(webDist, "web/dist") }
```

```go
//go:build !embed_frontend

// core/examples/hello/ui_nonembed.go
package hello

import "github.com/falkcorp/mediacore/ui"

// UIBundle returns the zero Bundle: the API-only placeholder.
func UIBundle() ui.Bundle { return ui.Bundle{} }
```

The embed files sit in package `hello`, next to `web/`, because `//go:embed` resolves relative to the
source file's directory and forbids `..` (MOD-20). The example's Makefile target builds `web/` (Vite
`outDir: 'web/dist'`, the default) before `go build -tags embed_frontend -o bin/hello
./examples/hello/cmd/hello` (07 §7.6), so the binary lands under `core/bin/`, which `core/.gitignore`
ignores (07 §7.3.1, FE8-4). ABO's G6 test imports `hello` without the tag and gets the zero bundle. `web/dist` is on
core's own embed-output list, which `make -C core ci` checks with its own tolerance of an unbuilt
`dist` (`go list` reports `pattern all:web/dist` against package directory `examples/hello`, which the
check normalises to a module-relative path before comparing, 03 §3.2 item 4, MOD8-11) and no Dockerfile cross-check, because core has no image. The same tolerance lets every check run on a tree with no built frontend (R10-9, MOD10-8): the tag-union tools of 03 §3.5.1 item 4 run `go list -e` and ignore exactly that embed-pattern error on hello, and core's `vet-darwin` runs without `embed_frontend` (`go vet` has no `-e`); hello's `ui_embed.go` imports only `embed` and has no GOOS constraint, and is compiled by the tagged hello build of core's e2e job (07 §7.18); it is never on ABO's root list
`scripts/ci/embed_build_outputs.txt`, whose entries must each match a `COPY --from` line (MOD7-6). The
S9 PR that creates `web/` adds `core/examples/hello/web/.gitignore` beside `core/web`'s (07 §7.3.1,
FE7-1).

### 6.12.6 Test

```go
// core/examples/hello/hello_test.go
package hello_test

import (
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/examples/hello"
	"github.com/falkcorp/mediacore/rbac"
)

func TestHello(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := coretest.New(t,
			coretest.WithModules(hello.New()),
			coretest.WithUser("v", hello.PermRead),
			coretest.WithUser("a", rbac.CoreAdmin))
		coretest.AssertPermission(t, h, http.MethodGet, "/api/hello/v1/greeting?name=x", hello.PermRead)

		var g hello.Greeting
		if st := h.As("v").GetJSON(t, "/api/hello/v1/greeting?name=world", &g); st != http.StatusOK || g.Seen != 1 {
			t.Fatalf("status %d, greeting %+v", st, g)
		}
		if run := h.RunTask(t, "hello.count", hello.CountParams{}); !run.Succeeded {
			t.Fatalf("count: %s: %s", run.State, run.Error)
		}
	})
}
```

## 6.13 Open items

| # | Item | Owner |
|---|---|---|
| O1 | Parallel hook start (boot-time optimisation), only if measured boot > 5 s on a large library | 06 |
| O2 | Exact `storage.Migration`/`MigrationTx`, `tasks.TaskSpec`/`RunItems` options, `httpapi` option names: this file follows the owning `05-*` files; `TestSpecGoBlocksCompile` detects drift | 05-storage, 05-tasks, 05-http-api |
| O3 | `Deps.Tasks` may enqueue another module's kinds by `Kind` string (R3: modules talk via tasks), permission-checked as the stamped actor; outside a request only the module's own kinds and `Manifest.TaskStarts` pass (SEC2-15); a disabled target module's kind is refused with `tasks` "unknown kind" | 05-tasks |
| O4 | `metricsstore` and activity-log destinations | 04, 05-metrics, 05-rbac |
| O5 | The `go`-statement warning list in `archtest` may be noisy for books during §6.11.1 steps 2–4; ratchet rather than fail | 06 / plan |
| O6 | Module-level settings that need a restart vs hot reload: per field in `config.Section` tags (`reload:"restart"`) | 05-config |
| O7 | (Closed in the integration pass: every `Deps` type of §6.4.4 is defined by its owning file, and 05-tasks states the actor stamping.) | — |

## 6.14 Alignment with the parallel contract files

This file owns `module.Manifest`, `module.Registrar`, `module.Deps` and `app`. Every row names the
owning symbol. Where an owning file still describes a superseded path, the row says what changes there.

| Contract | What 06 uses (owning symbol) | Reconciliation |
|---|---|---|
| 05-http-api §3, §4.9–§4.10, §8.2 | `Registrar.HTTP() *httpapi.Group`, `httpapi.Handle`, `httpapi.Route`, `httpapi.Invalid(httpapi.Field(...))`, `MountLegacy(httpapi.LegacyMount)` with `ServesSPA`, `Auth`, `TopSegments`, `DefaultCSP`, `Router.SPA`, `Router.NonSPA`, `Router.NonSPASegments()`, `TopSegments`, `SPAShared`, `LegacyMount.BuildID`, `ErrPublicNotAllowed`, `httpapi.ReservedSegments()`, subtree claims | `Manifest.LegacyPrefixes` feeds subtree claims and reserved IDs only; `NonSPA` = `TopSegments` minus `SPAShared` (FE3-1, R3-5); `Manifest.PublicRoutes` is the reviewed public list (COR3-3); stage 5 writes `X-App-Build` from the embedded bundle only (`UI.BuildID()` from S9, `LegacyMount.BuildID` before), as 05-http-api §4.2 states; that is the UI build ID, never the binary's identity (`app.BinaryID`, R5-7); there is no `app.Options.BuildID` (FE4-1); P8 mounts `ui.Handler` with `Router.SPA(h, httpapi.SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})` (FE5-10, requested from 05-http-api §4.10); the P8 shell-segment check is the kernel's `ProblemSPASegmentCollision`, `httpapi` only supplies `NonSPASegments()` (FE3-9); `LegacyAuthGin` only for `Options.LegacyAuthGinUntil`, `OwnAuth` only with a compat method (SEC2-17); the `CoreGroup` rule needs no `ui` exception (`internal/uihttp`, COR2-19); facade members deferred (SIMP2-6) |
| 05-storage §5S.3–§5S.4 | `Manifest.Storage storage.Spec` (derived indexes declarative only); `Registrar.DerivedIndex(name, storage.DerivedHooks, tasks.Definition)`; `Manifest.DataMigrations []datamigrate.DataMigration`; `Deps.Storage module.StorageHandles{Keyspace, Legacy, Indexes, Aux, Derived, Maintenance}` (`Maintenance`, a `legacy.Maintenance`, S2b to the S4 r2 PR, R7-2, SIMP8-4); `kernel.OpenOffline` and the offline-open procedure (§6.4.6, R7-4); `Manifest.LegacyStores` constructors (a `legacy.Stores` value, §6.4.11) over the supplier's own `legacy.Handles` and the recorded store backend (`ErrLegacyStoreMissing`) (R4-1; no `Spec.Grants` during the extraction); `c/storage/claims/<owner>`, `Claim.TakesOver`; cross-keyspace migration DAG with one checkpoint per boot; `DataMigration.Writes` | §6.5.1 is the one boot order; 05-storage §5S.4 references P2 and P4 (a)–(d) and keeps the mechanics (R2-8, COR2-7, DATA2-24); store at `core.storage.path`; no automatic checkpoint restore (DATA2-19); checkpoint reuse only within one uncompleted attempt (DATA3-1); `DerivedIndex` hooks move out of `storage.Spec` (COR2-15) and `DerivedHooks` keeps `Snapshot`/`Load` only, the rebuild being a `tasks.Definition` passed to `Registrar.DerivedIndex` (COR3-7); derived record `c/storage/derived/<module>/<name>` (COR4-16); the `Online` data-migration mode is deferred-build in 05-storage §5S.3.14 (SIMP5-7); fresh-install seed steps (`Migration.Seed`, COR5-8), with every step below an owner's lowest seed step stamped and that seed and every later step run in P4b (COR6-4); the `storage.Duties` handle through which each consumer reads and clears its `owed` entry (COR6-2, DATA6-9); a restore boot's lineage verdict `restored` and its swap-free owed entries (R6-4, DATA6-2); `storage.LegacyHandles` is `legacy.Handles` (R6-3); the served ledger's `last` record and foreign-open identity written at Run step (3b), not at §5S.4 step 9 (R5-5); the dirty set per index through `index.Spec.Dirty`, no manifest field (SIMP5-2); routed vs exclusive store models (the backend check split between P2, legacy supplied or not, and P4c, the full value, COR7-4) and legacy-only capabilities detected once, the kernel asserting only the two it drives, `Covering` and `WriteLock` (SIMP5-1, SIMP5-4, SIMP7-5, R9-1); one instance per legacy family and `storage.CommitLocked` across core's commit (R9-1); `storage.ResetRequest` as the one reset request (R9-6); the public crash-matrix kit `coretest/crashmatrix` (R9-7); the empty-store exemption from the upgrade-floor check and books' versioned steps numbered from `LegacyCap+1` (DATA7-7); a restore boot's lineage written in its `marker_written` batch (R7-4); the staged factory reset through `legacy.Maintenance` from S2b and `internal/storagehttp` from S4 r2 (R7-2), library files deleted only on `delete_library_files` (A-17); the state diagrams of the swap journal, the served ledger and duties, the sidecars and the open stamp, and the crash-matrix harness over §6.5.1's boot-writer table (A-8); checked writes on native rows only (A-3); the staging marker `<db>.staging` / `c/storage/staging` (A-10); `selfserved/<binary id>` (R5-7); no claim hand-over, mirror or rebuild step (R3-1); grants are deferred-build members of the cutover (R4-1) |
| 05-tasks | `Registrar.Tasks`, `Resources`, `Lanes`, `ChangeSink(tasks.ChangeSink)`; `Manifest.TaskLedger` (5T.3.11 held runs); `Manifest.TaskStarts`; `tasks.Common.Schedule` (+ `tasks.ScheduleFromConfig`, one schedule-key rule, R2-7); `Deps.Tasks tasks.Client`; `tasks.CPU()`, `RunItems(rc, par, items, fn, opts...)`, `tasks.ForEach` | closes 05-tasks open item 6 and the `Registrar.Schedule` question (it does not exist). Held runs and the ledger-gate rule of §6.5.3 are 05-tasks mechanics (COR-13); the system actor's permission set (SEC2-15) is 05-tasks' to state; core services start runs through `tasks.Stager` (never the `Executor`), whose lifecycle method is `Executor.Run` (§6.5.2); there is no tasks reconcile in P4d; `tasks.Stores` (`NativeStores` over `c/tasks/`, or books' `LegacyStores.Tasks`, a `func(legacy.Handles) (tasks.Stores, error)`, 5T.3.13; R4-1, R6-3; it was `tasks.LegacyStoresFunc`, and no named function type replaces it); the run store's `Recover` is the optional `legacy.TasksRecoverer`, detected by `internal/executor`'s mirror and run inside `Executor.Run` (SIMP5-4, R6-3, SIMP7-5, SIMP7-8); books' run store's `legacy.WriteLock` (`opsMu`) is held by `Executor.WriteRuns`/`Stager.WriteRuns` through `storage.CommitLocked` (R9-1); `tasks.Stores` gains the scan stand-down marker store (MOD6-4); core kinds with app former kinds take them from `app.Options.LegacyTaskAliases`, never from a literal in core (5T.8.2, MOD6-6); the synthesised `<ID>.datamigrate.<name>` definition is deferred-build with the `Online` mode (SIMP5-7) |
| 05-events | `Registrar.Topics(...events.TopicSpec)` (one catalog incl. `Durable`, `Notify`, `Realtime`, `Permission`, `Visible`), `Registrar.Subscribe(...events.Subscription)` built with `events.On`/`OnRaw`, `Registrar.OutboxHandler(...outbox.Handler)` incl. `ConsumeRaw`/`ConsumeIntoTaskRaw`, `Manifest.OutboxConsumers`; `Deps.Outbox outbox.Writer`; `Bus.Open`/`Bus.Close` in §6.5.2 | adopted as 05-events states it, including `Deps.Events events.Scope` (COR-21) |
| 05-realtime | catalog topics with `TopicSpec.Realtime` are streamed with no registrar call; redaction is the catalog's `TopicSpec.Redact`; `Registrar.Realtime() realtime.Registrar` with the one method `Projection(realtime.LegacyProjection) (http.Handler, error)` | hub built in P5, redactors and projections recorded in P6 and attached in P7; projections are books gin manifest routes, not aliases (COR2-1, SEC2-25); core-topic redaction is the topic's own (COR2-2) |
| 05-notify | notify metadata on the catalog entry (`TopicSpec.Notify`); `Deps.Notify notify.Publisher` | `Registrar.NotificationTopic` deleted (SIMP-5); `Publish` is the only entry point, and `Notify` metadata alone produces nothing (COR2-3); `legacy.Stores.NotifyRules` (`notify.RuleStore`, R4-1) |
| 05-rbac §3.1–§3.5 | `rbac.Permission`, `PermissionSpec{Name, Title, DefaultGrants, Sensitive, …}`, `RoleSeed`, `RoleName` (`"<ns>.<name>"`), `LegacyAlias`, `Requirement`, `Need`, `CoreAdmin`, `audit.Logger` | manifest lists are fed to `Service.Declarer(moduleID)` by the kernel in P5, after `rbac.New` (COR2-7); leaf types land at S2, including `Checker` and `RoleName` (COR2-9, COR4-11, MOD4-11), and the atom-carrying `LegacyAtom`/`LegacyCheck` values, evaluated from S4 (COR6-1); no `Grant`, `Scope` or `PermissionDef` type exists (COR-16); `rbac.Store` native (`c/rbac/`) or books' `legacy.Stores.Roles`, whose roles hold legacy atoms only, core scopes being an API-level mapping over them (A-2); `app.Options.LegacyAmbientScopes` belong to the principal (`CheckerInput.Ambient`), added after the key-scope intersection, never to a role (R9-4); seed roles during the extraction per 05-rbac §4.8 (`AcceptSeed` allowed, operator diff deferred, SIMP4-6); `Manifest.LegacyAliases` own-namespace only, core-targeting aliases as `app.Options.LegacyPermissionAliases`, legacy role IDs as `Manifest.LegacyRoleIDs` (05-rbac's `LegacySurfaces` and `core/rbac/legacy_abo.go` replaced, COR5-5) |
| 05-config §1.3–§1.4, §2.3 | `config.Define`, `Section[T]`, `Handle[T]`, `config.Register`; `Store.Boot` + boot record for `core.modules.<id>.enabled` (P0/P1); `Store.Load` without DB (P3), plus, at S2, config's DB layer over `app.Options.LegacyConfigStore` (a `config.OverrideStore`, COR3-1); `app.Options.LegacyConfig *config.Legacy` reduced to the legacy names of core keys; P4d step 1 over the config store from P4c (books' `legacy.Stores.Config` for ABO) in the order the store's `Open` (ABO's key guard and encryption init; the settings load at S2b) → `secrets.Open` with its legacy secrets (keyring, `k0-legacy`; nothing moved or re-imported, A-2) → `Store.SetSecrets` → `Store.Attach` (COR5-2, R4-6; no config import step, R3-1); HEAD's precedence for the app's legacy keys and every legacy secret left in HEAD's row and representation (R5-1, A-2); legacy names decoded with HEAD's per-type semantics at P0 (05-config §1.3.6, A-7); `Changed` as the optional `legacy.ConfigExternalWriter` (SIMP5-5, R6-3); `secrets.RetireUnused` in P5 (COR3-2); `config.Options.EnvPrefix` from `app.Options.EnvPrefix`; the pre-boot key `core.paths.state_dir` (no `data_dir`), resolved once by `config.ResolveStateDir` with HEAD's fallback (R3-4); `secrets.Scope` (`modules.<id>.`) | 05-config hands modules `Registrar.Config()` with "registration legal only before Store.Load"; since `Register` needs loaded config, sections are listed in `Manifest.Settings` and registered by the kernel before load; `module.Setting(r, s)` returns the bound handle |
| 05-metadata M1–M2 | `Registrar.Adapter(integrations.Descriptor)`, `app.Options.Adapters`, `Deps.Integrations *integrations.Registry` (the one instance store, SIMP-4), `Deps.HTTP *providerhttp.Registry`; `legacy.Stores.Integrations` (`integrations.InstanceStore`) and `.Gates` | adopted as proposed; legacy instance secrets are read through 05-config's `legacy.SecretsStore` in HEAD's representation, with no re-import (A-2), and there is no separate integrations step; `secrets.Options.LegacyStore` is typed `any` (R7-7) |
| 05-ai | `Registrar.AICapability(ai.CapabilitySpec) ai.Capability`, `ai.BatchApplyTask(cap, h, effects)` registered with `Registrar.Tasks`, `Deps.AI *ai.Router` | signatures as in 05-ai; `Registrar.AIBatchHandler` removed (COR2-11) |
| 05-download | `Deps.Downloads *download.Pool`; `DownloadWatch` deferred-build | grab/monitor are the deferred acquire build (SIMP-12, COR-3) |
| 05-health | `Health(...health.Check)`, `StartupSteps(...health.StartupStep)`, `Diagnostics(health.Contributor)` (S8); `Registrar.Health` and `Deps.Health health.Reporters` from S5 with the readiness core (COR4-8); `Deps.ExtendStart` and the stateless `app.ExtendStart`; `core.shutdown.budget` and its phase slices (§5H.4) | `core.lifecycle.stop_timeout`/`shutdown_budget` removed from 06 (OPS-13); `internal/startup` stays in ABO, used by `cmd` until S2b (MOD3-1, MOD4-17) |
| 05-updater | `app.Options.Updater *updater.Options`; `app.BinaryID` as `Self.BinaryID`; phase P−1 (`updater.Boot`) deferred-build | S8 moves the S0-hotfixed updater into `core/updater`, parameterised by release source (exit 75, `.prev`, semver comparison, level and floor checks, check-only unattended mode); `updater.Boot`, the boot-attempt counter, automatic rollback and signed manifests are deferred-build (A-6, ADV-S5); `--version-json` prints `{app, version, commit, ui_build_id, binary_id}`, the list 05-updater §5U.4 step 3's self-test checks (FE6-4); the moved updater keeps HEAD's pipeline with exactly two additions, the `core.updater.max_download` cap and that `--version-json` self-test, and archive download, checksums, the pre-update backup, the container refusal and the exec-mode restart are deferred-build (R10-8, OPS10-6); the binary ID is `buildid.Binary` from S0 (FE6-3); replaces 1.0.0's `Release updater.Source`; identity is the binary ID, never the UI build ID (R5-7) |
| 05-download, 05-indexer, 10 (v1.1.0) | `Deps.Downloads *download.Pool`; deferred `Deps.Indexers *indexer.Pool`, `Deps.Acquire *acquire.Service`, `Manifest.Acquire *AcquireDecl` (§6.4.9) | names as those files state them |
| 05-library | `Manifest.Library []library.RoleRequirement`; `Deps.Library library.Scope`; `legacy.Stores.Roots` returning `legacy.LibraryRootSource`; `library.Options.LegacySource any` | `library.RootSource` and `RouteKey` move to package `legacy` as `legacy.LibraryRootSource` (SIMP7-8) and `legacy.LibraryCoverID`, and `librarytest.RootSourceSuite` to `legacytest.LibraryRootSourceSuite` (SIMP7-8), since library has no native root store (R6-3, SIMP6-3 (b)); `library.SourcedRoot` is deleted, and a source-served root carries only HEAD's fields, the others fixed by the source (`library.ErrRootFieldFixed`, A-2); the kernel passes the built root source as `library.Options.LegacySource`, typed `any` and asserted by library at construction (`storage.ErrLegacyStoreType`, R7-7); a legacy-served root takes no `ReadShares` (SIMP7-2) |
| 05-authn §3.3, §5 | `app.Options.Authenticators []auth.Extension`; `Registrar.AuthMethod(auth.MethodSpec)`; `Deps.Auth`, `Deps.AuthAdmin` | `auth.Extension{Authenticator, Spec, Order}` requested from 05-authn; `auth.MethodSpec` is an S2 leaf (SEC3-14); `auth.BootWrites` is P4d step 4 and also mints the bootstrap token and the optional start-up key, each in its store's representation (DATA3-18, COR4-13, A-2); `auth.Store` has a native implementation (`c/auth/`) and books' legacy one (`legacy.Stores.Auth`); compat methods reach `auth` at P7, not P5 (R4-1, COR4-13) |
| 05-backup | `Registrar.Backup(...backup.Artifact)`; `Deps.Backup backup.Ensurer`; pre-boot restore phase P0a (the kernel's journaled swap from S2b, ABO staging only until S8, DATA4-6); revocation re-apply (step 2, `head_before` read before any append, SEC4-5) and post-restore hooks (step 3, also on a replaced-store boot) in P4d; `core.backup.dir` declared from S4 by the kernel's core-section list and `backup/journal`'s `c/backup/` spec registered from S4 (COR4-10, COR5-6); `StagedByBuild`/`Source` carry the binary ID (R5-7) | 05-backup's "module.Manifest.Backup" comment changes to `Registrar.Backup` (hooks close over services); P0a runs after P−1 and before P0 (DATA-23, OPS2-3), trusts the absolute targets in `pending.json` (DATA2-25), creates `inflight.json` before the first rename of every kind and keeps it until `applied` (DATA3-6, R9-7); the journal is read after the keyring and overrides (COR3-2, DATA3-5); P4d step 2 classifies per 05-backup §5B.3.6 (2), never by its own rule (DATA6-5), and on a restore boot writes `served_boot` and `backup.location.json` only after the hooks commit (DATA6-3); a staged restore appends no gap line for its own swap (R6-4) |
| 05-logging, 05-metrics | `Deps.Log *slog.Logger`, `Deps.Metrics *metrics.Scope`; `Registrar.LogPathRules(...log.PathRule)`, `Registrar.LogOtherUserData(log.OtherUserPredicate)`; `app.Options.MetricNamespace` (core series, A-5) beside `MetricPrefix` (module series); `app.Options.ServiceName` (resource `service.name`, stage 2's fixed `server.address`; MOD10-2, MOD10-11) and `app.Options.MetricViews` (histogram boundaries installed as views; R10-3, MOD10-6); `app.Options.PromRegisterer` (S2 r3–S8, OPS5-14); `legacy.Stores.Logs` (`log.JournalStore`, which commits its own batches: the stated exception to the caller's-batch rule, MOD5-10) | the module path-rule and other-user-data registrations 05-logging §3, §5 point at; no metric aliases: an app that keeps its names passes its own `MetricNamespace` (A-5); the App exporter registers on the app's registerer until S8, then the kernel mounts `/metrics` (MOD4-3) |
| 07 §7.4–§7.6 | `UIDescriptor` (all shell slots, `LegacyRedirects`), `app.Options.UI ui.Bundle` and `UIOptions ui.AppOptions` (S9), the S2 `ui` leaf (`ReservedSegments`, `ModuleManifest`, `UIIDs`, `Redirect`, FE4-6) | `/modules` (`internal/modulehttp`) and `internal/uihttp` routes ship at S4 (FE2-25, FE3-16); P8 checks shell segments against `NonSPA` (FE2-7, FE3-1) and passes `ui.BasicExemptAssets` to `Router.SPA` (FE5-10); no build-ID check (FE4-1); `ui_route` entries registered by the kernel at P7 from `LegacyRedirects` (FE4-5) |
| 08, 10, 05-indexer | §6.4.9 deferred-build members | — |

## Objections to 03

None open. Resolved in 03 v1.14.1 (integration pass, round 10): 03's R6 row and R10-1 history line spell core's run
route `GET /api/core/v1/tasks/runs/{id}` (05-tasks 5T.3.10), and 03 §3.3 "Write locks" builds one legacy store
instance per family once per App, not per process (COR10-10), as this file does (§6.4.11 `Stores`,
`TestLegacyStoresAreSingletons`). Before that, 03's round-9 revision states `legacy.WriteLock` as the fifth kernel-asserted capability, one instance per legacy family, `storage.CommitLocked`, config's per-key exclusive model and `coretest/crashmatrix` (R9-1, R9-7, COR9-11), as this file does. 03 v1.12.0 (advisor pass) states `app.Options.MetricNamespace` (A-5) and drops the extension,
sign-in-deletion and sentinel identifiers from its legacy list (A-2), as this file does. Resolved in 03 v1.11.0 (round 8): §3.3 is the one list of the capabilities the kernel asserts
(SIMP8-3), which this file cites, and R4 places
`internal/restoreswap` at L1, importing `storage` and imported only by `kernel` (R8-1, COR8-5). 03's round-7 revision states, and this file follows: the naming rule in Go initialism form
with the renames `legacy.TasksRecoverer`, `legacy.LibraryRootSource` and `legacy.RBACAtomWriter`
(SIMP7-8); the mirror test in the owner's external test package and the `any`-typed legacy option
fields (R7-7); and detection by the consuming party only (SIMP7-5). If 03 differs, 03 wins and this file
follows it.

Resolved in 03 v1.9.1 (integration pass, round 6): FE6-3's L0 leaf `core/buildid` (`buildid.Binary`,
`buildid.Commit`; lands at S0) is listed in 03 §3.3 and R4, and the run-recovery capability has one
spelling everywhere, now `legacy.TasksRecoverer` (05-tasks owns it; round 7, SIMP7-8). Resolved in 03 v1.9.0: R6-3's `legacy` package (L5, `legacy/legacytest`, `Manifest.LegacyStores` typed
`legacy.Stores`) and COR6-11's `signal.NotifyContext`. Resolved earlier: 03 v1.6.0 §3.6 states the compat inputs as 06 §6.4.1 does (FE3-1, R3-5): `LegacyPrefixes`
first segments are reserved IDs only, `NonSPA` is `TopSegments` minus `SPAShared`, module IDs are
validated at P1 and re-checked at P7 against the recorded mounts, and `ui.ReservedSegments` exists
from S2. 06 keeps `module.ReservedIDs()` static (the owner lists, evaluable in P1, COR2-19).
03 v1.4.0's §3.7 diagram points at §6.5.2 for the start/stop order (COR-8). Resolved earlier: the "service container" wording (SIMP-26), the Role-ID grammar (COR-16), the public L0
`version` package (MOD-12), `examples/hello` in the tree (MOD-7), and `module`'s layer (L7).

## Out-of-scope defects found

| Defect | Anchor | Note |
|---|---|---|
| `batchpoller` registered without `Groups`, never built; `TryGet` always misses | `internal/server/batch_poller_register.go:18-20`, `internal/server/registry_wire.go:422` | already in raw/07 §1.3; re-verified. Fixed as a side effect of §6.11.1 steps 3–4 |
| `plugins_init.go` comment says blank-importing deluge registers with `plugin.Global()`; it registers via serviceregistry | `internal/server/plugins_init.go:16`, `internal/plugins/deluge/register.go:24` | stale comment (raw/07 §2) |
| `EventBus.Publish` hands each handler the publisher's ctx in an unbounded goroutine, contrary to the recorded design decision | `internal/plugin/events.go:85-103` | raw/07 §2; superseded by 05-events |

## Review history

Round 1 (2026-11-04), findings applied to this file:

- COR-1 — `Deps` gains `Downloads`, `AI`, `Backup`, `Storage.Aux`; `Registrar` gains `ChangeSink`, `OutboxHandler`, `Realtime()`; acquire/indexer handles and `Manifest.Acquire` listed as deferred-build (§6.4.9); acyclic construction rule A8; §6.14 names owning symbols.
- COR-8 — one start/stop order in §6.5.2 (`Bus.Open` before executor; relay via scheduler `OnStart`; `Bus.Close` after executor drain, before workers/hooks).
- COR-11 — registrar side: `OutboxHandler` accepts `ConsumeRaw`/`ConsumeIntoTaskRaw`; durable topics are catalog entries.
- COR-12 — `Manifest.OutboxConsumers`; disabled modules' messages retained (§6.5.3).
- COR-13 — held runs (computed, 05-tasks 5T.3.11); `Manifest.TaskLedger` readable while disabled; `AssertModuleInert` and tests cover runs.
- COR-14 — enablement read in P1 through `config.Store.Boot` (operator layers + boot record); the UI writes the boot record; unknown module fail-closed only in operator layers.
- COR-16 — vocabulary of 05-rbac/05-http-api/05-tasks/05-storage; hello rewritten to compile; `TestSpecGoBlocksCompile`; Role-ID objection to 03.
- COR-18 — core capability routes in 03's `internal/<capability>http` packages, wired by the kernel (§6.4.6).
- COR-21 — module-scoped handle types with owners table; `RBAC` field dropped; actor stamping in `tasks.Client`.
- COR-24 — `Registrar.Context()`; `app.New` goroutine claim reworded; cleanup test.
- COR-26 — `app.Options.AppID`, `MetricPrefix`, `ReservedAPINamespaces` (03 v1.3.0).
- SIMP-4 — `Deps.Integrations` is the one integration-instance store; `<ID>/` secret scope removed.
- SIMP-5 — topics declared once via `Registrar.Topics` into the events catalog; `OutboxTopics`/`NotificationTopic` removed; realtime streams catalog topics; `Registrar.Subscribe` for bus subscriptions.
- SIMP-6 — schedules only via `tasks.Common.Schedule` / `tasks.ScheduleFromConfig`.
- SIMP-22 — `DependsOn` and its problem kinds dropped; `MinCoreVersion` kept with MOD-12 semantics; legacy prefixes are narrowed subtree claims.
- SEC-23 — note that facade/automation permissions are never `Sensitive`; `DefaultGrants` replaces `Grant`.
- SEC-29 — `secrets.Scope` prefix `modules.<id>.`; instance secrets only via `integrations.Env.Secrets`.
- MOD-7 — hello is a package in the core module; `archtest.SDKOnly` + consumer smoke check.
- MOD-12 — `version.Version` constant as the `MinCoreVersion` reference; objection to 03.
- MOD-20 — embed files moved into package `hello` beside `web/`.
- FE-1 — `LegacyMount.ServesSPA`; zero `Options.UI` while legacy owns `/`; `ProblemSPAOwner`.
- FE-16 — `UIDescriptor` lists every shell slot (banners, top bar, Users page, Activity panes, setup steps).
- FE-20 — `Description.Core`; `core.json` emitted only by hello.
- FE-23 — `module.ReservedIDs()` (union incl. `ui.ReservedSegments`).
- DATA-9, DATA-23, DATA-28 (R-B rows, 06 parts) — one checkpoint per boot over the migration DAG; pre-boot restore phase P0a; persisted claim records in P2.
- Cross-cutting 1, 2 — deferred-build members; books' legacy mount is permanent.
- OPS-13 (R-C/R-E request) — `core.shutdown.budget` from 05-health replaces the `core.lifecycle` stop/budget keys.
- Cross-cluster alignment — `app.Options.Updater *updater.Options` (05-updater); `Deps.Indexers`/`Deps.Acquire`/`AcquireDecl` deferred placeholders named as in 05-indexer/10.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-B request (COR-2, SIMP-17) — `Deps.Storage` is `module.StorageHandles{Keyspace, Legacy, Indexes, Aux, Derived}`, defined here; `Manifest.DataMigrations []datamigrate.DataMigration` added.
- integration pass: R-B request (DATA-18) — `Deps.AuthAdmin auth.Admin` for the `StageUserMerge` participant (05-authn §3.1).
- integration pass: R-G owner check (COR-21) — ownership table rows updated from "requested" to "defined" (`events.Scope`, `library.Scope`, `secrets.Scope`, `auth.Identities`, `health.Reporters`, `backup.Ensurer`, actor stamping); O7 closed.
- integration pass: R-F requests — `Deps.Downloads`, `AI`, deferred `Indexers`/`Acquire`/`AcquireDecl` and the dropped `NotificationTopic` were already applied; `DownloadWatch` marked as 05-download phase 2.
- integration pass: Objection to 03 (§3.7 diagram) removed: adopted by 03 v1.4.0. Stage range S0–S10.

Round 2 (2026-11-04), findings applied to this file:

- R2-8 / COR2-7 — §6.5.1 is the one boot-phase table, referenced by 05-config, 05-storage, 05-backup, 05-updater, 05-authn and 05-tasks; write rule (nothing in the store before the P4a checkpoint); `Store.Load` without DB in P3; P4 (a) checkpoint, (b) DAG, (c) handles, (d) ordered reconcilers incl. `secrets.Open` → configstore import → `AttachOverrides`; rbac `Declarer` moved from P1 to P5; `BootBlocking` data migrations in process via `tasks.ForEach`.
- OPS2-3 — phase P−1 `updater.Boot` before P0a; pre-boot path resolution; P0 refusals count as failed boots; rollback decided before any restore swap.
- COR2-1 — `realtime.Registrar.Projection` returns a lazy handler (hub built in P5, attached in P7, 503 until the hub starts); books serves it from its gin manifest at the three legacy paths.
- COR2-2 — core-topic redaction is the topic's own, never a module redactor.
- COR2-3 — `Deps.Notify notify.Publisher` (built by `notify.Service.For(module)`) with owners-table row.
- COR2-11 (lead request via R-F) — `Registrar.AIBatchHandler` removed; AI batch apply is a module kind from `ai.BatchApplyTask`; derived-index builds are module kinds.
- COR2-8 — `Options.DataDir` replaced by `StateDir` (compiled default of `core.paths.state_dir`, state files only); `Options.EnvPrefix` added; P2 opens `core.storage.path`.
- COR2-9 — §6.4.10 landing stage of every `Manifest`/`Registrar`/`Deps`/`app` member; rbac leaf types at S2; storage absent until S2b (no P2/P4, books opens its store in a start hook).
- COR2-13 — `Describe` runs P0–P7 in memory; `Register` must be side-effect-free outside `Deps`; books' recorded exception until its server construction moves into hooks.
- COR2-14 — hello declares `hello:settings.manage`; `module` import block completed (`storage/index`, `storage/datamigrate`, `ui`, `notify`); `TestSpecGoBlocksCompile` boots hello.
- COR2-15 — derived-index hooks via `Registrar.DerivedIndex` (declarative part stays in the manifest); Build as a module kind; §6.5.2 places the core components that invoke module callbacks (start step 3a, stopped before workers and hooks).
- COR2-19 — stale gin note fixed; `ui/diagnostics` and `ui/csp-report` move to `internal/uihttp`; compat top-level segments reach P1 through `Manifest.LegacyPrefixes`.
- DATA2-19 — store at `core.storage.path` (not under the state directory); migration failure keeps the checkpoint unconsumed, no automatic restore; test rewritten.
- DATA2-24 — one ordered list of every-boot writers (P4d), all after the checkpoint and the DAG, before listeners.
- DATA2-25 — P0a trusts the absolute targets in `pending.json` and P2 verifies the opened path.
- DATA2-28 — an empty store is stamped as a fresh install in P4a.
- DATA2-29 — orphaned claims are taken over only by `Claim.TakesOver`; step name `core/auth#1`.
- OPS2-7 — P0 alias uses buffered and recorded after the checkpoint (P4d step 6; step 5 since round 3).
- OPS2-23 — `core.shutdown.budget` restart-bound in both files (06 already said no).
- OPS2-24 — boot metric is a per-boot gauge with `phase` (P-1, P0a, P0…P8) and `step`.
- SEC2-15 — `Manifest.TaskStarts`; system actor permission set; `ProblemTaskStartUnknown`; test.
- SEC2-16 — `Options.Authenticators []auth.Extension` (only place for interactive/credential/owner methods); `Registrar.AuthMethod` for non-interactive compat methods; test.
- SEC2-17 — `MountLegacy` constraints: `LegacyAuthGin` only for `Options.LegacyAuthGinUntil`, `OwnAuth` only with a registered compat method; `ProblemLegacyAuth`; test.
- SEC2-25 — projections described as gin manifest routes wrapping `Hub.Projection`, not aliases.
- SIMP2-6 — facade-only members of `auth`/`httpapi` listed as deferred with `arrfacade` (§6.4.9).
- SIMP2-13 — metric segment decided by the modules the app ships, not the enabled set.
- SIMP2-17 — `Remove` dropped from the `Deps.Downloads` comment.
- MOD2-4 — no file of `cmd/` moves into `app`; `serve` calls `app.New`/`InstallProcessDefaults`.
- MOD2-10 / FE2-9 — hello grows with the stages from an S2 skeleton; `core.json` producible from S2 and regenerated in the PR that adds a core route.
- MOD2-21 — "Wave K0–K3" replaced by 09 stages and books-internal steps.
- FE2-7 — P8 check that shell route segments are not `NonSPA` segments (`ProblemSPASegmentCollision`).
- FE2-11 — reserved-ID inputs split: `LegacyPrefixes` first segments (top level, also `NonSPA`; `TopSegments` must be a subset) vs `ReservedAPINamespaces` (under `/api`); aligned with 03 v1.5.0 §3.6.
- FE2-25 — `/api/core/v1/modules` and `internal/uihttp` ship at S4.
- R2-2, R2-3, R2-4, R2-7 — referenced in P4 (revocations write legacy rows; claim modes as level steps; legacy migrations before claims) and in `Registrar.Tasks` (one schedule-key rule). (R2-2/R2-3/R2-4 parts superseded in round 3 by R3-1: one stored format, no claim-mode steps.)

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): P0a reads `<state_dir>/restore/pending.json`; no `core.paths.data_dir` (pre-boot key is `state_dir` only); aux DBs under `<db parent>/aux/`.
- integration pass (round 2): P4d step 5: hooks auth → rbac → audit, then the identity verification rebuild; Stages: P4d step 3 from S3 r2 (S3 r1 is a façade release). (Superseded in round 3 by R3-1: no verification rebuild; hooks are step 3.)
- integration pass (round 2): `Registrar.Realtime()`: one method `Projection(...) (http.Handler, error)`; `Redact` dropped (redaction is `TopicSpec.Redact`); P7 wording.
- integration pass (round 2): `Deps.ExtendStart func(step string)` (S2) for books' database/search code; owners-table row and §6.4.10 entry.

Round 3 (2026-11-04), findings applied to this file:

- R3-1 — dual-format machinery removed from the boot table (no mirror rebuild, adoption, claim hand-over, claim-mode level steps, identity verification rebuild or configstore import; fresh-install stamp no longer records adopt/contract steps); "one stored format per family" paragraph; storage grants in `Manifest.Storage` validated and logged at P2, restricted ranges at P4c, adapters built at P5; new §6.11.6 with books' grant declaration, additive native data, secrets rule, deferred cutover and the independent live hotfixes; checkpoint-before-write kept.
- R3-4 / OPS3-1 — `Options.StateDir` and P−1/P0a resolve with HEAD's fallback to the DB's directory; `TestStateDirFallbackMatchesHEAD`.
- R3-5 / FE3-1 — `LegacyPrefixes` first segments are reserved IDs only; `NonSPA` = `TopSegments` minus `SPAShared` (ABO: `login`, `invite`); P1/P7 split aligned with 03 v1.6.0; test rewritten; `TestABOManifestBootsAtS9`.
- COR3-1 — `Options.LegacyConfig` applied at P3 at S2 (ABO's serve opens the store first, HEAD's order); from S2b config's adapter reads the blob through books' grant; `TestLegacyConfigHonoured`.
- COR3-2 / DATA3-5 — P4d order: keyring and overrides, journal re-apply from the effective backup dir, post-restore hooks (restore or replaced store), `auth.BootWrites`, compat records (aligned with 05-authn/05-config v1.4.0); derive-key retirement (`secrets.RetireUnused`) in P5; tests.
- COR3-3 — §6.4.10 S2 `Options` listed field by field (no `Adapters`/`Updater`); `AuthMethod` and `ui.ReservedSegments` at S2; `Manifest.PublicRoutes` for hello's S2 public route.
- COR3-5 — module `Online` data migrations run as `<ID>.datamigrate.<name>` with `Writes` as Effects; test.
- COR3-7 — derived `Load`/check/record moved to Run step (3a); `DerivedIndex(name, hooks, build tasks.Definition)` with a module-relative name (rebuild task in `module`, not `storage`, for layering).
- COR3-8 — `tasks.Stager.StartIn` and `<ID>.ai.batch-apply.<name>` in the AI comment.
- COR3-10 — `TaskStarts` validated at P7 against registered kinds plus every `TaskLedger`; comment, §6.6 row, test.
- COR3-15 — write rule lists P7's writes (default permission grants, definition snapshots) and Run (3a)'s derived records.
- DATA3-1 — P4a checkpoint reuse restricted to the same uncompleted attempt and store identity.
- DATA3-2 — superseded by R3-1 (no hand-overs); P2 orphan rule kept for removed modules only.
- DATA3-6 — P0a keeps `inflight.json` until `applied` and decides "restore boot" from the journal.
- DATA3-7 — `Options.PreBootRestore` from S2b to S7; hooks from S4 on its report.
- DATA3-18 — `auth.BootWrites` is P4d step 4.
- DATA3-21 — fresh-install stamp in one batch.
- MOD3-1 / R3-3 — `internal/startup` stays in ABO until `StartupSteps` (S8); S2 kernel uses its own core step list; §6.11.1, §6.11.5.
- MOD3-4 — `core.json` at `core/openapi/core.json`; `httpapi` → `internal/openapi` at S2.
- MOD3-19 — `version.Version` starts at 0.1.0; MinCoreVersion assertion.
- FE3-2 / MOD3-14 — `Options.BuildID` generated once per release, shared by Vite, Makefile, both Docker stages and goreleaser; P8 `ProblemBuildID`; `LegacyMount.BuildID`; test.
- FE3-4 — `Options.UIOptions ui.AppOptions` merged into `ui.Options` in P8.
- FE3-8 — `ui.ReservedSegments` leaf lands at S2.
- FE3-9 — one name (`ProblemSPASegmentCollision`) and one test for the P8 check; `Router.NonSPASegments()` supplies data.
- FE3-16 — module catalog route package `internal/modulehttp` (L6) over a `ModulesView` interface.
- FE3-17 — `UIDescriptor.LegacyRedirects` is the one legacy-redirect list.
- OPS3-16 — one ASCII `phase` label set including `P4a`–`P4d`.
- OPS3-17 — until S8 P−1 is absent (no HEAD boot check unless O9).
- SEC3-14 — `AuthMethod` (recording only) at S2.
- SIMP3-2 / SIMP3-11 — `archtest.DeferredMembersAbsent` from S2; facade problem kinds moved out of the built `iota` block.
- SIMP3-3 — `Options.CompatLedger` and `Options.MetricAliases` at S2b.
- Coordinator (R-E, 05-config/05-logging v1.4.0) — `Options.LegacyConfig *config.Legacy` (S2 DB layer at P3 over `Rows`, from S2b at P4d); `config.ResolveStateDir` once before P−1; `Registrar.LogPathRules`/`LogOtherUserData` for the other-user-data journal predicate.
- Coordinator (R-D, 05-authn v1.4.0) — P4d hooks also on a replaced-store boot, `auth.BootWrites` after the hooks; `auth` leaf types at S2.
- SIMP3-5 — `Deps.Notify` lands at S7.
- Coordinator (R-C) — no tasks reconcile in P4d; executor lifecycle `Executor.Run`; starter handle `tasks.Stager`.

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): §6.11.6 grant example uses 05-storage's `Grant{To, Prefixes, Keys, Mode, Exclusive, Reason}` and the stage list config S2b/S3, auth/rbac S4, tasks and log S5, index S7; `Registrar.DerivedIndex` build may come from `datamigrate.DerivedBuildTask` (L4); `TestJournalReapply…` cites P4d step 2; `internal/plugin` deleted at S7.

Round 4 (2026-11-04), findings applied to this file:

- R4-1 / SIMP4-2 — `Manifest.LegacyStores` (one constructor per core store interface: config, auth, roles, tasks (`tasks.Stores`), logs, roots, AI jobs, gates, integration instances, notify rules, dirty set) over the supplier's own handles (`storage.LegacyHandles`), built at P4c even for a disabled module (A5); native `c/<subsystem>/` stores when no manifest supplies one; extension family with a legacy-row hash for native-only data; `ProblemGrant` replaced by `ProblemLegacyStore`; grants removed from `Manifest.Storage`, P1, P2, P4c, P5, §6.6 and the tests (deferred-build with the cutover); §6.11.6 rewritten as "Books' legacy stores"; no ABO key name left in core text (`opchange:`, `opv3:`).
- R4-2 — P4d step 1b: the integrations registry's load-time secret re-import listed as a boot write; §6.11.6 secrets bullet: every plaintext copy (legacy row and `config.yaml`) replaced by a never-matching sentinel; fail-closed after rollback; re-import rule.
- R4-4 / MOD4-1 — `Options.CompatLedger` comment: ABO embeds a generated, freshness-checked copy under the books module.
- R4-5 / MOD4-2 — §6.11.1 step 2: `internal/books` is the composition root only; helpers (`PageBooks`, selection helpers, `Writer`, resources, aliases) in a package below it; §6.11.3 row; `TestOnlyCompositionRootImportsModule`.
- R4-6 / COR4-1 — the key guard, encryption init, settings load, env sync and `config.Validate` run inside books' legacy config store at P4d step 1 from S2b, over the one store handle; §6.4.3 per-step successors; §6.6 row; `TestS2bAppConfigFromBlob`.
- COR4-2 — the store's `Changed` stays the change source from S2 to the S3 façade; Stages bullet and `TestLegacyConfigHonoured` cover S2b at runtime.
- COR4-3 — `Options.LegacyConfig` keeps only legacy key names and is not removed at S3; the codec lives in books' store until cutover (§6.4.10 S3 cell).
- COR4-4 — within S4, while `LegacyAuthGinUntil` is set, P4d steps 2–4 and P7's seed writes are skipped and counted; `TestDarkAuthReleaseWritesNothing`.
- COR4-5 — Online data migrations only over `Serialized` families with `SetIfUnchanged`/`SetIfAbsent` steps; module kind for attribution with `ConcurrencyKey`; test rewritten.
- COR4-6 — synthesised `<ID>.datamigrate.<name>` definition fields, implicit `TaskLedger` membership, name grammar; P7; `TestOnlineMigrationYieldsPastTimeout`.
- COR4-8 — `Registrar.Health` and `Deps.Health` at S5 (health readiness core lands with tasks).
- COR4-10 — `core.backup.dir` declared from S4 in `backup/journal`; journal tests run from S4.
- COR4-11 / MOD4-11 — S2 rbac leaf list (eight types plus `Checker`, `RoleName`) and auth leaf list aligned with 05-rbac.
- COR4-13 — bootstrap mint and start-up key inside `auth.BootWrites` (P4d step 4); compat methods at P7 in §6.14.
- COR4-16 — derived record `c/storage/derived/<module>/<name>`; the enqueued rebuild run listed in the write rule.
- COR4-17 / SIMP4-8 — the absent-grantee rule is moot: no grants during the extraction (R4-1).
- COR4-18 / FE4-1 / MOD4-9 — meta-only build ID per FE4-1's disposition: `Options.BuildID`, `ProblemBuildID`, the P8 check and its §6.6 row deleted; `MountLegacy` comment, test and §6.14 rows fixed.
- COR4-19 / SIMP4-1 — §6.4.9: list generated from every `Deferred-build members` heading; package-qualified entries; per-member lifetime.
- COR4-20 — `core.storage.path` a kernel pre-boot key from S2; ledger-row check from S2b; Basic-gate case dropped from the S2 config test.
- COR4-22 / DATA4-20 — 06's grant list deleted (no grants); "activity" no longer named as core-served.
- DATA4-2 — no grant involved; backend choice recorded and stable across releases of a stage (P2).
- DATA4-6 — `Options.PreBootRestore` deleted; the kernel's journaled swap runs at P0a from S2b, ABO only stages until S8.
- DATA4-10 — HEAD's store-open writers given homes (P4b migration, fresh-install batch, P4d step 4); constructors write nothing; §6.11.5 row; test.
- DATA4-21 — checked: 06 already places the fresh-install batch in P4a and P2 writes nothing; no change.
- FE4-5 — `UIOptions` funcs read pointers filled in a Start hook; `ui_route` entries registered by the kernel at P7.
- FE4-6 — `ui.ModuleManifest`, `UIIDs`, `Redirect` in the S2 `ui` leaf.
- FE4-8 — `UI.LegacyRedirects` filled in the URL-move release, so `ui_route` recording starts there.
- MOD4-3 — `Options.PromRegisterer` from S2 to S8; kernel mounts `/metrics` at S8.
- MOD4-17 — per-step successors of HEAD's seven startup steps; `cmd` stops using `internal/startup` at S2b.
- MOD4-18 — `Deps.ExtendStart` reaches ABO packages below layer 6 as an injected func.
- OPS4-8 — marker clause removed from `StateDir` and P−1; `app.ResolveStateDir`; `TestStateDirOneResolver`.
- OPS4-9 — P−1: a marker whose `to_build` is not the running binary is recorded `superseded` and counts nothing.
- SEC4-3 — native scopes never stored in legacy role rows (one-format paragraph, P7).
- SEC4-5 — P4d step 2 reads `head_before` before any append; gap and restore lines appended after the re-apply.
- SIMP4-3 — hello and `coretest` run on native stores from S4/S5; §6.4.10 note.
- SIMP4-6 — seed roles during the extraction: seed row = seed, `AcceptSeed` allowed, operator diff deferred (Manifest comment, P7, §6.4.9).
- SIMP4-11 — `Options.LegacyConfigStore` is the S2 bridge, deleted in the S2b PR; `Legacy.Rows` gone.

Integration pass (round 4):

- integration pass (round 4): P4d step 2/3: replaced store classified by store generation (DATA4-7), a lagging head re-applies without hooks; `LegacyStores.Logs` is `log.JournalStore`; §6.4.10 S2b/S3 rows name the `OverrideStore` methods per stage; `app.Options.Auth auth.Options` (S4) carries ABO's `DefaultDisabledAllowRemote=true`, key prefixes, cookie name and invite form; `TestOnlineDataMigrationIsModuleKind` checks the COR4-6 synthesised definition. §6.14 05-rbac row names `rbac.Store`.

Round 5 (2026-11-04), findings applied to this file:

- COR5-2 — P4d step 1 order written once: the store's `Open` (key guard, encryption init; settings load at S2b) → `secrets.Open` with the legacy copies (keyring, `k0-legacy`, R4-2 re-import) → `SetSecrets` → `Attach`; "no import" dropped; §6.4.3 S3 sentence rewritten; §6.14 row; `TestP4dStep1_LegacyOpenBeforeSecrets`.
- COR5-3 — the served ledger's `last` record and the foreign-open identity are written at the new Run step (3b), after every consumer; P4b no longer replaces them; write rule; `TestServedLedgerCommittedAfterConsumers`.
- COR5-5 — `Manifest.LegacyAliases` own namespace only; new `app.Options.LegacyPermissionAliases` (S4, app-level, may target `core:*`) and `Manifest.LegacyRoleIDs` (`fp-worker`) with `ProblemLegacyRole`; catalog, landing table, §6.6, §6.14.
- COR5-6 — S4: the kernel declares `core.backup.dir` in its core-section list and registers `backup/journal`'s `c/backup/` spec (`Serialized` `journal_head`, `journal_gap/`, `restore_hooked/`); `TestRevocationStagesJournalHeadAtS4`.
- COR5-7 — write rule lists the later every-boot writers (books' start-hook repairs incl. `ReconcileOpsV3WithV2` until S5 and `ReconcileVerifiedEmails` until S4 r2, Run (3b), `Recover` at Run step 5); counter seeding dropped (write rule, §6.11.5); new "Stages" bullet with the S2b done-check.
- COR5-8 — fresh-install stamp leaves `Migration.Seed` steps pending; hello's step is `Seed: true`; `TestHelloEndToEnd` cites `TestFreshInstallRunsSeedSteps`.
- COR5-12 / MOD5-10 — caller's-batch rule states its exceptions (`log.JournalStore`, `Recover`/`Prune`).
- COR5-13 — §6.11.6 names 04's implementation packages (no `legacystores` package); `TestLegacyFamiliesWrittenOnlyByLegacyStores` with a per-family allow-list.
- COR5-14 — `config.OverrideStore` lands at S2 with its read members; native implementation and `LegacyStores.Config` at S2b; write members at S3 (landing table, `LegacyConfigStore` comment).
- COR5-15 — P4d step 1c: the S4 r1 read-only decode census.
- COR5-16 / SIMP5-6 — §6.4.9: first column package-qualified identifiers only, `Also deferred (not checked)` column, heading match after an optional section number, generator self-test.
- COR5-17 — the `Online` migration test has one name (05-storage's), over a test-module fixture, cited as deferred; 06's two `Online` tests removed.
- FE5-3 / R5-7 — "UI build ID" vs binary identity: new `app.BinaryID` (version + executable SHA-256, computed before P−1, S2b) for updater, storage and backup; P−1 marker check; `TestBinaryIDDistinguishesBuilds`.
- FE5-1 / R5-7 — `Options` comment: `npm run build` stamps the UI build ID on every path; `TestBuildID_HeaderEqualsEmbeddedMeta` requires a meta that is neither `""` nor `dev`.
- FE5-10 — P8 mounts `ui.Handler` with `Router.SPA(h, httpapi.SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})`; §6.14 rows.
- FE5-11 — `UIDescriptor.LegacyRedirects` comment: kernel registers `ui_route` entries at P7, `ui.Handler` records.
- FE5-13 — S9 landing cell and `LegacyRedirects` comment: §7.19 steps 4 and 5 ship in one release.
- MOD5-6 / SIMP5-9 — §6.4.8: `<pkg>/<pkg>test` conformance suites as SDK test-support packages with the one naming rule; ABO suite row uses it.
- MOD5-11 — `books.Module(…)` everywhere; the guard reads non-test imports; the G6 test lives in `internal/books`.
- OPS5-6 — `MetricAliases`: each alias ships in the PR that moves its emitter.
- OPS5-13 — `StateDir`/P−1: the fallback's DB path may come from the operator YAML file (HEAD parity); one test name, `TestStateDir_OneResolverYAMLDBPath`.
- OPS5-14 — `PromRegisterer` set from S2 r3 (the release that deletes the façade exporter), nil in r1/r2.
- OPS5-15 (R-C request) — a boot refusal under a pending marker naming the running binary exits with `restart_exit_code` (75), not 78 (P−1, §6.6, `TestBootRefusalCountsAsFailedBoot`, §6.14).
- SIMP5-1 — exclusive vs routed store models (LegacyStores comment, P2 `legacy+native`, P4c, one-format paragraph, §6.11.6, `TestLegacyStores`).
- SIMP5-2 — `LegacyStores.Dirty` deleted; the dirty set goes per index through `index.Spec.Dirty`.
- SIMP5-3 — no result-journal store field (books code over its own claim).
- SIMP5-4 — legacy-only members are optional interfaces detected by type assertion at P4c (e.g. `tasks.Recoverer`, R-C request).
- SIMP5-5 — `Changed` is the optional `config.ExternalWriter` (S2 to the S3 PR); `OpenEnv` stage references removed.
- SIMP5-7 — `Online` data migrations deferred-build: `DataMigrations` comment, P4b, P7, Run step 5, §6.14, tests.
- SIMP5-8 — one-format paragraph cites 03 §3.3 instead of restating the store list.
- SIMP5-13 — cutover bullet: a later build removes the `LegacyStores` field behind the cutover's `UpgradeFloor`.
- R5-1 — P3/P4d precedence for legacy keys; the four already-encrypted secrets are not moved, and neither step 1(ii) nor step 1b sentinels or re-imports them (R-F request; §6.11.6 secrets bullet).
- R5-5 — see COR5-3.
- R5-6 / DATA5-4 — extension fingerprint over security-relevant fields, Widening-only suspension, monotone revocation, never-empty legacy scope (LegacyStores comment, one-format paragraph, §6.11.6).
- integration pass (round 5): P4b stages the served ledger's `owed` entries and the pending foreign-open identity; Run (3b) also writes `c/storage/boot` and then `<state_dir>/storage.boot.json` (05-storage step 14b); P4d step 3 builds read-only `auth.Store`/`rbac.Store` over `rc.Previous` (`RestoreDeps.Previous`, SEC5-10); §6.4.10 S2b/S3 rows name `config.OverrideStore`'s native members and the optional `config.LegacyOpener`, `ExternalWriter`, `LegacySecrets`, `Suspender`, `CommitObserver` (SIMP5-4); `Self.Build` → `Self.BinaryID` (05-updater); §6.11.6 uses 04's `internal/books/configstore`, cites the five early fixes, names ABO's `["none"]` scope; `internal/books/contract` exists from S5 (MOD5-9); the books suite row is `TestLegacyStoreSuites` (SIMP5-9).

Round 6 (2026-11-04), findings applied to this file:

- R6-3 / SIMP6-2 / SIMP6-3 — new §6.4.11 package `legacy` (L5, outside the API-stability gate, importable by apps, not by hello): `Manifest.LegacyStores` stays a v1 field whose type is `legacy.Stores` (lead decision); `legacy.Handles` (was `storage.LegacyHandles`), the capability interfaces (`ConfigOpener`, `ConfigExternalWriter`, `ConfigSecrets`, `ConfigCommitObserver`, `TasksRecoverer`, `ExtConfirmer`, `SecretsStore`, …), `legacy.TaskStoresFunc`, `legacy.RootSource` (library legacy-only, option (b)) and `legacy/legacytest`; owners call capabilities through unexported mirrors checked by `export_test.go`; cutover deletes a family's members in a minor; A1, A5, Deps, Options, catalog, P1, P4c, landing table, §6.6, §6.10, §6.11.6, §6.14 rewritten.
- R6-4 / DATA6-2 — P0a records restoreswap's sidecar identity; P4b gives a restore boot the lineage verdict `restored` and swap-free owed entries; P4d steps 2–3: no `non_journaling_build` gap and no forced confirmation for a restore boot's own swap.
- COR6-1 — §6.4.10: `rbac.LegacyAtom`/`LegacyCheck` land at S2 as atom-carrying values, evaluated from S4; S4 row; §6.14 05-rbac row.
- COR6-2 — P4b/P4c: consumers read and clear `owed` through the kernel-built `storage.Duties` handle; Run (3b) wording; §6.14 05-storage row.
- COR6-3 — HEAD's `seedRolesAndTokens` stays in books' start hook through S4 r1 (write rule, P4d step 4, "Stages", §6.11.5 row); `TestDarkAuthReleaseWritesNothing` narrowed to core writes; `TestDarkAuthReleaseKeepsHEADSeeding`.
- COR6-4 — P4a stamps only steps below an owner's lowest seed step; that seed and every later step run in P4b; hello's seed writes only an absent key; `TestHelloEndToEnd` case.
- COR6-6 — S4 row: `backup/journal`'s `Serialized` is `["journal_head", "served_boot"]`.
- COR6-7 / SIMP6-10 — `legacy.Stores` comment cites 05-storage's exception list, `providerhttp.GateStore` included.
- COR6-8 — `legacy.ConfigOpener` lands at S2b; P3 says config calls no `Open` at S2.
- COR6-9 — §6.4.8 suite list includes `storage/index/indextest`.
- COR6-10 — P4d step 1(iv) cites `Store.Attach` (05-config §1.4 step 5), not `AttachOverrides`.
- COR6-11 — `package main` exempt from R5's signal ban; `TestNoGlobals` row; Objections to 03.
- DATA6-3 — on a restore boot `served_boot` and `backup.location.json` are written after the hooks; `restore_aborted` line; swapped-back restore lines neither resumed nor evidence; `TestRestoreBootWritesMarkersAfterHooks`.
- DATA6-4 / DATA6-5 — P4d step 2 classifies per 05-backup §5B.3.6 (2) (marker, generation, anchored head, lineage, owed journal entry) and lists its writes.
- DATA6-6 — P0a applies a staged factory reset; the next boot is a fresh install; `PebbleStore.Reset` deleted from S2b.
- DATA6-7 — P4d step 5: the usage consumer writes `c/compat/recording_since/<owner>` = `detected` when it clears `owed.usage`.
- DATA6-9 — P4b: `gen` owed per derived index, cleared at rebuild completion (05-storage owns the keying).
- FE6-3 — `app.BinaryID` delegates to `buildid.Binary`, an L0 leaf landing at S0, used by ABO's S0 guard and `--version-json`; test row.
- FE6-4 — one `--version-json` field list `{app, version, commit, ui_build_id, binary_id}`; `commit` from `vcs.revision` (+dirty) or `""`; goreleaser's dead `-X main.commit`/`main.date` deleted; `TestVersionJSONFields`; §6.14 05-updater row.
- MOD6-4 (consistency) — `legacy.Stores.Tasks` comment and §6.14 05-tasks row include the scan stand-down marker store.
- MOD6-6 — `app.Options.LegacyTaskAliases` (S5, by T4), `ProblemLegacyTaskAlias`, §6.6 row, `TestLegacyTaskAliases`.
- SIMP6-1 (consistency) — S3 row and §6.4.11: `legacy.ConfigSuspender` deferred-build with the config basis.
- SIMP6-5 (consistency) — a legacy half without `Covering` covers every record (`legacy.Stores` comment, P4c, `TestLegacyStores`).
- SIMP6-8 — §6.4.9 grammar: first column a package-qualified identifier or exactly `—`; the unchecked column in any position; `DownloadWatch` listed only here.
- Lead decision (round 6, one legacy-secret mechanism) — P4d step 1b deleted; step 1(ii) re-imports every moved secret, integration instances' included, through 05-config's `legacy.SecretsStore`; write rule and §6.14 05-metadata row.
- integration pass (round 6): `legacy.TaskRecoverer` spelling everywhere (05-tasks owns it); `Stores.Tasks` is a plain `func(Handles) (tasks.Stores, error)` (the named `TaskStoresFunc` was never defined); the `RootSource` comment and the 05-library row name `legacy.LibraryCoverID` and `legacytest.RootSourceSuite` and delete `library.SourcedRoot`; `legacy.ConfigExternalWriter` spelling in the config-keys test row; P4 records the owed-duty entries at `c/storage/owed/<duty>`; P4d step 1(ii): instance secrets go through `secrets`; "the six early fixes" (R6-5); Objections to 03: FE6-3 (`core/buildid`) and the R6-3 spelling resolved by 03 v1.9.1. Already applied: `Manifest.LegacyStores` typed `legacy.Stores`; P4d step 1b deleted; P4d step 2 classifies per 05-backup (DATA6-5).

Round 7 (2026-11-04), findings applied to this file:

- R7-2 / COR7-1 / SEC7-4 / SEC7-5 / DATA7-6 — `module.StorageHandles.Maintenance` (`legacy.Maintenance`: `UpgradeFloors`, `StageFactoryReset`) from S2b, for the module that declares legacy claims, deleted in the S8 PR (§6.4.4, §6.4.10 S2b and S8 rows, §6.4.11); books' gin handler stages the reset behind HEAD's chain until S4 r2, then the core route of `internal/storagehttp` (L6, S4 r2) with the legacy path as its `Alias` (§6.4.6); P0a: nothing destructive before the restart, the live blob never written with defaults, HEAD's side effects in books' post-reset hook after the reset journal reads `applied` (not undoable; 14 Q-K); the intent records the Pebble open stamp; `TestFactoryResetFromLegacyRoute_S2b`.
- R7-2 / COR7-9 / OPS7-11 — staging a restore and a reset refuse each other; P0a resumes an in-flight journal first, and a pending reset supersedes a pending restore; `reset_pending` makes `make rollback`, `deploy` and the updater refuse (`ROLLBACK_IGNORE_RESET=1`); `TestRestoreAndResetBothStaged`.
- R7-2 / DATA7-7 — P2 states the upgrade-floor check and the empty-store exemption; P4a's fresh-install batch records books' legacy steps 1..`LegacyCap`, books' versioned steps numbered from `LegacyCap+1`; §6.6 row.
- DATA7-17 — P0a no longer handles the revocation journal or rewrites the lineage and location files: it deletes `storage.boot.json` (verdict none) and cites 05-storage §5S.3.15 (reset journal `set_aside` until P4d step 1 rewrote the keyring header).
- R7-4 / DATA7-11 — `kernel.OpenOffline` (§6.4.6) and `app.OpenOffline`/`Offline` (§6.4.5, S2b): guard-aware offline open, promotion batch at a clean close; ABO subcommands that write use it, `diagnostics query` read-only; `TestOfflineOpenIsNotForeign`, `TestCLISubcommandsUseOfflineOpen`.
- R7-4 / DATA7-4 / DATA7-5 — a restore boot writes `c/storage/boot` and `storage.boot.json` in its `marker_written` batch before journal `applied` (P4b, P4d step 3, Run (3b)); no anchor rewrite between `swapped` and `applied`, step x restores the saved `backup.location.json`; test row extended.
- R7-4 / DATA7-12 — "Stages" P0a bullet cites the three checkpoint-restore rules.
- R7-4 / COR7-8 — on a staged restore the auth and role constructors run a second time over `storage.Admin.OpenReadOnly` handles that fail writes with `ErrReadOnly` (`legacy.Stores` comment, P4d step 3).
- R7-4 / COR7-10 — the kernel passes restoreswap's recorded identity to storage's open; the journal record types are storage-level and restoreswap is L1.
- DATA7-18 — P0a: restoreswap also records the highest `OPTIONS-<n>`, and storage's open at P2 (not P4b) reads the rewrite.
- R7-7 / COR7-2 / SIMP7-1 — §6.4.11 mirror test: `export_test.go` exports aliases only, `legacy_mirror_test.go` in `package <owner>_test` asserts both directions (the l. 1456 wording).
- R7-7 / SIMP7-4 — `secrets.Options.LegacyStore` and `library.Options.LegacySource` typed `any`, asserted at construction, `ErrLegacyStoreType` fails boot (§6.4.11, P4c, P4d step 1(ii), §6.6 row, §6.14 rows).
- COR7-3 — the kernel, not config, calls `Open` (§6.4.3); detection names the two driven capabilities; at S2 config detects `ConfigExternalWriter` itself at P3.
- COR7-4 — P2 compares legacy supplied or not; P4c compares the full backend value before P4d (`ProblemLegacyStore` comment, §6.6 row, `TestLegacyStores`, §6.14).
- SIMP7-5 — no kernel record of capabilities; the kernel asserts only `ConfigOpener`, `ConfigSecrets` and, for the backend value, `Covering`; every other capability is detected once by its owner.
- COR7-5 — §6.4.11 "Landing and lifetime": a member is deleted in the first release after its cutover that no longer sets it, a minor.
- COR7-7 — `Handles.Ext`'s `<store>` segment is 05-storage's fixed table (`TestLegacyHandlesExtSegmentsGolden`).
- SIMP7-3 — "What stays outside `legacy`" cites 05-storage's one table.
- SIMP7-8 — `legacy.TasksRecoverer`, `legacy.LibraryRootSource` (and `legacy.RBACAtomWriter` named in the rule) throughout; Objections to 03 updated.
- SIMP7-2 (consistency) — a legacy-served root takes no `ReadShares` (§6.4.11, §6.14 05-library row).
- FE7-13 — `--version-json`'s `ui_build_id` is the `X-App-Build` value, not `/modules`' shell-only field.
- FE7-2 — a consumer of `commit` strips `+dirty` and follows 09 P10's fallback order.
- FE7-1 (consistency) — `vcs.modified` keeps Go's definition; `core/web`'s and hello's `.gitignore` keep build outputs from setting it (§6.4.5, §6.12.5).
- MOD7-6 — §6.12.5: hello's `web/dist` is on core's own list only, no Dockerfile cross-check.
- R7-1 (cross-cluster, 05-config) — P4d step 1(v) `Store.NarrowStored` (05-config §1.4 step 6b, audited `core.config.legacy_narrowed`): Basic-gate narrowing from S3, auth narrowing from S4 r1; write rule lists it.
- R7-2 / DATA7-17 (cross-cluster, 05-config) — the kernel passes `legacy.ConfigOpenEnv.ResetUnfinished` to `Open` (P4d step 1(i)) and sets `secrets.Options.ResetBoot` on the reset boot (step 1(ii)), which rewrites the keyring header's `install_id`.
- OPS7-1 (cross-cluster, 05-metrics) — §6.4.8 lists `coretest.MetricsProvider(opts app.Options)`, the kernel's provider built without booting, scraped through `Provider.Handler()`.
- integration pass (round 7): §6.4.6 offline open: `ErrOfflinePendingSteps`, the four restore/reset tokens, `staging-scrub` in the subcommand list; P0a: `KindCheckpoint` file `restore/<id>/inflight-checkpoint.json`, the reset set-aside's targets (module aux DBs, `config.boot.json`, `storage.boot.json` moved, not deleted) to `<db parent>/.mediacore-reset/<id>/previous/`, `reset/<id>/inflight.json` kept until `hook_done`, `reset_pending`/`reset_inflight`, the hook ends with `ResetRun.Done`; §6.4.11 adds `legacy.ConfigResetArgs` and `legacy.ResetRun` (R-B request); `storage.ErrLegacyStoreType` qualified everywhere; SIMP7-8 sweep (`LibraryRootSource` comment and conformance, `legacytest.LibraryRootSourceSuite`, objections paragraph).

Round 8 (2026-11-04), findings applied to this file:

- R8-1 / SIMP8-1 — P0a: a factory reset is `KindReset` of the one swap: one atomic intent slot `restore/pending.json` with `kind`, `storage.ErrSwapPending`, one rejection rule (`expires_at`, `staged_by_build`, a moved open stamp), one journal, the token pair `restore_pending`/`restore_inflight`; the `reset/` slot, `.mediacore-reset/`, `reset_pending`/`reset_inflight`, `ROLLBACK_IGNORE_RESET`, the precedence rule and `TestRestoreAndResetBothStaged` deleted (`TestSwapSlotExclusive`).
- COR8-1 — `restorejournal.ResetStore` (HEAD's `POST /system/reset`; no aux DB, no hook) and `restorejournal.ResetFactory`; both legacy routes stage through `legacy.Maintenance` from S2b and are `Alias`es of the one `internal/storagehttp` route from S4 r2 (`reset_kind=store`, confirm `RESET`, 422); `TestSystemResetStagesStoreOnlyReset_S2b`.
- COR8-2 — `kernel.stageReset` calls `ConfigResetArgs.ResetArgs(ctx, kind)` (a `legacy.ConfigResetPlan`: `hook_args`, `fresh_rows`) and stages through `restoreswap.Swapper.StageReset`; `legacy.Maintenance.StageFactoryReset` is that function and `internal/storagehttp` receives it as `storagehttp.Stager`, never `storage.Admin` (§6.4.6, P0a, §6.4.11).
- COR8-3 / DATA8-1 — every target pre-validated at staging and set aside beside itself; a `Recreatable` aux DB outside the DB's filesystem listed in the intent's `delete` (`ConfigResetRun.Delete`); a failed set-aside moves back, journals `rejected`, boots degraded (`factory_reset_failed`).
- COR8-4 / OPS8-5 — the hook never fails boot, always journals `hook_done`, skips kept paths, runs bounded per batch and exempt from the 60 s limit (`Hook.ExtendsStart`, §6.4.3, §6.5.2, §6.8); the kernel journals `hook_done` when no app hook exists; dangerous `RootDir` refused at staging (400).
- COR8-5 — restoreswap imports `storage`, imported and run only by the kernel; no swap method on `storage.Admin` (P0a).
- COR8-6 — the refusal sentinel is `storage.ErrSwapPending` (L1), returned from S2b; exclusion across processes by the atomic slot.
- COR8-8 — write rule says "set-aside of `storage.boot.json`"; `ConfigOpenEnv.Reset` handed at `Open` on the reset boot (`set_aside`) and while `applied` lacks `hook_done`; token test row corrected.
- COR8-9 — S2b row lists `ConfigResetArgs`, `ConfigResetRun`, `ConfigOpenEnv.Reset`; P4d step 3 `Keyspace(owner)` and `Ext` = `Keyspace(owner).Sub("ext/<segment>")`; `<db parent>/config.yaml`.
- SIMP8-3 — the kernel asserts only 03 §3.3's list (four, `ConfigResetArgs` at staging); Detection and P4c reworded; Objections to 03: none open (03 v1.11.0).
- SIMP8-4 — `UpgradeFloors()` deleted (ABO's backup reads books' `Manifest.Storage.UpgradeFloor`); `legacy.Maintenance` and `StorageHandles.Maintenance` deleted in the S4 r2 PR (§6.4.4, §6.4.10, §6.4.11).
- SIMP8-7 — `ResetRun` renamed `legacy.ConfigResetRun` (`Args`, `Delete`, `Done(ctx, failed)`); `ResetUnfinished` deleted (`env.Reset != nil`); lifetime note for the settings cutover.
- SIMP8-9 — offline open runs the checks of P2–P3 plus the swap tokens; `Offline.Storage` hands a nil `Maintenance`.
- R8-2 / DATA8-3 / SEC8-4 — §6.4.6: `OpenOffline` computes the boot verdicts and stages the owed entries before it promotes; `TestOfflineOpenAfterForeignOpenKeepsEvidence`.
- DATA8-4 — offline-open users corrected: `reserved-prefs` stays a direct break-glass open (and repairs core's reserved records from S2b), `seed` refused on an empty store ("start `serve` once first"), `dedup-bench` read-only, `Isolate` child mode listed.
- DATA8-5 — "Stages" P0a: an open checkpoint's offline finish commits the offline close batch with the step-e identities before `applied_offline`.
- DATA8-6 — P0a and "Stages": a `KindCheckpoint` journal at `swapped` or `failed` admits a candidate that implements the journaled swap (`--print-restore-journal`), which resumes it; `failed_by_build` does not.
- DATA8-7 (consistency) — P4d step 2: the journal is never moved on a restore boot before `applied`.
- DATA8-12 — P0a re-runs reset renames only before `set_aside`; undo steps cited to 05-storage.
- DATA8-13 — (3b) lists the Pebble open stamp and stays the one statement (05-storage step 14b follows); `storage.boot.json` is moved; P4d step 2 "P2's lineage verdict".
- SEC8-5 — P0a: the reset boot's fresh-install batch writes the intent's `fresh_rows` (books: HEAD's `factoryDefaultConfig` blob).
- R8-4 / SEC8-6 / SEC8-9 (consistency, 05-config request) — P4d step 1(v): the Basic gate narrows whenever `true`, `core.http.rate_limit.enabled` narrows, `NarrowStored` returns `owed`; new step 1(vi) `Store.WriteOperatorNames` writes `operator-names.json` (05-config step 6c).
- R8-3 / OPS8-2 — §6.4.6: `staging-restore` (S0, opens no store) in the subcommand list; `staging-scrub` and `staging-restore` run with the archive's release binary.
- OPS8-7 — `ROLLBACK_IGNORE_RESTORE=1` overrides `restore_pending` only, never `restore_inflight`.
- OPS8-10 — `coretest.MetricsProvider(t, opts)` boots a zero-module harness so the kernel's own families are seeded.
- OPS8-12 — §6.5.2 `Lifecycle.RequestRestart(reason)`; §6.8 `core.lifecycle.restart_exit_code` (75, `ProtectAdmin`, replaces the unshipped updater key); `TestRestartRequest_ExitsRestartCode`.
- FE8-4 — §6.12.5: hello builds to `core/bin/hello`, ignored by `core/.gitignore`.
- MOD8-11 — §6.12.5: hello's embed pattern is normalised per 03 §3.2 item 4.
- FE8-7 — §6.4.7 `ProblemUIAppIDMismatch` (P8, S9), §6.6 row and `TestUIAppIDMismatch`: the kernel refuses a bundle whose `mediacore-app` meta differs from `Options.AppID`.
- integration pass (round 8): `legacy` reset comment no longer names the former carrier type (sweep (a)); the detection bullet and "Objections to 03" cite 03 §3.3 as the one statement of the kernel's asserted capabilities instead of restating it (sweep (g)), keeping only where in this file's phases each is asserted and that the staging one is asserted for both reset kinds.

Advisor (2026-11-04), resolutions applied to this file:

- A-2 / ADV-3 / ADV-S1 — representation-neutral extraction: the extension family, `legacy_ref.fp`, Widening suspension, `legacy.ExtConfirmer`, `Handles.Ext` and its segment table, `["none"]`, the moved-secret sentinels and re-import, P4d step 1(v) `NarrowStored` and step 1(vi) `WriteOperatorNames`, and the `auth` revoke-after-rollback duty removed from the boot table, the kernel wiring, §6.4.6, §6.4.11, §6.11.6, §6.10 and §6.14; legacy stores keep HEAD's representation behind the core interfaces, ABO roles hold legacy atoms only (P7), native stores keep the full design.
- A-3 / ADV-S2 — §6.5.1 "Checked writes": `Expect`/`Serialized` checks in the boot table bind native rows and rows core newly writes; legacy writers keep HEAD's lock discipline (`legacy.Stores` comment, §6.11.6).
- A-4 / ADV-S3 — `archtest.DeferredMembersAbsent` and the §6.4.9 column grammar removed; the table is a reader's index.
- A-5 / ADV-S4 — `app.Options.MetricNamespace` (core series), `Options.MetricAliases` deleted, `coretest.MetricsProvider` and §6.9 use the namespace (§6.4.5, §6.4.10, §6.14).
- A-6 / ADV-S5 — P−1 `updater.Boot` deferred-build with the boot-attempt counter and automatic rollback; the marker-exit-code clauses, §6.6 row and Stages bullet updated; `TestBootRefusalCountsAsFailedBoot` marked deferred.
- A-7 / ADV-2 — P0 row: legacy names decode with HEAD's per-type semantics, fail-closed on auth and `ProtectAdmin` bools.
- A-8 / ADV-4 — §6.5.1 "Boot writers and the crash matrix": every durable writer from P−1 to Run (3b) with its 05-storage state diagram; `TestBootCrashMatrix`.
- A-10 / ADV-6, A-12 / ADV-S6 — §6.4.6 staging subcommands: `staging-restore` writes the staging marker (`<db>.staging`, `c/storage/staging`), `staging-scrub` refuses without a matching marker and `--confirm-destroy-credentials`; `TestStagingScrubRefusesUnmarkedStore`.
- A-14 / ADV-S8 — P4d step 5 (compat usage records, `recording_since`), the `usage` duty and buffered-use persistence removed; P0 hands alias uses to compat's process-local counter.
- A-15 / ADV-S9 — §6.4.9: deferred packages are design sketches; the SDK freezes none of their members.
- A-16 / ADV-11 — §6.2: literal counts informative, generated at each stage's base commit.
- A-17 (Q-K) — §6.4.6, P0a, `legacy.Maintenance`, `ConfigResetArgs.ResetArgs(…, deleteLibraryFiles)`: the native reset route deletes library files only with `delete_library_files: true`, the legacy alias passes it; `TestFactoryResetLibraryFilesOptIn`.
- Consistency — §6.14 05-storage row: `Maintenance` lives S2b to the S4 r2 PR (was "S8").
- integration pass (advisor): §6.4.5 adds `app.Options.LegacyAmbientScopes` (05-rbac §6.3) and the S4 stage row lists it; §6.4.11 adds `legacy.TasksWriteLock` to the capability list, states `legacy.Handles` has no extension family, drops `legacy.ConfigSuspender` and `library.RootSpec.Legacy`, and `ConfigResetPlan` gains `LibraryRoot`/`LibraryFiles` with `hook_args` as 05-storage states; §6.5.1 states the lead's A-3 ruling (voluntary compare-and-set).

Round 9:

- COR9-1 / DATA9-1 (R9-1) — §6.4.11: new `legacy.WriteLock` (`Lock(keys) (unlock func())`) replacing `legacy.TasksWriteLock`; core commits a legacy-staged batch only through `storage.CommitLocked`, so HEAD's identity mutexes span core's commit; §6.4.10 S4 row, §6.5.1 "Checked writes", P4c, capability list.
- DATA9-2 (R9-1) — §6.4.11 `Stores` comment, P4c and §6.11.6: exactly one instance of each legacy store family per App, shared with books' own P6 store; `TestLegacyStoresAreSingletons`.
- COR9-8 (R9-1) — §6.4.10 S5 row: `legacy.WriteLock` on the run store (`opsMu`), asserted on the built `Stores.Runs` value; `Clocks`, `Pause`, `StandDown` share its hold; `WriteRuns` uses `storage.CommitLocked`.
- COR9-3 (R9-4) — §6.4.5 `LegacyAmbientScopes`: principal-only, added after the key-scope intersection, `Confined()` decided before ambient; no role-read injection; §6.14 05-rbac row.
- COR9-4 / SEC9-4 (R9-6) — §6.4.6 one reset contract `storage.ResetRequest` through `Stager.StageReset`, `kernel.stageReset`, `ResetArgs` and `Maintenance.StageFactoryReset`; 422 `library_files_confirm_required` on a preflight mismatch, nothing staged; alias path values only; `ConfigResetRun.DeleteLibraryFiles`; `TestFactoryResetLibraryFilesOptIn`.
- DATA9-4 (R9-6) — P0a: a reset journal stopped mid-set-aside always moves back every `moved` entry before `rejected`.
- COR9-12 — P0a: one set-aside destination rule (under `<db parent>`, under the state directory, otherwise beside itself); a failed set-aside renames `inflight.json` to `rejected.json`.
- DATA9-3 (R9-7) — P0a: `inflight.json` is created before the first rename of every kind; step g only journals `swapped`; crash-matrix case.
- DATA9-5 (R9-7) — Stages P0a bullet and writer table: a hookless restore boot (S2b–S4 r1) reaches `applied` through `opened → marker_written`, writing `c/storage/boot` only.
- COR9-5 (R9-7) — §6.4.8 public kit `coretest/crashmatrix`; §6.5.1 writer table complete from P0a (books' `Open` key-file write, the kernel's `applied`/`hook_done` and `delete` removals, the hookless marker batch, the post-reset hook's gap-fill rewrite) and labelled state FS; `TestBootCrashMatrix_ABO`.
- COR9-6 (R9-9) — §6.4.5 `Offline.StagingMarker`, `Offline.WriteStagingMarker`, `app.ReadStagingMarker`, `app.OfflineCommands`/`Command`; §6.4.6 staging subcommands and users; `TestCLIExposesCoreOfflineCommands`.
- OPS9-8 (R9-9) — §6.4.6: the staging binary is identified by the archive's `binary_id`; `staging-restore` refuses a mismatch.
- COR9-15 — §6.4.6 and Stages: the open-checkpoint offline finish uses the kernel's internal close-batch function, not `OpenOffline`.
- COR9-7 / DATA9-12 — §6.5.1: the crash-matrix range is P0a to Run (3b); the P−1 row is dropped until P−1 is built.
- MOD9-1 / MOD9-2 (R9-3) — §6.4.8 `MetricsProvider`: `TestABOMetricsGatherNoDuplicateOrMissing` checks families, labels and series values from S5.
- DATA9-6 (R9-5) — §6.4.5 `LegacyConfigStore` and Stages: the peer allow-list is file- and env-only at HEAD (`json:"-"`), never a UI-saved blob value.
- COR9-11 — §6.4.11 `Stores` comment: config is exclusive per key (`Alias.LegacyBlob`), backends `legacy+native`.
- SEC9-8 — §6.4.5 `LegacyAmbientScopes`: H1 writes the ambient scopes into every converted non-confined role and key before retiring the option.
- integration pass (round 9): §6.4.11: `legacy.WriteLock` is `WriteLocks(names []string) []storage.Lock` with `LockRank`, `storage.CommitLocked(ctx, b, d, stage, locks...)`, nested `CommitLocked` joins and nested `WriteRuns` fails with `ErrWriteRunsNested`; lock lists per 05-storage (not `counterMu`); `ConfigResetArgs` and `ConfigResetPlan`: the preflight comparison is `kernel.stageReset`'s; legacy-only capabilities list `legacy.TasksScheduleTicks`; §6.4.10 S4 and S5 rows (`TasksScheduleTicks`, `LockRuns`, `WriteRuns(ctx, open, stage)`); §6.4.5 `PromRegisterer *metrics.Shared`, `OfflineCommands` adds `secrets export-keyring` and `secrets rotate-derive`; §6.4.11 `Maintenance` cites 05-storage's set-aside destination rule; §6.4.6 one reset contract (`LegacyConfirm`, comparison in `kernel.stageReset`) and a pre-S0 archive cannot be staged; §6.5.1 crash-matrix range "P0a to Run (3b)"; §6.14 test renamed `TestFactoryResetNativeRouteKeepsLibraryFilesByDefault`. Item "`legacy.WriteLock` in the kernel-asserted list" was already satisfied (five, per 03 §3.3).

Round 10:

- COR10-1 (R10-1) — §6.4.4 `Deps.Tasks` row: books' five legacy operations routes stay books' gin routes at every stage, reading runs through `tasks.Client` from T0+T1 (v2 shape by T8); no operations route or alias moves to core; core's run route is `GET /api/core/v1/tasks/runs/{id}`.
- COR10-3 / MOD10-1 / MOD10-4 / MOD10-5 (R10-3) — §6.4.3 `MountLegacy` and §6.4.5 `PromRegisterer`: the legacy mount keeps `otelgin` as HEAD's sole recorder of gin requests; core's stage 2 records only core and module routes; pre-mount rejections are a §9.2.1 row; no `httpapi.ReportLegacyRoute`.
- MOD10-6 (R10-3) — §6.4.5 `app.Options.MetricViews` (instrument name → boundaries, installed as views at Provider build; ABO passes `telemetry.HistogramBuckets()`); §6.4.8 `MetricsProvider` takes it and checks `le`; §6.4.10 S2 row; §6.14 row.
- MOD10-2 / MOD10-11 — §6.4.5 `app.Options.ServiceName` (resource `service.name` merged with `resource.Default()`, stage 2's fixed `server.address`, never `Host`); §6.4.10 S2 row; §6.14 row.
- COR10-2 / DATA10-1 (R10-4) — §6.4.11 `WriteLock` comment: the hold lives on the batch; `storage.LocksHeld(b, l)`; identity store, run store, `SecretsStore.Write` and the config store's `Stage` are checked, captured-batch writes refused; §6.4.10 S4 row.
- DATA10-3 (R10-4) — §6.4.11 `WriteLock` comment: one total `LockRank` order (05-storage §5S.3.2's table), core-side mutexes before `CommitLocked` (secrets' keyed mutex the one in-stage exception), no `stage` calls a covered HEAD-mutex method; `TestLockRanksTotalOrder`.
- DATA10-2 (R10-5) — §6.4.11 `WriteLock` comment and §6.4.10 S3 row: `CommitLocked` runs the store's post-commit step (`Committed`, the gap-fill write) before it releases the locks; every `configBlobSaveMu` writer path goes through it.
- COR10-5 — §6.4.11 `WriteLock` comment, §6.4.10 S4/S5 rows, §6.4.11 capability detection and §6.5.1 P4: the kernel calls `WriteLocks` with the `legacy` names at P4c and hands owners `auth.Options.Locks`, `RunLocks`, `SettingsLocks`; owners never call it.
- DATA10-4 / COR10-4 (R10-6) — §6.5.1 P0a: a failure exit from every state (`rechecked` → `swapped_back`, `staged` → `rejected`); state-file copies are copy-if-exists; the intent records `hook_paths` and the labelled filesystem is bounded by them, so the legacy alias keeps HEAD's `RootDir` and Open Library wipe; boot-writer P0a and Run 2 rows; §6.4.8 kit error-return mode; `TestBootCrashMatrix` and `TestFactoryResetFromLegacyRoute_S2b` rows.
- COR10-6 — §6.5.1 P4d 4 row: the verified-email reconcile runs under the locks 05-authn §3.4 names for `legacy.AuthReconciler.Reconcile`.
- COR10-7 — §6.4.8 kit: after-boot tree diff against the label log, core's `Prev` is hello from the merge base; §6.5.1 Run 2 row adds `.readonly-key` and routes both HEAD token files through `legacy.ConfigOpenEnv.Files` until S4 r2; P4d 4 row: `auth.RestoreDeps` takes the labelled state filesystem.
- COR10-8 — §6.4.5 `LegacyAmbientScopes`: `/metrics` is `Router.Infra(path, "core:metrics.read", h)` on every app, reached by non-confined ABO principals through the ambient scope.
- COR10-9 — §6.4.6 "One request contract" and §6.5.1 P0a: a hookless app refuses `delete_library_files` (422 `ErrResetRequestInvalid`); each reset alias's `Selector` drops the body's `kind`, `delete_library_files`, `library_root` and `library_file_count`; `TestFactoryResetNativeAppCompletes` row.
- COR10-10 — §6.4.10: S2b row adds `coretest/crashmatrix` and `legacy.ConfigOpenEnv.Files`/`legacy.StateFiles`; S3 row adds `legacy.WriteLock` and `legacy.LockConfigSettings` on books' config store; S5 row names the built `Stores.Tasks` value (was `Stores.Runs`). "Once per App" kept.
- SEC10-1 (R10-2) — §6.4.5 `LegacyAmbientScopes`: H1 writes ambient scopes into a key only when its effective set before ambient is not `Confined()`, never from the owner alone.
- SEC10-9 — §6.4.5 `OfflineCommands`: `secrets export-keyring` opens no store; `rotate-derive` and `reset-password` open read-write, `set-install-id` read-only.
- DATA10-5 (R10-11) — §6.4.5 staging marker and §6.4.6 staging subcommands: the marker carries `binary_id` (written by `WriteStagingMarker` from `BinaryID()`); `staging-scrub` refuses unless both halves agree and `binary_id` is its own; test row.
- DATA10-8 — §6.5.1 P0a: from S2b P0a resumes a journal left in flight by S1–S2 r3's ABO-local swap (same JSON shape; `TestS2bResumesS1Journal`).
- MOD10-8 (R10-9) — §6.12.5: the tag-union tools tolerate hello's unbuilt `web/dist` through `go list -e`; core's `vet-darwin` runs without `embed_frontend`.
- integration pass (round 10): §6.4.5 `app.Options.ServedMeterProvider` (S2 r1–r2 bridge, deleted in the S2 r3 PR) and `ServiceName` feeding `metrics.Options.ServiceName` and `httpapi.Options.ServerName`; `WriteStagingMarker` records the running `buildid.Binary` as `BinaryID`, marker and manifest `binary_id` agreeing; `MountLegacy` and `PromRegisterer` comments: no route-report identifier, no `otel_scope_name` distinction, pre-mount refusals with no `http_route`; §6.4.11 `WriteLock` comment and P4 (c)/(d): `auth.Deps.Locks`/`auth.RestoreDeps.Locks`, `secrets.Options.SettingsLocks`, `storage.OnCommitted` post-commit steps, `storage.ErrNotLocked`; `legacy.ConfigCommitObserver` removed from the S3 row and capability list; updater step 5 → step 3 (twice) and the two additions; the exec-mode restart deferred-build in §6.5.2; both round-10 objections to 03 resolved by 03 v1.14.1. Already as requested: `secrets export-keyring` opens no store.

Round 11:

- VER11-1 (R11-1) — §6.4.5 `app.Options.HTTPMetricsScope` (S2; ABO passes otelgin's scope as data) and the S2 field row; `MountLegacy` and `PromRegisterer` comments: the wrapper clears `r.Pattern` before gin (`httpapi.MatchedPattern` for SEC2-22), stage 2 and otelgin share one instrument set, the "disjoint by `http_route`" wording deleted, `WithoutScopeInfo` kept.
- R11-5 (BLK11 note) — §6.5.1 P0a failure edge: the reset state is named as 05-storage names it, `set_aside_started` (before it, `rejected` with nothing moved; at or after it, moved targets go back first), not `staged`.
