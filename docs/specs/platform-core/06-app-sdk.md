<!-- file: docs/specs/platform-core/06-app-sdk.md -->
<!-- version: 1.3.0 -->
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
| A1 | A module is `Manifest()` (pure data, validated before storage opens) **plus** `Register(*Registrar)` (runs after migrations and config load, receives typed `Deps`). **Rule: anything the kernel needs before services exist, or while the module is disabled, lives in the `Manifest`; anything that needs a service lives in `Register`.** So permissions/roles, storage spec (keyspace, legacy claims, migrations, the declarative part of derived indexes), library roles, settings sections, its outbox consumer names, its task-ID ledger, the foreign task kinds its system actor may start, legacy HTTP prefixes and UI descriptor IDs are manifest fields. Routes, tasks, resources, topics (the one catalog, SIMP-5), subscriptions, outbox handlers, change sinks, adapters, health/diagnostics, backup artifacts, derived-index hooks, compat auth methods, hooks and workers are registrar calls: each closes over a service built in `Register` (COR2-15). | 03 §3.7 requires validation before listeners, migrations before services, and 05-config requires sections to be registered before the config store loads, while route handlers need migrated storage and loaded config. One `Register` cannot satisfy both. Outbox consumer names and the task-ID ledger are manifest data so that a disabled module's messages are retained (COR-12) and its persisted runs resolve as held (COR-13). Cost: two places to look; mitigated by the rule being mechanical. |
| A2 | **No generic DI container in core.** Core's own ~30 services are built by hand-written, typed code in `kernel/services` (a struct with one field per service, constructed in a fixed order). A module builds its internal services with plain Go constructors inside `Register`. | Evaluated below (§6.3.1). |
| A3 | `Deps` is a **struct of typed, module-scoped handles**, never `any`, never nil. Each handle's **type** carries the module identity, so scoping is enforced by the owning package and cannot be spoofed by the caller: `events.Scope` publishes only the module's declared topics, `library.Scope` carries the module's owner, `tasks.Client` stamps the actor from ctx, `secrets.Scope` reads only `modules.<id>.` names, `*metrics.Scope` prefixes the module's metric names (03 §3.6 `MetricPrefix`, §6.4.4). A shared pointer to a whole service never appears in `Deps`. | Replaces 113 `Get[T]` string lookups with compile-time types; a scope type is the only place R3/R8 can be enforced for a handle (COR-21). |
| A4 | Registrar methods **record problems instead of returning errors** (like `testing.T`); the kernel joins every problem of every module into one `*ValidationError`. | An operator sees all misconfigurations at once, not one per restart. |
| A5 | Module code runs only through the kernel: no `init()`, no package-level mutable state, background goroutines only via `Registrar.Worker`. A disabled module's `Register`, migrations, hooks and workers are **never invoked**. | This is what makes G1 a structural property rather than a per-feature check. Enforced by `archtest.NoInit` + `archtest.TestNoGlobals` over module packages, and by `coretest.AssertModuleInert`. |
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
// ui.ReservedSegments (shell routes, 07 §7.4.3) and rbac.ReservedNamespaces
// ("core", "custom") (03 §3.6). It is static, so P1 can evaluate it. Two
// app-specific inputs are added by the kernel in P1, both known before any
// MountLegacy call (COR2-19, FE2-11):
//   - the first path segment of every manifest's LegacyPrefixes: the compat
//     top-level segments (ABO: auth, socket.io, ping, status, logout,
//     public). They are also NonSPA segments; a LegacyMount's
//     TopSegments (05-http-api §8.2) must be a subset of them (checked in P7);
//   - app.Options.ReservedAPINamespaces: second segments under /api (ABO: the
//     ABS unversioned /api/<seg> names); reserved IDs only, never NonSPA.
// A fresh slice per call (R5); a test asserts the static union equals 03's row.
func ReservedIDs() []string

// Module is what an app hands to app.New. Implementations are plain values
// constructed by the app's main (e.g. books.New(), hello.New()); they must not
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
	// (pre-split) or to the tag being released (post-split). Empty = no check.
	MinCoreVersion string

	// DefaultEnabled is the value of core.modules.<id>.enabled when unset.
	DefaultEnabled bool

	// Requires lists integration kinds the module needs at runtime.
	// Required=false entries only drive the settings UI and health text.
	Requires []CapabilityRequirement

	// RBAC declarations, types owned by 05-rbac §3.2: permissions in this
	// module's namespace (each with its DefaultGrants into core seed roles
	// "editor"/"viewer" or the module's own roles), the module's own seed roles
	// ("<ID>.<name>", 05-rbac §3.3) and legacy permission-string aliases. P1
	// checks only their namespaces; the kernel feeds them to rbac's Declarer in
	// P5, after rbac.New has its c/rbac/ handle (COR2-7).
	Permissions   []rbac.PermissionSpec
	Roles         []rbac.RoleSeed
	LegacyAliases []rbac.LegacyAlias

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
	// included (05-tasks 5T.3.10). P7 refuses a kind that no enabled module or
	// core registers (ProblemTaskStartUnknown); a kind of a disabled module is
	// refused at Start with tasks' "unknown kind" (O3).
	TaskStarts []tasks.Kind

	// OutboxConsumers names every outbox consumer the module may register
	// ("<ID>.<name>"). They are known while the module is disabled, so the
	// relay keeps retaining messages for them (05-events, COR-12); an
	// OutboxHandler whose consumer name is not listed here is a problem.
	OutboxConsumers []string

	// Storage is the module's storage declaration (05-storage §5S.3): its
	// m/<ID>/ keyspace, legacy claims (books: ABO's 258 families), versioned
	// migrations (run before Register, under the boot's single pre-migration
	// checkpoint, G2), read adapters, and the DECLARATIVE part of each derived
	// index (name, version, Blocking), so the claim ledger and readiness gating
	// know the index while the module is disabled. The Build/Snapshot/Load
	// closures are registrar data (Registrar.DerivedIndex), because they close
	// over services built in Register (COR2-15).
	Storage storage.Spec

	// DataMigrations are the module's large, resumable rewrites
	// (05-storage §5S.3.6). They live outside storage.Spec because
	// storage/datamigrate (L4) imports tasks and storage (L1) cannot.
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
	// or exact paths ("/ping") the module occupies (09); each becomes a claim,
	// and its first segment is a reserved module ID and a NonSPA segment known
	// in P1 (§6.4.1, FE2-11). httpapi.Router.Build refuses any two
	// owners whose subtree claims overlap, which also covers facade vs legacy
	// (05-http-api §4.9, narrowed by SIMP-22: no claim kinds, no YieldsTo).
	// Only paths listed here may be passed to MountLegacy. "/" is not a
	// subtree claim: it is the SPA fallback, owned either by ui.Handler or by
	// exactly one LegacyMount with ServesSPA (§6.4.3, FE-1).
	LegacyPrefixes []string

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
}

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
//     route in a golden (TestOwnAuthRoutesGolden, 05-http-api §8.2).
func (r *Registrar) MountLegacy(m httpapi.LegacyMount)

