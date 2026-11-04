<!-- file: docs/specs/platform-core/06-app-sdk.md -->
<!-- version: 1.1.0 -->
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
are specified, but no S0–S11 stage builds them. The `module` package therefore cannot import them in
v1. Every SDK member that depends on one of them is marked **deferred-build** below. It is added
additively, with no change to existing fields, in the wave that builds the package (§6.4.9).

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
| A1 | A module is `Manifest()` (pure data, validated before storage opens) **plus** `Register(*Registrar)` (runs after migrations and config load, receives typed `Deps`). **Rule: anything the kernel needs before services exist, or while the module is disabled, lives in the `Manifest`; anything that needs a service lives in `Register`.** So permissions/roles, storage spec (keyspace, legacy claims, migrations), library roles, settings sections, its outbox consumer names, its task-ID ledger, legacy HTTP prefixes and UI descriptor IDs are manifest fields. Routes, tasks, resources, topics (the one catalog, SIMP-5), subscriptions, outbox handlers, change sinks, adapters, health/diagnostics, backup artifacts, hooks and workers are registrar calls. | 03 §3.7 requires validation before listeners, migrations before services, and 05-config requires sections to be registered before the config store loads, while route handlers need migrated storage and loaded config. One `Register` cannot satisfy both. Outbox consumer names and the task-ID ledger are manifest data so that a disabled module's messages are retained (COR-12) and its persisted runs resolve as held (COR-13). Cost: two places to look; mitigated by the rule being mechanical. |
| A2 | **No generic DI container in core.** Core's own ~30 services are built by hand-written, typed code in `kernel/services` (a struct with one field per service, constructed in a fixed order). A module builds its internal services with plain Go constructors inside `Register`. | Evaluated below (§6.3.1). |
| A3 | `Deps` is a **struct of typed, module-scoped handles**, never `any`, never nil. Each handle's **type** carries the module identity, so scoping is enforced by the owning package and cannot be spoofed by the caller: `events.Scope` publishes only the module's declared topics, `library.Scope` carries the module's owner, `tasks.Client` stamps the actor from ctx, `secrets.Scope` reads only `modules.<id>.` names, `*metrics.Scope` prefixes the module's metric names (03 §3.6 `MetricPrefix`, §6.4.4). A shared pointer to a whole service never appears in `Deps`. | Replaces 113 `Get[T]` string lookups with compile-time types; a scope type is the only place R3/R8 can be enforced for a handle (COR-21). |
| A4 | Registrar methods **record problems instead of returning errors** (like `testing.T`); the kernel joins every problem of every module into one `*ValidationError`. | An operator sees all misconfigurations at once, not one per restart. |
| A5 | Module code runs only through the kernel: no `init()`, no package-level mutable state, background goroutines only via `Registrar.Worker`. A disabled module's `Register`, migrations, hooks and workers are **never invoked**. | This is what makes G1 a structural property rather than a per-feature check. Enforced by `archtest.NoInit` + `archtest.TestNoGlobals` over module packages, and by `coretest.AssertModuleInert`. |
| A6 | Enablement is restart-bound (`core.modules.<id>.enabled`) and fail-closed. It is resolved in P1 from `config.Store.Boot` (operator layers plus the pre-storage boot record, 05-config §1.3.5), the read that exists before storage opens and before anything that depends on enablement (P3 section registration, P4 migrations, P6 `Register`) (§6.5.3, COR-14). There are no inter-module dependencies: `DependsOn` was dropped because R3 forbids module imports and no planned module needs another (SIMP-22). | Old decision 10 (02 §2.2); `ServeMux` routes cannot be unmounted. |
| A7 | Tests use `testing/synctest` for time; core has no `Clock` interface in `Deps`. | Go 1.25+ virtualises `time` inside a bubble; a clock abstraction would be a second, weaker mechanism. |
| A8 | **Construction is acyclic: services are built before `Register`, registrations are consumed after it.** P5 builds every core service a `Deps` handle wraps. P6 runs each `Register`, which only records. P7 hands the recorded registrations to their services: task definitions to the executor, topics to the catalog, subscriptions to the bus, outbox handlers to the relay, legacy projections to the realtime hub, change sinks to the task writer, adapters to the integrations registry, health checks to the health registry. No service is built inside `Register`, and no registration takes effect before every module has registered. | COR-1: the acquire service was needed inside `Register` but was to be built only when `Register` declared a media type. Under A8 the trigger is manifest data (`Manifest.Acquire`, deferred-build), so P5 can build the service and P6 only attaches typed hooks. |

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
already says so in its `kernel/...` row; see "Objections to 03" for the §3.7 diagram wording.

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
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	// deferred-build (§6.4.9): acquire, arrfacade, integrations/indexer
)

