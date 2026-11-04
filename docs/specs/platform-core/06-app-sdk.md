<!-- file: docs/specs/platform-core/06-app-sdk.md -->
<!-- version: 1.0.0 -->
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
| A1 | A module is `Manifest()` (pure data, validated before storage opens) **plus** `Register(*Registrar)` (runs after migrations, receives typed `Deps`). **Rule: anything the kernel needs before constructing services lives in the `Manifest`; anything that needs a service lives in `Register`.** So permissions, keyspace and legacy claims, migrations, settings sections, UI descriptor IDs, dependencies and required capabilities are manifest fields; routes, tasks, schedules, topics, outbox handlers, adapters, health checks, hooks, workers and the *arr provider are registrar calls. | 03 §3.7 requires validation before listeners *and* migrations before services. A single `Register` cannot do both because its handlers need migrated storage. Cost: two places to look; mitigated by the rule being mechanical. |
| A2 | **No generic DI container in core.** Core's own ~30 services are built by hand-written, typed code in `kernel/services` (a struct with one field per service, constructed in a fixed order). A module builds its internal services with plain Go constructors inside `Register`. | Evaluated below (§6.3.1). |
| A3 | `Deps` is a **struct of typed, module-scoped handles**, never `any`, never nil. Scoping is applied by the kernel (keyspace `m/<id>/`, topic prefix `<id>.`, secret names `<id>/…`, metric prefix, logger attribute). | Replaces 113 `Get[T]` string lookups with compile-time types; scoping enforces R3/R8 at the handle rather than by review. |
| A4 | Registrar methods **record problems instead of returning errors** (like `testing.T`); the kernel joins every problem of every module into one `*ValidationError`. | An operator sees all misconfigurations at once, not one per restart. |
| A5 | Module code runs only through the kernel: no `init()`, no package-level mutable state, background goroutines only via `Registrar.Worker`. A disabled module's `Register`, migrations, hooks and workers are **never invoked**. | This is what makes G1 a structural property rather than a per-feature check. Enforced by `archtest.NoInit` + `archtest.TestNoGlobals` over module packages, and by `coretest.AssertModuleInert`. |
| A6 | Enablement is restart-bound (`core.modules.<id>.enabled`), dependency-checked, and fail-closed. | Old decision 10 (02 §2.2); `ServeMux` routes cannot be unmounted. |
| A7 | Tests use `testing/synctest` for time; core has no `Clock` interface in `Deps`. | Go 1.25+ virtualises `time` inside a bubble; a clock abstraction would be a second, weaker mechanism. |

### 6.3.1 Does core need a generic DI container?

