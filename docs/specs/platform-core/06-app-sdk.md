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
| A1 | A module is `Manifest()` (pure data, validated before storage opens) **plus** `Register(*Registrar)` (runs after migrations and config load, receives typed `Deps`). **Rule: anything the kernel needs before services exist lives in the `Manifest`; anything that needs a service lives in `Register`.** So dependencies, permissions/roles/grants, storage spec (keyspace, legacy claims, migrations), library roles, settings sections, legacy HTTP prefixes and UI descriptor IDs are manifest fields; routes, tasks, resources, topics, subscriptions, adapters, health/diagnostics, hooks, workers and the *arr provider are registrar calls. | 03 §3.7 requires validation before listeners, migrations before services, and 05-config requires sections to be registered before the config store loads, while route handlers need migrated storage and loaded config. One `Register` cannot satisfy both. Cost: two places to look; mitigated by the rule being mechanical. |
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
| Test seam | `Override(name, instance)` | `coretest` options replace whole core services (`coretest.WithConfig`, `WithAdapters`, fakes) and a module's own constructors take interfaces |

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

	"github.com/falkcorp/mediacore/arrfacade"
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
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
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
	// have run, config is loaded, and every module this one DependsOn has
	// registered. It constructs the module's services from r.Deps() and records
	// routes, tasks, topics, adapters, health checks, hooks and workers.
	// A returned error aborts boot (fail-closed).
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
	// migrations, Register and Start follows this graph (topological, ties by ID).
	DependsOn []ID

	// Requires lists integration kinds the module needs at runtime.
	// Required=false entries only drive the settings UI and health text.
	Requires []CapabilityRequirement

	// RBAC declarations (05-rbac §3.2): permissions in this module's namespace,
	// default roles, grants of own scopes to core seed roles, and legacy
	// permission-string aliases. Fed by the kernel to rbac's Declarer.
	Permissions   []rbac.PermissionDef
	Roles         []rbac.RoleSeed
	Grants        []rbac.Grant
	LegacyAliases []rbac.LegacyAlias

	// Storage is the module's storage declaration (05-storage §5S.3): its
	// m/<ID>/ keyspace, legacy claims (books: ABO's 258 families), versioned
	// migrations (run before Register, inside a checkpoint — G2), data
	// migrations, derived indexes and compat adapters.
	Storage storage.Spec

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

	// LegacyPrefixes are un-namespaced HTTP subtrees the module occupies for a
	// compatibility window (09); each becomes an httpapi.PrefixClaim of kind
	// Legacy, validated against core, other modules and facades
	// (05-http-api §4.9). Only paths listed here may be passed to MountLegacy.
	LegacyPrefixes []string
}

// CapabilityRequirement names an integration kind the module uses.
type CapabilityRequirement struct {
	Kind     integrations.Kind // "download", "indexer", "metadata", "ai", "notify" (05-metadata M1)
	Required bool              // true: boot fails when no instance of this kind is configured
	Why      string            // shown in settings and in the boot error
}