// ID is a module identifier: ^[a-z][a-z0-9]{1,15}$ and not in ReservedIDs().
// ID.Valid reports both.
type ID string

// Valid reports whether id matches the naming rule and is not reserved.
func (id ID) Valid() bool

// ReservedIDs returns the union of httpapi.ReservedSegments (server paths),
// ui.ReservedSegments (shell routes, 07 §7.4.3) and rbac.ReservedNamespaces
// ("core", "custom") (03 §3.6). The kernel also refuses every segment in
// app.Options.ReservedAPINamespaces. A fresh slice per call (R5); a test
// asserts the union equals 03's row.
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
	// A returned error aborts boot (fail-closed).
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
	// ("<ID>.<name>", 05-rbac §3.3) and legacy permission-string aliases. The
	// kernel feeds them to rbac's Declarer in P1.
	Permissions   []rbac.PermissionSpec
	Roles         []rbac.RoleSeed
	LegacyAliases []rbac.LegacyAlias

	// TaskLedger lists every task kind this module has ever registered,
	// including FormerKinds ("<ID>.<name>"). It is readable while the module
	// is disabled, so 05-tasks classifies the module's persisted runs and
	// op-ID ledger lines as held instead of failing boot (5T.3.11, COR-13).
	// A registered kind missing from it is a problem (ProblemTaskLedger).
	TaskLedger []tasks.Kind

	// OutboxConsumers names every outbox consumer the module may register
	// ("<ID>.<name>"). They are known while the module is disabled, so the
	// relay keeps retaining messages for them (05-events, COR-12); an
	// OutboxHandler whose consumer name is not listed here is a problem.
	OutboxConsumers []string

	// Storage is the module's storage declaration (05-storage §5S.3): its
	// m/<ID>/ keyspace, legacy claims (books: ABO's 258 families), versioned
	// migrations (run before Register, under the boot's single pre-migration
	// checkpoint, G2), data migrations, derived indexes and read adapters.
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

	// LegacyPrefixes are un-namespaced HTTP subtrees the module occupies
	// (09); each becomes a subtree claim. httpapi.Router.Build refuses any two
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
func (r *Registrar) MountLegacy(m httpapi.LegacyMount)

// Tasks registers task definitions; kinds must be "<ID>.<name>". Schedules are
// part of each definition (tasks.Common.Schedule, bound to a config key with
// tasks.ScheduleFromConfig, 05-tasks 5T.3.8); there is no second timer API.
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
// topics with Realtime metadata are streamed without any call here; the
// registrar's Redact(topic, fn) attaches a per-subscriber redactor to one of
// the module's OWN Realtime topics, and the books module attaches its legacy
// stream projections (Hub.Projection, 05-realtime §4.9). It never declares a
// topic name or permission (SIMP-5).
func (r *Registrar) Realtime() realtime.Registrar

// Adapter registers an integration adapter the module ships (05-metadata M1.3);
// its ID prefix must equal the module ID. Core adapters are enabled in
// app.Options.Adapters instead.
func (r *Registrar) Adapter(d integrations.Descriptor)

// AI declarations, signatures owned by 05-ai.
func (r *Registrar) AICapability(spec ai.CapabilitySpec) ai.Capability // zero handle on error
func (r *Registrar) AIBatchHandler(c ai.Capability, h ai.BatchHandler)

// Health, startup steps and diagnostics (05-health).
func (r *Registrar) Health(c ...health.Check)
func (r *Registrar) StartupSteps(s ...health.StartupStep)
func (r *Registrar) Diagnostics(c health.Contributor)

