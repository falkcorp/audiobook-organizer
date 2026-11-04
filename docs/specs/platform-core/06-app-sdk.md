<!-- file: docs/specs/platform-core/06-app-sdk.md -->
<!-- version: 1.6.0 -->
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

## 6.3 Design decisions

| # | Decision | Rationale and cost |
|---|---|---|
| A1 | A module is `Manifest()` (pure data, validated before storage opens) **plus** `Register(*Registrar)` (runs after migrations and config load, receives typed `Deps`). **Rule: anything the kernel needs before services exist, or while the module is disabled, lives in the `Manifest`; anything that needs a service lives in `Register`.** So permissions/roles, storage spec (keyspace, legacy claims, migrations, the declarative part of derived indexes), the legacy store implementations a module supplies to core subsystems (R4-1), library roles, settings sections, its outbox consumer names, its task-ID ledger, the foreign task kinds its system actor may start, legacy HTTP prefixes and UI descriptor IDs are manifest fields. Routes, tasks, resources, topics (the one catalog, SIMP-5), subscriptions, outbox handlers, change sinks, adapters, health/diagnostics, backup artifacts, derived-index hooks, compat auth methods, hooks and workers are registrar calls: each closes over a service built in `Register` (COR2-15). | 03 §3.7 requires validation before listeners, migrations before services, and 05-config requires sections to be registered before the config store loads, while route handlers need migrated storage and loaded config. One `Register` cannot satisfy both. Outbox consumer names and the task-ID ledger are manifest data so that a disabled module's messages are retained (COR-12) and its persisted runs resolve as held (COR-13). Cost: two places to look; mitigated by the rule being mechanical. |
| A2 | **No generic DI container in core.** Core's own ~30 services are built by hand-written, typed code in `kernel/services` (a struct with one field per service, constructed in a fixed order). A module builds its internal services with plain Go constructors inside `Register`. | Evaluated below (§6.3.1). |
| A3 | `Deps` is a **struct of typed, module-scoped handles**, never `any`, never nil. Each handle's **type** carries the module identity, so scoping is enforced by the owning package and cannot be spoofed by the caller: `events.Scope` publishes only the module's declared topics, `library.Scope` carries the module's owner, `tasks.Client` stamps the actor from ctx, `secrets.Scope` reads only `modules.<id>.` names, `*metrics.Scope` prefixes the module's metric names (03 §3.6 `MetricPrefix`, §6.4.4). A shared pointer to a whole service never appears in `Deps`. | Replaces 113 `Get[T]` string lookups with compile-time types; a scope type is the only place R3/R8 can be enforced for a handle (COR-21). |
| A4 | Registrar methods **record problems instead of returning errors** (like `testing.T`); the kernel joins every problem of every module into one `*ValidationError`. | An operator sees all misconfigurations at once, not one per restart. |
| A5 | Module code runs only through the kernel: no `init()`, no package-level mutable state, background goroutines only via `Registrar.Worker`. A disabled module's `Register`, migrations, hooks and workers are **never invoked**. The one exception is its legacy store constructors (`Manifest.LegacyStores`, §6.11.6): the data they serve belongs to core subsystems (users, roles, runs), so the kernel builds them whether the module is enabled or not, and a constructor may only capture its handles (no goroutine, no write, R4-1). | This is what makes G1 a structural property rather than a per-feature check. Enforced by `archtest.NoInit` + `archtest.TestNoGlobals` over module packages, and by `coretest.AssertModuleInert`. |
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
is public-but-unstable (L8; apps must not import it, R2); `app` (L9) and `coretest` are SDK.

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
	// a Seed step runs on a fresh install too, §6.5.1 P4a, COR5-8), and the
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
	// key name or row format (03 R1). Two models (SIMP5-1, 05-storage
	// §5S.3.11 "Adapter selection"): for an EXCLUSIVE store (Config, Auth,
	// Roles, Tasks, Logs, AIJobs) a set field replaces the native
	// implementation; for a ROUTED store (Roots, Gates, Integrations,
	// NotifyRules) the native store always serves and the legacy one serves
	// in addition the records its Covers accepts. A nil field means the
	// subsystem uses only its native implementation over c/<subsystem>/, which
	// is what hello, coretest and every new app get (SIMP4-3). Each field may
	// be set by at most one manifest (ProblemLegacyStore). The kernel calls the
	// constructors at P4c with the module's own handles, enabled or not (A5).
	// Each field lands with its subsystem (§6.4.10).
	LegacyStores LegacyStores

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