| Question | `serviceregistry` today | Explicit construction (chosen) |
|---|---|---|
| Ordering | runtime Kahn sort over `Needs` strings (`graph.go:19`) | statement order; using a value before it is constructed does not compile |
| Missing dependency | `ErrUnknownService` at boot, or a silent skip when a group is wrong (`batchpoller`) | compile error |
| Type safety | `Build` returns `any`; `Get[T]` panics on mismatch (`container.go:245`) | static types |
| Optional services | typed-nil returns that every consumer must nil-check (raw/07 §6.10; e.g. `metricsstore` returns `(*database.PebbleMetricsStore)(nil)`, `internal/server/registry_wire.go:283-291`) | `if cfg.AI.Enabled { … }` at the one construction site; consumers receive a non-nil value or a documented no-op |
| Cycles | `PostInit` cross-wiring after `Build` (6 implementors, e.g. `internal/dedup/lifecycle.go:76`) | explicit setter after both are built, or replace the edge by an event subscription (most of today's `PostInit`s subscribe to the event bus) |
| `IncludeGroup` | string groups `core`/`ai`/`scheduler`/`plugins`/`activity`; `activity` gated on `DatabasePath` (`server.go:824-826`) | plain `if` in `books` `Register`; the group concept disappears |
| Test seam | `Override(name, instance)` | `coretest` options replace whole core services (`coretest.WithStorage`, `WithAdapter`) and a module's own constructors take interfaces |

A container earns its keep when the composition set is open-ended and assembled from discovered
registrants. That is exactly what this design removes (N4). With ≤ 30 core services in a fixed
order and modules that own their internals, a container would re-introduce `any` and string keys for
no remaining benefit. The kernel therefore has **a typed services struct, not a container**; see
"Objections to 03" for the wording change this implies in 03 §3.3/§3.7.

## 6.4 Go API

All packages below are in module `github.com/falkcorp/mediacore`. `module` is SDK; `kernel` is
public-but-unstable (apps must not import it, R2); `app` and `coretest` are SDK.

### 6.4.1 `module` — identity and the `Module` interface

```go
package module

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/falkcorp/mediacore/arrfacade"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/integrations/indexer"
	"github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/auth"
)

// ID is a module identifier: ^[a-z][a-z0-9]{1,15}$ (03 §3.6). "core" is reserved.
type ID string

// Valid reports whether id matches the naming rule and is not reserved.
func (id ID) Valid() bool

// Module is what an app hands to app.New. Implementations are plain values
// constructed by the app's main (e.g. books.New(), hello.New()); they must not
// do work in their constructor beyond capturing options.
type Module interface {
	// Manifest returns the module's static declaration. It must be pure,
	// cheap and deterministic: the kernel and tests may call it any number of
	// times, from any goroutine, before and after Register.
	Manifest() Manifest

	// Register is called exactly once, after core and this module's migrations
	// have run and after every module this one DependsOn has registered.
	// It constructs the module's services from r.Deps() and records routes,
	// tasks, schedules, topics, handlers, adapters, health checks, hooks and
	// workers. A returned error aborts boot (fail-closed).
	Register(r *Registrar) error
}
```

### 6.4.2 `module.Manifest`

```go
// Manifest is everything the kernel validates before any storage is opened.
type Manifest struct {
	ID          ID
	Version     string // semver, e.g. "1.12.0"; reported by GET /api/core/v1/modules
	DisplayName string // human name, e.g. "Audiobooks"

	// MinCoreVersion is the lowest mediacore version this module was built
	// against (semver). Boot fails when mediacore.Version is lower.
	MinCoreVersion string

	// DefaultEnabled is the value of core.modules.<id>.enabled when unset.
	DefaultEnabled bool

	// DependsOn lists modules that must be present AND enabled. Order of
	// Register/Start follows this graph (topological, ties by ID).
	DependsOn []ID

	// Requires lists integration capabilities the module needs at runtime.
	// Required=false entries only drive the settings UI and health text.
	Requires []CapabilityRequirement

	// Permissions this module defines. Every name must be "<ID>:<action>[.<sub>]".
	// Routes, tasks and facade endpoints may only use declared permissions
	// (plus core's rbac.Public / rbac.AnyAuthenticated / core:* atoms).
	Permissions []rbac.PermissionSpec

	// Keyspace configures storage ownership: the module always owns m/<ID>/;
	// Legacy lists pre-existing key prefixes it adopts in place (books uses
	// this for ABO's 258 families, 05-storage). Overlap between any two claims
	// across modules or with core keyspaces fails boot (R8).
	Keyspace KeyspaceSpec

	// Migrations are forward-only, versioned per module (05-storage). They
	// run before Register, inside a checkpoint (G2).
	Migrations []storage.Migration

	// Settings are the typed config sections the module owns. Each section
	// name must be "modules.<ID>" or start with "modules.<ID>.".
	Settings []config.AnySection

	// UI lists the descriptor IDs the module's client bundle may contribute.
	// The server reports them in /api/core/v1/modules; the shell refuses
	// client entries whose IDs are not listed (G3, 07 §7.4).
	UI UIDescriptor

	// Legacy declares un-namespaced surfaces kept for a compatibility window
	// (09): HTTP path prefixes outside /api/<ID>/, legacy permission atoms
	// mapped to namespaced ones, legacy task kinds. Validated for collisions.
	Legacy LegacySurfaces
}

// CapabilityRequirement names an integration capability the module uses.
type CapabilityRequirement struct {
	Capability Capability // CapDownload, CapIndexer, CapMetadata, CapAI, CapNotify
	Required   bool       // true: boot fails when no adapter instance is configured
	Why        string     // shown in settings and in the boot error
}

type Capability string

const (
	CapDownload Capability = "download"
	CapIndexer  Capability = "indexer"
	CapMetadata Capability = "metadata"
	CapAI       Capability = "ai"
	CapNotify   Capability = "notify"
)

// KeyspaceSpec — the m/<ID>/ prefix is implicit and cannot be configured.
type KeyspaceSpec struct {
	Legacy []storage.LegacyClaim // e.g. {Prefix: "book:", Why: "ABO book rows"}
}

// UIDescriptor lists the IDs of client-side contributions (07 §7.3).
type UIDescriptor struct {
	Pages          []string // route IDs, e.g. "library", "book"
	Nav            []string // nav entry IDs
	SettingsPanels []string // settings panel IDs
	Widgets        []string // optional dashboard widgets
}

// LegacySurfaces is the compatibility ledger for an extracted app (09).
type LegacySurfaces struct {
	PathPrefixes []string                        // e.g. "/api/v1/", "/api/v3/ops/"
	Permissions  map[string]rbac.Permission      // "library.view" -> "books:read"
	TaskKinds    map[string]string               // old def ID -> new kind
	RemoveAfter  string                          // module version at which the window closes
}
```

`rbac.PermissionSpec` (owned by [05-rbac](05-rbac.md)) carries default grants:

```go
package rbac

type Permission string // "<ns>:<action>[.<sub>]"

type RoleName string

const (
	RoleAdmin  RoleName = "admin"  // holds core:admin, which implies every permission
	RoleEditor RoleName = "editor"
	RoleViewer RoleName = "viewer"
)

type PermissionSpec struct {
	Name          Permission
	Title         string     // shown on the Users/Roles page
	Description   string
	DefaultGrants []RoleName // seeded on first boot of the module only; admin edits win afterwards
	// Sensitive permissions are never granted by an API key scope preset and
	// require an interactive session method (replaces today's credGuard list).
	Sensitive bool
}
```

### 6.4.3 `module.Registrar`

```go
// Registrar is handed to Module.Register. It is not goroutine-safe and is
// closed when Register returns; calls after that record ErrRegistrarClosed.
type Registrar struct{ /* unexported */ }

// Deps returns the module-scoped dependency handles. Always non-nil fields.
func (r *Registrar) Deps() Deps

// Route registers native routes. Paths are relative to /api/<ID>/v1; a
// route whose resolved path is outside that prefix (or a declared
// Legacy.PathPrefixes entry) is a problem. Every route must declare a
// permission (R6); httpapi refuses the zero value.
func (r *Registrar) Route(routes ...httpapi.Route)

// LegacyHandler mounts an http.Handler under a declared legacy prefix
// (used by books to mount ABO's gin engine during migration, N7). The
// handler is wrapped in the same core middleware chain as native routes,
// and each legacy prefix must name the permission check policy explicitly.
func (r *Registrar) LegacyHandler(prefix string, policy httpapi.LegacyPolicy, h http.Handler)

// Task registers task definitions; kinds must be "<ID>.<name>". Each
// definition carries its tasks.Parallelism (R7) — tasks rejects the zero value.
func (r *Registrar) Task(defs ...tasks.Definition)

// Schedule registers recurring triggers for this module's own kinds.
func (r *Registrar) Schedule(s ...tasks.Schedule)

// Topic declares event topics this module publishes ("<ID>.<noun>.<verb>.v<n>").
// Subscribing to other modules' topics needs no declaration.
func (r *Registrar) Topic(t ...events.TopicSpec)

// OutboxHandler registers a durable consumer (05-events). Handler names are
// "<ID>.<name>"; a handler for a topic of a module not in DependsOn is allowed
// (outbox is the cross-module correctness channel, R3).
func (r *Registrar) OutboxHandler(h ...outbox.Handler)

// Adapter registers an implementation of a core capability contract that
// this module ships (e.g. a TV metadata provider). Kind is "<ID>.<name>".
// Core adapters (deluge, newznab, ...) are enabled in app.Options instead.
func (r *Registrar) Adapter(a ...Adapter)

// Health registers component health checks ("<ID>.<component>").
func (r *Registrar) Health(c ...health.Check)

// Backup declares module-owned artifacts outside storage (05-backup).
func (r *Registrar) Backup(a ...backup.Artifact)

// ArrFacade registers this module's *arr-v3 provider (08). At most one per module.
func (r *Registrar) ArrFacade(p arrfacade.Provider)

// Hook registers a Start/Stop pair. Hooks of one module run in registration
// order on start and reverse order on stop.
func (r *Registrar) Hook(h Hook)

// Worker registers a supervised long-running goroutine started after all
// hooks have started and stopped (ctx cancelled) before any hook stops.
func (r *Registrar) Worker(w Worker)

// Err returns the problems recorded so far, joined; nil if none. Modules may
// call it to bail out early, the kernel always checks it after Register.
func (r *Registrar) Err() error

type Hook struct {
	Name  string                          // "<ID>.<name>", unique
	Start func(ctx context.Context) error // may be nil
	Stop  func(ctx context.Context) error // may be nil; ctx carries the stop deadline
}

type WorkerPolicy int

const (
	// DegradeOnExit: an early return or panic marks health "<ID>.<worker>"
	// unhealthy and is logged; the app keeps serving. Default.
	DegradeOnExit WorkerPolicy = iota + 1
	// StopAppOnExit: an early return or panic begins a graceful shutdown and
	// App.Run returns the worker's error. For workers the module cannot run
	// without (e.g. a library watcher whose loss would silently stop imports
	// is NOT one of these — it degrades; a data-integrity relay is).
	StopAppOnExit
)

type Worker struct {
	Name   string
	Run    func(ctx context.Context) error // must return promptly after ctx is done
	Policy WorkerPolicy                    // zero value refused: say which
}

// Adapter is a capability implementation a module ships.
type Adapter struct {
	Capability Capability
	Kind       string // "<ID>.<name>"
	// Factory receives the adapter instance's config section (decoded by the
	// capability package) and a provider-scoped HTTP client; see 05-download etc.
	Factory any // a typed factory from the capability package, e.g. metadata.Factory[Q, R]
}
```

`Adapter.Factory` is `any` only at this boundary because each capability has its own generic
factory type; the capability package provides the typed constructor (`metadata.AdapterOf[Q, R](kind, f)`
returns a `module.Adapter`) and the kernel hands the value back to the same package, which type-asserts
once at registration (fail-closed, reported as a problem).

### 6.4.4 `module.Deps`

```go
// Deps are the handles a module may use. Every handle is scoped to the
// module by the kernel and safe for concurrent use. None is ever nil; a
// capability with no configured adapter returns ErrNotConfigured from its
// lookup methods instead.
type Deps struct {
	Module ID

	Log     *slog.Logger  // child of the app logger with attr module=<ID> (05-logging)
	Metrics metrics.Scope // registers <app>_<ID>_* on the App's registry, never the global one (05-metrics)

	Storage storage.Keyspace           // m/<ID>/; writes outside are refused (R8)
	Legacy  storage.LegacyKeyspaces    // the claimed legacy prefixes, by claim name
	Index   storage.IndexFactory       // full-text indexes owned by this module (storage/index)

	Tasks  tasks.Client   // enqueue/query runs of any kind (permission-checked as the caller)
	Events events.Bus     // publish only declared <ID>.* topics; subscribe to any topic
	Outbox outbox.Writer  // same-batch durable messages (05-events)

	Config  config.Reader   // this module's sections + the read-only public core sections
	Secrets secrets.Scope   // names under "<ID>/"; values never logged (05-config)

	Library library.Roots   // os.Root-confined media roots (05-library)
	Auth    auth.Identities // user lookup, current principal from ctx (05-authn)
	RBAC    rbac.Checker    // Can(ctx, perm) / Require(ctx, perm) (05-rbac)
	Audit   audit.Recorder  // append-only, actor taken from ctx (05-rbac)
	Health  health.Reporter // push-style status for this module's components (05-health)

	HTTP     providerhttp.Factory // instance-scoped outbound clients (05-metadata)
	Download download.Clients     // configured download-client instances (05-download)
	Indexers indexer.Set          // configured indexers (05-indexer)
	Metadata metadata.Registry    // typed lookup: metadata.Lookup[Q, R](d.Metadata, kind) (05-metadata)
	AI       ai.Router            // provider-neutral AI (05-ai)
	Notify   notify.Dispatcher    // notification fan-out (05-notify)
}

// ErrNotConfigured is returned by a capability lookup with no configured instance.
var ErrNotConfigured = errors.New("module: capability not configured")
```

`var ErrNotConfigured` is an exported package-level variable of interface type `error`; R5 forbids
exported map/slice/func/pointer variables, not sentinel errors (sentinels are immutable by
convention and are what `errors.Is` needs). `TestNoGlobals` allowlists `var Err… = errors.New(…)`.

### 6.4.5 `app`

```go
package app

// Options configure one process. Zero values are valid defaults unless noted.
type Options struct {
	ID          string // app identifier, [a-z][a-z0-9]{1,15}; metric prefix "<ID>_", e.g. "abo"
	DisplayName string // e.g. "Audiobook Organizer"
	Version     string // the app's release version (updater, /api/core/v1/modules)

	// DataDir is the state directory; opened with os.OpenRoot and never
	// escaped (secrets key file, Pebble dir, backups).
	DataDir string
	// ConfigFile is optional; layered under env/flags/DB overrides (05-config).
	ConfigFile string
	// Args/Env feed the flag and env layers; nil means os.Args[1:]/os.Environ().
	Args []string
	Env  []string

	// UI is the app's embedded SPA (07 §7.6). The zero Bundle serves the API-only placeholder.
	UI ui.Bundle

	// Adapters enables core adapters by kind (e.g. deluge.Adapter(), newznab.Adapter()).
	// Only enabled kinds may be configured; instances come from config.
	Adapters []module.Adapter

	// Logger overrides the default slog handler construction (tests); nil = from config.
	Logger *slog.Logger

	// Release identifies the update source for core/updater (05-updater).
	Release updater.Source
}

type App struct{ /* unexported: kernel.Kernel */ }

// New validates every manifest, opens storage, runs core then module
// migrations, builds core services, calls Register on each enabled module in
// dependency order, and builds the HTTP handler. It does not start goroutines
// or listeners. On error nothing is left open.
func New(ctx context.Context, opts Options, mods ...module.Module) (*App, error)

// Run starts hooks, workers, the task executor, scheduler, outbox relay,
// realtime hub and listeners; blocks until ctx is done or a StopAppOnExit
// worker exits; then shuts down in reverse order within
// core.lifecycle.shutdown_budget. A clean shutdown after ctx cancellation
// returns nil.
func (a *App) Run(ctx context.Context) error

// Handler is the fully assembled root handler (for coretest and embedding in
// another server). Valid after New.
func (a *App) Handler() http.Handler

// Modules reports each module's resolved state (enabled, disabled, reason).
func (a *App) Modules() []ModuleState

type ModuleState struct {
	ID       module.ID
	Version  string
	Enabled  bool
	Disabled string // reason when !Enabled: "config", "dependency <id> disabled"
}
```

Signal handling stays in `main` (`signal.NotifyContext`), so `Run` is testable and a host that embeds
the app controls its own lifecycle. ABO keeps its cobra command tree; its `serve` command calls
`app.New` + `Run` (§6.11).

### 6.4.6 `kernel` (shape only; apps never import it)

| Package | Responsibility |
|---|---|
| `kernel/catalog` | manifest validation, dependency graph (Kahn, ties by ID), enablement resolution, collision checks across modules and core (IDs, permissions, legacy atoms, keyspace and legacy claims, config sections, task kinds, topics, legacy path prefixes, facade URL bases, UI IDs) |
| `kernel/services` | `type Services struct{ Storage *storage.DB; Tasks *executor.Executor; … }` built by `func Build(ctx, opts, cfg) (*Services, error)` in a fixed, hand-written order; `Close` in reverse |
| `kernel/supervisor` | hook start/stop ordering, worker supervision (`sync.WaitGroup.Go`), panic recovery, timeouts, shutdown budget |
| `kernel` | `Kernel` = catalog + services + supervisor; `Registrar`/`Deps` construction per module (scoping wrappers) |

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
	ProblemUnknownDependency
	ProblemDependencyCycle
	ProblemDependencyDisabled
	ProblemCoreTooOld
	ProblemPermissionNamespace
	ProblemDuplicatePermission
	ProblemUndeclaredPermission
	ProblemKeyspaceOverlap
	ProblemMigrationOrder
	ProblemSettingsNamespace
	ProblemTaskKindNamespace
	ProblemDuplicateTaskKind
	ProblemTopicNamespace
	ProblemRouteOutsidePrefix
	ProblemRouteWithoutPermission
	ProblemLegacyPrefixCollision
	ProblemUIDescriptor
	ProblemCapabilityMissing
	ProblemFacadeIncomplete
	ProblemFacadeURLBaseCollision
	ProblemUnknownModuleInConfig
	ProblemRegistrarClosed
	ProblemWorkerPolicy
	ProblemAdapter
)