// UIDescriptor lists the IDs of client-side contributions (07 §7.4).
type UIDescriptor struct {
	Pages          []string // route IDs, e.g. "library", "book"
	Nav            []string // nav entry IDs
	SettingsPanels []string // settings panel IDs
	Widgets        []string // optional home/system/diagnostics widgets
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

Default grants are expressed the 05-rbac way: a module grants its own scopes to core seed roles
(`rbac.Grant{Role: "core.viewer", Scopes: []rbac.Scope{"hello:read"}}`) and may seed its own roles
(`books.editor`); it can never grant `core:*` (05-rbac §4.9).

### 6.4.3 `module.Registrar`

```go
// Registrar is handed to Module.Register. It is not goroutine-safe and is
// closed when Register returns; calls after that record ProblemRegistrarClosed.
// Methods record problems rather than return errors (A4); Err reports them.
type Registrar struct{ /* unexported */ }

// Deps returns the module-scoped dependency handles. Every field is non-nil.
func (r *Registrar) Deps() Deps

// HTTP returns the module's route group rooted at /api/<ID>/v1, with the full
// core chain applied (05-http-api §3.1). Register routes with
// httpapi.Handle(r.HTTP(), httpapi.Route{...}, fn). Every route needs Perm (R6).
func (r *Registrar) HTTP() *httpapi.Group

// MountLegacy mounts an http.Handler (books: the gin engine during migration,
// N7) on prefixes declared in Manifest.LegacyPrefixes (05-http-api §8.2).
// m.Owner is set by the registrar.
func (r *Registrar) MountLegacy(m httpapi.LegacyMount)

// Tasks registers task definitions; kinds must be "<ID>.<name>". Schedules are
// part of each definition (tasks.Common.Schedule, 05-tasks 5T.3.8).
func (r *Registrar) Tasks(defs ...tasks.Definition)

// Resources declares the resource kinds this module owns (G8, 05-tasks 5T.3.4).
func (r *Registrar) Resources(kinds ...string)

// Lanes declares remote work lanes (05-tasks 5T.3.9).
func (r *Registrar) Lanes(l ...tasks.Lane)

// Topics declares bus topics this module publishes; OutboxTopics declares
// durable topics (05-events). Subscriptions and outbox consumers are made in
// Register itself with events.Subscribe(d.Events, …) / outbox.Consume(d.Outbox, …);
// the kernel freezes both when Register returns.
func (r *Registrar) Topics(t ...events.TopicSpec)
func (r *Registrar) OutboxTopics(t ...events.TopicSpec)

// Adapter registers an integration adapter the module ships (05-metadata M1.3);
// its ID prefix must equal the module ID. Core adapters are enabled in
// app.Options.Adapters instead.
func (r *Registrar) Adapter(d integrations.Descriptor)

// Kind-specific integration declarations, owned by their contracts.
func (r *Registrar) AICapability(s ai.CapabilitySpec)        // 05-ai
func (r *Registrar) AIBatchHandler(h ai.BatchHandler)        // 05-ai
func (r *Registrar) NotificationTopic(s notify.TopicSpec)    // 05-notify
func (r *Registrar) DownloadWatch(w download.Watch)          // 05-download

// Health, startup steps and diagnostics (05-health).
func (r *Registrar) Health(c ...health.Check)
func (r *Registrar) StartupSteps(s ...health.StartupStep)
func (r *Registrar) Diagnostics(c health.Contributor)

// Backup declares module-owned artifacts outside storage (05-backup).
func (r *Registrar) Backup(a ...backup.Artifact)

// ArrFacade registers this module's *arr-v3 provider (08). At most one per module.
func (r *Registrar) ArrFacade(p arrfacade.Provider)

// Hook registers a Start/Stop pair. Hooks of one module run in registration
// order on start and reverse order on stop.
func (r *Registrar) Hook(h Hook)

// Worker registers a supervised long-running goroutine, started after all
// hooks have started and stopped (ctx cancelled) before any hook stops.
func (r *Registrar) Worker(w Worker)

// Err returns the problems recorded so far, joined; nil if none.
func (r *Registrar) Err() error

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
// by the kernel and safe for concurrent use. None is ever nil; a capability
// with no configured instance returns integrations.ErrNotConfigured (or the
// kind package's equivalent) from its lookups instead.
type Deps struct {
	Module ID

	Log     *slog.Logger  // child of the app logger with attr module=<ID> (05-logging)
	Metrics metrics.Scope // registers <app>_<ID>_* on the App's registry, never the global one (05-metrics)

	Storage storage.Handles // Keyspace m/<ID>/, Legacy claims, Indexes (05-storage)

	Tasks  tasks.Starter  // start/observe runs of any kind, permission-checked as the caller (05-tasks 5T.3.10)
	Events *events.Bus    // publish only declared <ID>.* topics; subscribe to any (05-events)
	Outbox *outbox.Outbox // same-batch durable messages and consumers (05-events)

	Secrets secrets.Scope    // names under "<ID>/"; values never logged (05-config part 2)
	Library *library.Manager // os.Root-confined media roots (05-library)

	Auth  auth.Identities // principal from ctx, user lookup (05-authn)
	RBAC  rbac.Checker    // Can/Require against the request principal (05-rbac)
	Audit audit.Recorder  // append-only, actor from ctx (05-rbac)

	Health *health.Registry // push-style component status (05-health)

	HTTP         providerhttp.Factory   // instance-scoped outbound clients (05-metadata M2)
	Integrations *integrations.Registry // configured instances of every kind; typed access via
	                                    // integrations.Lease[T] and the kind packages (05-metadata M1)
}

// ErrStopped is returned by any handle used after the app stopped.
var ErrStopped = errors.New("module: app stopped")
```

`var ErrStopped` is an exported package-level variable of interface type `error`; R5 forbids exported
map/slice/func/pointer variables, not sentinel errors, and `TestNoGlobals` allowlists
`var Err… = errors.New(…)`. Settings are not in `Deps`: a module reads its own sections through
`module.Setting(r, section)` handles obtained in `Register`, and core's public settings through the
core service handles that need them.

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

	// Adapters enables core adapters (e.g. deluge.Descriptor(), webhook.Descriptor());
	// only enabled adapters may be configured; instances come from config (05-metadata M1.3).
	Adapters []integrations.Descriptor

	// Logger overrides the default slog handler construction (tests); nil = from config.
	Logger *slog.Logger

	// Release identifies the update source for core/updater (05-updater).
	Release updater.Source
}