// LegacyStores lists the core store interfaces a module may implement over
// its own legacy claims (R4-1, §6.11.6). Each constructor is called once per
// boot at P4c with the declaring module's own handles (storage.LegacyHandles:
// its Legacy range, exactly as the module receives it, and its extension
// family m/<ID>/ext/<store>/), whether the module is enabled or not (A5). A
// constructor only captures its handles (no goroutine, no I/O, no write), so
// it is safe for Describe. The interfaces and their native implementations
// are owned by the subsystem's file; they are SDK and frozen by the
// API-stability gate. Each interface holds only the native contract; a
// member only a pre-core store can give meaning to is an optional interface
// of the same package, which the kernel detects by type assertion at P4c and
// which goes with that family's cutover (05-storage §5S.3.11 "Legacy
// capabilities", SIMP5-4). Every write method takes the caller's
// *storage.Batch (05-storage participant binding), with two stated
// exceptions that commit their own batches and never join a multi-owner
// commit: log.JournalStore, which is append-only and names only log types,
// so log (L0) imports no storage (05-logging §3), and the maintenance
// methods Recover and Prune (05-tasks) (COR5-12, MOD5-10). Data with no
// legacy form goes to the extension family, never additively in a legacy
// row; each extension record carries legacy_ref.fp, a fingerprint of the
// security-relevant fields of the legacy row it extends (never per-request
// fields such as last use, R5-6). A record whose type has a Widening field is
// suspended until an admin re-confirms it when an older build changed those
// fields underneath it; a record without one is re-stamped by its next
// routine write; a revocation an older build made stays revoked (05-storage
// "Extension records", R5-6, DATA5-4). The full-text dirty set is not a
// field here: a module that keeps a pre-core dirty set passes it per index
// as index.Spec.Dirty when it opens the index (05-storage §5S.3.8, SIMP5-2).
// There is no result-journal store: internal/ai/resultjournal stays books
// code over books' own claim (SIMP5-3). Field and interface names follow the
// owning files, which win on detail; 03 §3.3 "Store interfaces" is the one
// list (SIMP5-8).
type LegacyStores struct {
	Config       func(storage.LegacyHandles) (config.OverrideStore, error)       // S2b: settings overrides and legacy secret copies (05-config)
	Auth         func(storage.LegacyHandles) (auth.Store, error)                 // S4: users, API keys, OAuth links, invites, sessions (05-authn §3.4)
	Roles        func(storage.LegacyHandles) (rbac.Store, error)                 // S4: roles (05-rbac §3.4)
	Tasks        tasks.LegacyStoresFunc                                          // S5: tasks.Stores (runs, clocks, pause marker; 05-tasks §5T.3.13)
	Logs         func(storage.LegacyHandles) (log.JournalStore, error)           // S5: system-log rows the journal reads and prunes (05-logging)
	Roots        func(storage.LegacyHandles) (library.RootSource, error)         // S7: library import roots (05-library)
	AIJobs       func(storage.LegacyHandles) (ai.JobStore, error)                // S7: AI batch jobs (05-ai)
	Gates        func(storage.LegacyHandles) (providerhttp.GateStore, error)     // S7: provider holds and quota counters (05-metadata M2)
	Integrations func(storage.LegacyHandles) (integrations.InstanceStore, error) // S7: legacy-backed integration instances (05-metadata M1)
	NotifyRules  func(storage.LegacyHandles) (notify.RuleStore, error)           // S7: legacy notification rules (05-notify)
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
//     from S2b, whose Open core config calls first in P4d step 1 (§6.5.1); a
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

	Storage StorageHandles // Keyspace m/<ID>/, Legacy claims, Indexes, Aux opener, Derived generations (05-storage)

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
// §6.11.6).
type StorageHandles struct {
	Keyspace storage.Keyspace           // m/<ID>/
	Legacy   storage.LegacyRange        // zero value unless the manifest declared claims
	Indexes  index.Opener               // full-text indexes the module declared (05-storage §5S.3.8)
	Aux      storage.AuxOpener          // auxiliary DBs under <db parent>/aux/<ID>/<name> (05-storage §5S.3.10)
	Derived  map[string]storage.Derived // generation handle per declared DerivedIndex (§5S.3.9)
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
| `Storage` | `module.StorageHandles` | this file (above); field types 05-storage §5S.3.4, §5S.3.8–§5S.3.10 (defined) | reads and writes outside `m/<ID>/` and the module's claims refused (R8); the module's `LegacyStores` constructors receive the same `Legacy` range plus its extension family and nothing wider (§6.11.6) |
| `Tasks` | `tasks.Client` | 05-tasks 5T.3.10 (defined, with `RunView`) | the kernel overwrites `StartRequest.Actor` with the ctx principal, or with the module's system actor outside a request, so a module cannot start a run as another actor (05-tasks 5T.3.10, defined). The system actor's permission set is exactly the `Perms.Start` of the module's own kinds plus those of `Manifest.TaskStarts`, never `core:admin` or an `AuthRequired` scope (SEC2-15) |
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
	// 05-config §1.7), resolved ONCE per process, before P−1, by
	// config.ResolveStateDir (05-config §1.4 step 0), whose result P−1, P0a,
	// P0 and every consumer share. It keeps HEAD's rule
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
	// (Manifest.LegacyStores.Config, R4-1), which stays until the settings
	// family's cutover (COR4-3). nil = an app with no legacy names. S2.
	LegacyConfig *config.Legacy

	// LegacyConfigStore is the S2 bridge only (S2 has no kernel storage):
	// books' legacy config store, built by ABO's main over the store its serve
	// opened before New. config's DB layer attaches over it at P3, so a
	// UI-saved value (the rate limiter, CORS origins, the peer allow-list, the
	// log level) is honoured at boot with HEAD's precedence for the app's
	// legacy keys (R5-1), and the store's Changed (the optional
	// config.ExternalWriter, S2 to the S3 PR, SIMP5-5) fires after every blob
	// write by ABO's legacy config path, so it is honoured at runtime as HEAD
	// does (COR3-1). The config.OverrideStore interface lands here with its
	// read members only, naming no storage or secrets type (COR5-14).
	// Deleted in the S2b PR, when Manifest.LegacyStores.Config takes over over
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
	// (MOD4-3, 05-metrics). nil = the App's own registry.
	PromRegisterer prometheus.Registerer

	// MetricAliases are the app's legacy metric names for series that moved
	// into a core subsystem (metrics.Alias, 05-metrics §3, §13): core holds no
	// app's legacy names (R1), and each alias needs a row in CompatLedger.
	// Never used for module series. The field lands at S2b, with compat; each
	// alias is added in the PR that moves its emitter, with its ledger row and
	// the deploy/ dashboard switch (09 §9.2.7, OPS5-6).
	MetricAliases []metrics.Alias

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

// New runs boot phases P−1 to P8 (§6.5.1): updater boot check, pending
// restore swap, config boot view, manifest validation and enablement,
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
// lowercase hex SHA-256 of the executable (os.Executable, read once per
// call; no ldflags, no package state, R5; debug.ReadBuildInfo VCS fields may
// be logged beside it, never part of it). New computes it once, before P−1,
// and hands the one value to updater.Boot (Self.Build, the marker's
// to_build), storage (c/storage/selfserved/<binary id>, the served ledger's
// build) and backup (StagedByBuild, Source); ABO's --version-json prints it
// beside the UI build ID. Two binaries of one commit with different code, or
// two API-only builds, therefore never share an identity, which the UI build
// ID ("dev" without an embedded bundle) cannot guarantee (R5-7, FE5-3).
func BinaryID(opts Options) (string, error)

// ResolveStateDir returns the state directory New will use for opts: the
// same config.ResolveStateDir call over the same flags, env and
// Options.StateDir (05-config §1.7). An app that must touch state files
// before New (ABO's S2 serve: the key guard and encryption init) calls it
// instead of a resolver of its own (OPS4-8).
func ResolveStateDir(opts Options) (string, error)

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
the app controls its own lifecycle. ABO keeps its cobra command tree: no file of `cmd/` moves into
`app` (MOD2-4). Its `serve` command replaces its OTel and slog bootstrap with calls to `app.New`,
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
| `kernel/catalog` | manifest validation, enablement resolution (P1, from `config.Store.Boot`), collision checks across modules and core (IDs against `module.ReservedIDs()`, `Options.ReservedAPINamespaces` and the first segments of every `LegacyPrefixes` entry; permission namespaces, legacy atoms, legacy role IDs (`LegacyRoleIDs`, COR5-5), keyspace and legacy claims, legacy store supply (each `LegacyStores` field set by at most one manifest, R4-1), config sections, task kinds, topic names, outbox consumer names, legacy path prefixes, UI IDs; facade URL bases when `arrfacade` is built), `MinCoreVersion` against `version.Version` |
| `kernel/services` | `type Services struct{ Storage *storage.DB; Tasks tasks.Executor; … }` built by `func Build(ctx, opts, cfg) (*Services, error)` in a fixed, hand-written order; `Close` in reverse |
| `kernel/supervisor` | start/stop ordering of §6.5.2, hook start/stop, worker supervision (`sync.WaitGroup.Go`), panic recovery, timeouts, shutdown budget |
| `kernel` | `Kernel` = catalog + services + supervisor; `Registrar`/`Deps` construction per module (the scope types of §6.4.4); hands registrations to services in P7; mounts core capability routes |

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
lacks them (FE2-25).

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
	ProblemLegacyStore            // a LegacyStores field set by two manifests (P1), or a store backend that differs from the one the store records (P2, ErrLegacyStoreMissing, 05-storage) (R4-1)
	ProblemLegacyRole             // a Manifest.LegacyRoleIDs entry listed by two manifests or equal to a core seed role ID (P1, COR5-5); appended, so no built kind is renumbered

	// deferred-build (08): appended when arrfacade lands, so adding them never
	// renumbers a built kind (SIMP3-11); listed in the deferred-members
	// archtest (§6.4.9):
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
store interface (`authtest`, `rbactest`, `configtest`, `logtest`, `taskstest`, `librarytest`,
`aitest`, `providerhttptest`, `integrationstest`, `notifytest`), each exporting
`<Interface>Suite` and the fakes of its interface, named by the 05-storage §5S.3.11 rule
(`<pkg>test.<Interface>Suite`, `TestNative<Interface>_RoundTrip`, SIMP5-9). They are SDK (an app's
legacy stores run them, and the API-stability gate freezes them), may import any core package, and
no non-test core package imports them; 03 §3.3 lists them (MOD5-6). Each lands with its interface.

`coretest/fakes` provides in-memory implementations of every built capability contract (download,
metadata, ai, notify; indexer once built) so module tests never need a network. Time-dependent tests run inside
`synctest.Test(t, func(t *testing.T) { h := coretest.New(t, …) … })`; the harness starts no real
timers outside the bubble.

### 6.4.9 Deferred-build members

Added additively in the build that creates the package (03 §3.3 "public, later"; cross-cutting
resolution 1). Until then the `module` package does not import these packages, and no core v1
symbol refers to them. The first column holds only package-qualified Go identifiers that must not
exist in non-test files under `core/`; every other deferred item is in the last column, which the
check does not read (COR5-16, SIMP5-6).

| Member | Waits for | Purpose | Also deferred (not checked) |
|---|---|---|---|
| `module.Manifest.Acquire` | `acquire` (10) | manifest trigger for building the acquire service in P5 (A8) | — |
| `module.AcquireDecl` | `acquire` (10) | `{Vocabulary, Categories}`, the type of `Manifest.Acquire` | — |
| `module.Deps.Acquire` | `acquire` (10) | `acquire.Register[T](d.Acquire, mediaType)` in `Register` attaches typed hooks only | — |
| `module.Deps.Indexers` | `integrations/indexer` (05-indexer) | `SearchAll`, `RSS`, `Fetch`, `Caps` for module search tasks | — |
| `module.Registrar.ArrFacade` | `arrfacade` (08) | at most one per module; checks in P7 | — |
| `module.ProblemFacadeIncomplete` | `arrfacade` (08) | appended to the `ProblemKind` block, so no built kind is renumbered (SIMP3-11) | — |
| `module.ProblemFacadeURLBaseCollision` | `arrfacade` (08) | as above | — |
| `module.Registrar.DownloadWatch` | `integrations/download` monitor (05-download phase 2) | the grab/monitor pipeline, built with `acquire`; ABO does not use it (SIMP-12) | — |

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

**One check (SIMP3-2, SIMP4-1, COR4-19).** `archtest.DeferredMembersAbsent(t, list)` runs in the root
module's `make ci` from S2 (09 S2) and fails when any identifier of the list appears in a non-test
`.go` file under `core/`. The list is generated from every heading under `docs/specs/platform-core/`
whose text, after an optional section number, is exactly `Deferred-build members` (03 §3.4 is the
one grammar; this section is one such heading), reading only the **first column** of its table. Every
first-column entry is a package-qualified identifier (`pkg.Type`, `pkg.Type.Member`, `pkg.Func`), so a
deferred `download.Event` never matches `audit.Event`; a behavioural restriction on a built member is
never listed (an absence test cannot check it), and the generator's self-test fails on an entry that
does not parse as one or that names a member already built (COR5-16, SIMP5-6). Lifetime: the PR that
builds a deferred member deletes **that member's** entry; the test is deleted when the list is empty.
The deferred-build files (05-authn §3.5, 05-http-api §3.6, 08 §8.0) cite this test instead of a prose
grep.

### 6.4.10 Landing stage of each SDK member (COR2-9, MOD2-10)

A member exists from the 09 stage that lands the package it names (04 §8.1); before that it is
absent, not stubbed, and the kernel phase that consumes it is skipped (§6.5.1). Each later member is
added additively. `rbac`'s leaf types land at S2 so `httpapi.Route.Perm` and the `auth` leaf compile
there: `Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`, `Public`, `AnyAuthenticated`,
`Delegated`, plus the `Checker` interface and `RoleName` that `auth.Principal` and `auth.User` name;
05-rbac §3.1 decides the list and 04 §8.1 and 05-http-api §3 repeat it (COR2-9, MOD4-11, COR4-11). The
`auth` leaf is `MethodSpec`, `Principal`, `User`, `UserID`, `UserStatus` and `Assurance`. A
`LegacyStores` field lands with its subsystem; until then that subsystem does not exist, and books'
own code serves those rows exactly as at HEAD.

| Stage | `module.Manifest` | `module.Registrar` | `module.Deps` | `app` |
|---|---|---|---|---|
| S2 | `ID`, `Version`, `DisplayName`, `MinCoreVersion`, `DefaultEnabled`, `Settings`, `UI` (IDs only), `LegacyPrefixes`, `PublicRoutes` | `Context`, `Deps`, `HTTP`, `MountLegacy`, `AuthMethod` (recording only; the `auth` leaf types land at S2, SEC3-14), `LogPathRules`, `LogOtherUserData` (recorded from S2, consumed when the journal query lands, 05-logging), `Hook`, `Worker`, `Err` | `Module`, `Log`, `Metrics`, `ExtendStart` | `Options` fields `AppID`, `MetricPrefix`, `ReservedAPINamespaces`, `DisplayName`, `Version`, `EnvPrefix`, `StateDir`, `ConfigFile`, `Args`, `Env`, `LegacyConfig`, `LegacyConfigStore` (the `config.OverrideStore` interface lands here with its read members, naming no `storage` or `secrets` type; its `Changed` is the optional `config.ExternalWriter`, S2 to the S3 PR; COR5-14, SIMP5-5), `PromRegisterer` (set from S2 r3, OPS5-14), `LegacyAuthGinUntil`, `Logger`; `New`, `Run`, `Handler`, `Modules`, `ResolveStateDir`, `ExtendStart`, `Describe` (`Core`, `OpenAPI`) (COR3-3) |
| S2b | `Storage`; `LegacyStores.Config` and the native `config.OverrideStore` (read only: the store's `Open` at P4d step 1, `Load`, `Suspended`, `Changed`; config writes nothing to the store before S3; COR5-14) | — | `Storage` (`Keyspace`, `Legacy`, `Aux`) | `Options.CompatLedger`, `Options.MetricAliases` (each alias in the PR that moves its emitter, OPS5-6); `BinaryID` (its first consumer is storage's self-served record, R5-7); `Options.LegacyConfigStore` deleted (SIMP4-11) |
| S3 | `LegacyStores.Config` also written by core config (the write members `Stage`, `Confirm`, `Committed` and the legacy secret copies for `secrets.Open`, 05-config owns the names; `config.ExternalWriter` deleted in the S3 PR, SIMP5-5; COR5-14) | — | `Secrets` | — (`LegacyConfig` stays: it carries names, not the codec, COR4-3) |
| S4 | `Permissions`, `Roles`, `LegacyAliases` (own namespace), `LegacyRoleIDs`; `PublicRoutes` emptied for hello; `LegacyStores.Auth`, `LegacyStores.Roles` | `AuthMethod` also handed to `auth` | `Auth`, `AuthAdmin`, `Audit` | `Options.Authenticators`, `Options.Auth`, `Options.LegacyPermissionAliases` (COR5-5); `LegacyAuthGinUntil` cleared by the release in which core auth decides requests (§6.5.1 "Stages"); the kernel declares the restart-bound `core.backup.dir` (alias `backup_dir`) in its own core-section list, so `backup/journal` imports no `config`, and registers `backup/journal`'s `storage.Spec` for the core keyspace `c/backup/` (owner `backup`; `Serialized: ["journal_head"]`, prefixes `journal_gap/` and `restore_hooked/`), so a revocation's journal head and gap line commit from S4 r2; `backup` adopts both at S8 (COR4-10, COR5-6) |
| S5 | `TaskLedger`, `TaskStarts`, `DataMigrations` (`BootBlocking` only; `Online` deferred, SIMP5-7); `LegacyStores.Tasks`, `LegacyStores.Logs` | `Tasks`, `Resources`, `Lanes`, `ChangeSink`, `Topics`, `Subscribe`, `Health` | `Tasks`, `Events`, `Health` (readiness reporters: the `health` readiness core, `ComponentID`, `Reporter` and the `Ready` view that dispatch waits on, lands with `tasks`, COR4-8) | — |
| S6 | `OutboxConsumers` | `OutboxHandler`, `Realtime` | `Outbox` | — |
| S7 | `Requires`, `Library`; `LegacyStores.Roots`, `.AIJobs`, `.Gates`, `.Integrations`, `.NotifyRules` (books' dirty set reaches `storage/index` per index through `index.Spec.Dirty`, not the manifest, SIMP5-2) | `Adapter`, `AICapability`, `DerivedIndex` | `Storage.Indexes`, `Storage.Derived`, `Library`, `HTTP`, `Integrations`, `Downloads`, `AI`, `Notify` (with `integrations/notify`, SIMP3-5) | `Options.Adapters` |
| S8 | — | `StartupSteps`, `Diagnostics`, `Backup` | `Backup` (the rest of `health` lands behind the S5 `Health` handles) | `Options.Updater`; `Options.PromRegisterer` removed (MOD4-3) |
| S9 | `UI.LegacyRedirects` (books fills it in the release that moves its URLs to `/books/...`, 07 §7.19 step 5, which ships with step 4, so `ui_route` recording starts there, FE4-8, FE5-13) | — | — | `Options.UI`, `Options.UIOptions`, `Description.UIManifests` |

Package `ui` exists from S2 with one dependency-free leaf holding `ReservedSegments()` and the plain
types `ModuleManifest`, `UIIDs` and `Redirect`, so `module.ReservedIDs()` and 03's equality test hold
from S2 and `internal/modulehttp` (L6, S4) has a type for `modules[].ui`; the rest of `ui` lands at S9
(FE3-8, FE4-6, COR3-3; 04 §8.1).

`examples/hello` grows with the same stages (MOD2-10): S2 skeleton (module, `app`, a greeting route
without the stored count and with `Perm: rbac.Public` until core authentication exists, declared in
`Manifest.PublicRoutes` as `getGreeting`, and the `openapi` subcommand emitting `core/openapi/core.json`), storage and its seed migration 1 at S2b (COR5-8), its permissions and the
route's `rbac.Need(PermRead)` at S4, `hello.count` at S5, and `ui_embed.go`/`ui_nonembed.go` with its web package at S9. hello supplies no
`LegacyStores`, so from S4 it and `coretest` run on core's native auth and role stores and from S5 on
the native run store, each landing with its subsystem (SIMP4-3, 03 §3.4 rule (b)). §6.12 shows the S9 state. G6
and G7 are proven from S5, the first stage at which every import of the full example exists
(§6.10).

## 6.5 Lifecycle

### 6.5.1 Boot phases (inside `app.New`): the one table

This table is the **only** normative statement of boot order (R2-8). 05-updater §5U.3, 05-backup
§5B.3.4, 05-config §1.4, 05-storage §5S.4, 05-authn §5, 05-rbac and 05-tasks describe what their own
step does and reference the phase named here; where one of them lists an order, this table wins.

**Write rule.** Nothing is written to the store before the pre-migration checkpoint (P4a). Before it,
the only writes are files outside the store that a pre-boot step owns (the updater's attempt counter,
the restore journal and its renames, the quarantine of an unparsable boot record) and in-memory
buffers. Every every-boot writer of P4d (the config and integration secret re-imports, revocation-journal
re-apply, post-restore hooks, `auth.BootWrites`, compat usage records) runs after the checkpoint and the migration DAG, in the
order of P4d, and before any listener (DATA2-24). The later every-boot writers, all still before
listeners (§6.5.2), are: books' start-hook repairs at Run step 2 (05-storage §5S.9.3: the journal-clock
repair, the identification-index stamp, and, until each successor lands, HEAD's `ReconcileOpsV3WithV2`
before books builds its ops registry, until S5, and HEAD's `ReconcileVerifiedEmails`, until S4 r2); the
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

**One stored format per family; the app supplies its own legacy stores (R3-1, R4-1).** No boot phase
copies, mirrors, adopts or rebuilds a family. The data ABO already keeps for generic services (the
rows behind each store interface of 03 §3.3, the one list, SIMP5-8) stays in books' legacy claims for
the whole extraction. The core subsystem that serves it calls **books' own implementation** of the
subsystem's store interface (`Manifest.LegacyStores`; the full-text dirty set per index through
`index.Spec.Dirty`, SIMP5-2), ABO code over ABO keys, built at P4c over books' own `Legacy` range:
core holds no audiobook key name or row
format (03 R1) and no core subsystem reads a module's rows, so there are no storage grants during
the extraction. An app that supplies none (hello, `coretest`, a TV app) gets core's native store over
`c/<subsystem>/`. Data with no legacy form (native scopes, hashed sessions, native one-time tokens)
is kept by books' implementation in an extension family no older build reads, never additively in a
legacy row (SEC4-3: an extra string in a role row un-confines HEAD's worker-only roles); each
extension record carries `legacy_ref.fp`, a fingerprint of the security-relevant fields of the legacy
row it extends (never per-request fields such as last use, so ordinary use on either build suspends
nothing). A record with a Widening field is suspended until an admin re-confirms it when an older
build changed those fields during a rollback; one without is re-stamped by its next routine write;
a revocation made by an older build stays revoked (R5-6, DATA5-4). New API keys always carry explicit,
non-empty scopes, and a key with no legacy-equivalent scope is written to the legacy row with a scope
that grants nothing on an older build, never an empty list, so no row an older build reads can widen
(HEAD reads empty scopes as "all role permissions", `internal/server/middleware/auth.go:405-408`
@748fd787a; R5-6). Which implementation serves a record is fixed by the build's manifests (exclusive
stores) or by the legacy store's `Covers` (routed stores, SIMP5-1), and the choice is recorded in the
store; a build whose choice differs fails P2 (`ProblemLegacyStore`, 05-storage `ErrLegacyStoreMissing`). Moving a family to a
native core keyspace and deleting the legacy one is the **cutover**, out of the extraction: a later,
per-family, one-way step, checkpointed by P4a and guarded by the owner's level like any format change;
storage grants exist only there (05-storage).

| Phase | Step | Store writes | On failure |
|---|---|---|---|
| P−1 | `updater.Boot` (05-updater §5U.3), only when `Options.Updater` is set. Uses the state directory `config.ResolveStateDir` resolved once before this phase from flags, env and `Options.StateDir` with HEAD's fallback, whose database path may also come from the operator YAML file (§6.4.5 `StateDir`; R3-4, OPS5-13) — pre-boot inputs, never the DB, so a config that P0 refuses cannot stop it — and reads `<state_dir>/updates/pending.json`. The binary ID (`app.BinaryID`, §6.4.5) is computed once before this phase and is the identity every later phase uses (R5-7). The marker's absolute `db_path` is only the DB path `ReaderCheck` needs, never an input of the state-directory resolution (OPS4-8). `Boot` first checks that the marker's `to_build` is the running binary's ID (never the UI build ID, FE5-3); a marker of another build (a manual rollback or deploy inside the confirmation window, or one a pre-S8 build left behind) is appended to `history.jsonl` as `superseded` and counts nothing (OPS4-9, 05-updater). Otherwise it counts this start as a boot attempt and decides rollback **before** any restore swap: when it restores `.prev`, a restore intent staged by the failing build is then rejected as foreign by 05-backup step a (`ReaderCheck` ignores `restore_pending` for such an intent, 05-updater). Every later failure in this table, P0 refusals included, counts as a failed boot (OPS2-3), and while the marker names the running binary such a boot refusal (an `ErrUpgradeFloor` included) exits with the marker's `restart_exit_code` (default 75), never the floor release's distinct 78, so a unit that stops on 78 still restarts and `Boot` can roll back (05-updater 5U.4, OPS5-15) | none (`updates/pending.json` only) | per 05-updater: roll back to `.prev`, or keep the binary with `rollback_blocked` |
| P0a | Pre-boot restore swap (05-backup §5B.3.4), with the state directory resolved as in P−1: if `<state_dir>/restore/pending.json` exists, swap using the **absolute targets recorded in it** (DATA2-25). At step g the intent is renamed to `restore/<id>/inflight.json`, not deleted, and is removed only when the restore journal reaches `applied` or `swapped_back`; P0a also resumes any journal whose state lies between `previous_moved` and `applied`. The kernel decides "this is a restore boot" from that journal, never from `pending.json` alone, so a crash between the swap and P4d's hooks re-runs the (idempotent) hooks at the next boot, and `CheckReader`, `make rollback` and `deploy` refuse while such a journal exists (DATA3-6). P2 verifies that storage opens at the recorded DB path; if not, the swap is undone | none (journal and renames only) | per 05-backup: swap back; boot degraded (`restore_failed`, `restore_rejected`) |
| P0 | `config.NewStore` with `EnvPrefix = Options.EnvPrefix`; register core sections; `Store.Boot` resolves core sections from file → env → flags and the boot record `<state_dir>/config.boot.json` (05-config §1.3.5, §1.4). A config file that sets the pre-boot key `core.paths.state_dir` is refused (05-config §1.7). Alias uses seen here (config and env window aliases, 05-config `OnLegacyUse`) are buffered in memory and recorded at P4d (OPS2-7) | none (an unparsable boot record is renamed `.bad`, fail-open, per 05-config) | fail-closed |
| P1 | `Manifest()` of **every** module, enabled or not; `kernel/catalog` validates IDs against `module.ReservedIDs()`, `Options.ReservedAPINamespaces` and the first segment of every manifest's `LegacyPrefixes` (reserved IDs only, FE3-1); `MinCoreVersion` against `version.Version`; the namespaces of permissions, settings, task kinds, outbox consumer names and the task-ID ledger; cross-module and core collisions (a disabled module's names, claims and legacy stores stay reserved, so every manifest is checked); each `LegacyStores` field supplied by at most one manifest (`ProblemLegacyStore`). Static checks only: no rbac call, because `rbac` has no store yet (COR2-7). Resolve enablement from the `BootView`: `core.modules.<id>.enabled`, default `DefaultEnabled` (§6.5.3) | none | all problems joined → `*ValidationError` |
| P2 | Open storage at `core.storage.path` resolved from the `BootView` (05-storage §5S.4 step 1: absolute; `os.OpenRoot` on the DB's parent directory, which need not be under the state directory, DATA2-19, COR2-8). Store guard: the single per-owner reader-level guard and the fast-exit flag (05-storage §5S.3.7). Spec validation: keyspace and legacy claims against the persisted claim records `c/storage/claims/<owner>` (DATA-28); a claim on disk whose owner is absent from the build (a removed module) stays orphaned until a declared claim sets `Claim.TakesOver` (05-storage rule 5), never silently re-owned (DATA2-29). There are no claim hand-overs during the extraction, so no other orphan case exists (DATA3-2 is resolved by R3-1). **Store backends (R4-1):** for each core subsystem the build's choice (the supplying module's legacy store, or native, for an exclusive store; `legacy+native` or native for a routed store, SIMP5-1) is compared with the choice the store records; a build that would switch an existing store fails here (`ProblemLegacyStore`, 05-storage `ErrLegacyStoreMissing`: a native store would show an empty user, role or run set), and the choice of a first boot is recorded after the checkpoint. Every release of the extraction from the stage that lands a subsystem supplies the same legacy store, so a rollback between them never trips it (DATA4-2). P0a's recorded-path check | none (read only) | fail-closed |
| P3 | Register the settings sections of every **enabled** module, then `Store.Load` from the operator layers and the boot record; at S2 only (no kernel storage), config's DB layer also attaches here over `Options.LegacyConfigStore`, books' legacy config store over the store ABO's serve opened, with DB-override precedence: unlocked keys only, and for the app's legacy keys HEAD's precedence, under which an env name HEAD treats as a default sits below the stored value and locks nothing (COR3-1, R5-1; 05-config owns the alias flag). From S2b the DB layer is the config store built at P4c (books' legacy config store for ABO, native otherwise), attached at P4d step 1. `Load` seals the registry. A disabled module's sections are not registered: its stored values are retained, unvalidated, until it is re-enabled | none | fail-closed (an invalid operator-layer value) |
| P4 | In this order. **(a)** One pre-migration checkpoint for the boot (05-storage §5S.3.12); if it cannot be taken, boot halts before anything is written. A checkpoint is reused only when the same uncompleted upgrade attempt took it for the same store identity (install marker plus store generation); a completed checkpoint, or one left by a swapped-back restore, is never reused, and `storage restore-checkpoint` refuses a checkpoint older than the current upgrade attempt (DATA3-1). An **empty** store is instead stamped as a fresh install per 05-storage §5S.4 steps 5–6, in **one batch**: install marker, every owner at the build's level, every registered versioned step recorded as applied **except seed steps** (DATA2-28, DATA3-21). A seed step (`storage.Migration.Seed`, allowed only with `Additive`, 05-storage §5S.3.5) stays pending and runs in (b) as an ordinary step in its own batch, with no checkpoint, since the store held nothing; so hello's `seed-names` and an app's default rows exist on a fresh install (COR5-8, `TestFreshInstallRunsSeedSteps`). **(b)** The cross-keyspace migration DAG (05-storage): every pending core and **enabled-module** step in DAG order. Books' legacy migrations (versions 1..66) run under books' own claims, which books holds in place for the whole extraction, so no step needs another owner's rows, and no core step reads a module's rows (R4-1). A step that changes an owner's stored format raises that owner's level through the one per-owner guard (05-storage §5S.3.7): books' own format changes during the extraction, and each later per-family cutover. A step whose preconditions do not hold is deferred (05-storage §5S.3.5). `BootBlocking` data migrations run in process through `tasks.ForEach` with persisted shard cursors, not as executor runs (COR2-7); the `Online` mode is deferred-build (§6.4.2, SIMP5-7). After the DAG, storage records the claim ledger, the backends and the store generation (05-storage §5S.4 step 9). It does **not** replace the served ledger's `last` record or the foreign-open identity here: every consumer (P4d steps 2 and 4, step 5's usage record, the derived loader at Run (3a)) reads the previous boot's record, which is replaced only at Run step (3b), after all of them have committed (R5-5, COR5-3). **(c)** Storage issues the core subsystems' keyspace handles; the kernel then builds each core subsystem's store: for each `LegacyStores` field some manifest sets, it calls the constructor with that module's own handles (`storage.LegacyHandles`: its `Legacy` range and its extension family), whether the module is enabled or not, and detects the store's legacy-only capabilities by type assertion (05-storage §5S.3.11 "Legacy capabilities", SIMP5-4; e.g. the optional `tasks.Recoverer` on the run store, whose `Recover` is the start-time repair at Run step 5); it builds the native store over `c/<subsystem>/` when no manifest supplies an exclusive store, and always for a routed one, which then serves every record the legacy store's `Covers` does not accept (R4-1, SIMP5-1). **(d)** The every-boot reconcilers, each a function of its owning package over the stores from (c), with no service built yet, in this fixed order (COR3-2, DATA3-5): 1. the config store's attach, in this order (COR5-2): (i) the store's `Open` (a legacy-only capability; the native store has none): for ABO, books' legacy config store runs ABO's key-regeneration guard and settings-encryption init, which loads the legacy key, and, at S2b (until the S3 façade), the settings blob load, env sync and `config.Validate`, over the kernel's single store handle, failing boot where HEAD's step is Fatal, so nothing ever runs on, or writes, defaults over the stored blob (R4-6, COR4-1); (ii) from S3, `secrets.Open` with the store's legacy secret copies (05-config §2.3–§2.4): core's keyring, the capture of the key (i) loaded as `k0-legacy`, and the R4-2 roll-forward re-import of the moved config secrets, its only store writes besides `c/secrets/meta`; no derive-key retirement, which P5 decides (COR3-2). The secrets HEAD already stores encrypted (the OpenAI and Google Books keys, the Hardcover token, the Basic password) are not moved: books' store keeps reading HEAD's encrypted rows, so no sentinel ever strands them after a rollback (R5-1); (iii) `Store.SetSecrets`; (iv) `Store.Attach` (05-config `AttachOverrides`; its `Load` is read-only; there is no config import step, R3-1): DB overrides become visible, with HEAD's precedence for the app's legacy keys (R5-1), and an enabled module's settings are validated against them; 1b. from S7, the integrations registry's load-time secret re-import over its instance store from (c): an instance secret whose legacy copy differs from the sentinel (re-entered on an older build during a rollback) is re-imported into `c/secrets/` and re-sentinelled, in one batch (R4-2, 05-metadata M1). This applies only to moved (formerly plaintext) secrets: the four HEAD already stores encrypted (OpenAI key, Google Books key, Hardcover token, Basic password; `internal/config/persistence.go:1955-1958` @748fd787a) stay in HEAD's rows, read in place by books' stores, with no sentinel and no re-import in step 1(ii) or here (R5-1(b)); 1c. at S4 r1 only (§6.5.1 "Stages"), the read-only decode census over `auth.Store` and `rbac.Store` scans (05-authn §5), which writes nothing (COR5-15); 2. revocation-journal tail re-apply (05-backup), with the journal located from the **effective** `core.backup.dir` (overrides attached in step 1; the key exists from S4, COR4-10; when the directory changed since the journal's last line, 05-backup first moves the journal: copy, verify the chain, rename) and every line verified with the keyring opened in step 1. Order (SEC4-5, DATA4-7, DATA4-15): the journal's `install_id` is checked against `c/storage/install` before any line is used; read `head_before`, the store's journal head, **before** anything is appended this boot; classify the boot by **store generation**, never by head lag: a P0a restore boot is a restore boot, a store whose generation or directory identity differs from the one `backup.location.json` records is a **replaced store** (an older build's in-place restore, a file copy), and otherwise it is the same store whatever its head; every verified revocation after `head_before` (the seq of the journal's last **revocation** line is the end; gap and restore lines are bookkeeping and never count) is written through auth's or rbac's store from (c) — on the same store a lagging head only re-applies, with no hooks; only then are the due gap line and `restore` line appended, each with its head update in its own batch, and `backup.location.json` records this store's generation (05-backup §5B.3.6 row "Boot re-apply"); 3. post-restore hooks (05-backup `RestoreHook`, e.g. `auth.PostRestore`), on a restore boot as P0a decides it or on a replaced-store boot as step 2 classifies it (hooks with `KindExternal` and `Revocations` nil, SEC4-6), in the order auth, rbac, audit; 4. `auth.BootWrites` (05-authn §5: seed `_system`, stamp legacy API keys that lack an expiry, mint the bootstrap hash and the optional start-up read-only key, and, for ABO, books' legacy auth store's verified-email reconcile, which books' start hook runs as HEAD's `ReconcileVerifiedEmails` until this step exists at S4 r2, COR5-7), through auth's store, so P5 writes nothing (DATA3-18, COR4-13, DATA4-10); 5. the alias uses buffered in P0 are recorded in the compat usage ledger (09 §9.2.9). There is no tasks step: `tasks` has no post-DAG reconciler (05-tasks step 0, R3-1); the run store's start-time repair of stale records runs inside `Executor.Run` at Run step 5, before listeners, and until `tasks` lands (S5) books' start hook runs HEAD's `ReconcileOpsV3WithV2` before it builds its ops registry (`internal/database/store.go:1861-1871` @748fd787a, COR5-7). There is no identity rebuild step | yes, from (a) on | fail-closed. A failed migration step leaves earlier steps committed and the checkpoint **unconsumed** for the offline `storage restore-checkpoint`; there is no automatic restore (DATA2-19, 05-storage §5S.3.5). A failing reconciler or settings validation fails per its owning file |
| P5 | `kernel/services.Build`: every core service in fixed order (A8), including the services behind every `Deps` handle and the realtime hub; each core subsystem is built over its store from P4c; `rbac.New` and then `Declarer` per manifest, disabled ones included so their permissions stay known (§6.5.3, COR2-7); `auth` with `Options.Authenticators`; the backup service decides derive-key retirement (`secrets.RetireUnused`) now that the effective backup directory is known, never from inside `secrets.Open` (COR3-2); `notify` before `updater`, whose update notifications go through it; adapters from `Options.Adapters` (module adapters arrive in P6 and are instantiated lazily on first lookup) | none | fail-closed |
| P6 | For each enabled module in ID order: build its `Registrar` and `Deps`, call `Register(r)`, collect `r.Err()`, close the registrar. Registrations are only recorded; `Register` writes nothing outside its handles (§6.4.1) | none | all problems joined |
| P7 | Consume registrations (A8): task definitions to the executor (R7 checks, schedule keys declared under the owner's namespace, R2-7; kinds in `TaskLedger`; each `TaskStarts` kind registered or in any manifest's `TaskLedger`, COR3-10; derived rebuild kinds named `<ID>.derived.<name>.build`, COR3-7), topics to the catalog (05-events validation), subscriptions to the bus, outbox handlers to the relay (topics declared `Durable`, consumer names declared), legacy projections to the realtime hub (redaction is each catalog entry's `TopicSpec.Redact`, so nothing is registered for it), change sinks to the task writer, adapters to the integrations registry, compat auth methods to `auth` (non-interactive only; from S4), `LegacyAuthGin` and `OwnAuth` checks (SEC2-17), checks to health; one compat entry (surface `ui_route`) per `UIDescriptor.LegacyRedirects` row, for `ui.Handler`'s recorder (from S9, FE4-5). Seed roles are reconciled (05-rbac §4.8: the seed row is the seed, a restricted addition waits in `PendingSeed` for `AcceptSeed`, SIMP4-6) and default permission grants are seeded for permissions new to this boot, through rbac's store (for ABO, native scopes go to books' extension family, never into a legacy role row, R4-1). Post-checks: R6 over the built router, required capabilities configured, module IDs re-checked against every recorded mount's `TopSegments` and `SPAShared`, each of which must be a first segment of a declared `LegacyPrefixes` entry (03 §3.6 P1/P7 split) | seed-role rows and default permission grants; task definition snapshots `c/tasks/def/<kind>` (05-tasks 5T.4 step 1) | joined |
| P8 | Build the root `http.Handler`: core capability routes, module routes, legacy mounts, the `/` owner (either `ui.Handler`, built with `Options.UIOptions` merged into its `ui.Options` and mounted with `Router.SPA(h, httpapi.SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})`, so stage 8's Basic-gate asset exemption survives the S9 switch, 05-http-api §4.10, FE5-10; or the `ServesSPA` legacy mount with its `LegacyMount.BasicExemptAssets`, FE-1, FE3-4). Check that no `ui.ReservedSegments()` entry is in `Router.NonSPASegments()` (`TopSegments` minus `SPAShared`, 03 §3.6; `ProblemSPASegmentCollision`, FE2-7, FE3-1, FE3-9). There is no build-ID check: the only UI build ID is the `/` owner's embedded one (FE4-1) | none | fail-closed |

`New` stops core services' goroutines and closes storage and everything opened so far, in reverse
order, on any failure.

**Stages (COR2-9).** A row whose package has not landed (§6.4.10) is absent, not stubbed.

- **S2.** The kernel runs with `storage` absent: P2 and P4 are skipped and `Deps.Storage` does not
  exist. ABO's `serve` opens the store, runs books' migrations and HEAD's other startup steps
  **before** `app.New`, in HEAD's order and through its own `internal/startup` (`cmd/root.go:274-391`
  @748fd787a; MOD3-1), with the state directory from `app.ResolveStateDir` (OPS4-8). It hands the open
  store to `books.Module(store)` and passes `Options.LegacyConfigStore`, books' legacy config store over
  that store, over which config's DB layer attaches at P3 (COR3-1). There is never a second Pebble
  open, and the start-timeout extensions of that pre-`app.New` code use `app.ExtendStart`.
- **From S2b** the kernel opens the store at P2 and builds books' legacy config store at P4c from
  `Manifest.LegacyStores.Config` over books' own range; config attaches it at P4d step 1, where it also
  runs ABO's key guard, encryption init and settings load (R4-6), so a UI-saved value is honoured at
  every stage. Its `Changed` (the optional `config.ExternalWriter`, deleted in the S3 PR, SIMP5-5)
  fires after every blob write by ABO's legacy config path (books code, the writer until the S3
  façade), and config re-reads and republishes live keys, so a perimeter edit (rate limiter, CORS
  origins, peer allow-list, log level) applies without a restart as at HEAD (COR4-2). `serve` runs no
  startup step from S2b. `secrets.Open` in P4d step 1 exists from S3, always after the store's `Open`
  (COR5-2).
- **HEAD's every-boot store repairs between S2b and their successors (COR5-7).** From S2b, when
  `serve` no longer opens the store, books' start hook (Run step 2) runs HEAD's `ReconcileOpsV3WithV2`
  before it builds its ops registry until `tasks` lands (S5), and HEAD's `ReconcileVerifiedEmails`
  until P4d step 4 exists (S4 r2); each then moves to its successor, the run store's `Recover` inside
  `Executor.Run` and books' legacy auth store inside `auth.BootWrites`. An S2b done-check boots a store
  with a stale opv3 record and an unstamped email row and finds both repaired (05-storage §5S.9.3).
- **P0a.** From S2b the kernel's journaled swap runs at P0a (05-backup §5B.3.4: resume, step x's swap
  back after a P2, P4b or hook failure, step h's bookkeeping until `applied` and the removal of
  `inflight.json`, and the swap that `storage restore-checkpoint` needs), over the kernel's own
  manifests' floors; until S8 ABO's `internal/backup` only stages the restore (writes `pending.json`),
  and from S8 05-backup stages it too (DATA4-6). P4d step 3 runs the hooks from S4, when `auth`, `rbac`
  and `audit` exist, whenever either reports a restore boot (DATA3-7). P4d step 2 exists from S4, with
  `backup/journal`.