// Sentinels for errors.Is(err, module.ErrDependencyDisabled) etc.
var (
	ErrDependencyDisabled = problemSentinel(ProblemDependencyDisabled)
	ErrCoreTooOld         = problemSentinel(ProblemCoreTooOld)
	// ... one per ProblemKind
)
```

### 6.4.8 `coretest`

```go
package coretest

// Harness is an in-process App: in-memory Pebble (vfs.NewMem), a temp
// os.Root DataDir, no listeners; requests go straight to App.Handler().
type Harness struct{ /* unexported */ }

// New boots core with the given options. With no WithModules option it boots
// core with ZERO modules (gate G7). It registers t.Cleanup for shutdown and
// fails the test on any boot error.
func New(t testing.TB, opts ...Option) *Harness

type Option func(*settings)

func WithModules(mods ...module.Module) Option
func WithDisabled(ids ...module.ID) Option      // sets core.modules.<id>.enabled=false
func WithConfig(kv map[string]any) Option       // config overrides layer
func WithAdapters(a ...module.Adapter) Option   // e.g. fakes from coretest/fakes
func WithUser(name string, perms ...rbac.Permission) Option // pre-seeded principal

// Boot is New without failing the test: it returns the boot error, for
// validation tests that expect a *module.ValidationError.
func Boot(t testing.TB, opts ...Option) (*Harness, error)