// AuthMethod registers a compat authentication method of this module, named
// "<ID>.<name>" (books: books.abs, 05-authn §3.3 AddMethod). The kernel
// refuses (ProblemAuthMethod) any spec that is Interactive, sets
// MayChangeCredentials, or whose authenticator implements OwnerVerifier: only
// app.Options.Authenticators may carry those (SEC2-16). Consumed in P7.
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
// journal and undo, opchange:); tasks.Writer records every gated write's
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
// topics with Realtime metadata are streamed without any call here. The
// registrar has two methods:
//   - Redact(topic, fn) attaches a per-subscriber redactor to one of the
//     module's OWN Realtime topics. Redaction of a core topic (e.g. the
//     current-item label of core.task.run-changed.v1) is a property of that
//     core topic and its read model (05-tasks), never a module redactor
//     (COR2-2);
//   - Projection(p realtime.LegacyProjection) http.Handler returns a lazy
//     handler bound to the hub, which P5 has built; the hub's catalog
//     subscriptions are attached in P7, and the handler answers 503 until the
//     hub starts (Run step 1). Books serves it from its gin manifest at
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
// task kind, built with ai.BatchApplyTask(cap, h, effects) and registered with
// Tasks; core.ai.batch-poll starts it through Client.Start instead of calling
// module code inline, so the writes run under the module's Effects (COR2-11).

// Health, startup steps and diagnostics (05-health).
func (r *Registrar) Health(c ...health.Check)
func (r *Registrar) StartupSteps(s ...health.StartupStep)
func (r *Registrar) Diagnostics(c health.Contributor)

// Backup declares module-owned artifacts outside storage (05-backup). They are
// registrar calls, not manifest data, because their Snapshot/Restore hooks
// close over services built in Register.
func (r *Registrar) Backup(a ...backup.Artifact)