// Backup declares module-owned artifacts outside storage (05-backup). They are
// registrar calls, not manifest data, because their Snapshot/Restore hooks
// close over services built in Register.
func (r *Registrar) Backup(a ...backup.Artifact)

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
//   func (r *Registrar) DownloadWatch(w ...download.Watch) // 05-download monitor pipeline

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

	Storage storage.Handles // Keyspace m/<ID>/, Legacy claims, Indexes, Aux opener (05-storage)

	Tasks  tasks.Client  // start/observe runs of any kind by Kind string (R3); actor stamped by the kernel (05-tasks 5T.3.10)
	Events events.Scope  // publish only this module's catalog topics (subscriptions: Registrar.Subscribe) (05-events)
	Outbox outbox.Writer // stage durable messages into the module's own batches (05-events)

	Secrets secrets.Scope // names under "modules.<ID>."; values never logged (05-config part 2)
	Library library.Scope // roots, FS(rootID), Transfer/TransferIn with the module's owner implied (05-library)

	Auth  auth.Identities // read view: user lookup by ID/username, principal from ctx (05-authn)
	Audit audit.Logger    // Record/RecordIn/Emit; actor from ctx (05-rbac §3.5)

	Health health.Reporters // push status for the module's own declared checks only (05-health)

	HTTP         *providerhttp.Registry // instance-scoped outbound clients (05-metadata M2)
	Integrations *integrations.Registry // the ONE integration-instance store (SIMP-4): configured
	                                    // instances of every kind; typed access via
	                                    // integrations.Lease[T] and the kind packages (05-metadata M1)
	Downloads *download.Pool  // client pool: Items, Files, Relocate, Remove (05-download; ABO's path update)
	AI        *ai.Router      // provider routing for the module's AI capabilities (05-ai)
	Backup    backup.Ensurer  // Ensure only: "archive unless one verified within maxAge" (05-backup)

	// Deferred-build (§6.4.9):
	//   Acquire  *acquire.Service // 10; built in P5 when an enabled manifest sets Acquire
	//   Indexers *indexer.Pool    // 05-indexer
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
| `Metrics` | `*metrics.Scope` | 05-metrics §7 (defined) | module series named `<MetricPrefix>_*` while the app hosts one module, `<MetricPrefix>_<ID>_*` with two or more; `MetricPrefix` is `app.Options.MetricPrefix` (03 §3.6) |
| `Storage` | `storage.Handles` | 05-storage §5S.3.4; `Aux storage.AuxOpener` field requested (§5S.3.10 already says "exposed to modules through module.Deps") | writes outside `m/<ID>/` and the module's claims refused (R8) |
| `Tasks` | `tasks.Client` | 05-tasks 5T.3.10 (defined) | the kernel overwrites `StartRequest.Actor` with the ctx principal, or with the module's system actor outside a request, so a module cannot start a run as another actor (rule requested in 05-tasks) |
| `Events` | `events.Scope` | 05-events (requested: 05-events v1.1.0 hands modules `*events.Bus` and says `Publish` refuses another module's topic, which a shared `*Bus` cannot know; a scope carrying the module ID is what makes that rule enforceable) | `Publish` refuses topics outside this module's catalog entries |
| `Outbox` | `outbox.Writer` | 05-events §5E.4 (defined) | stages only into topics this module declared `Durable` |
| `Secrets` | `secrets.Scope` | 05-config §2.3 (requested, SEC-29) | prefix `modules.<ID>.`; integration-instance secrets are reachable only through `integrations.Env.Secrets`, never through a module scope |
| `Library` | `library.Scope` | 05-library (requested; today `Manager` + caller-supplied `Root.FS(caller)`) | owner is the module, so "role written by its owner only" cannot be spoofed |
| `Auth` | `auth.Identities` | 05-authn §3.1 (requested: the read-only subset of `*auth.Service`: `User`, `UserByUsername`, `Users`, `PrincipalFrom`) | no credential or session mutation |
| `Audit` | `audit.Logger` | 05-rbac §3.5 (defined) | actor from ctx; action namespace `<ID>.` checked |
| `Health` | `health.Reporters` | 05-health (requested: `Reporter(id ComponentID) (Reporter, error)` for IDs this module registered) | cannot report another component's status |
| `HTTP` | `*providerhttp.Registry` | 05-metadata (defined) | instance-scoped throttles and quotas (R5) |
| `Integrations` | `*integrations.Registry` | 05-metadata M1 (defined) | adapter IDs outside the module prefix refused at registration |
| `Downloads` | `*download.Pool` | 05-download (defined) | grab/monitor methods are deferred-build (SIMP-12) |
| `AI` | `*ai.Router` | 05-ai (defined) | only capabilities the module declared |
| `Backup` | `backup.Ensurer` | 05-backup (requested: the `Ensure` method of `Service` as its own interface) | no restore or prune |

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
	// ReservedAPINamespaces are top-level segments the app's compatibility
	// surfaces occupy (ABO: the ABS unversioned /api/* segments); added to
	// module.ReservedIDs() so no module can take them (03 §3.6).
	ReservedAPINamespaces []string
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

	// UI is the app's embedded SPA (07 §7.6). The zero Bundle serves the
	// API-only placeholder, unless a module's LegacyMount sets ServesSPA, in
	// which case "/" belongs to that mount and ui.Handler is not mounted
	// (FE-1). A non-zero UI together with a ServesSPA mount is a boot error.
	UI ui.Bundle

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

// New runs boot phases P0–P8 (§6.5.1): pending restore swap, manifest
// validation, storage open, migrations, config load, enablement, core
// services, Register of each enabled module, and the HTTP handler.
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

// Describe runs phases P1 and P6 against in-memory storage (no DataDir, no
// listeners, every module treated as enabled) and returns the OpenAPI 3.1
// documents and each module's UI manifest. Used by an app's "openapi"
// subcommand (05-http-api §7, 07 §7.8) and by contract tests.
func Describe(ctx context.Context, opts Options, mods ...module.Module) (Description, error)

type Description struct {
	Core        []byte            // core's OpenAPI document; emitted ONLY by core/examples/hello's "openapi" subcommand (FE-20)
	OpenAPI     map[string][]byte // per module ID, JSON (05-http-api Router.OpenAPI)
	UIManifests []ui.ModuleManifest // per module: ID, Version, DisplayName, UI IDs, permission names (07 §7.6.1)
}
```

Signal handling stays in `main` (`signal.NotifyContext`), so `Run` is testable and a host that embeds
the app controls its own lifecycle. ABO keeps its cobra command tree; its `serve` command calls
`app.New` + `Run` (§6.11). `core.json` has a single producer so that apps cannot emit diverging copies:
hello's `openapi` subcommand writes `Description.Core` to
`core/web/packages/api-client/openapi/core.json`, freshness-gated in core CI. An app's subcommand
writes only its modules' documents (07 §7.8).

### 6.4.6 `kernel` (shape only; apps never import it)

| Package | Responsibility |
|---|---|
| `kernel/catalog` | manifest validation, enablement resolution (P1, from `config.Store.Boot`), collision checks across modules and core (IDs against `module.ReservedIDs()`, permissions, legacy atoms, keyspace and legacy claims, config sections, task kinds, topic names, outbox consumer names, legacy path prefixes, facade URL bases, UI IDs), `MinCoreVersion` against `version.Version` |
| `kernel/services` | `type Services struct{ Storage *storage.DB; Tasks tasks.Executor; … }` built by `func Build(ctx, opts, cfg) (*Services, error)` in a fixed, hand-written order; `Close` in reverse |
| `kernel/supervisor` | start/stop ordering of §6.5.2, hook start/stop, worker supervision (`sync.WaitGroup.Go`), panic recovery, timeouts, shutdown budget |
| `kernel` | `Kernel` = catalog + services + supervisor; `Registrar`/`Deps` construction per module (the scope types of §6.4.4); hands registrations to services in P7; mounts core capability routes |

Core capability routes (`/api/core/v1/tasks`, `/roles`, `/audit`, `/events`, `/outbox`, …) live in
the per-capability route packages `internal/<capability>http` of 03 §3.3 (e.g. `internal/taskhttp`,
`internal/rbachttp`, `internal/audithttp`) at R4 L6. Each imports its capability and `httpapi`, and
is wired onto `Router.CoreGroup()` only by the kernel (COR-18). The module catalog's own
route, `GET /api/core/v1/modules` (07 §7.5.1), is one of them (`kernel/catalog` supplies the data,
the route package renders it). The `ui` package (L6) registers its own `ui/diagnostics` and
`ui/csp-report` routes the same way.

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
// os.Root DataDir, no listeners; requests go straight to App.Handler().
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

Added additively in the wave that builds the package (03 §3.3 "public, later"; cross-cutting
resolution 1). Until then the `module` package does not import these packages, and no core v1
symbol refers to them.

| Member | Package | Purpose |
|---|---|---|
| `Manifest.Acquire *AcquireDecl{Vocabulary, Categories}` | `acquire` (10) | manifest trigger for building the acquire service in P5 (A8) |
| `Deps.Acquire *acquire.Service` | `acquire` (10) | `acquire.Register[T](d.Acquire, mediaType)` in `Register` attaches typed hooks only |
| `Deps.Indexers *indexer.Pool` | `integrations/indexer` (05-indexer) | `SearchAll`, `RSS`, `Fetch`, `Caps` for module search tasks |
| `Registrar.ArrFacade(arrfacade.Provider)` | `arrfacade` (08) | at most one per module; checks in P7 (`ProblemFacadeIncomplete`, `ProblemFacadeURLBaseCollision`) |
| `Registrar.DownloadWatch(...download.Watch)` | `integrations/download` monitor (05-download) | the grab/monitor pipeline; ABO does not use it (SIMP-12) |

## 6.5 Lifecycle

### 6.5.1 Boot (inside `app.New`)

| Phase | Step | On failure |
|---|---|---|
| P0a | Pre-boot: if 05-backup's restore journal marks a staged restore, perform its swap now, before any config file or storage is read. Only the data directory is resolved, from flags/env (05-backup §5B.3.4, DATA-23) | per 05-backup: automatic swap-back and boot degraded with the restore marked failed |
| P0 | `config.NewStore`; register core sections; `Store.Boot` resolves core sections from file → env → flags and the boot record `<state_dir>/config.boot.json` (05-config §1.3.5, §1.4 step 2); DB overrides are applied in P3 | fail-closed (an unparsable boot record is quarantined, fail-open, per 05-config) |
| P1 | `Manifest()` of **every** module, enabled or not; `kernel/catalog` validates IDs against `module.ReservedIDs()`, `MinCoreVersion` against `version.Version`, namespaces, outbox consumer names, the task-ID ledger, cross-module and core collisions (a disabled module's names and claims stay reserved, so every manifest is checked). Resolve enablement from the `BootView`: `core.modules.<id>.enabled`, default `DefaultEnabled` (§6.5.3) | all problems joined → `*ValidationError` |
| P2 | Open storage under `DataDir` (`os.OpenRoot`); verify keyspace and legacy claims against the persisted claim records `c/storage/claims/<owner>` (05-storage, DATA-28): claims on disk but absent from the build are orphaned until an explicit adoption migration, never silently re-owned | fail-closed |
| P3 | Register the settings sections of every **enabled** module, then `Store.Load` (seals the registry; DB overrides applied; 05-config §1.4 step 3). A disabled module's sections are not registered: its stored values are retained, unvalidated, until it is re-enabled | fail-closed |
| P4 | Migrations: one pre-migration checkpoint for the boot, then every pending core and **enabled-module** step in the order of 05-storage's cross-keyspace DAG (steps may declare `After` other owners' versions, e.g. `core.auth#1` after `books#66`) (DATA-9) | fail-closed; checkpoint restored per 05-storage |
| P5 | `kernel/services.Build`: every core service in fixed order (A8), including the services behind every `Deps` handle; adapters from `Options.Adapters` (module adapters arrive in P6 and are instantiated lazily on first lookup) | fail-closed |
| P6 | For each enabled module in ID order: build its `Registrar` and `Deps`, call `Register(r)`, collect `r.Err()`, close the registrar. Registrations are only recorded | all problems joined |
| P7 | Consume registrations (A8): task definitions to the executor (R7 checks, schedule kinds exist, kinds in `TaskLedger`), topics to the catalog (05-events validation), subscriptions to the bus, outbox handlers to the relay (topics declared `Durable`, consumer names declared), legacy projections to the realtime hub, change sinks to the task writer, adapters to the integrations registry, checks to health. Seed default grants for permissions new to this boot. Post-checks: R6 over the built router, required capabilities configured | joined |
| P8 | Build the root `http.Handler`: core capability routes, module routes, legacy mounts, the `/` owner (either `ui.Handler` or the `ServesSPA` legacy mount, FE-1) | fail-closed |

`New` stops core services' goroutines and closes storage and everything opened so far, in reverse
order, on any failure.

### 6.5.2 Run and shutdown

This section is the **one** statement of start/stop order. 05-events and 05-tasks reference it
instead of restating it (COR-8).

Start order: (1) core background services (metrics, realtime hub); (2) module hooks, modules in ID
order, hooks of one module in registration order; (3) module workers; (4) `Bus.Open`: bus delivery to
the subscriptions frozen in P6 begins; (5) task executor (resume sweep); (6) scheduler, whose
`OnStart` schedules start the outbox relay task `core.outbox.relay`, so outbox delivery begins here;
(7) listeners; (8) readiness true. No bus handler or outbox consumer can run before its module's
hooks have started, and the resume sweep's `core.task.*` events are delivered.

If any hook's `Start` fails or exceeds `core.lifecycle.start_timeout`, every already-started hook is
stopped in reverse order (the semantics of `serviceregistry.Container.Start` today, `container.go:164`),
and `Run` returns the joined error.

Stop order on ctx cancel or `StopAppOnExit`: readiness false → listeners `Shutdown` (bounded) →
scheduler stop → executor drain (05-tasks bounded drain; the relay run is cancelled with
`ErrShutdown` and acks nothing further) → `Bus.Close` with `core.events.close_drain` → cancel workers'
ctx and wait → module hooks `Stop` in exact reverse start order → realtime hub → storage close. No
bus handler or outbox consumer can run after its module's Stop hook. Deadlines come from **one**
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
| Settings value invalid for an **enabled** module | fail-closed | module would run with undefined config |
| Stored settings of a **disabled** module | not validated (section not registered, 05-config §1.4 step 3); retained | the module does not run; validated when it is re-enabled |
| Migration error | fail-closed; checkpoint restored per 05-storage | G2 |
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
| `core.shutdown.budget` | duration | `25s` total, defined by 05-health §5H.4 (which also slices it per phase) | no |
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
| `TestDisabledModuleWithRunsBoots` | COR-13: hello disabled with `WithRuns("hello.count", 3)` and an op-ledger line, resolved through hello's `TaskLedger`; boot succeeds, runs listed held, none dispatched; re-enable resumes them |
| `TestDisabledModuleOutboxRetained` | COR-12: a message for a disabled module's declared consumer survives `orphan_retention` and another consumer's ack, and is delivered after re-enable |
| `TestUnknownModuleInConfig_OperatorFails`, `TestUnknownModuleInConfig_BootRecordWarns` | COR-14 split by layer |
| `TestEnablementFromBootRecord` | COR-14: a disable written through `config.Apply` lands in the boot record and takes effect at the next boot (read in P1); an operator env value beats it |
| `TestMigrationFailureHaltsBoot` | G2 with a failing hello migration: no later step ran, storage unchanged after checkpoint restore, previous binary's read path still decodes |
| `TestStartOrder`, `TestStopOrder` | COR-8: a bus handler and an outbox consumer of hello never observe the module before its Start hook or after its Stop hook (instrumented hooks, `synctest`) |
| `TestStartRollbackReverseOrder`, `TestStopTimeoutAbandons` | hook ordering; uses `synctest` for timeouts |
| `TestWorkerPolicies` | `DegradeOnExit` vs `StopAppOnExit`, panic recovery |
| `TestRegistrarClosedAfterRegister`, `TestRegistrarContextCancelled` | late calls recorded; `r.Context()` cancelled with `app.New`'s ctx (COR-24) |
| `TestNewCleansUpCoreGoroutines` | `goleak`-style check: a boot failing in P7 leaves no core goroutine running (COR-24) |
| `TestDepsScoping` | write outside `m/hello/` refused; publish to a topic outside hello's catalog entries refused; secret outside `modules.hello.` refused; `StartRequest.Actor` overwritten by the kernel; `library.Scope` writes attributed to hello; metric names under `MetricPrefix` |
| `TestSPAOwner` | FE-1: a `ServesSPA` legacy mount owns `/` and `ui.Handler` is absent; two owners fail boot |
| `TestHelloEndToEnd` | §6.12 example: route permission, task under `-race` with `CPU()` workers, migration once, settings default/override, `/api/core/v1/modules` lists nav entry |
| `TestSpecGoBlocksCompile` | COR-16: every Go block in this file marked as part of the hello example is extracted and compiled against core in CI |
| `archtest.TestNoGlobals`, `archtest.NoInit` over `core/...` and `core/examples/...` | R5, A5 |
| `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` | MOD-7, 03 R2: hello is inside the core module path, so Go's `internal/` rule does not stop it importing `internal/...` or `kernel/...`; this check does, and `scripts/ci/core_consumer_smoke.py` builds a consumer under an unrelated module path with a negative compile check |

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
   declared legacy prefixes (N7), permanently (cross-cutting resolution 2), with `ServesSPA: true`
   until ABO adopts the shell build (07 §7.19, FE-1). `serviceregistry` keeps working **inside** books
   (it is app code; R5 binds core only).
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
	PermRead  rbac.Permission = "hello:read"
	PermAdmin rbac.Permission = "hello:admin"
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
		DataDir:     os.Getenv("HELLO_DATA_DIR"),
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
| O3 | `Deps.Tasks` may enqueue another module's kinds by `Kind` string (R3: modules talk via tasks), permission-checked as the stamped actor; a disabled target module's kind is refused with `tasks` "unknown kind" | 05-tasks |
| O4 | `metricsstore` and activity-log destinations | 04, 05-metrics, 05-rbac |
| O5 | The `go`-statement warning list in `archtest` may be noisy for books during K1–K3; ratchet rather than fail | 06 / plan |
| O6 | Module-level settings that need a restart vs hot reload: per field in `config.Section` tags (`reload:"restart"`) | 05-config |
| O7 | The scope types requested in §6.4.4 (`events.Scope`, `library.Scope`, `secrets.Scope`, `auth.Identities`, `health.Reporters`, `backup.Ensurer`, `storage.Handles.Aux`) and the kernel's actor stamping in `tasks.Client`: until each owning file defines its type, `Deps` cannot be implemented as written | 05-events, 05-library, 05-config, 05-authn, 05-health, 05-backup, 05-storage, 05-tasks |

## 6.14 Alignment with the parallel contract files

This file owns `module.Manifest`, `module.Registrar`, `module.Deps` and `app`. Every row names the
owning symbol. Where an owning file still describes a superseded path, the row says what changes there.

| Contract | What 06 uses (owning symbol) | Reconciliation |
|---|---|---|
| 05-http-api v1.1.0 §3, §4.9–§4.10, §8.2 | `Registrar.HTTP() *httpapi.Group`, `httpapi.Handle`, `httpapi.Route`, `httpapi.Invalid(httpapi.Field(...))`, `MountLegacy(httpapi.LegacyMount)` with `ServesSPA`, `Router.SPA`, `httpapi.ReservedSegments()`, subtree claims | `Manifest.LegacyPrefixes` feeds subtree claims; the narrowed rule (no two owners overlap, SIMP-22), `ServesSPA` and `Router.SPA` are 05-http-api's (FE-1) |
| 05-storage §5S.3 | `Manifest.Storage storage.Spec`; `Deps.Storage storage.Handles` (+ `Aux storage.AuxOpener`); `c/storage/claims/<owner>`; cross-keyspace migration DAG with one checkpoint per boot | P2 and P4 follow 05-storage (DATA-9, DATA-28) |
| 05-tasks v1.1.0 | `Registrar.Tasks`, `Resources`, `Lanes`, `ChangeSink(tasks.ChangeSink)`; `Manifest.TaskLedger` (5T.3.11 held runs); `tasks.Common.Schedule` (+ `tasks.ScheduleFromConfig`); `Deps.Tasks tasks.Client`; `tasks.CPU()`, `RunItems(rc, par, items, fn, opts...)` | closes 05-tasks open item 6 and the `Registrar.Schedule` question (it does not exist). Held runs and the ledger-gate rule of §6.5.3 are 05-tasks mechanics (COR-13) |
| 05-events v1.1.0 | `Registrar.Topics(...events.TopicSpec)` (one catalog incl. `Durable`, `Notify`, `Realtime`, `Permission`, `Visible`), `Registrar.Subscribe(...events.Subscription)` built with `events.On`/`OnRaw`, `Registrar.OutboxHandler(...outbox.Handler)` incl. `ConsumeRaw`/`ConsumeIntoTaskRaw`, `Manifest.OutboxConsumers`; `Deps.Outbox outbox.Writer`; `Bus.Open`/`Bus.Close` in §6.5.2 | adopted as 05-events states it; one difference: `Deps.Events` is a module-scoped `events.Scope`, not `*events.Bus` (COR-21; requested from 05-events) |
| 05-realtime v1.1.0 | catalog topics with `TopicSpec.Realtime` are streamed with no registrar call; `Registrar.Realtime() realtime.Registrar` (`Redact`) and books' legacy projections (`Hub.Projection`) | adopted as 05-realtime states it (SIMP-5) |
| 05-notify | notify metadata on the catalog entry (`TopicSpec.Notify`) | `Registrar.NotificationTopic` deleted (SIMP-5) |
| 05-rbac §3.1–§3.5 | `rbac.Permission`, `PermissionSpec{Name, Title, DefaultGrants, Sensitive, …}`, `RoleSeed`, `RoleName` (`"<ns>.<name>"`), `LegacyAlias`, `Requirement`, `Need`, `CoreAdmin`, `audit.Logger` | manifest lists are fed to `Service.Declarer(moduleID)` by the kernel in P1; no `Grant`, `Scope` or `PermissionDef` type exists (COR-16) |
| 05-config v1.1.0 §1.3–§1.4, §2.3 | `config.Define`, `Section[T]`, `Handle[T]`, `config.Register`; `Store.Boot` + boot record for `core.modules.<id>.enabled` (P0/P1); `secrets.Scope` (`modules.<id>.`) | 05-config hands modules `Registrar.Config()` with "registration legal only before Store.Load"; since `Register` needs loaded config, sections are listed in `Manifest.Settings` and registered by the kernel before load; `module.Setting(r, s)` returns the bound handle |
| 05-metadata M1–M2 | `Registrar.Adapter(integrations.Descriptor)`, `app.Options.Adapters`, `Deps.Integrations *integrations.Registry` (the one instance store, SIMP-4), `Deps.HTTP *providerhttp.Registry` | adopted as proposed |
| 05-ai | `Registrar.AICapability(ai.CapabilitySpec) ai.Capability`, `AIBatchHandler(ai.Capability, ai.BatchHandler)`, `Deps.AI *ai.Router` | signatures as in 05-ai |
| 05-download | `Deps.Downloads *download.Pool`; `DownloadWatch` deferred-build | grab/monitor are the deferred acquire build (SIMP-12, COR-3) |
| 05-health v1.1.0 | `Health(...health.Check)`, `StartupSteps(...health.StartupStep)`, `Diagnostics(health.Contributor)`; `Deps.Health health.Reporters`; `core.shutdown.budget` and its phase slices (§5H.4) | `core.lifecycle.stop_timeout`/`shutdown_budget` removed from 06 (OPS-13) |
| 05-updater | `app.Options.Updater *updater.Options` | replaces 1.0.0's `Release updater.Source` |
| 05-download, 05-indexer, 10 (v1.1.0) | `Deps.Downloads *download.Pool`; deferred `Deps.Indexers *indexer.Pool`, `Deps.Acquire *acquire.Service`, `Manifest.Acquire *AcquireDecl` (§6.4.9) | names as those files state them |
| 05-library | `Manifest.Library []library.RoleRequirement`; `Deps.Library library.Scope` | — |
| 05-backup | `Registrar.Backup(...backup.Artifact)`; `Deps.Backup backup.Ensurer`; pre-boot restore phase P0a | 05-backup's "module.Manifest.Backup" comment changes to `Registrar.Backup` (hooks close over services); P0a runs before P0 (DATA-23) |
| 05-logging, 05-metrics | `Deps.Log *slog.Logger`, `Deps.Metrics *metrics.Scope` | — |
| 07 §7.4–§7.6 | `UIDescriptor` (all shell slots), `app.Options.UI ui.Bundle`, `ui.ReservedSegments` | — |
| 08, 10, 05-indexer | §6.4.9 deferred-build members | — |

## Objections to 03

1. **§3.7 `App.Run` line.** 03 v1.3.0's diagram lists "start executor, scheduler, outbox relay,
   realtime hub, listeners". The single start/stop order is §6.5.2 here (COR-8: hub first, then hooks,
   workers, `Bus.Open`, executor, scheduler with the relay via `OnStart`, listeners). Proposed: the
   diagram line points to 06 §6.5.2 instead of listing an order.

Resolved in 03 v1.3.0 and removed here: the "service container" diagram wording (SIMP-26), the
Role-ID grammar (`<moduleID>.<name>`, COR-16), the public L0 `version` package (MOD-12),
`examples/hello` in the tree (MOD-7), and `module`'s layer (L7).

## Out-of-scope defects found

| Defect | Anchor | Note |
|---|---|---|
| `batchpoller` registered without `Groups`, never built; `TryGet` always misses | `internal/server/batch_poller_register.go:18-20`, `internal/server/registry_wire.go:422` | already in raw/07 §1.3; re-verified. Fixed as a side effect of K2/K3 |
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