func (h *Harness) App() *app.App
func (h *Harness) As(user string) *Client          // authenticated client for a seeded user
func (h *Harness) Anonymous() *Client
func (h *Harness) RunTask(t testing.TB, kind string, params any) tasks.RunView // enqueue and wait
func (h *Harness) Storage() storage.Inspector      // read-only view of every keyspace
func (h *Harness) FS() *RecordingFS                // records every mutation under library roots

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
// module is absent from GET /api/core/v1/modules, and RecordingFS saw no
// mutation attributed to it. Its keyspace data is unchanged (retained).
func AssertModuleInert(t testing.TB, h *Harness, id module.ID)
```

`coretest/fakes` provides in-memory implementations of every capability contract (download, indexer,
metadata, ai, notify) so module tests never need a network. Time-dependent tests run inside
`synctest.Test(t, func(t *testing.T) { h := coretest.New(t, …) … })`; the harness starts no real
timers outside the bubble.

## 6.5 Lifecycle

### 6.5.1 Boot (inside `app.New`)

| Phase | Step | On failure |
|---|---|---|
| P0 | Resolve config layers far enough to read `core.*` (file → env → flags; DB overrides are applied in P3) | fail-closed |
| P1 | `Manifest()` of every module; `kernel/catalog` validates IDs, `MinCoreVersion` against `mediacore.Version`, `DependsOn` graph, namespaces, cross-module and core collisions, enablement (`core.modules.<id>.enabled`, default `DefaultEnabled`), and that a disabled module has no enabled dependent | all problems joined → `*ValidationError` |
| P2 | Open storage under `DataDir` (`os.OpenRoot`); verify keyspace and legacy claims against the on-disk claim ledger (05-storage) | fail-closed |
| P3 | Core migrations; load DB config overrides; validate every settings section (core + **all** modules, enabled or not, so a disabled module's bad settings are reported but do not block — see error table) | fail-closed except as noted |
| P4 | For each **enabled** module in topological order: checkpoint, run its pending `Migrations` (G2) | fail-closed; nothing of later modules runs |
| P5 | `kernel/services.Build`: core services in fixed order; adapters from `Options.Adapters` + module adapters (registered in P6, instantiated lazily on first lookup) | fail-closed |
| P6 | For each enabled module in topological order: `Register(r)`; then `r.Err()`; registrar closed. Seed default grants for permissions new to this boot | all problems joined |
| P7 | Post-registration checks: R6 over the built router, R7 via `tasks` registration, facade completeness (08), task kinds referenced by schedules exist, outbox handlers' topics are declared by some module | joined |
| P8 | Build the root `http.Handler`: core routes, module routes, legacy handlers, facades, `ui.Handler` | fail-closed |

`New` closes storage and everything opened so far, in reverse order, on any failure.

### 6.5.2 Run and shutdown

Start order: (1) core background services (metrics, realtime hub, outbox relay); (2) module hooks,
modules in topological order, hooks of one module in registration order; (3) module workers; (4) task
executor, then scheduler (so no task can be dispatched before its module's hooks have started);
(5) listeners; (6) readiness true.

If any hook's `Start` fails or exceeds `core.lifecycle.start_timeout`, every already-started hook is
stopped in reverse order (the semantics of `serviceregistry.Container.Start` today, `container.go:164`),
and `Run` returns the joined error.

Stop order on ctx cancel or `StopAppOnExit`: readiness false → listeners `Shutdown` (bounded) →
scheduler stop → executor drain (05-tasks bounded drain) → cancel workers' ctx, wait up to
`core.lifecycle.stop_timeout` → module hooks `Stop` in exact reverse start order, each with its own
`stop_timeout` → outbox relay, realtime hub → storage close. The total is capped by
`core.lifecycle.shutdown_budget`; work still running at the cap is abandoned with an ERROR log naming
it, and storage is closed last regardless (Pebble's WAL makes that safe for committed batches).

### 6.5.3 Enablement (restart-bound, G1)

* Desired state: config key `core.modules.<id>.enabled` (layered like every key; the settings UI
  writes the DB override layer). Hot reload: **no** — a change is persisted, `GET /api/core/v1/modules`
  marks `restartRequired: true`, and nothing else changes until restart.
* Disabled module: manifest still read (its keyspace/legacy claims, permissions, settings section
  names stay reserved so nothing else can take them and its data is retained); migrations, `Register`,
  hooks, workers **not invoked**; no route, task kind, schedule, topic, outbox handler, adapter, health
  check, facade or nav entry exists. Persisted task runs of its kinds are held, not executed or deleted
  (05-tasks state `held`); outbox messages addressed to its handlers stay pending (05-events).
* Its permissions remain known to `rbac` (roles referencing them stay valid) and are shown as
  "module disabled" on the Roles page.
* A disabled module with an enabled dependent fails boot (`ProblemDependencyDisabled`) naming both;
  the kernel never silently cascades a disable.
* Re-enabling runs any migrations added while disabled, then registers normally.

## 6.6 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| Invalid/duplicate/reserved module ID, dependency unknown or cyclic | fail-closed at P1, all problems joined | composition errors are programmer errors; booting a partial graph hides them |
| `MinCoreVersion` > `mediacore.Version` | fail-closed (`ErrCoreTooOld`) | module may call APIs that do not exist |
| Disabled module with an enabled dependent | fail-closed | silent cascade would disable features the operator did not choose |
| `core.modules.<id>.enabled` names an unknown module | fail-closed (`ProblemUnknownModuleInConfig`), message lists known IDs | a typo intended to disable a module must not leave it running; a removed module's leftover key is a one-line fix |
| Permission not `<ID>:`-prefixed, duplicate, or route/task uses undeclared permission | fail-closed | R6 and D7 namespacing |
| Keyspace or legacy-prefix overlap | fail-closed | R8; overlapping writes corrupt data |
| Settings section outside `modules.<ID>` | fail-closed | config namespace collision |
| Settings value invalid for an **enabled** module | fail-closed | module would run with undefined config |
| Settings value invalid for a **disabled** module | fail-open: WARN + health `core.config` degraded | the module does not run; blocking boot would punish disabling it |
| Module migration error | fail-closed; checkpoint restored per 05-storage; later modules do not migrate | G2 |
| `Register` returns error or panics | fail-closed; panic converted to error with stack in the log | partial registration leaves dangling routes/tasks |
| Registrar misuse (route outside prefix, missing permission, missing `Parallelism`, duplicate kind, call after close) | recorded, fail-closed after P6/P7 | collected so the operator sees all at once |
| Required capability with zero configured instances | fail-closed (`ProblemCapabilityMissing`) with `Why` | a TV app with no indexer cannot do its job; optional ones only degrade |
| Adapter factory type mismatch | fail-closed (`ProblemAdapter`) | the only `any` boundary must not fail at first use |
| Hook `Start` error/timeout | fail-closed: reverse-stop started hooks, `Run` returns error | a half-started module may accept work it cannot finish |
| Hook `Stop` error/timeout | fail-open: logged, joined into `Run`'s return, shutdown continues | shutdown must reach storage close |
| Worker early return/panic, `DegradeOnExit` | fail-open: health unhealthy, ERROR log, no restart | restarting a crashing loop hides the bug; readiness reflects it |
| Worker early return/panic, `StopAppOnExit` | fail-closed: graceful shutdown, `Run` returns the error | declared as essential by the module |
| `Deps` handle used after Stop | returns `module.ErrStopped` | prevents writes after storage close |
| Capability lookup with no instance | returns `ErrNotConfigured` | callers decide; never a nil handle |

## 6.7 Concurrency contract

* `Manifest()` may be called concurrently and repeatedly (parallel `coretest` tests); it must not
  mutate shared state.
* `Register` runs once per boot, on one goroutine, in topological order; it must not start goroutines
  (use `Worker`) and must not block on I/O beyond reading its own config/keyspace. `Registrar` is not
  goroutine-safe and is invalid after `Register` returns.
* Every `Deps` handle is goroutine-safe. Library-scale loops inside module code use `tasks.RunItems`
  with an explicit `tasks.Parallelism` (R7, CLAUDE.md concurrency rule); hand-rolled pools in module
  code must be bounded (`errgroup.SetLimit`), and `archtest` flags `go` statements in module packages
  outside `_test.go` for review (warning list, not a hard failure, because some libraries need them).
* Hooks start sequentially (deterministic ordering, simple rollback). Boot latency is dominated by
  migrations and index opening, not hooks; parallel start per topological level is an open item, not a
  default.
* Workers run under `sync.WaitGroup.Go`; each gets a ctx cancelled at shutdown and must return within
  `core.lifecycle.stop_timeout`.
* All shutdown waits are bounded (§6.5.2); nothing in the kernel waits without a deadline.

## 6.8 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.modules.<id>.enabled` | bool | `Manifest.DefaultEnabled` | no (restart-bound; UI shows restart required) |
| `core.lifecycle.start_timeout` | duration | `60s` per hook | no |
| `core.lifecycle.stop_timeout` | duration | `30s` per hook and for the worker wait | no |
| `core.lifecycle.shutdown_budget` | duration | `120s` total | no |
| `modules.<id>.*` | per module schema | per `config.Section` defaults | per field (05-config) |