// DerivedIndex supplies the hooks (Build, Snapshot, Load) of a derived index
// whose declarative part is in Manifest.Storage (COR2-15). The kernel runs the
// version/generation check and Load at the end of P7, after every module has
// registered; Build runs as the MODULE task kind "<ID>.derived.<name>.build",
// which this call registers on the module's behalf with the module's own
// Effects, so a rebuild never runs module code under a core kind (COR2-11).
// A declared index without hooks, or hooks for an undeclared index, records
// ProblemDerivedIndex.
func (r *Registrar) DerivedIndex(name string, h storage.DerivedHooks)

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
	Metrics *metrics.Scope // module series under app.Options.MetricPrefix on the App's registry, never the global one (05-metrics, 03 §3.6)

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
// §5S.3.4, COR-2). Core subsystems receive the same values from the kernel.
type StorageHandles struct {
	Keyspace storage.Keyspace           // m/<ID>/
	Legacy   storage.LegacyRange        // zero value unless the manifest declared claims
	Indexes  index.Opener               // full-text indexes the module declared (05-storage §5S.3.8)
	Aux      storage.AuxOpener          // auxiliary DBs under <data-dir>/aux/<ID>/ (§5S.3.10)
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
| `Metrics` | `*metrics.Scope` | 05-metrics §7 (defined) | module series named `<MetricPrefix>_*` while the app **ships** one module (the module list passed to `app.New`), `<MetricPrefix>_<ID>_*` with two or more; enablement never changes a series name, so disabling one module of two does not rename the other's (SIMP2-13); `MetricPrefix` is `app.Options.MetricPrefix` (03 §3.6) |
| `Storage` | `module.StorageHandles` | this file (above); field types 05-storage §5S.3.4, §5S.3.8–§5S.3.10 (defined) | reads and writes outside `m/<ID>/` and the module's claims refused (R8) |
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

	// EnvPrefix is the prefix of every env name (05-config §1.3,
	// [A-Z][A-Z0-9]{1,15}); default strings.ToUpper(AppID) (ABO: "ABO") (COR2-8).
	EnvPrefix string
	// StateDir is the compiled default of core.paths.state_dir (05-config).
	// core.paths.state_dir and core.paths.data_dir are PRE-BOOT keys: P−1 and
	// P0a resolve them from flags, env and this default only, never from the
	// config file or the DB, so a config that P0 refuses cannot hide a staged
	// update or restore (§6.5.1, OPS2-3). The state directory holds state
	// files only (boot record, keyring, updates/, restore journal); the store
	// opens at core.storage.path, which may be elsewhere (DATA2-19).
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

	// Authenticators are the app's identity extensions (05-authn §3.3;
	// ABO: internal/cfaccess), wired by the kernel with
	// auth.Registrar.AddAuthenticator. This compile-time list is the ONLY
	// place a MethodSpec may be Interactive or set MayChangeCredentials, and
	// the only place an OwnerVerifier may come from; modules register compat
	// methods with Registrar.AuthMethod, which refuses all three (SEC2-16).
	Authenticators []auth.Extension // {Authenticator, Spec auth.MethodSpec, Order}

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
`app` (MOD2-4). Its `serve` command replaces its store, OTel and slog bootstrap with calls to `app.New`,
`app.InstallProcessDefaults` (05-logging) and `Run`, and its flags map onto `app.Options` (§6.11).
`core.json` has a single producer so that apps cannot emit diverging copies: hello's `openapi`
subcommand writes `Description.Core` to `core/web/packages/api-client/openapi/core.json`,
freshness-gated in core CI. It is producible from S2, when the hello skeleton lands (§6.11.1), and a
PR that adds a core route regenerates it in the same PR (MOD2-10, FE2-9). An app's subcommand writes
only its modules' documents (07 §7.8). Books meets `Register`'s side-effect rule only once its server
construction (`server.NewServer`, which loads the HNSW snapshot) has moved into start hooks
(§6.11.1 step 4); until then ABO's `openapi` subcommand does not call `Describe` over books and emits
no books document. A books route group becomes native only when rewritten for its own reasons
(09 §9.2, 05-http-api §8.4), and the first such rewrite carries that move.

### 6.4.6 `kernel` (shape only; apps never import it)

| Package | Responsibility |
|---|---|
| `kernel/catalog` | manifest validation, enablement resolution (P1, from `config.Store.Boot`), collision checks across modules and core (IDs against `module.ReservedIDs()`, `Options.ReservedAPINamespaces` and the first segments of every `LegacyPrefixes` entry; permission namespaces, legacy atoms, keyspace and legacy claims, config sections, task kinds, topic names, outbox consumer names, legacy path prefixes, UI IDs; facade URL bases when `arrfacade` is built), `MinCoreVersion` against `version.Version` |
| `kernel/services` | `type Services struct{ Storage *storage.DB; Tasks tasks.Executor; … }` built by `func Build(ctx, opts, cfg) (*Services, error)` in a fixed, hand-written order; `Close` in reverse |
| `kernel/supervisor` | start/stop ordering of §6.5.2, hook start/stop, worker supervision (`sync.WaitGroup.Go`), panic recovery, timeouts, shutdown budget |
| `kernel` | `Kernel` = catalog + services + supervisor; `Registrar`/`Deps` construction per module (the scope types of §6.4.4); hands registrations to services in P7; mounts core capability routes |

Core capability routes (`/api/core/v1/tasks`, `/roles`, `/audit`, `/events`, `/outbox`, …) live in
the per-capability route packages `internal/<capability>http` of 03 §3.3 (e.g. `internal/taskhttp`,
`internal/rbachttp`, `internal/audithttp`) at R4 L6. Each imports its capability and `httpapi`, and
is wired onto `Router.CoreGroup()` only by the kernel (COR-18). The module catalog's own
route, `GET /api/core/v1/modules` (07 §7.5.1), is one of them (`kernel/catalog` supplies the data,
the route package renders it). `POST /api/core/v1/ui/diagnostics` and `POST /api/core/v1/ui/csp-report`
live in `internal/uihttp`, a capability route package like the others, so the `CoreGroup` owner rule
("used only by `internal/<capability>http`", 05-http-api §3) holds without an exception for `ui`, and
the `ui` package (S9) registers no route (COR2-19). The module catalog route and `internal/uihttp`
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
	ProblemFacadeIncomplete       // deferred-build (08)
	ProblemFacadeURLBaseCollision // deferred-build (08)
	ProblemUnknownModuleInConfig
	ProblemRegistrarClosed
	ProblemWorkerPolicy
	ProblemAdapter
	ProblemChangeSinkDuplicate
	ProblemTaskStartUnknown       // Manifest.TaskStarts names a kind no enabled module or core registers (SEC2-15)
	ProblemAuthMethod             // AuthMethod spec interactive, credential-changing, owner-verifying, or not "<ID>.<name>" (SEC2-16)
	ProblemLegacyAuth             // LegacyAuthGin by a module other than Options.LegacyAuthGinUntil, or OwnAuth without a registered compat method (SEC2-17)
	ProblemDerivedIndex           // derived index declared without hooks, or hooks for an undeclared index (COR2-15)
	ProblemSPASegmentCollision    // a ui.ReservedSegments() entry is also a NonSPA segment (P8, FE2-7)
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

`coretest/fakes` provides in-memory implementations of every built capability contract (download,
metadata, ai, notify; indexer once built) so module tests never need a network. Time-dependent tests run inside
`synctest.Test(t, func(t *testing.T) { h := coretest.New(t, …) … })`; the harness starts no real
timers outside the bubble.

### 6.4.9 Deferred-build members

Added additively in the build that creates the package (03 §3.3 "public, later"; cross-cutting
resolution 1). Until then the `module` package does not import these packages, and no core v1
symbol refers to them.

| Member | Package | Purpose |
|---|---|---|
| `Manifest.Acquire *AcquireDecl{Vocabulary, Categories}` | `acquire` (10) | manifest trigger for building the acquire service in P5 (A8) |
| `Deps.Acquire *acquire.Service` | `acquire` (10) | `acquire.Register[T](d.Acquire, mediaType)` in `Register` attaches typed hooks only |
| `Deps.Indexers *indexer.Pool` | `integrations/indexer` (05-indexer) | `SearchAll`, `RSS`, `Fetch`, `Caps` for module search tasks |
| `Registrar.ArrFacade(arrfacade.Provider)` | `arrfacade` (08) | at most one per module; checks in P7 (`ProblemFacadeIncomplete`, `ProblemFacadeURLBaseCollision`) |
| `Registrar.DownloadWatch(...download.Watch)` | `integrations/download` monitor (05-download phase 2) | the grab/monitor pipeline, built with `acquire`; ABO does not use it (SIMP-12) |

Facade-only members of other built packages are deferred the same way: `auth.KeySpec.Purpose`/`Module`,
`PurposeFacade`, query-string key extraction, `httpapi.Router.FacadeGroup`, `RouteKind Facade` and the
stage-13 facade clause land with `arrfacade` (08 §8.0, SIMP2-6), so S2 and S4 do not build them.

### 6.4.10 Landing stage of each SDK member (COR2-9, MOD2-10)

A member exists from the 09 stage that lands the package it names (04 §8.1); before that it is
absent, not stubbed, and the kernel phase that consumes it is skipped (§6.5.1). Each later member is
added additively. `rbac`'s leaf types (`Permission`, `Requirement`, `Need`, `AnyAuthenticated`,
`Public`) land at S2 so `httpapi.Route.Perm` compiles there (COR2-9).