- **Until S4**, P5 has no `rbac`, P7 seeds no default permission grants, and P4d step 4 does not exist.
  **Within S4**, while `Options.LegacyAuthGinUntil` is still set (the dark release in which gin's auth
  decides every request, 09 S4), the kernel builds `auth` and `rbac` over books' legacy stores for the
  read-only decode census of P4d step 1c (COR5-15) and runs none of P4d steps 2–4 or P7's seed writes:
  each is skipped, logged and counted. They run from the release that clears the field (COR4-4).
- **Until S8**, P−1 is absent: the hotfixed HEAD updater has no boot check, unless the owner picks
  09 §9.12 O9's boot-counter alternative (OPS3-17).

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
duties) and the foreign-open identity, after every reader of the previous record has committed (P4d
steps 2 and 4, step 5's usage record, the derived loader at (3a)); each of those consumers is
idempotent on re-detection (a second `gap` line is harmless, `RevokeSignInsBefore` reuses the same
time, a rebuild re-runs), so a boot that fails anywhere before (3b) leaves the previous record in place
and the next boot detects the same fallback (R5-5, COR5-3, DATA5-6; 05-storage
`TestFailedBootAfterRollbackKeepsFallbacks`); (4) `Bus.Open`: bus
delivery to the subscriptions frozen in P6 begins; (5) task executor, `Executor.Run` (the resume sweep and the run store's `Recover` run inside it; 05-tasks has no boot-time reconcile step in P4d); (6) scheduler, whose `OnStart`
schedules start the outbox relay task `core.outbox.relay`, so outbox delivery begins here;
(7) listeners; (8) readiness true. No bus handler, outbox consumer or other module callback can run
before its module's hooks have started, and the resume sweep's `core.task.*` events are delivered.

If any hook's `Start` fails or exceeds `core.lifecycle.start_timeout`, every already-started hook is
stopped in reverse order (the semantics of `serviceregistry.Container.Start` today, `container.go:164`),
and `Run` returns the joined error.

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
| A `LegacyStores` field supplied by two manifests (P1), or a build whose store backend for a subsystem differs from the one the store records (P2) | fail-closed (`ProblemLegacyStore`) | two writers of one subsystem's data, or a native store that would show an empty user, role or run set, must never boot (R4-1) |
| A legacy store's load fails at P4d step 1 (ABO: key guard, encryption init, settings blob, `config.Validate`) | fail-closed where HEAD's step is Fatal | running on defaults would write defaults over the stored blob on the first settings save (R4-6, `cmd/root.go:360-363` @748fd787a) |
| `AuthMethod` interactive, credential-changing or owner-verifying; `LegacyAuthGin` by a module other than `Options.LegacyAuthGinUntil`; `OwnAuth` route without a registered compat method | fail-closed (`ProblemAuthMethod`, `ProblemLegacyAuth`) | a module must not decide its own authentication strength (SEC2-16, SEC2-17) |
| Derived index declared without hooks, or hooks without a declaration | fail-closed (`ProblemDerivedIndex`) | readiness gating would wait forever, or an index would be unknown while disabled (COR2-15) |
| A shell route segment is also a `NonSPA` segment (a mount's `TopSegments`; `SPAShared` segments are not `NonSPA`) | fail-closed at P8 (`ProblemSPASegmentCollision`) | a full page load of that shell page would get a 404 problem+json (FE2-7, FE3-1) |
| Settings value invalid for an **enabled** module | fail-closed | module would run with undefined config |
| Stored settings of a **disabled** module | not validated (section not registered, 05-config §1.4 step 3); retained | the module does not run; validated when it is re-enabled |
| Migration error | fail-closed; earlier steps stay committed and the checkpoint stays unconsumed for the offline `storage restore-checkpoint`; no automatic restore (DATA2-19) | G2; an automatic restore would fight the crash-loop reuse rule and non-atomic legacy steps (05-storage §5S.3.5) |
| Any failure from P0 on, after an auto-applied update | counted as a failed boot by `updater.Boot` (P−1), which rolls back once the attempt budget is spent; while the pending marker names the running binary the process exits with the marker's `restart_exit_code` (default 75), not 78; without such a marker 78 stands for a floor refusal (05-updater 5U.4, OPS5-15) | config refusals must reach the updater's rollback (OPS2-3), and a supervisor that does not restart 78 would stop the attempts short of it |
| Config file sets `core.paths.state_dir` | fail-closed at P0, naming the flag and env name to use | P−1 and P0a read it before any config file, so a file value would be silently ignored by them |
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
| `core.lifecycle.start_timeout` | duration | `60s` per hook | no |
| `core.shutdown.budget` | duration | `25s` total, defined by 05-health §5H.4 (which also slices it per phase) | no (restart-bound; 05-health states the same, OPS2-23) |
| `modules.<id>.*` | per module schema | per `config.Section` defaults | per field (05-config) |

## 6.9 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_kernel_module_info` | gauge (1) | `module`, `version`, `state` (`enabled`/`disabled`) |
| `mediacore_kernel_boot_phase_seconds` | gauge, set once per boot | `phase`: exactly `P-1`, `P0a`, `P0`, `P1`, `P2`, `P3`, `P4a`, `P4b`, `P4c`, `P4d`, `P5`, `P6`, `P7`, `P8` (ASCII hyphen-minus; the one label set, which 09 and every done-check use verbatim, OPS3-16); `step` (the bounded set of migration and reconciler step names inside a phase, e.g. `books#66` in `P4b`, `revocation-reapply` in `P4d`; `""` for the phase total) (OPS2-24) |
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
| `TestBootPhaseOrder_NoWriteBeforeCheckpoint` | R2-8: an instrumented store records the first write of a boot with auth's boot writes, a revocation re-apply, a post-restore hook and buffered alias uses pending; it follows the checkpoint, the reconcilers run in the P4d order (keyring and overrides before the journal, COR3-2), and no store constructor, legacy-store constructor or `Register` writes (DATA4-10) |
| `TestP4dStep1_LegacyOpenBeforeSecrets` | COR5-2: on the first S3 boot with ABO's key file, the store's `Open` runs before `secrets.Open`, `k0-legacy` is recorded, and a moved secret re-entered on the older build is re-imported; the four secrets HEAD already encrypts stay in HEAD's rows (R5-1) |
| `TestRevocationStagesJournalHeadAtS4` | COR5-6: at S4 r2 a key revocation commits with its `c/backup/journal_head` update (the kernel registers `backup/journal`'s `c/backup/` spec), and an `Append` failure commits its `journal_gap/` line, before `backup` lands |
| `TestServedLedgerCommittedAfterConsumers` | R5-5, COR5-3: a boot that fails after P4b (a P4d step 1 refusal) or after (3a) leaves the previous served record and foreign-open identity in place, so the next boot still appends the `gap` line and revokes pre-rollback sign-ins (with 05-storage's `TestFailedBootAfterRollbackKeepsFallbacks`); a successful boot writes them only at Run step (3b) |
| `TestJournalReapplyVerifiesWithKeyring`, `TestJournalReapplyUsesOverriddenBackupDir` | COR3-2, DATA3-5, COR4-10, from S4: with `core.backup.dir` set only in the settings blob, P4d step 2 reads the journal from that directory and verifies every line with the keyring opened in step 1; with 05-backup's `TestRollbackWithoutRestore_NoKeyConfirmation` and `TestOlderBuildRestore_DetectedDespiteGapLine`, the replaced-store test reads `head_before` before the gap line is appended (SEC4-5) |
| `TestLegacyStores` | R4-1: a `LegacyStores` field set by two manifests fails P1 with `ProblemLegacyStore`; a build whose backend choice differs from the store's record fails P2; a disabled supplying module keeps its stores in force; a constructor receives only the supplier's own handles and writes nothing; with no supplier every subsystem runs on its native `c/<subsystem>/` store; for a routed store (SIMP5-1) a record the legacy store does not `Cover` goes to the native store and the recorded backend is `legacy+native`; a legacy-only capability is found by type assertion and its absence means "none" (SIMP5-4); the boot copies, mirrors or rebuilds nothing |
| `TestDarkAuthReleaseWritesNothing` | COR4-4: while `Options.LegacyAuthGinUntil` is set at S4, auth and rbac are built over the supplied stores, P4d steps 2–4 and P7's seed writes are skipped and counted, and no identity or role row is written |
| `TestLegacyConfigHonoured` | COR3-1, COR4-2, COR4-20: at S2 a value set only in the settings blob (rate limit off, a CORS origin) is effective after P3 through config's DB layer over `Options.LegacyConfigStore`; at S2 and S2b a write by ABO's legacy config path republishes it through the store's `Changed` (`config.ExternalWriter`, SIMP5-5) without a restart; from S2b the boot value arrives through books' legacy config store at P4d step 1. The Basic gate is not asserted here: it stays gin's, reading `AppConfig`, until S3 |
| `TestBuildID_HeaderEqualsEmbeddedMeta` | FE3-2, FE4-1, FE5-1: for a `make build` binary, a Docker image and a goreleaser artefact, `X-App-Build` equals the embedded `<meta name="mediacore-build">`, and for every `embed_frontend` artefact that meta is neither `""` nor `dev` (the `npm run build` script stamped it, R5-7); no build path links an ID, and an API-only build answers `dev` |
| `TestBinaryIDDistinguishesBuilds` | R5-7, FE5-3: two `make build-api` binaries built from one commit with different code have different `app.BinaryID`s although both answer `X-App-Build: dev`; the second one's `RaisesLevel` step is deferred because `c/storage/selfserved/<binary id>` holds no record for it, and a staged-restore intent of the first is rejected by the second |
| `TestStateDirFallbackMatchesHEAD` | OPS3-1: with no explicit state directory and an uncreatable default, P−1, P0a and P0 resolve the directory of `core.storage.path`, as HEAD does; an explicit value never falls back |
| `TestStateDir_OneResolverYAMLDBPath` | OPS4-8, OPS5-13 (05-config's name, the one name): with a DB path set only in ABO's YAML file and an uncreatable default, P−1, P0a, P0, the boot record, the restore intent and `.encryption_key` all resolve to that DB's directory through `app.ResolveStateDir`, as HEAD does; the update marker's `db_path` never changes the result |
| `TestBootRefusalCountsAsFailedBoot` | OPS2-3, OPS5-15: with an update pending, a config refused in P0 increments the updater's attempt counter and exits with the marker's `restart_exit_code` (75), not 78, and the budget's last attempt restores `.prev` before any restore swap |
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
| `TestHelloEndToEnd` | §6.12 example: route permission, task under `-race` with `CPU()` workers, the seed migration runs once on the fresh store (so `world` is seen once, COR5-8, with 05-storage's `TestFreshInstallRunsSeedSteps`), settings default/override, `/api/core/v1/modules` lists nav entry |
| `TestSpecGoBlocksCompile` | COR-16: every Go block in this file marked as part of the hello example is extracted, compiled against core in CI, and **booted** with `coretest.New`, so a manifest-level refusal (an undeclared permission, a bad schedule key) fails the test, not only a compile error (COR2-14) |
| `archtest.TestNoGlobals`, `archtest.NoInit` over `core/...` and `core/examples/...` | R5, A5 |
| `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` | MOD-7, 03 R2: hello is inside the core module path, so Go's `internal/` rule does not stop it importing `internal/...` or `kernel/...`; this check does, and `scripts/ci/core_consumer_smoke.py` builds a consumer under an unrelated module path with a negative compile check |

ABO (root module `internal/arch` or `cmd` tests):

| Test | Proves |
|---|---|
| `TestSecondModuleNoAudiobookEdit` | G6, from S5 (§6.4.10), a test of package `internal/books` (MOD5-11): `coretest.New(t, WithModules(books.Module(…), hello.New()))` boots; the test file is the only change needed — enforced by building it in CI with a `git diff --name-only` check that the hello wiring PR touched no `internal/` audiobook package (plan acceptance test) |
| `TestBooksDisabledInert` | G1 for the real module |
| `TestABOManifestBootsAtS9` | FE3-1: the kernel boots ABO's real S9 manifest (books with its `TopSegments`, `SPAShared` `login`/`invite`, and the shell bundle) with no P8 problem |
| `TestS2bAppConfigFromBlob` | COR4-1, R4-6: from S2b a value set only in the blob is effective in `AppConfig` after boot, and a `PUT /api/v1/config` changing one field leaves every other non-default blob field byte-identical |
| books legacy store suites | R4-1: books' `LegacyStores` implementations pass core's store suites (`<pkg>test.<Interface>Suite`, §6.4.8, owned by each subsystem's file; the umbrella test is the one 05-storage §5S.3.11 names, SIMP5-9) with HEAD fixtures |
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
   aliases, go to a package below it (`internal/books/contract`, name open, 04 §2.4) that imports no
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
| `opRegistrationGate`, `opCatalogGate` (`server_lifecycle.go:66,88`) | P6/P7 validation |
| `scanner.SetStore(store)` (`server.go:805`) and similar setters | constructor parameters in books wiring |

### 6.11.6 Books' legacy stores (R4-1)

During the extraction books keeps every one of ABO's 258 families as its own claim, held in place
(05-storage §5S.9.1 is the one ownership table), and no core subsystem reads them. Where a core
subsystem serves data ABO already keeps, books passes its own implementation of that subsystem's
store interface in `Manifest.LegacyStores` (§6.4.2), and the kernel builds it at P4c over books' own
handles (§6.5.1). Shape, as books' manifest uses it:

```go
// internal/books/manifest.go (ABO; the composition root, §6.11.1 step 2).
// A field is added in the PR of the stage that lands its subsystem
// (§6.4.10); before that the rows are served by books' own code exactly as
// at HEAD. The constructors are thin functions of this package; the
// implementations are ABO's store code over ABO's keys and row formats in
// the packages 04 §2.4 names, none of which imports internal/books back
// (R5-2, COR5-13): the config-blob-backed stores (Config, Integrations, the
// books-root half of Roots) in the config-store package beside
// internal/config, the task store in internal/books/taskstore, the provider
// quota counters in internal/metadata/dailyquota, and the rest in
// internal/books/store. TestLegacyFamiliesWrittenOnlyByLegacyStores allows
// writes to each family only from its implementing package (§6.10).
LegacyStores: module.LegacyStores{
	Config:       newConfigStore,       // S2b (core config writes from S3); config-store package
	Auth:         newAuthStore,         // S4; internal/books/store
	Roles:        newRoleStore,         // S4; internal/books/store
	Tasks:        newTaskStores,        // S5; internal/books/taskstore
	Logs:         newJournalStore,      // S5; internal/books/store
	Roots:        newRootSource,        // S7, routed; config-store package and internal/books/store
	AIJobs:       newAIJobStore,        // S7; internal/books/store
	Gates:        newGateStore,         // S7, routed; internal/metadata/dailyquota and internal/books/store
	Integrations: newInstanceStore,     // S7, routed; config-store package
	NotifyRules:  newRuleStore,         // S7, routed; internal/books/store
},
// The full-text dirty set is not a manifest field: books passes it as
// index.Spec.Dirty when its Register opens the search index (SIMP5-2).
```

What this guarantees:

- **One stored format, so any build reads the store.** Every build of the extraction, the hotfixed
  HEAD included, reads the same rows; a rollback needs no floor build, no claim hand-over and no
  rebuild, and the only per-owner level steps are real format changes (books' own migrations; later,
  each cutover).
- **Core holds no audiobook key name or row format (03 R1, SIMP4-2).** Key names, codecs and HEAD's
  store code stay in ABO behind the core interfaces; core neither re-implements them nor carries a
  second copy of the ops format. Core enforces its invariants above the interface (never-empty scopes,
  upgrade-on-use, the staging rules) and ships a store suite per interface, which books' implementations
  pass with HEAD fixtures (§6.10). The cost is the trade the simplicity review named: the interfaces
  are SDK, frozen by the API-stability gate, and write exclusivity on ABO's ranges is held by package
  structure (the ABO archtest) rather than by storage.
- **Native-only data never reaches an older build as a widening.** Books' implementation keeps it in
  an extension family of its own claims that no older build reads, never additively in a legacy row
  (SEC4-3). Each extension record carries `legacy_ref.fp`, a fingerprint of the security-relevant
  fields of the legacy row it extends (never last-use or similar per-request fields, so use on either
  build suspends nothing); when an older build changed those fields during a rollback, a record with a
  Widening field is suspended until an admin re-confirms it, one without is re-stamped by its next
  write, and a revocation the older build made stays revoked, so a roll-forward never silently widens
  (R5-6, DATA5-4). New API keys always carry explicit, non-empty scopes; a key with no
  legacy-equivalent scope gets a legacy scope that grants nothing on an older build, never `[]`.
- **Moving a secret never weakens a gate (R4-2, R5-1).** Only secrets HEAD keeps in plaintext are
  moved. The first core write of such a secret stores it encrypted and replaces every plaintext copy,
  the legacy row **and** the gap-fill `config.yaml` copy, with an unguessable, never-matching
  sentinel, never an empty value; after a rollback the older build refuses that credential until the
  operator re-enters it (fail-closed; the release note says so). The four secrets HEAD already stores
  encrypted (the OpenAI and Google Books keys, the Hardcover token, the Basic password;
  `internal/config/persistence.go:1955-1958` @748fd787a) are not moved: books' store keeps reading
  HEAD's encrypted rows, so a rollback never strands the Basic gate (R5-1). On roll-forward the
  encrypted copy wins unless the legacy value differs from the sentinel, which means an operator
  re-entered it on the older build: that value is re-imported and re-sentinelled by `secrets.Open` in
  P4d step 1 (05-config owns the mechanics; books' legacy config store writes the copies).
- **Cutover is deferred.** Moving a family to `c/<sub>/` and deleting the legacy rows is a later,
  per-family, one-way step, decided on measured data, checkpointed by P4a and behind the owner's level
  guard; its rollback floor is the cutover release itself. It is the only step in which core reads a
  module's rows, so storage grants are its deferred-build members (05-storage). The cutover build
  still reads the legacy data through the family's legacy store, so a **later** build removes the
  family's `LegacyStores` field (and that store's legacy-only capabilities) behind the cutover's
  `UpgradeFloor` (05-storage §5S.3.13 "After it", SIMP5-13). Nothing in this file schedules one.
- **The live security hotfixes** (revoked-key re-activation, unscoped child keys, the `RequireAdmin`
  scope check, session-token exposure; `todo.d/2026-11-04_platform_core_followups.md`) ship in ABO
  independently and early. They are ordinary HEAD fixes, not a compatibility build.

An app with no legacy data (hello, a TV app) supplies no `LegacyStores`, and the same core subsystems
use their native `c/<sub>/` stores (SIMP4-3). Which implementation serves a record is fixed by the
build's manifests (exclusive stores) or by the legacy store's `Covers` (routed stores: library roots,
provider gates, integration instances, notify rules, whose native store serves every record not
covered; SIMP5-1), and the choice is recorded in the store, so it never changes between boots of one
store during the extraction (P2, `ProblemLegacyStore`).

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
				// Seed: runs on a fresh install too; a non-seed step would be
				// stamped applied there without running (COR5-8).
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
	"fmt"

	"github.com/falkcorp/mediacore/storage"
)

// seedNames writes the first name so the count task has something to count.
// Keys are relative to m/hello/; "name/world" is the key the service reads as
// Keyspace.Sub("name") + "world".
func seedNames(ctx context.Context, tx *storage.MigrationTx) error {
	if err := tx.Set([]byte("name/world"), []byte("1")); err != nil {
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
`outDir: 'web/dist'`, the default) before `go build -tags embed_frontend ./examples/hello/cmd/hello`
(07 §7.6). ABO's G6 test imports `hello` without the tag and gets the zero bundle.

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
| 05-storage §5S.3–§5S.4 | `Manifest.Storage storage.Spec` (derived indexes declarative only); `Registrar.DerivedIndex(name, storage.DerivedHooks, tasks.Definition)`; `Manifest.DataMigrations []datamigrate.DataMigration`; `Deps.Storage module.StorageHandles{Keyspace, Legacy, Indexes, Aux, Derived}`; `Manifest.LegacyStores` constructors over the supplier's own `LegacyRange` and the recorded store backend (`ErrLegacyStoreMissing`) (R4-1; no `Spec.Grants` during the extraction); `c/storage/claims/<owner>`, `Claim.TakesOver`; cross-keyspace migration DAG with one checkpoint per boot; `DataMigration.Writes` | §6.5.1 is the one boot order; 05-storage §5S.4 references P2 and P4 (a)–(d) and keeps the mechanics (R2-8, COR2-7, DATA2-24); store at `core.storage.path`; no automatic checkpoint restore (DATA2-19); checkpoint reuse only within one uncompleted attempt (DATA3-1); `DerivedIndex` hooks move out of `storage.Spec` (COR2-15) and `DerivedHooks` keeps `Snapshot`/`Load` only, the rebuild being a `tasks.Definition` passed to `Registrar.DerivedIndex` (COR3-7); derived record `c/storage/derived/<module>/<name>` (COR4-16); the `Online` data-migration mode is deferred-build in 05-storage §5S.3.14 (SIMP5-7); fresh-install seed steps (`Migration.Seed`, COR5-8); the served ledger's `last` record and foreign-open identity written at Run step (3b), not at §5S.4 step 9 (R5-5); the dirty set per index through `index.Spec.Dirty`, no manifest field (SIMP5-2); routed vs exclusive store models and legacy-only capabilities by type assertion (SIMP5-1, SIMP5-4); `selfserved/<binary id>` (R5-7); no claim hand-over, mirror or rebuild step (R3-1); grants are deferred-build members of the cutover (R4-1) |
| 05-tasks | `Registrar.Tasks`, `Resources`, `Lanes`, `ChangeSink(tasks.ChangeSink)`; `Manifest.TaskLedger` (5T.3.11 held runs); `Manifest.TaskStarts`; `tasks.Common.Schedule` (+ `tasks.ScheduleFromConfig`, one schedule-key rule, R2-7); `Deps.Tasks tasks.Client`; `tasks.CPU()`, `RunItems(rc, par, items, fn, opts...)`, `tasks.ForEach` | closes 05-tasks open item 6 and the `Registrar.Schedule` question (it does not exist). Held runs and the ledger-gate rule of §6.5.3 are 05-tasks mechanics (COR-13); the system actor's permission set (SEC2-15) is 05-tasks' to state; core services start runs through `tasks.Stager` (never the `Executor`), whose lifecycle method is `Executor.Run` (§6.5.2); there is no tasks reconcile in P4d; `tasks.Stores` (`NativeStores` over `c/tasks/`, or books' `LegacyStores.Tasks`, a `tasks.LegacyStoresFunc`, 5T.3.13; R4-1); the run store's `Recover` is the optional `tasks.Recoverer`, detected at P4c and run inside `Executor.Run` (SIMP5-4); the synthesised `<ID>.datamigrate.<name>` definition is deferred-build with the `Online` mode (SIMP5-7) |
| 05-events | `Registrar.Topics(...events.TopicSpec)` (one catalog incl. `Durable`, `Notify`, `Realtime`, `Permission`, `Visible`), `Registrar.Subscribe(...events.Subscription)` built with `events.On`/`OnRaw`, `Registrar.OutboxHandler(...outbox.Handler)` incl. `ConsumeRaw`/`ConsumeIntoTaskRaw`, `Manifest.OutboxConsumers`; `Deps.Outbox outbox.Writer`; `Bus.Open`/`Bus.Close` in §6.5.2 | adopted as 05-events states it, including `Deps.Events events.Scope` (COR-21) |
| 05-realtime | catalog topics with `TopicSpec.Realtime` are streamed with no registrar call; redaction is the catalog's `TopicSpec.Redact`; `Registrar.Realtime() realtime.Registrar` with the one method `Projection(realtime.LegacyProjection) (http.Handler, error)` | hub built in P5, redactors and projections recorded in P6 and attached in P7; projections are books gin manifest routes, not aliases (COR2-1, SEC2-25); core-topic redaction is the topic's own (COR2-2) |
| 05-notify | notify metadata on the catalog entry (`TopicSpec.Notify`); `Deps.Notify notify.Publisher` | `Registrar.NotificationTopic` deleted (SIMP-5); `Publish` is the only entry point, and `Notify` metadata alone produces nothing (COR2-3); `LegacyStores.NotifyRules` (`notify.RuleStore`, R4-1) |
| 05-rbac §3.1–§3.5 | `rbac.Permission`, `PermissionSpec{Name, Title, DefaultGrants, Sensitive, …}`, `RoleSeed`, `RoleName` (`"<ns>.<name>"`), `LegacyAlias`, `Requirement`, `Need`, `CoreAdmin`, `audit.Logger` | manifest lists are fed to `Service.Declarer(moduleID)` by the kernel in P5, after `rbac.New` (COR2-7); leaf types land at S2, including `Checker` and `RoleName` (COR2-9, COR4-11, MOD4-11); no `Grant`, `Scope` or `PermissionDef` type exists (COR-16); `rbac.Store` native (`c/rbac/`) or books' `LegacyStores.Roles`; seed roles during the extraction per 05-rbac §4.8 (`AcceptSeed` allowed, operator diff deferred, SIMP4-6); `Manifest.LegacyAliases` own-namespace only, core-targeting aliases as `app.Options.LegacyPermissionAliases`, legacy role IDs as `Manifest.LegacyRoleIDs` (05-rbac's `LegacySurfaces` and `core/rbac/legacy_abo.go` replaced, COR5-5) |
| 05-config §1.3–§1.4, §2.3 | `config.Define`, `Section[T]`, `Handle[T]`, `config.Register`; `Store.Boot` + boot record for `core.modules.<id>.enabled` (P0/P1); `Store.Load` without DB (P3), plus, at S2, config's DB layer over `app.Options.LegacyConfigStore` (a `config.OverrideStore`, COR3-1); `app.Options.LegacyConfig *config.Legacy` reduced to the legacy names of core keys; P4d step 1 over the config store from P4c (books' `LegacyStores.Config` for ABO) in the order the store's `Open` (ABO's key guard and encryption init; the settings load at S2b) → `secrets.Open` with its legacy secret copies (keyring, `k0-legacy`, the R4-2 re-import of moved secrets) → `Store.SetSecrets` → `Store.Attach` (COR5-2, R4-6; no config import step, R3-1); HEAD's precedence for the app's legacy keys and the four already-encrypted secrets left in place (R5-1); `Changed` as the optional `config.ExternalWriter` (SIMP5-5); `secrets.RetireUnused` in P5 (COR3-2); `config.Options.EnvPrefix` from `app.Options.EnvPrefix`; the pre-boot key `core.paths.state_dir` (no `data_dir`), resolved once by `config.ResolveStateDir` with HEAD's fallback (R3-4); `secrets.Scope` (`modules.<id>.`) | 05-config hands modules `Registrar.Config()` with "registration legal only before Store.Load"; since `Register` needs loaded config, sections are listed in `Manifest.Settings` and registered by the kernel before load; `module.Setting(r, s)` returns the bound handle |
| 05-metadata M1–M2 | `Registrar.Adapter(integrations.Descriptor)`, `app.Options.Adapters`, `Deps.Integrations *integrations.Registry` (the one instance store, SIMP-4), `Deps.HTTP *providerhttp.Registry`; `LegacyStores.Integrations` (`integrations.InstanceStore`) and `.Gates` | adopted as proposed; the registry's load-time secret re-import is P4d step 1b (R4-2) |
| 05-ai | `Registrar.AICapability(ai.CapabilitySpec) ai.Capability`, `ai.BatchApplyTask(cap, h, effects)` registered with `Registrar.Tasks`, `Deps.AI *ai.Router` | signatures as in 05-ai; `Registrar.AIBatchHandler` removed (COR2-11) |
| 05-download | `Deps.Downloads *download.Pool`; `DownloadWatch` deferred-build | grab/monitor are the deferred acquire build (SIMP-12, COR-3) |
| 05-health | `Health(...health.Check)`, `StartupSteps(...health.StartupStep)`, `Diagnostics(health.Contributor)` (S8); `Registrar.Health` and `Deps.Health health.Reporters` from S5 with the readiness core (COR4-8); `Deps.ExtendStart` and the stateless `app.ExtendStart`; `core.shutdown.budget` and its phase slices (§5H.4) | `core.lifecycle.stop_timeout`/`shutdown_budget` removed from 06 (OPS-13); `internal/startup` stays in ABO, used by `cmd` until S2b (MOD3-1, MOD4-17) |
| 05-updater | `app.Options.Updater *updater.Options`; `updater.Boot` as phase P−1; `app.BinaryID` as `Self.Build` and the marker's `to_build` | replaces 1.0.0's `Release updater.Source`; `Boot` resolves paths from flags, env and its own marker, decides rollback before P0a, and counts P0 refusals (OPS2-3); a boot refusal under a marker naming the running binary exits with its `restart_exit_code` (75), not 78 (OPS5-15); identity is the binary ID, never the UI build ID (R5-7) |
| 05-download, 05-indexer, 10 (v1.1.0) | `Deps.Downloads *download.Pool`; deferred `Deps.Indexers *indexer.Pool`, `Deps.Acquire *acquire.Service`, `Manifest.Acquire *AcquireDecl` (§6.4.9) | names as those files state them |
| 05-library | `Manifest.Library []library.RoleRequirement`; `Deps.Library library.Scope` | — |
| 05-authn §3.3, §5 | `app.Options.Authenticators []auth.Extension`; `Registrar.AuthMethod(auth.MethodSpec)`; `Deps.Auth`, `Deps.AuthAdmin` | `auth.Extension{Authenticator, Spec, Order}` requested from 05-authn; `auth.MethodSpec` is an S2 leaf (SEC3-14); `auth.BootWrites` is P4d step 4 and also mints the bootstrap hash and the optional start-up key (DATA3-18, COR4-13); `auth.Store` has a native implementation (`c/auth/`) and books' legacy one (`LegacyStores.Auth`); compat methods reach `auth` at P7, not P5 (R4-1, COR4-13) |
| 05-backup | `Registrar.Backup(...backup.Artifact)`; `Deps.Backup backup.Ensurer`; pre-boot restore phase P0a (the kernel's journaled swap from S2b, ABO staging only until S8, DATA4-6); revocation re-apply (step 2, `head_before` read before any append, SEC4-5) and post-restore hooks (step 3, also on a replaced-store boot) in P4d; `core.backup.dir` declared from S4 by the kernel's core-section list and `backup/journal`'s `c/backup/` spec registered from S4 (COR4-10, COR5-6); `StagedByBuild`/`Source` carry the binary ID (R5-7) | 05-backup's "module.Manifest.Backup" comment changes to `Registrar.Backup` (hooks close over services); P0a runs after P−1 and before P0 (DATA-23, OPS2-3), trusts the absolute targets in `pending.json` (DATA2-25) and keeps `inflight.json` until `applied` (DATA3-6); the journal is read after the keyring and overrides (COR3-2, DATA3-5) |
| 05-logging, 05-metrics | `Deps.Log *slog.Logger`, `Deps.Metrics *metrics.Scope`; `Registrar.LogPathRules(...log.PathRule)`, `Registrar.LogOtherUserData(log.OtherUserPredicate)`; `app.Options.MetricAliases []metrics.Alias`; `app.Options.PromRegisterer` (S2 r3–S8, OPS5-14); `LegacyStores.Logs` (`log.JournalStore`, which commits its own batches: the stated exception to the caller's-batch rule, MOD5-10) | the module path-rule and other-user-data registrations 05-logging §3, §5 point at; metric aliases supplied by the app with ledger rows (SIMP3-3); the App exporter registers on the app's registerer until S8, then the kernel mounts `/metrics` (MOD4-3) |
| 07 §7.4–§7.6 | `UIDescriptor` (all shell slots, `LegacyRedirects`), `app.Options.UI ui.Bundle` and `UIOptions ui.AppOptions` (S9), the S2 `ui` leaf (`ReservedSegments`, `ModuleManifest`, `UIIDs`, `Redirect`, FE4-6) | `/modules` (`internal/modulehttp`) and `internal/uihttp` routes ship at S4 (FE2-25, FE3-16); P8 checks shell segments against `NonSPA` (FE2-7, FE3-1) and passes `ui.BasicExemptAssets` to `Router.SPA` (FE5-10); no build-ID check (FE4-1); `ui_route` entries registered by the kernel at P7 from `LegacyRedirects` (FE4-5) |
| 08, 10, 05-indexer | §6.4.9 deferred-build members | — |

## Objections to 03

None open. 03 v1.6.0 §3.6 states the compat inputs as 06 §6.4.1 does (FE3-1, R3-5): `LegacyPrefixes`
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