## 6.9 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_kernel_module_info` | gauge (1) | `module`, `version`, `state` (`enabled`/`disabled`) |
| `mediacore_kernel_boot_phase_seconds` | histogram | `phase` (`P1`…`P8`) |
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
| `TestDisabledDependencyFails`, `TestUnknownModuleInConfigFails` | enablement errors |
| `TestMigrationFailureHaltsBoot` | G2 with a failing hello migration: no later module migrated, storage unchanged after checkpoint restore, previous binary's read path still decodes |
| `TestStartRollbackReverseOrder`, `TestStopReverseOrder`, `TestStopTimeoutAbandons` | hook ordering; uses `synctest` for timeouts |
| `TestWorkerPolicies` | `DegradeOnExit` vs `StopAppOnExit`, panic recovery |
| `TestRegistrarClosedAfterRegister` | late calls recorded |
| `TestDepsScoping` | write outside `m/hello/` refused; publish undeclared topic refused; secret outside `hello/` refused; metric name prefixed |
| `TestHelloEndToEnd` | §6.12 example: route permission, task under `-race` with `CPU()` workers, migration once, settings default/override, `/api/core/v1/modules` lists nav entry |
| `archtest.TestNoGlobals`, `archtest.NoInit` over `core/...` and `core/examples/...` | R5, A5 |