| Stage | `module.Manifest` | `module.Registrar` | `module.Deps` | `app` |
|---|---|---|---|---|
| S2 | `ID`, `Version`, `DisplayName`, `MinCoreVersion`, `DefaultEnabled`, `Settings`, `UI` (IDs only), `LegacyPrefixes` | `Context`, `Deps`, `HTTP`, `MountLegacy`, `Hook`, `Worker`, `Err` | `Module`, `Log`, `Metrics` | `Options` (all but `UI`, `Authenticators`), `New`, `Run`, `Handler`, `Modules`, `Describe` (`Core`, `OpenAPI`) |
| S2b | `Storage` | — | `Storage` (`Keyspace`, `Legacy`, `Aux`) | — |
| S3 | — | — | `Secrets` | — |
| S4 | `Permissions`, `Roles`, `LegacyAliases` | `AuthMethod` | `Auth`, `AuthAdmin`, `Audit` | `Options.Authenticators`; `LegacyAuthGinUntil` cleared |
| S5 | `TaskLedger`, `TaskStarts`, `DataMigrations` | `Tasks`, `Resources`, `Lanes`, `ChangeSink`, `Topics`, `Subscribe` | `Tasks`, `Events` | — |
| S6 | `OutboxConsumers` | `OutboxHandler`, `Realtime` | `Outbox`, `Notify` | — |
| S7 | `Requires`, `Library` | `Adapter`, `AICapability`, `DerivedIndex` | `Storage.Indexes`, `Storage.Derived`, `Library`, `HTTP`, `Integrations`, `Downloads`, `AI` | `Options.Adapters` |
| S8 | — | `Health`, `StartupSteps`, `Diagnostics`, `Backup` | `Health`, `Backup` | `Options.Updater` |
| S9 | — | — | — | `Options.UI`, `Description.UIManifests` |

`examples/hello` grows with the same stages (MOD2-10): S2 skeleton (module, `app`, a greeting route
without the stored count and with `Perm: rbac.Public` until core authentication exists, and the
`openapi` subcommand emitting `core.json`), storage and migration 1 at S2b, its permissions and the
route's `rbac.Need(PermRead)` at S4, `hello.count` at S5, and `ui_embed.go`/`ui_nonembed.go` with its web package at S9. §6.12 shows the S9 state. G6
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
buffers. Every every-boot writer (mirror rebuilds, revocation re-apply, config import, tasks
reconcile, post-restore hooks, compat usage records, seeded grants) runs after the checkpoint and the
migration DAG, in the order of P4d, and before any listener (DATA2-24).