type App struct{ /* unexported: kernel.Kernel */ }

// New validates every manifest, opens storage, runs core then module
// migrations, loads config, builds core services, calls Register on each
// enabled module in dependency order, and builds the HTTP handler. It does not
// start goroutines or listeners. On error nothing is left open.
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

// Describe runs phases P1 and P6 against in-memory storage (no DataDir, no
// listeners, every module treated as enabled) and returns the app's OpenAPI
// 3.1 documents and each module's UI manifest. Used by an app's "openapi"
// subcommand (05-http-api §7, 07 §7.8) and by contract tests.
func Describe(ctx context.Context, opts Options, mods ...module.Module) (Description, error)

type Description struct {
	OpenAPI     map[string][]byte // per owner ("core", module IDs), JSON (05-http-api Router.OpenAPI)
	UIManifests []UIManifest      // per module: ID, Version, DisplayName, UI IDs, permission scopes
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
	ProblemSettingsUndeclared
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
func WithAdapters(d ...integrations.Descriptor) Option // e.g. fakes from coretest/fakes
func WithUser(name string, scopes ...rbac.Scope) Option // pre-seeded principal

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
func AssertPermission(t testing.TB, h *Harness, method, path string, perm rbac.Scope)

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
| P4 | For each **enabled** module in topological order: checkpoint, run its pending `Storage.Migrations` (G2) | fail-closed; nothing of later modules runs |
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
| Adapter descriptor invalid (ID outside module prefix, duplicate, secret field not marked) | fail-closed (`ProblemAdapter`, 05-metadata M1.4) | a bad adapter must not fail at first use |
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
   builds today's `*server.Server` and mounts its gin engine with `Registrar.MountLegacy` under the
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
	PermRead  rbac.Scope = "hello:read"
	PermAdmin rbac.Scope = "hello:admin"
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
		MinCoreVersion: "0.1.0",
		DefaultEnabled: true,
		Permissions: []rbac.PermissionDef{
			{Scope: PermRead, Title: "Read greetings"},
			{Scope: PermAdmin, Title: "Run hello maintenance"},
		},
		// Default grant: viewers may read; hello:admin stays with core:admin holders.
		Grants: []rbac.Grant{{Role: "core.viewer", Scopes: []rbac.Scope{PermRead}}},
		Storage: storage.Spec{
			Keyspace: true, // m/hello/
			Migrations: []storage.Migration{
				{Version: 1, Name: "seed-names", Compat: storage.Additive, Up: seedNames},
			},
		},
		Settings: []module.SettingsSection{module.Settings(m.settings)},
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

// core/examples/hello/migrations.go
// seedNames writes the first name so the count task has something to count.
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
		return Greeting{}, httpapi.Invalid("name", fmt.Sprintf("must be 1..%d characters", cfg.MaxNameLen))
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
the core problem envelope, enforces `Perm` (R6) and contributes `hello_getGreeting` to the OpenAPI
document. The route resolves to `/api/hello/v1/greeting`. `httpapi.Invalid` stands for the
field-validation error constructor of 05-http-api.

### 6.12.3 Task `hello.count`

```go
// core/examples/hello/count.go
package hello

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
			Perms:   tasks.Permissions{Start: PermAdmin},
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
the zero `Parallelism` is invalid, so a forgotten value fails registration instead of silently running
one worker (R7, N8 — the exact failure CLAUDE.md records for `RunItemsOptions.Concurrency`). Counters go
through `ItemRC.Count` and `Label` receives only the item, so there is no shared mutable state in worker
goroutines. (A library-scale source would use `tasks.RunSeq` over the scan instead of collecting it.)

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
			coretest.WithUser("a", rbac.Scope("core:admin")))
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

## 6.14 Alignment with the parallel contract files

This file owns `module.Manifest`, `module.Registrar`, `module.Deps` and `app`. The `05-*` files were
written in parallel; where they name a registration path, this file adopts it unless it conflicts with
boot ordering:

| Contract | What 06 adopts | Reconciliation |
|---|---|---|
| 05-http-api §3.1, §4.9, §8.2 | `Registrar.HTTP() *httpapi.Group`, `httpapi.Handle`, `MountLegacy(httpapi.LegacyMount)`, prefix claims, `Router.FacadeGroup` for 08 | `Manifest.LegacyPrefixes` feeds `PrefixClaim`s so collisions are found in P1, before storage opens |
| 05-storage §5S.3 | `Manifest.Storage storage.Spec`; `Deps.Storage storage.Handles` | — |
| 05-tasks 5T.3 | `Registrar.Tasks`, `Resources`, `Lanes`; schedules inside `tasks.Common`; `Deps.Tasks tasks.Starter`; `tasks.CPU()`, `RunItems(rc, par, items, fn, opts)` | — |
| 05-events | `Registrar.Topics`, `OutboxTopics`; subscriptions/consumers made in `Register` via `Deps.Events`/`Deps.Outbox` | 05-events mentions `Registrar.Consumers`; calling `outbox.Consume(d.Outbox, …)` during `Register` is equivalent (registration happens at app construction and is frozen when `Register` returns) and avoids a closure-taking registrar method |
| 05-rbac §3.2 | `PermissionDef`, `RoleSeed`, `Grant`, `LegacyAlias`, `Scope`, `Requirement` | 05-rbac describes a `Declarer` reached via `Registrar.Permissions()`; 06 puts the same four lists in the **Manifest** (05-rbac's own text says `PermissionDef` "is declared in a module manifest") so namespace and grant rules are checked in P1; the kernel feeds them to the `Declarer` |
| 05-config §1.3 | `config.Define`, `Section[T]`, `Handle[T]`, `config.Register` | 05-config hands modules `Registrar.Config()` with "registration legal only before Store.Load"; since `Register` needs loaded config, sections are listed in `Manifest.Settings` and registered by the kernel in P3 before load; `module.Setting(r, s)` returns the bound handle |
| 05-metadata M1.3 (objection 2) | `Registrar.Adapter(integrations.Descriptor)`, `app.Options.Adapters []integrations.Descriptor`, `Deps.Integrations` | adopted as proposed |
| 05-ai, 05-notify, 05-download | `AICapability`, `AIBatchHandler`, `NotificationTopic`, `DownloadWatch` | signatures owned by those files |
| 05-health | `Health`, `StartupSteps`, `Diagnostics`; `Deps.Health *health.Registry` | — |
| 05-library | `Manifest.Library []library.RoleRequirement`; `Deps.Library *library.Manager` | — |

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