ABO (root module `internal/arch` or `cmd` tests):

| Test | Proves |
|---|---|
| `TestSecondModuleNoAudiobookEdit` | G6: `coretest.New(t, WithModules(books.New(…), hello.New()))` boots; the test file is the only change needed — enforced by building it in CI with a `git diff --name-only` check that the hello wiring PR touched no `internal/` audiobook package (plan acceptance test) |
| `TestBooksDisabledInert` | G1 for the real module |
| `archtest.NoKernelImports` | R2 |

## 6.11 Migration from ABO

### 6.11.1 Strangler order

1. **Wave K0** — land `module`, `kernel`, `app`, `coretest`, `examples/hello`; core boots with zero
   modules and with hello (G6/G7 at core level).
2. **Wave K1** — ABO's `serve` command calls `app.New(ctx, opts, books.New(legacy))`. `books.Register`
   builds today's `*server.Server` and mounts its gin engine with `Registrar.LegacyHandler` under the
   declared legacy prefixes (N7). `serviceregistry` keeps working **inside** books (it is app code; R5
   binds core only).
3. **Wave K2** — remove the global factory map: each of the 24 registering files replaces
   `func init(){ serviceregistry.Register(def) }` with `func ServiceDefs() []serviceregistry.ServiceDef`;
   `books` passes one explicit list to `serviceregistry.NewContainerFrom(defs...)`; `Register`,
   `ResetForTest`, `IncludeAll/IncludeGroup` deleted. This alone removes test-isolation defects 1–4 of
   raw/07 §6 and the `batchpoller` class of bug. Cost: 24 files, mechanical, one PR.
4. **Wave K3…** — replace clusters by plain constructors in `books/wire_*.go`, deleting their
   `ServiceDef` and their `Get[T]` call sites (113 sites, 31 files): generic services first (they move
   to core and arrive through `Deps`), then audiobook clusters. When the list is empty, delete
   `internal/serviceregistry` (1,157 lines incl. tests).

### 6.11.2 The 43 `serviceregistry` registrants

Rows are raw/07 §1.3. "core via Deps" means the service is replaced by a core service the kernel
builds; "books" means a plain constructor in the books module; final package placement is decided in
[04-package-mapping](04-package-mapping.md).

| Registrant(s) | Group | Destination |
|---|---|---|
| `eventbus` (`internal/plugin/register.go:14`) | core | core `events` via `Deps.Events` |
| `ophub`, `opregistry` (`internal/operations/registry/register.go:84,93`) | scheduler | core `tasks` (executor/hub in `internal/executor`) via `Deps.Tasks` |
| `updater`, `updatescheduler` (`internal/updater/register.go:60,76`) | scheduler | core `updater` (kernel-built, `Options.Release`) |
| `configupdate` (`internal/config/register.go:13`) | core | core `config` (DB override layer) |
| `filesystem` (`internal/fileops/register.go:13`) | core | core `library` via `Deps.Library` |
| `system`, `dashboard` (`internal/server/registry_wire.go:97`, `internal/sysinfo/register.go:12`) | core | core `health` diagnostics + a books dashboard widget for library counts |
| `metricsstore` (`registry_wire.go:276`) | core | core `metrics` persistence if kept (05-metrics decides); else deleted |
| `searchindex` (`internal/search/register.go:106`) | scheduler | books, on core `storage/index` via `Deps.Index` |
| `pebble-activitystore`, `activitystore`, `activity`, `activitywriter` (`internal/activity/register.go:37,75,105,120`) | activity | core (task history + `audit`; 04 decides the split); `DatabasePath` gate disappears because core always has storage |
| `embeddingstore`, `chromemstore`, `aijobsstore`, `aiscanstore`, `pipelinemanager`, `dedup`, `embedclient`, `llmparser`, `metadatascorer`, `metadatallmscorer` (`registry_wire.go:109,133,165,298,319,179`; `internal/ai/register.go:33,105,136,158`) | ai | books (`wire_ai.go`), on core `Deps.AI` for provider access; one `if cfg.AI.Enabled` replaces per-`Build` gating |
| `batchpoller` (`internal/server/batch_poller_register.go:18`) | none (never built) | books, constructed explicitly when AI batch is enabled — **fixes** the latent defect |
| `metadatastate`, `metafetch`, `olservice` (`internal/metafetch/register.go:12,22,35`) | core | books, on core `Deps.Metadata`/`Deps.HTTP` |
| `itunes` (`registry_wire.go:343`) | core | books |
| `work`, `audiobook`, `organize`, `quarantine`, `scan`, `merge`, `importpath`, `batch` (`internal/work/register.go:13`, `internal/audiobooks/register.go:12,21`, `internal/quarantine/register.go:14`, `internal/scanner/register.go:13`, `internal/merge/register.go:12`, `internal/importer/register.go:12`, `internal/batch/register.go:13`) | core | books |
| `metafetchplugin`, `itunesplugin`, `delugeplugin`, `dedupplugin`, `acoustidplugin` (`internal/plugins/*/register.go`) | plugins | books task definitions (`r.Task(...)`); deluge's client moves to core `integrations/download/deluge` (05-download) |

Count check by service name: eventbus 1, ophub/opregistry 2, updater 2, configupdate 1, filesystem 1,
system/dashboard 2, metricsstore 1, searchindex 1, activity 4, ai 10, batchpoller 1, metafetch 3,
itunes 1, domain 8, plugins 5 = **43**.

### 6.11.3 `internal/plugin` and `pkg/plugin/sdk`