| Phase | Step | Store writes | On failure |
|---|---|---|---|
| P−1 | `updater.Boot` (05-updater §5U.3), only when `Options.Updater` is set. Resolves the state directory from flags, env and `Options.StateDir` (pre-boot keys, never the config file or the DB, so a config that P0 refuses cannot stop it) and reads `<state_dir>/updates/pending.json`. The DB path `ReaderCheck` needs is the absolute path recorded in that marker by the build that applied the update. It counts this start as a boot attempt and decides rollback **before** any restore swap: when it restores `.prev`, a restore intent staged by the failing build is then rejected as foreign by 05-backup step a (`ReaderCheck` ignores `restore_pending` for such an intent, 05-updater). Every later failure in this table, P0 refusals included, counts as a failed boot (OPS2-3) | none (`updates/pending.json` only) | per 05-updater: roll back to `.prev`, or keep the binary with `rollback_blocked` |
| P0a | Pre-boot restore swap (05-backup §5B.3.4): if `<data_dir>/restore/pending.json` exists, swap using the **absolute targets recorded in it**, with the state and data directories resolved as in P−1 (DATA2-25). P2 verifies that storage opens at the recorded DB path; if not, the swap is undone | none (journal and renames only) | per 05-backup: swap back; boot degraded (`restore_failed`, `restore_rejected`) |
| P0 | `config.NewStore` with `EnvPrefix = Options.EnvPrefix`; register core sections; `Store.Boot` resolves core sections from file → env → flags and the boot record `<state_dir>/config.boot.json` (05-config §1.3.5, §1.4). A config file that sets a pre-boot key (`core.paths.state_dir`, `core.paths.data_dir`) is refused. Alias uses seen here (config and env window aliases, 05-config `OnLegacyUse`) are buffered in memory and recorded at P4d (OPS2-7) | none (an unparsable boot record is renamed `.bad`, fail-open, per 05-config) | fail-closed |
| P1 | `Manifest()` of **every** module, enabled or not; `kernel/catalog` validates IDs against `module.ReservedIDs()`, `Options.ReservedAPINamespaces` and the first segment of every manifest's `LegacyPrefixes`; `MinCoreVersion` against `version.Version`; the namespaces of permissions, settings, task kinds, outbox consumer names and the task-ID ledger; cross-module and core collisions (a disabled module's names and claims stay reserved, so every manifest is checked). Static checks only: no rbac call, because `rbac` has no store yet (COR2-7). Resolve enablement from the `BootView`: `core.modules.<id>.enabled`, default `DefaultEnabled` (§6.5.3) | none | all problems joined → `*ValidationError` |
| P2 | Open storage at `core.storage.path` resolved from the `BootView` (05-storage §5S.4 step 1: absolute; `os.OpenRoot` on the DB's parent directory, which need not be under the state directory, DATA2-19, COR2-8). Store guard: per-owner reader levels and the fast-exit flag (05-storage §5S.3.7). Spec validation: keyspace and legacy claims against the persisted claim records `c/storage/claims/<owner>` (DATA-28); a claim on disk but absent from the build stays orphaned until a declared claim sets `Claim.TakesOver` (05-storage rule 5), never silently re-owned (DATA2-29). P0a's recorded-path check | none (read only) | fail-closed |
| P3 | Register the settings sections of every **enabled** module, then `Store.Load` from the operator layers and the boot record **only**; the DB override layer is not read yet (it is attached at P4d). `Load` seals the registry. A disabled module's sections are not registered: its stored values are retained, unvalidated, until it is re-enabled | none | fail-closed (an invalid operator-layer value) |
| P4 | In this order. **(a)** One pre-migration checkpoint for the boot (05-storage §5S.3.12); if it cannot be taken, boot halts before anything is written. An **empty** store is instead stamped as a fresh install per 05-storage §5S.4 step 5: install marker, every owner at the build's level, adopt and contract steps recorded as applied, so mirrors are never active (DATA2-28). **(b)** The cross-keyspace migration DAG (05-storage): every pending core and **enabled-module** step in DAG order (steps may declare `After` other owners' versions, e.g. `core/auth#1` after `books#66`, DATA-9). Books legacy migrations run first, under 05-storage's explicit, logged legacy-migration grant, before any claim-mode level step enforces core claims (R2-4); claim-mode changes are stamped level steps of the one guard (R2-3); a step whose preconditions do not hold is deferred (05-storage §5S.3.5). `BootBlocking` data migrations run in process through `tasks.ForEach` with persisted shard cursors, not as executor runs; only `Online` ones are `core.storage.migrate` runs, started after Run step 5 (COR2-7). **(c)** Storage issues the core subsystems' keyspace handles. **(d)** The every-boot reconcilers, each a function of its owning package over the handle from (c), with no service built yet, in this fixed order: 1. mirror rebuilds of adopted identity families, run by the kernel (05-storage §5S.3.11, 05-authn §5); 2. revocation-journal tail re-apply (05-backup), whose revocations write the legacy row in the same batch as the native one (R2-2); 3. `secrets.Open`, then the configstore import or re-import (05-config §1.12.2), then `AttachOverrides`: DB overrides become visible and an enabled module's settings are validated against them; 4. tasks reconcile and clock adoption (05-tasks); 5. post-restore hooks (05-backup `RestoreHook`); 6. the alias uses buffered in P0 are recorded in the compat usage ledger (09 §9.2.9) | yes, from (a) on | fail-closed. A failed migration step leaves earlier steps committed and the checkpoint **unconsumed** for the offline `storage restore-checkpoint`; there is no automatic restore (DATA2-19, 05-storage §5S.3.5). A failing reconciler or settings validation fails per its owning file |
| P5 | `kernel/services.Build`: every core service in fixed order (A8), including the services behind every `Deps` handle and the realtime hub; `rbac.New` (store `c/rbac/`) and then `Declarer` per manifest, disabled ones included so their permissions stay known (§6.5.3, COR2-7); `auth` with `Options.Authenticators`; `notify` before `updater`, whose update notifications go through it; adapters from `Options.Adapters` (module adapters arrive in P6 and are instantiated lazily on first lookup) | none | fail-closed |
| P6 | For each enabled module in ID order: build its `Registrar` and `Deps`, call `Register(r)`, collect `r.Err()`, close the registrar. Registrations are only recorded; `Register` writes nothing outside its handles (§6.4.1) | none | all problems joined |
| P7 | Consume registrations (A8): task definitions to the executor (R7 checks, schedule keys declared under the owner's namespace, R2-7; kinds in `TaskLedger`; `TaskStarts` kinds exist), topics to the catalog (05-events validation), subscriptions to the bus, outbox handlers to the relay (topics declared `Durable`, consumer names declared), redactors and legacy projections to the realtime hub, change sinks to the task writer, adapters to the integrations registry, compat auth methods to `auth` (non-interactive only), `LegacyAuthGin` and `OwnAuth` checks (SEC2-17), checks to health. Seed default grants for permissions new to this boot. Then the derived-index version/generation check and `Load` (COR2-15). Post-checks: R6 over the built router, required capabilities configured, every `LegacyMount.TopSegments` entry is a first segment of a declared `LegacyPrefixes` entry | seeded grants; derived generation records | joined |
| P8 | Build the root `http.Handler`: core capability routes, module routes, legacy mounts, the `/` owner (either `ui.Handler` or the `ServesSPA` legacy mount, FE-1). Check that no `ui.ReservedSegments()` entry is also a `Router.NonSPA()` segment (`ProblemSPASegmentCollision`, FE2-7) | none | fail-closed |

`New` stops core services' goroutines and closes storage and everything opened so far, in reverse
order, on any failure.

**Stages (COR2-9).** A row whose package has not landed (§6.4.10) is absent, not stubbed. Until S2b
the kernel runs with `storage` absent: P2 and P4 are skipped, `Deps.Storage` does not exist, and books
opens its store and runs its own migrations in a start hook exactly as HEAD does, so there is never a
second Pebble open. Until S3, P4d step 3 does not exist and `config` loads over the books-provided
legacy source (04 §8.1); until S4, P5 has no `rbac` and P7 seeds no grants; until S8, P−1 is the
hotfixed HEAD updater's own check (09 S0).

### 6.5.2 Run and shutdown

This section is the **one** statement of start/stop order. 05-events and 05-tasks reference it
instead of restating it (COR-8).

Start order: (1) core background services (metrics, realtime hub); (2) module hooks, modules in ID
order, hooks of one module in registration order; (3) module workers; (3a) the core components that
invoke module callbacks: the storage index reconciler and derived-index builder (05-storage
§5S.3.8–§5S.3.9), the health runner of module checks, and `OnChange` delivery for module settings
sections (COR2-15); (4) `Bus.Open`: bus delivery to the subscriptions frozen in P6 begins; (5) task
executor (resume sweep); `Online` data migrations start here; (6) scheduler, whose `OnStart`
schedules start the outbox relay task `core.outbox.relay`, so outbox delivery begins here;
(7) listeners; (8) readiness true. No bus handler, outbox consumer or other module callback can run
before its module's hooks have started, and the resume sweep's `core.task.*` events are delivered.

If any hook's `Start` fails or exceeds `core.lifecycle.start_timeout`, every already-started hook is
stopped in reverse order (the semantics of `serviceregistry.Container.Start` today, `container.go:164`),
and `Run` returns the joined error.

Stop order on ctx cancel or `StopAppOnExit`: readiness false → listeners `Shutdown` (bounded) →
scheduler stop → executor drain (05-tasks bounded drain; the relay run is cancelled with
`ErrShutdown` and acks nothing further) → `Bus.Close` with `core.events.close_drain` → stop the
components of start step (3a) → cancel workers' ctx and wait → module hooks `Stop` in exact reverse
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
  data is retained); migrations, `Register`, hooks, workers **not invoked**; no route, task kind,
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
| `Manifest.TaskStarts` names an unregistered kind | fail-closed (`ProblemTaskStartUnknown`) | the system actor's permission set must be reviewable (SEC2-15) |
| `AuthMethod` interactive, credential-changing or owner-verifying; `LegacyAuthGin` by a module other than `Options.LegacyAuthGinUntil`; `OwnAuth` route without a registered compat method | fail-closed (`ProblemAuthMethod`, `ProblemLegacyAuth`) | a module must not decide its own authentication strength (SEC2-16, SEC2-17) |
| Derived index declared without hooks, or hooks without a declaration | fail-closed (`ProblemDerivedIndex`) | readiness gating would wait forever, or an index would be unknown while disabled (COR2-15) |
| A shell route segment is also a `NonSPA` segment | fail-closed at P8 (`ProblemSPASegmentCollision`) | a full page load of that shell page would get a 404 problem+json (FE2-7) |
| Settings value invalid for an **enabled** module | fail-closed | module would run with undefined config |
| Stored settings of a **disabled** module | not validated (section not registered, 05-config §1.4 step 3); retained | the module does not run; validated when it is re-enabled |
| Migration error | fail-closed; earlier steps stay committed and the checkpoint stays unconsumed for the offline `storage restore-checkpoint`; no automatic restore (DATA2-19) | G2; an automatic restore would fight the crash-loop reuse rule and non-atomic legacy steps (05-storage §5S.3.5) |
| Any failure from P0 on, after an auto-applied update | counted as a failed boot by `updater.Boot` (P−1), which rolls back once the attempt budget is spent | config refusals must reach the updater's rollback (OPS2-3) |
| Config file sets `core.paths.state_dir` or `core.paths.data_dir` | fail-closed at P0, naming the flag and env name to use | P−1 and P0a read these before any config file, so a file value would be silently ignored by them |
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
| `mediacore_kernel_boot_phase_seconds` | gauge, set once per boot | `phase` (`P-1`, `P0a`, `P0`…`P8`: the §6.5.1 phases in ASCII), `step` (the bounded set of migration, mirror, adoption and reconciler step names inside P4, e.g. `core.tasks.adopt-active`; `""` for the phase total) (OPS2-24) |
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
| `TestBootPhaseOrder_NoWriteBeforeCheckpoint` | R2-8: an instrumented store records the first write of a boot with a configstore re-import, a mirror rebuild, a revocation re-apply and buffered alias uses pending; it follows the checkpoint, and the reconcilers run in the P4d order |
| `TestBootRefusalCountsAsFailedBoot` | OPS2-3: with an update pending, a config refused in P0 increments the updater's attempt counter, and the budget's last attempt restores `.prev` before any restore swap |
| `TestPreBootKeyInConfigFileRefused` | `core.paths.state_dir` in the config file fails P0; the same value by env boots |
| `TestDescribeInMemoryNoIO` | COR2-13: `Describe` over hello runs P0–P7 on `vfs.NewMem` with a temp state directory, touches no other path, and returns the same OpenAPI document as a live boot |
| `TestStartOrder`, `TestStopOrder` | COR-8: a bus handler and an outbox consumer of hello never observe the module before its Start hook or after its Stop hook (instrumented hooks, `synctest`) |
| `TestStartRollbackReverseOrder`, `TestStopTimeoutAbandons` | hook ordering; uses `synctest` for timeouts |
| `TestWorkerPolicies` | `DegradeOnExit` vs `StopAppOnExit`, panic recovery |
| `TestRegistrarClosedAfterRegister`, `TestRegistrarContextCancelled` | late calls recorded; `r.Context()` cancelled with `app.New`'s ctx (COR-24) |
| `TestNewCleansUpCoreGoroutines` | `goleak`-style check: a boot failing in P7 leaves no core goroutine running (COR-24) |
| `TestDepsScoping` | write outside `m/hello/` refused; publish or notify to a topic outside hello's catalog entries refused; secret outside `modules.hello.` refused; `StartRequest.Actor` overwritten by the kernel; `library.Scope` writes attributed to hello; metric names under `MetricPrefix` |
| `TestModuleSystemActor_CannotStartUndeclaredKind` | SEC2-15: from a hello worker, starting a `core:system.manage` kind not in `TaskStarts` is refused; a declared one is allowed; `TaskStarts` naming an unknown kind fails P7 |
| `TestModuleAuthMethod_CannotBeInteractive` | SEC2-16: `AuthMethod` with `Interactive`, `MayChangeCredentials` or an `OwnerVerifier` fails boot; `Options.Authenticators` may carry them |
| `TestLegacyAuthGinOnlyNamedModule` | SEC2-17: `LegacyAuthGin` from a module other than `Options.LegacyAuthGinUntil`, and an `OwnAuth` route without a compat method, fail boot |
| `TestRealtimeProjectionLazy` | COR2-1: a projection handler obtained in P6 answers 503 before the hub starts and streams catalog events after it |
| `TestDerivedIndexHooksAfterRegister` | COR2-15: `Load` runs after every `Register`; `Build` runs as `<ID>.derived.<name>.build` with the module's Effects; a declaration without hooks fails boot |
| `TestReservedIDsIncludeLegacyTopSegments` | FE2-11: a module ID equal to the first segment of another manifest's `LegacyPrefixes`, or to an `Options.ReservedAPINamespaces` entry, fails P1 |
| `TestReservedSegmentsDisjointFromNonSPA` | FE2-7: a legacy prefix whose first segment is a shell route segment fails P8 |
| `TestSPAOwner` | FE-1: a `ServesSPA` legacy mount owns `/` and `ui.Handler` is absent; two owners fail boot |
| `TestHelloEndToEnd` | §6.12 example: route permission, task under `-race` with `CPU()` workers, migration once, settings default/override, `/api/core/v1/modules` lists nav entry |
| `TestSpecGoBlocksCompile` | COR-16: every Go block in this file marked as part of the hello example is extracted, compiled against core in CI, and **booted** with `coretest.New`, so a manifest-level refusal (an undeclared permission, a bad schedule key) fails the test, not only a compile error (COR2-14) |
| `archtest.TestNoGlobals`, `archtest.NoInit` over `core/...` and `core/examples/...` | R5, A5 |
| `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` | MOD-7, 03 R2: hello is inside the core module path, so Go's `internal/` rule does not stop it importing `internal/...` or `kernel/...`; this check does, and `scripts/ci/core_consumer_smoke.py` builds a consumer under an unrelated module path with a negative compile check |