| Today | Becomes |
|---|---|
| `internal/plugin.Plugin`, `Registry`, `Global()`, `Register` (`plugin.go:25`, `registry.go:19,33`) | deleted. The only implementor, webhook (`internal/plugins/webhook/plugin.go:33`), becomes the core `integrations/notify/webhook` adapter enabled in `Options.Adapters`, subscribing through `events` |
| `plugin.EventBus` (`internal/plugin/events.go:85`, goroutine per handler with the publisher's ctx) | core `events` (05-events: bounded dispatch, bus-lifetime ctx, as design Q1 decided) |
| `plugin.PluginRouter`, `/api/v1/plugins/{id}` (`plugins_init.go:42`) | deleted; module routes go through `Registrar.Route` inside the protected chain. The 6 `/api/v1/plugins` routes (raw/09 §1.1) become `/api/core/v1/adapters` (configured adapter instances, health) |
| `plugin.Capability`, vestigial `DownloadClient`/`MediaPlayer`/`Notifier` (`plugin.go:17-21,52-57`) | deleted; capabilities are `module.Capability` + the `integrations/*` contracts |
| `config.AppConfig.Plugins` enable flags | `core.modules.<id>.enabled` (modules) and adapter instance `enabled` (adapters); migration of the stored value in 09 |
| `pkg/plugin/sdk.Plugin`/`Registry` (`plugin.go:19`, `registration.go:12`) and its aliases | deleted after the six bundles become `tasks.Definition` lists in books; `PageBooks` (`iterate.go:41`) becomes a books helper over `tasks.RunItems`; `tools/cmd/sdkguard` replaced by `archtest` R2 |
| `pkg/ops` | promoted into core `tasks` (05-tasks, N9) with audiobook types removed |
| `internal/opscatalog` (`catalog.go:56`, `TestCatalogLinkedIntoBinary`) | deleted: explicit lists make "linked but unregistered" impossible |

### 6.11.4 The 111 non-test `func init()`

Classes from raw/07 §5.1.

| Class | Count | Replacement | Lands in |
|---|---|---|---|
| A `serviceregistry.Register` | 24 files / 43 calls | §6.11.2 (K2 explicit list, then constructors) | books / core |
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
a `coretest` harness (no global); the eight `gin.SetMode` disappear with gin; `gob.Register` and the
compile-time assertion stay.

### 6.11.5 Other ABO pieces

| Today | Becomes |
|---|---|
| `server.NewServer` (`server.go:653`) and `Server.Start` (`server_lifecycle.go:245`) | books `Register` (K1: wraps them; later: dissolves them) + kernel phases P1–P8 |
| `startup.Table` Fatal/Degraded steps (`internal/startup/steps.go`) | kernel phase table; Degraded steps become health checks registered by the owning service |
| `opRegistrationGate`, `opCatalogGate` (`server_lifecycle.go:66,88`) | P6/P7 validation |
| `scanner.SetStore(store)` (`server.go:805`) and similar setters | constructor parameters in books wiring |

## 6.12 Worked example: the `hello` module

Lives at `core/examples/hello` (public package, excluded from the API-stability gate) so both core CI
and ABO's G6 test can import it. Layout:

```
core/examples/hello/
  hello.go          Module, Manifest, Register
  greeting.go       service + route
  count.go          task hello.count
  migrations.go     migration 1
  cmd/hello/main.go app main
  cmd/hello/ui_embed.go / ui_nonembed.go
  web/              npm package @mediacore-examples/hello (07 §7.8)
```

### 6.12.1 Module, manifest, settings, migration

```go
// core/examples/hello/hello.go
package hello

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

const ID module.ID = "hello"

const (
	PermRead  rbac.Permission = "hello:read"
	PermAdmin rbac.Permission = "hello:admin"
)

// Settings is section modules.hello.
type Settings struct {
	Greeting   string `config:"greeting"     doc:"Prefix of every greeting"`
	MaxNameLen int    `config:"max_name_len" doc:"Longest accepted name" min:"1" max:"256"`
}

type Module struct {
	settings *config.Section[Settings]
}

func New() *Module {
	return &Module{
		settings: config.NewSection("modules.hello", Settings{Greeting: "Hello", MaxNameLen: 64}),
	}
}

func (m *Module) Manifest() module.Manifest {
	return module.Manifest{
		ID:             ID,
		Version:        "1.0.0",
		DisplayName:    "Hello",
		MinCoreVersion: "0.1.0",
		DefaultEnabled: true,
		Permissions: []rbac.PermissionSpec{
			{Name: PermRead, Title: "Read greetings",
				DefaultGrants: []rbac.RoleName{rbac.RoleViewer, rbac.RoleEditor}},
			{Name: PermAdmin, Title: "Run hello maintenance"}, // admin only (core:admin)
		},
		Migrations: []storage.Migration{
			{Version: 1, Name: "seed-names", Apply: seedNames},
		},
		Settings: []config.AnySection{m.settings},
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
		names:    d.Storage.Sub("name/"), // m/hello/name/<name> -> count
		settings: m.settings.Bind(d.Config),
		log:      d.Log,
	}
	r.Route(greetingRoute(svc))
	r.Task(countTask(svc))
	return r.Err()
}

// core/examples/hello/migrations.go
// seedNames writes the first name so the count task has something to count.
func seedNames(ctx context.Context, m storage.MigrationTx) error {
	if err := m.Set([]byte("name/world"), []byte("0")); err != nil {
		return fmt.Errorf("hello: seed: %w", err)
	}
	return nil
}
```

`config.NewSection` returns `*config.Section[Settings]`; `Bind(config.Reader)` returns a
`config.Value[Settings]` whose `Get()` is lock-free and reflects hot-reloaded fields (05-config).
`storage.MigrationTx` is the batch-scoped writer of 05-storage, already confined to `m/hello/`.

### 6.12.2 Route `GET /api/hello/v1/greeting`

```go
// core/examples/hello/greeting.go
package hello

type service struct {
	names    storage.Keyspace
	settings config.Value[Settings]
	log      *slog.Logger
}

type GreetingQuery struct {
	Name string `query:"name"`
}

type Greeting struct {
	Message string `json:"message"`
	Seen    int64  `json:"seen"`
}

func greetingRoute(s *service) httpapi.Route {
	return httpapi.Get("/greeting", PermRead, s.greet,
		httpapi.Summary("Greet a name and count how often it was greeted"))
}

func (s *service) greet(ctx context.Context, q GreetingQuery) (Greeting, error) {
	cfg := s.settings.Get()
	if q.Name == "" || utf8.RuneCountInString(q.Name) > cfg.MaxNameLen {
		return Greeting{}, httpapi.BadRequest("name", "must be 1..%d characters", cfg.MaxNameLen)
	}
	n, err := storage.Increment(ctx, s.names, []byte("name/"+q.Name), 1)
	if err != nil {
		return Greeting{}, fmt.Errorf("hello: count %q: %w", q.Name, err)
	}
	return Greeting{Message: cfg.Greeting + ", " + q.Name, Seen: n}, nil
}
```

`httpapi.Get[Q, R any](path string, perm rbac.Permission, h func(context.Context, Q) (R, error), opts ...httpapi.RouteOption) httpapi.Route`
(05-http-api) decodes `Q`, encodes `R`, maps errors to the core envelope, and contributes the
operation to the OpenAPI document. The route resolves to `/api/hello/v1/greeting`.

### 6.12.3 Task `hello.count`

```go
// core/examples/hello/count.go
package hello

type CountParams struct {
	Prefix string `json:"prefix"`
}

type CountResult struct {
	Names      int64 `json:"names"`
	Greetings  int64 `json:"greetings"`
}

func countTask(s *service) tasks.Definition {
	return tasks.Task("hello.count", tasks.TaskSpec[CountParams, CountResult]{
		Title:      "Count greeted names",
		Permission: PermAdmin,
		Effects:    tasks.ReadOnly(tasks.Resource{Module: string(ID), Kind: "name"}),
		Run: func(rc *tasks.RC, p CountParams) (CountResult, error) {
			var names, greetings atomic.Int64
			items := s.names.Scan(rc.Context(), storage.Prefix("name/"+p.Prefix)) // iter.Seq2[storage.KV, error]
			err := tasks.RunItems(rc, tasks.CPU(), items,
				func(ctx context.Context, kv storage.KV) error {
					n, err := strconv.ParseInt(string(kv.Value), 10, 64)
					if err != nil {
						return fmt.Errorf("hello: bad count at %q: %w", kv.Key, err)
					}
					names.Add(1)
					greetings.Add(n)
					return nil
				},
				tasks.Label(func(i, total int) string { // Label runs in worker goroutines:
					return fmt.Sprintf("%d names", names.Load()) // read only atomics (CLAUDE.md)
				}),
			)
			return CountResult{Names: names.Load(), Greetings: greetings.Load()}, err
		},
	})
}
```

`tasks.CPU()` is a `tasks.Parallelism` of `runtime.NumCPU()` workers; there is no zero-value
`Parallelism`, so omitting it does not compile into a sequential loop (R7, N8). The scan is an
`iter.Seq2`, so the task does not materialise the keyspace. Exact `RunItems` option names are owned by
[05-tasks](05-tasks.md).

### 6.12.4 Nav entry and settings panel (client side)

```ts
// core/examples/hello/web/src/index.ts
import { defineModule } from '@mediacore/shell';
import WavingHandIcon from '@mui/icons-material/WavingHand';
import { manifest } from './manifest.gen'; // generated from Manifest() — 07 §7.4

export const helloModule = defineModule(manifest, {
  routes: [{ id: 'greeting', path: 'greeting', lazy: () => import('./GreetingPage') }],
  nav: [{ id: 'hello', label: 'Hello', icon: WavingHandIcon, to: 'greeting', permission: 'hello:read' }],
  settingsPanels: [{ id: 'hello', label: 'Hello', lazy: () => import('./HelloSettings') }],
  permissionsHints: { view: ['hello:read'], actions: { runCount: 'hello:admin' } },
});
```

### 6.12.5 `main.go`

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
		ID:          "hello",
		DisplayName: "Hello",
		Version:     version,
		DataDir:     os.Getenv("HELLO_DATA_DIR"),
		UI:          uiBundle(), // ui_embed.go (embed_frontend) or ui_nonembed.go
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

// core/examples/hello/cmd/hello/ui_embed.go
package main

import (
	"embed"

	"github.com/falkcorp/mediacore/ui"
)

//go:embed all:web/dist
var webDist embed.FS

func uiBundle() ui.Bundle { return ui.MustFromFS(webDist, "web/dist") }
```

```go
//go:build !embed_frontend

// core/examples/hello/cmd/hello/ui_nonembed.go
package main

import "github.com/falkcorp/mediacore/ui"

func uiBundle() ui.Bundle { return ui.Bundle{} } // API-only placeholder
```

The SPA build is copied to `cmd/hello/web/dist` by the example's Makefile target before
`go build -tags embed_frontend` (07 §7.6).

### 6.12.6 Test

```go
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
		run := h.RunTask(t, "hello.count", hello.CountParams{})
		if run.State != tasks.StateSucceeded {
			t.Fatalf("count: %s", run.State)
		}
	})
}
```

## 6.13 Open items

| # | Item | Owner |
|---|---|---|
| O1 | Parallel hook start per topological level (boot-time optimisation) — only if measured boot > 5 s on a large library | 06 |
| O2 | Exact `storage.Migration`/`MigrationTx`, `tasks.TaskSpec`/`RunItems` options, `httpapi.Get` option names — this file follows the owning `05-*` files | 05-storage, 05-tasks, 05-http-api |
| O3 | Whether `Deps.Tasks` may enqueue another module's kinds without that module in `DependsOn` (current answer: yes, permission-checked; R3 says modules talk via tasks) | 05-tasks |
| O4 | `metricsstore` and activity-log destinations | 04, 05-metrics, 05-rbac |
| O5 | The `go`-statement warning list in `archtest` may be noisy for books during K1–K3; ratchet rather than fail | 06 / plan |
| O6 | Module-level settings that need a restart vs hot reload: per field in `config.Section` tags (`reload:"restart"`) | 05-config |

## Objections to 03

1. **"Service container" wording (03 §3.3 `kernel/...` row, §3.7 "build services (container, explicit,
   no init())").** This file concludes core needs **no generic DI container** (§6.3.1). Proposed
   amendment: `kernel/...` = "module catalog, lifecycle supervisor, typed core-services struct"; §3.7
   "build services (explicit typed construction, no init(), no container)". Behaviour is unchanged;
   the objection prevents a reader from re-creating `serviceregistry` under a new name.
2. **`core/examples/...` is not in the 03 tree.** Proposed addition: `examples/hello` — public,
   excluded from the API-stability gate, subject to R5 and `archtest`; needed by G6/G7 and by
   [07](07-ui-shell.md) for the shell e2e suite.
3. **R4 layer of `module`.** `module.Deps` references `integrations/*`, `library`, `audit`, `backup`
   and `health`; `module.Registrar` references `arrfacade`. All are in lower layers per R4, so no
   change — recorded to confirm the check was done.

## Out-of-scope defects found

| Defect | Anchor | Note |
|---|---|---|
| `batchpoller` registered without `Groups`, never built; `TryGet` always misses | `internal/server/batch_poller_register.go:18-20`, `internal/server/registry_wire.go:422` | already in raw/07 §1.3; re-verified. Fixed as a side effect of K2/K3 |
| `plugins_init.go` comment says blank-importing deluge registers with `plugin.Global()`; it registers via serviceregistry | `internal/server/plugins_init.go:16`, `internal/plugins/deluge/register.go:24` | stale comment (raw/07 §2) |
| `EventBus.Publish` hands each handler the publisher's ctx in an unbounded goroutine, contrary to the recorded design decision | `internal/plugin/events.go:85-103` | raw/07 §2; superseded by 05-events |