ABO (root module `internal/arch` or `cmd` tests):

| Test | Proves |
|---|---|
| `TestSecondModuleNoAudiobookEdit` | G6, from S5 (§6.4.10): `coretest.New(t, WithModules(books.New(…), hello.New()))` boots; the test file is the only change needed — enforced by building it in CI with a `git diff --name-only` check that the hello wiring PR touched no `internal/` audiobook package (plan acceptance test) |
| `TestBooksDisabledInert` | G1 for the real module |
| `archtest.NoKernelImports` | R2 |

## 6.11 Migration from ABO

### 6.11.1 Strangler order

Named by 09 stage (MOD2-21); the old wave labels K0–K3 are retired.

1. **S2 — SDK and skeleton.** Land `module`, `kernel`, `app`, `coretest` and the hello skeleton
   (§6.4.10); core boots with zero modules. Each later stage adds its SDK members and grows hello;
   G6/G7 are proven at core level from S5.
2. **S2 — books wraps the server.** ABO's `serve` command calls `app.New(ctx, opts, books.New(legacy))`.
   `books.Register` builds today's `*server.Server` (the one recorded exception to `Register`'s
   side-effect rule until step 4, §6.4.5) and mounts its gin engine with
   `Registrar.MountLegacy` under the declared legacy prefixes (N7), permanently (cross-cutting
   resolution 2), with `ServesSPA: true` until ABO adopts the shell build (07 §7.19, FE-1). Until S2b
   books also opens the store in a start hook (§6.5.1 "Stages"). `serviceregistry` keeps working
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
| `internal/plugin.Plugin`, `Registry`, `Global()`, `Register` (`plugin.go:25`, `registry.go:19,33`) | deleted. The only implementor, webhook (`internal/plugins/webhook/plugin.go:33`), becomes the core `integrations/notify/webhook` adapter enabled in `Options.Adapters`, subscribing through `events` |
| `plugin.EventBus` (`internal/plugin/events.go:85`, goroutine per handler with the publisher's ctx) | core `events` (05-events: bounded dispatch, bus-lifetime ctx, as design Q1 decided) |
| `plugin.PluginRouter`, `/api/v1/plugins/{id}` (`plugins_init.go:42`) | deleted; module routes go through `Registrar.HTTP()` inside the core chain. The 6 `/api/v1/plugins` routes (raw/09 §1.1) become `/api/core/v1/integrations/...` (configured adapter instances, health; 05-metadata M1.3) |
| `plugin.Capability`, vestigial `DownloadClient`/`MediaPlayer`/`Notifier` (`plugin.go:17-21,52-57`) | deleted; capabilities are `integrations.Kind` + the `integrations/*` contracts |
| `config.AppConfig.Plugins` enable flags | `core.modules.<id>.enabled` (modules) and adapter instance `enabled` (adapters); migration of the stored value in 09 |
| `pkg/plugin/sdk.Plugin`/`Registry` (`plugin.go:19`, `registration.go:12`) and its aliases | deleted after the six bundles become `tasks.Definition` lists in books; `PageBooks` (`iterate.go:41`) becomes a books helper over `tasks.RunItems`; `tools/cmd/sdkguard` replaced by `archtest` R2 |
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
| `startup.Table` Fatal/Degraded steps (`internal/startup/steps.go`) | kernel phase table; Degraded steps become health checks registered by the owning service |
| `opRegistrationGate`, `opCatalogGate` (`server_lifecycle.go:66,88`) | P6/P7 validation |
| `scanner.SetStore(store)` (`server.go:805`) and similar setters | constructor parameters in books wiring |

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
05-storage §5S.3.1, §5S.3.4–§5S.3.5 (`Spec`, `Keyspace`, `KV`, `Migration`, `MigrationTx`) and
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
				{Version: 1, Name: "seed-names", Compat: storage.Additive, Up: seedNames},
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
| 05-http-api §3, §4.9–§4.10, §8.2 | `Registrar.HTTP() *httpapi.Group`, `httpapi.Handle`, `httpapi.Route`, `httpapi.Invalid(httpapi.Field(...))`, `MountLegacy(httpapi.LegacyMount)` with `ServesSPA`, `Auth`, `TopSegments`, `DefaultCSP`, `Router.SPA`, `Router.NonSPA`, `httpapi.ReservedSegments()`, subtree claims | `Manifest.LegacyPrefixes` feeds subtree claims, reserved IDs and `NonSPA` segments (FE2-11); `LegacyAuthGin` only for `Options.LegacyAuthGinUntil`, `OwnAuth` only with a compat method (SEC2-17); the `CoreGroup` rule needs no `ui` exception (`internal/uihttp`, COR2-19); facade members deferred (SIMP2-6) |
| 05-storage §5S.3–§5S.4 | `Manifest.Storage storage.Spec` (derived indexes declarative only); `Registrar.DerivedIndex(name, storage.DerivedHooks)`; `Manifest.DataMigrations []datamigrate.DataMigration`; `Deps.Storage module.StorageHandles{Keyspace, Legacy, Indexes, Aux, Derived}`; `c/storage/claims/<owner>`, `Claim.TakesOver`; cross-keyspace migration DAG with one checkpoint per boot | §6.5.1 is the one boot order; 05-storage §5S.4 references P2 and P4 (a)–(d) and keeps the mechanics (R2-8, COR2-7, DATA2-24); store at `core.storage.path`; no automatic checkpoint restore (DATA2-19); `DerivedIndex` hooks move out of `storage.Spec` (COR2-15) |
| 05-tasks | `Registrar.Tasks`, `Resources`, `Lanes`, `ChangeSink(tasks.ChangeSink)`; `Manifest.TaskLedger` (5T.3.11 held runs); `Manifest.TaskStarts`; `tasks.Common.Schedule` (+ `tasks.ScheduleFromConfig`, one schedule-key rule, R2-7); `Deps.Tasks tasks.Client`; `tasks.CPU()`, `RunItems(rc, par, items, fn, opts...)`, `tasks.ForEach` | closes 05-tasks open item 6 and the `Registrar.Schedule` question (it does not exist). Held runs and the ledger-gate rule of §6.5.3 are 05-tasks mechanics (COR-13); the system actor's permission set (SEC2-15) and the tasks reconcile in P4d are 05-tasks' to state |
| 05-events | `Registrar.Topics(...events.TopicSpec)` (one catalog incl. `Durable`, `Notify`, `Realtime`, `Permission`, `Visible`), `Registrar.Subscribe(...events.Subscription)` built with `events.On`/`OnRaw`, `Registrar.OutboxHandler(...outbox.Handler)` incl. `ConsumeRaw`/`ConsumeIntoTaskRaw`, `Manifest.OutboxConsumers`; `Deps.Outbox outbox.Writer`; `Bus.Open`/`Bus.Close` in §6.5.2 | adopted as 05-events states it, including `Deps.Events events.Scope` (COR-21) |
| 05-realtime | catalog topics with `TopicSpec.Realtime` are streamed with no registrar call; `Registrar.Realtime() realtime.Registrar` with `Redact` and `Projection(realtime.LegacyProjection) http.Handler` | hub built in P5, redactors and projections recorded in P6 and attached in P7; projections are books gin manifest routes, not aliases (COR2-1, SEC2-25); core-topic redaction is the topic's own (COR2-2) |
| 05-notify | notify metadata on the catalog entry (`TopicSpec.Notify`); `Deps.Notify notify.Publisher` | `Registrar.NotificationTopic` deleted (SIMP-5); `Publish` is the only entry point, and `Notify` metadata alone produces nothing (COR2-3) |
| 05-rbac §3.1–§3.5 | `rbac.Permission`, `PermissionSpec{Name, Title, DefaultGrants, Sensitive, …}`, `RoleSeed`, `RoleName` (`"<ns>.<name>"`), `LegacyAlias`, `Requirement`, `Need`, `CoreAdmin`, `audit.Logger` | manifest lists are fed to `Service.Declarer(moduleID)` by the kernel in P5, after `rbac.New` (COR2-7); leaf types land at S2 (COR2-9); no `Grant`, `Scope` or `PermissionDef` type exists (COR-16) |
| 05-config §1.3–§1.4, §2.3 | `config.Define`, `Section[T]`, `Handle[T]`, `config.Register`; `Store.Boot` + boot record for `core.modules.<id>.enabled` (P0/P1); `Store.Load` without DB (P3); `secrets.Open`, configstore import and `AttachOverrides` (P4d); `config.Options.EnvPrefix` from `app.Options.EnvPrefix`; pre-boot keys `core.paths.state_dir`/`data_dir`; `secrets.Scope` (`modules.<id>.`) | 05-config hands modules `Registrar.Config()` with "registration legal only before Store.Load"; since `Register` needs loaded config, sections are listed in `Manifest.Settings` and registered by the kernel before load; `module.Setting(r, s)` returns the bound handle |
| 05-metadata M1–M2 | `Registrar.Adapter(integrations.Descriptor)`, `app.Options.Adapters`, `Deps.Integrations *integrations.Registry` (the one instance store, SIMP-4), `Deps.HTTP *providerhttp.Registry` | adopted as proposed |
| 05-ai | `Registrar.AICapability(ai.CapabilitySpec) ai.Capability`, `ai.BatchApplyTask(cap, h, effects)` registered with `Registrar.Tasks`, `Deps.AI *ai.Router` | signatures as in 05-ai; `Registrar.AIBatchHandler` removed (COR2-11) |
| 05-download | `Deps.Downloads *download.Pool`; `DownloadWatch` deferred-build | grab/monitor are the deferred acquire build (SIMP-12, COR-3) |
| 05-health | `Health(...health.Check)`, `StartupSteps(...health.StartupStep)`, `Diagnostics(health.Contributor)`; `Deps.Health health.Reporters`; `core.shutdown.budget` and its phase slices (§5H.4) | `core.lifecycle.stop_timeout`/`shutdown_budget` removed from 06 (OPS-13) |
| 05-updater | `app.Options.Updater *updater.Options`; `updater.Boot` as phase P−1 | replaces 1.0.0's `Release updater.Source`; `Boot` resolves paths from flags, env and its own marker, decides rollback before P0a, and counts P0 refusals (OPS2-3) |
| 05-download, 05-indexer, 10 (v1.1.0) | `Deps.Downloads *download.Pool`; deferred `Deps.Indexers *indexer.Pool`, `Deps.Acquire *acquire.Service`, `Manifest.Acquire *AcquireDecl` (§6.4.9) | names as those files state them |
| 05-library | `Manifest.Library []library.RoleRequirement`; `Deps.Library library.Scope` | — |
| 05-authn §3.3, §5 | `app.Options.Authenticators []auth.Extension`; `Registrar.AuthMethod(auth.MethodSpec)`; `Deps.Auth`, `Deps.AuthAdmin` | `auth.Extension{Authenticator, Spec, Order}` requested from 05-authn; the identity mirror rebuild is the kernel's P4d step 1 (DATA2-24) |
| 05-backup | `Registrar.Backup(...backup.Artifact)`; `Deps.Backup backup.Ensurer`; pre-boot restore phase P0a; revocation re-apply and post-restore hooks in P4d | 05-backup's "module.Manifest.Backup" comment changes to `Registrar.Backup` (hooks close over services); P0a runs after P−1 and before P0 (DATA-23, OPS2-3) and trusts the absolute targets in `pending.json` (DATA2-25) |
| 05-logging, 05-metrics | `Deps.Log *slog.Logger`, `Deps.Metrics *metrics.Scope` | — |
| 07 §7.4–§7.6 | `UIDescriptor` (all shell slots), `app.Options.UI ui.Bundle` (S9), `ui.ReservedSegments` | `/modules` and `internal/uihttp` routes ship at S4 (FE2-25); P8 checks shell segments against `NonSPA` (FE2-7) |
| 08, 10, 05-indexer | §6.4.9 deferred-build members | — |

## Objections to 03

None open. The FE2-11 mismatch is resolved by 03 v1.5.0 §3.6: every reserved name has one owner
list (`admin`, `tasks` dropped, `logout` left the shell list, `healthz` → `livez`), and the two compat
inputs are split as in §6.4.1. 06 keeps `module.ReservedIDs()` static (the owner lists, evaluable in
P1, COR2-19); the kernel adds both compat inputs to the reserved set in P1.
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
- OPS2-7 — P0 alias uses buffered and recorded after the checkpoint (P4d step 6).
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
- R2-2, R2-3, R2-4, R2-7 — referenced in P4 (revocations write legacy rows; claim modes as level steps; legacy migrations before claims) and in `Registrar.Tasks` (one schedule-key rule).
