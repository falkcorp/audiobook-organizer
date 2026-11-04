<!-- file: docs/specs/platform-core/05-health.md -->
<!-- version: 1.0.0 -->
<!-- guid: 182e343b-a8da-4dd5-8327-3939173ed75c -->
<!-- last-edited: 2026-11-04 -->

# 05 — Health (`core/health`): liveness, readiness, components, diagnostics, sysinfo

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6, §7, §9;
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) (infra routes); [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) (aidispatch health).
Layer 3 (R4). `health` (root package) imports only `log`, `metrics`, `config`; `health/diagnostics`
imports `tasks` (the bundle is a task) — same layer, no cycle, because `tasks` imports only the
`health` root (for readiness signals), never `health/diagnostics`.

## 5H.1 Purpose & scope

| Concern | Package |
|---|---|
| Process liveness and readiness probes; supervisor signals (sd_notify) | `health` |
| Component health registry (instance-scoped, per `App`) | `health` |
| Startup step classification (Fatal / Degraded with fixed tokens) | `health` (`Startup`) |
| Diagnostics bundle (core sections + module contributions, redacted) | `health/diagnostics` |
| Host/runtime information | `health/sysinfo` |
| Stop budget (how long each shutdown phase may take) | defined here, executed by the kernel ([06-app-sdk](06-app-sdk.md)) |

Out of scope: metrics exposition ([05-metrics](05-metrics.md)), log redaction rules themselves
([05-logging](05-logging.md); used here), library statistics (module dashboards).

## 5H.2 Current state at HEAD

| Fact | Anchor |
|---|---|
| `/health`, `/api/health`, `/api/v1/health`: same handler, unauthenticated, registered before the `/api/*` redirect | `internal/server/server_lifecycle.go:1749-1755` |
| The handler always returns 200; reports `ready`, `memdb_ready`, `warmup_ms`, sorted fixed `degraded` tokens; never error text, paths, versions or counts | `internal/server/handlers/system/handler.go:222-275` (rule stated `:223-228`) |
| Store probe is `store.CountAuthors()` — an audiobook aggregate used as a liveness ping | `handler.go:245-253` |
| `memdb_warming` token is a books concept in the system handler | `handler.go:197-199,255-257` |
| Startup table: 12 steps, Fatal/Degraded, fixed tokens `metrics_disabled`, `search_index`; AST test ties every `Run` call to a row | `internal/startup/steps.go:6-24,69-116`; `startup.Table` is a package var (`raw/12` §9) |
| sd_notify: `READY=1` after HTTP start (deliberately not after warm-up), `STOPPING=1`, `STATUS=`, `EXTEND_TIMEOUT_USEC`; no `WATCHDOG=1` | `internal/sdnotify/sdnotify.go:128-144`; `server_lifecycle.go:335-342`; grep finds no watchdog ping |
| Plugin health: `plugin.Registry.HealthCheckAll` over the webhook plugin only; exposed on `/system/status` as raw `err.Error()` strings | `internal/plugin/registry.go:162-174`; `handlers/system/handler.go:288-298` |
| AI endpoint health is a package-global `defaultHealth` | `internal/aidispatch/health.go:55-58` |
| `sysinfo.SystemStatus` mixes host data (memory, runtime, uptimes) with library data (book/author/series counts, root directory path, import sizes) | `internal/sysinfo/service.go:126-148` |
| `DashboardService.GetHealthCheckResponse` has no non-test caller and would echo `dbErr.Error()` | `internal/sysinfo/dashboard_service.go:79-101`; grep |
| Diagnostics export: AI-oriented ZIP of books, authors, series, iTunes albums, logs, operations; per-viewer row filters via `ExportOptions` ("the zero value keeps everything whole") | `internal/diagnostics/service.go:138-150,166`; system info section `:89-99,294-310` |
| Export runs as v2 op `diagnostics.export` with a 5-minute default watchdog the export used to exceed | `internal/server/diagnostics_ops.go:30-40`; comment `service.go:155-163` |
| Stop budget: unit `TimeoutStopSec` 30 s, but HTTP drain alone may take 30 s, then the ops drain 10 s and activity flush 5 s | `server_lifecycle.go:176-178,582,611` — the phases can sum to 45 s, past the unit's limit |

## 5H.3 Go API

### 5H.3.1 Component registry

```go
package health // github.com/falkcorp/mediacore/health

type Status uint8

const (
	StatusUnknown Status = iota
	StatusUp
	StatusDegraded
	StatusDown
)

// ComponentID is "<moduleID>.<name>", e.g. "core.storage", "core.outbox",
// "core.integrations.download.deluge-main", "books.memdb".
type ComponentID string

// Result is one observation. Reason is a FIXED machine token
// ([a-z0-9_]{1,40}); it is the only thing unauthenticated probes ever show.
// Detail is free text shown only on the authenticated endpoint, after
// redaction (05-logging).
type Result struct {
	Status Status
	Reason string
	Detail string
	At     time.Time
}

// Check declares one component (module.Registrar.Health takes these, 06 §6.4.3).
type Check struct {
	ID        ComponentID
	Title     string
	Critical  bool          // Down => process not ready
	Readiness bool          // must be Up or Degraded for readiness
	Probe     func(ctx context.Context) Result // nil = push-only (Reporter)
	Interval  time.Duration // 0 = core.health.check_interval
	Timeout   time.Duration // 0 = core.health.check_timeout
}

// Registry is one per App (R5). Modules register Checks via Registrar.Health and
// push through module.Deps.Health (a Reporter scoped to the module's IDs).
type Registry struct{ /* unexported */ }

func (r *Registry) Register(c Check) (Reporter, error) // duplicate ID -> error
func (r *Registry) Snapshot() Snapshot
func (r *Registry) Ready() (ready bool, notReady []string) // tokens: "<component>:<reason>"
func (r *Registry) Live() bool
// Wait blocks until every listed component is Up or Degraded (tasks Ready signals).
func (r *Registry) Wait(ctx context.Context, ids ...ComponentID) error

type Reporter interface {
	Report(res Result) // push model: breakers, outbox dead letters, watchdog, warm-up
}

type Snapshot struct {
	Ready      bool
	Live       bool
	Components []ComponentState // sorted by ID
	Startup    []string         // degraded startup tokens
}

type ComponentState struct {
	ID       ComponentID
	Result   Result
	Critical bool
	Duration time.Duration // last check duration
}
```

`tasks.ReadySignal` is `health.ComponentID`; `Common.Ready` defaults to `{"core.storage"}`; the
books warm-up becomes component `books.memdb` (Readiness, not Critical) so book-reading tasks wait
on it exactly as they wait on the store warm-up today ([05-tasks](05-tasks.md) 5T.4).

Core registers: `core.storage` (open + a cheap engine ping, replacing `CountAuthors`),
`core.config`, `core.secrets`, `core.tasks` (executor running; Degraded when paused or a zombie cap
is hit), `core.scheduler`, `core.outbox` (Degraded while any dead letter exists), `core.events`
(Degraded while drops exceed a rate), `core.metrics`, `core.updater` (only when enabled), and one
component per configured integration adapter instance (`core.integrations.<capability>.<instance>`,
reported by `providerhttp` breakers and adapter checks; [05-metadata](05-metadata.md),
[05-download](05-download.md), [05-ai](05-ai.md)).

### 5H.3.2 Startup steps

```go
type StepClass uint8

const (
	Fatal    StepClass = iota + 1 // error stops the process before it serves
	Degraded                      // error logged, Token recorded, startup continues
)

type Step struct {
	Name   string    // literal used at the Run call site
	Class  StepClass
	Token  string    // fixed token for Degraded steps; empty for Fatal
	Reason string    // the review record for the class
}

// Startup is the successor of internal/startup: a per-App table (core rows
// + module rows from Registrar.StartupSteps, an addition to 06) instead of a package var.
type Startup struct{ /* unexported */ }

func (s *Startup) Run(ctx context.Context, name string, fn func(context.Context) error) error // unknown name -> error (fail-closed)
func (s *Startup) Degraded() []string
```

`archtest.StartupSteps(t, pkgs...)` replaces `internal/startup/steps_test.go`'s AST scan: every
`Run` literal is a row and every row is run (`steps.go:18-24`). A failed Degraded step registers
component `core.startup.<token>` with `StatusDegraded`.

### 5H.3.3 Probes and HTTP

| Route | Auth | Status code | Body |
|---|---|---|---|
| `GET /livez` | `rbac.Public` | 200 while the kernel heartbeat goroutine ticked within `core.health.live_stall`; 503 otherwise | `{"live":true}` |
| `GET /readyz` | `rbac.Public` | 200 iff startup complete **and** not shutting down **and** every Readiness component Up/Degraded **and** no Critical component Down; else 503 | `{"ready":false,"not_ready":["core.storage:warming"]}` — tokens only |
| `GET /api/core/v1/health` | `core:system.view` | 200 | full `Snapshot` with redacted `Detail`, check durations, versions |
| `GET /api/core/v1/system/info` | `core:system.view` | 200 | `sysinfo.Info` |
| `POST /api/core/v1/diagnostics` | `core:system.manage` | 202 + run ref | starts `core.diagnostics.export` |
| `GET /api/core/v1/diagnostics/{run}` | the run's actor or `core:admin` | 200 zip / 404 / 410 expired | bundle |

Unauthenticated bodies follow today's rule (`handler.go:223-228`): booleans, fixed tokens,
milliseconds — never error text, versions, counts or paths. ABO's `/health` shape (`status`,
`ready`, `memdb_ready`, `warmup_ms`, `degraded`, always 200) is served by the **books** module's
compatibility layer reading this registry ([09-abo-on-core](09-abo-on-core.md)), so core carries
no audiobook field.

### 5H.3.4 Supervisor signals

The kernel uses `core/internal/sdnotify` (from `internal/sdnotify`): `READY=1` once listeners are up
and startup completed — **not** after module warm-up (current deliberate choice,
`server_lifecycle.go:335-339`); `STATUS=` with the not-ready tokens while not ready; `WATCHDOG=1` at
half of `WATCHDOG_USEC` when the unit sets `WatchdogSec`, driven by the same heartbeat as `/livez`
(new: today there is no watchdog ping); `STOPPING=1` at shutdown start; `EXTEND_TIMEOUT_USEC` only
through the tasks executor's bounded extension ([05-tasks](05-tasks.md) 5T.4).

### 5H.3.5 Diagnostics bundle

```go
package diagnostics // github.com/falkcorp/mediacore/health/diagnostics

// Section is one part of a bundle. Sections write only through w, which
// redacts every string it serialises.
type Section struct {
	Name  string // "<moduleID>.<name>"; becomes the directory in the zip
	Perm  rbac.Requirement // viewer must satisfy it, else the section is omitted (listed as omitted)
	Write func(ctx context.Context, w Writer, v Viewer) error
}

type Writer interface {
	JSON(name string, v any) error
	File(name string, r io.Reader) error // text files only; size-capped
}

func WriteJSONL[T any](w Writer, name string, items iter.Seq[T]) error

// Viewer is the requesting actor's permissions, resolved at request time and
// carried into the task (the run outlives the request: current ExportOptions rule).
type Viewer struct {
	ActorID string
	Can     func(rbac.Scope) bool
}

// Contributor is registered by a module (Registrar.Diagnostics, an addition to 06).
type Contributor interface {
	Sections() []Section
}
```

(The `Writer` interface has `JSON` and `File`; generic JSONL is the free function `WriteJSONL`
because Go methods cannot have type parameters.)

Core sections:

| Section | Content | Redaction |
|---|---|---|
| `core.manifest` | bundle version, app name/version, created-at, section list, omitted sections, redaction counts | — |
| `core.system` | `sysinfo.Info` | library root paths replaced by `<root:NAME>/...` ([05-library](05-library.md) root names) |
| `core.build` | `debug.ReadBuildInfo()`: module path/versions, VCS revision, Go version | — |
| `core.config` | effective config with per-key source layer ([05-config](05-config.md)) | secret-typed fields emitted as `<secret:NAME>` references, never values; fields tagged sensitive emitted as `<redacted>` |
| `core.health` | `Snapshot` with `Detail` | pattern redaction |
| `core.tasks` | last `core.diagnostics.task_runs` runs: kind, state, timings, error text | params/results only for kinds the viewer may view (`Perms.View`); error text pattern-redacted |
| `core.logs` | last `core.diagnostics.log_lines` structured log records from the in-memory ring ([05-logging](05-logging.md)) | pattern redaction |
| `core.metrics` | Prometheus text exposition of the App registry | — |
| `core.storage` | keyspace sizes, engine metrics | — |
| `core.outbox` | consumer lag, dead-letter headers (payloads omitted) | — |

Redaction has three layers, applied in this order, and fails closed: (1) **typed** — secret-typed
config values are never serialised (05-config); (2) **pattern** — every string passes the
`log.Redactor` (credentials in URLs, authorization headers, API keys and bearer values, cookies,
invite paths as today's `internal/logging/redact_path.go:107`, absolute filesystem paths outside
known roots); a redactor error replaces the value with `<redacted:error>`; (3) **permission** —
sections and rows the viewer may not read are omitted (today's `ExportOptions`,
`internal/diagnostics/service.go:138-150`, generalised). The manifest records how many values each
layer redacted, so a reader knows the bundle is not raw.

Generation is the core task `core.diagnostics.export`: Shape Task, `ReadOnly`, `Resume: ResumeDrop`,
`Timeout: 30m`, progress per section (the watchdog lesson of `service.go:155-163`), `Perms.Start:
core:system.manage`. The zip is written through an `os.Root` opened on
`core.diagnostics.dir` (no path escapes), named `<run-id>.zip`, deleted after
`core.diagnostics.retention` by the scheduled `core.diagnostics.prune`.

### 5H.3.6 sysinfo

```go
package sysinfo // github.com/falkcorp/mediacore/health/sysinfo

type Info struct {
	App            string
	Version        string
	Commit         string
	GoVersion      string
	OS, Arch       string
	NumCPU         int
	GOMAXPROCS     int
	Goroutines     int
	Memory         Memory        // runtime.MemStats subset + process RSS + host total/available
	ProcessUptime  time.Duration
	SystemUptime   time.Duration // 0 when the platform cannot say
	Roots          []RootSpace   // per library root: name, free/total bytes (via core/library)
	Container      bool          // running in a container (affects updater policy, 05-updater)
}

type Memory struct {
	HeapAlloc, HeapSys, Sys, RSS, HostTotal, HostAvailable uint64
	NumGC uint32
}

type RootSpace struct {
	Name             string
	FreeBytes, TotalBytes uint64
}

func Collect(ctx context.Context, roots []library.Root) Info
```

Platform files `memory_{linux,darwin,windows}.go` and `uptime_{linux,darwin,windows}.go` move from
`internal/sysinfo` unchanged in behaviour.

## 5H.4 Lifecycle and stop budget

1. `app.New`: kernel creates the `Registry` and `Startup`; core and module components/steps register.
2. `App.Run`: startup steps run in table order; listeners open; `READY=1`; checks start (one
   goroutine per component with a `Probe`, each on its own interval with jitter; push-only
   components cost nothing).
3. Shutdown: readiness flips to not-ready **first** (load balancers and the SPA reconnect logic
   see 503 before connections drop), `STOPPING=1`, then the kernel runs the phases below with
   deadlines carved from one budget.

| Phase (order) | Budget | Today |
|---|---|---|
| total `core.shutdown.budget` | 25 s (leaves 5 s of the unit's 30 s `TimeoutStopSec`) | phases unbounded as a sum (up to 45 s) |
| HTTP drain | `min(core.shutdown.http_drain=10s, remaining)` | 30 s (`server_lifecycle.go:582`) |
| tasks drain | `min(core.tasks.shutdown_drain=10s, remaining)` | 10 s (`:611`) |
| events close | `min(core.events.close_drain=2s, remaining)` | — |
| module stops, storage close | remainder | activity flush 5 s (`:178`) |

## 5H.5 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Check panics | open: recovered → `StatusDown`, reason `check_panic`, counted | one bad check must not take the process down |
| Check exceeds timeout | open: `StatusDown`, reason `check_timeout` | — |
| Critical component Down | readiness closed (503); liveness unaffected | restarting does not fix a down dependency; draining traffic does |
| Duplicate component ID / unknown startup step name | closed: boot fails | programmer error |
| Fatal startup step fails | closed: process exits before serving | current table semantics |
| Degraded startup step fails | open: token recorded, component Degraded | current table semantics |
| Kernel heartbeat stalls | liveness closed (503; `WATCHDOG=1` stops) → supervisor restarts | a wedged process should be restarted |
| Diagnostics redactor error | closed per value (`<redacted:error>`) | never leak on error |
| Diagnostics section error | open: section replaced by an error note in the manifest; bundle continues | partial bundle beats none |
| Bundle download by another actor | closed: 404 | bundles contain the requester's view |
| sysinfo platform call fails | open: field zero | informational |

## 5H.6 Concurrency contract

`Registry`, `Reporter`, `Startup` are goroutine-safe; `Snapshot`/`Ready` are lock-free reads of an
atomically swapped state. Checks run on per-component goroutines (bounded by the static component
set), never concurrently with themselves; a check's context is canceled on its timeout and on
shutdown. Diagnostics run inside one task with sections written sequentially (the zip writer is
not concurrent); section producers may use `tasks.RunItems` internally.

## 5H.7 Config keys

| Key | Type | Default | Hot reload |
|---|---|---|---|
| `core.health.check_interval` | duration | 30s | yes |
| `core.health.check_timeout` | duration | 5s | yes |
| `core.health.live_stall` | duration | 60s | yes |
| `core.shutdown.budget` | duration | 25s | yes |
| `core.shutdown.http_drain` | duration | 10s | yes |
| `core.diagnostics.dir` | path | `<data-dir>/diagnostics` | no |
| `core.diagnostics.retention` | duration | 24h | yes |
| `core.diagnostics.log_lines` | int | 5000 | yes |
| `core.diagnostics.task_runs` | int | 200 | yes |
| `core.diagnostics.max_file_bytes` | int | 64 MiB | yes |

## 5H.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_health_component_status` | gauge (0 unknown, 1 up, 2 degraded, 3 down) | `component` |
| `mediacore_health_check_duration_seconds` | histogram | `component` |
| `mediacore_health_check_failures_total` | counter | `component`, `reason` |
| `mediacore_health_ready` | gauge (0/1) | — |
| `mediacore_health_startup_degraded` | gauge (0/1) | `step` |
| `mediacore_diagnostics_bundles_total` | counter | `outcome` |
| `mediacore_diagnostics_redactions_total` | counter | `layer` (`typed`, `pattern`, `permission`) |
| `mediacore_shutdown_phase_seconds` | histogram | `phase` |

`reason` is a closed token set per component.

## 5H.9 Tests

| Test | Proves |
|---|---|
| `TestReadyz_CriticalDownIs503_LivezStays200` | probe split |
| `TestReadyz_FlipsBeforeHTTPDrain` | drain order |
| `TestProbes_BodyHasOnlyTokens` (fuzz Reason/Detail with secrets and paths) | no leak |
| `TestCheck_PanicAndTimeoutBecomeDown` (synctest) | isolation |
| `TestRegistry_Wait_UnblocksOnUp` | tasks readiness |
| `TestStartup_UnknownStepRefused`, `archtest.StartupSteps` self-test | table discipline |
| `TestHeartbeat_StallFailsLivezAndStopsWatchdog` (synctest) | liveness |
| `TestStopBudget_PhasesNeverExceedTotal` (synctest; each phase sleeps past its slice) | budget |
| `TestDiagnostics_SecretsNeverSerialised` (config with secret refs; scan zip bytes for the secret values) | typed redaction |
| `TestDiagnostics_PatternRedaction` (URLs with credentials, bearer headers, invite paths) | pattern redaction |
| `TestDiagnostics_ViewerFiltersSections`, `TestDiagnostics_DownloadOtherActor404` | permission |
| `TestDiagnostics_PathsConfinedToRoot` | `os.Root` |
| `TestNoGlobals_Health` (R5 AST) | no globals |

## 5H.10 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/startup` (`Table` var, `Steps`) | `health.Startup`; core rows (store, telemetry→metrics, secure state dir, key guard, encryption→secrets, config load/validate, registration gates, http) in core; `searchindex` (token `search_index`) becomes a `books` Degraded step |
| `handlers/system.HealthCheck` and the three `/health` routes | core `/livez`, `/readyz`; ABO `/health` shape served by the `books` compat layer from the registry for the compat window |
| `CountAuthors` store probe | `core.storage` check (engine ping) |
| memdb warm-up readiness | component `books.memdb` (push) |
| `plugin.Registry.HealthCheckAll` + `/system/status` `plugin_health` | adapter components; `/system/status` stays a `books` route (it is mostly library data) and reads component states |
| `aidispatch.defaultHealth` global (`health.go:55`) | instance owned by the AI integration, reporting components ([05-ai](05-ai.md)) |
| `internal/sdnotify` | `core/internal/sdnotify` (+ watchdog ping) |
| `internal/sysinfo` host parts (memory, uptime, runtime) | `health/sysinfo` |
| `internal/sysinfo` library parts, `DashboardService` | `books` dashboard; `GetHealthCheckResponse` deleted (no caller) |
| `internal/diagnostics` zip/section machinery | `health/diagnostics` |
| SlimBook/authors/series/version-groups/iTunes/missing-fields sections, `BuildBatchJSONL` | `books` `Contributor` sections; the AI batch builder goes with [05-ai](05-ai.md) |
| op `diagnostics.export` | `core.diagnostics.export` (`FormerKinds: ["diagnostics.export"]`); `diagnostics.ai-analyze` stays `books` |

## 5H.11 Open items

1. Should `/readyz` require module warm-up (e.g. `books.memdb`) or only core? This spec says modules
   decide per component via `Readiness`; ABO's deploy currently waits on `/health` `ready`, so `books.memdb`
   is Readiness=true to keep that behaviour.
2. Ring-buffer size and format for `core.logs` depend on [05-logging](05-logging.md).
3. Whether `/metrics` stays unauthenticated is decided in [05-metrics](05-metrics.md); `core.metrics`
   section in bundles is unaffected.
4. [06-app-sdk](06-app-sdk.md) §6.4.3 must add `Registrar.StartupSteps` and `Registrar.Diagnostics`;
   `Registrar.Health(c ...health.Check)` and `Deps.Health health.Reporter` already match this file.

## Out-of-scope defects found

1. Shutdown phases can exceed the unit's stop timeout: HTTP drain up to 30 s (`server_lifecycle.go:582`)
   followed by the ops drain up to 10 s (`:611`) and the activity flush 5 s (`:178`), against
   `TimeoutStopSec` 30 s (`:176`). A slow HTTP drain therefore leads to SIGKILL before the ops registry
   stamps interrupted runs. Fixed by design in 5H.4; also fixable in ABO alone by carving one budget.
2. `/system/status` returns plugin health as raw `err.Error()` strings (`handlers/system/handler.go:288-298`);
   authenticated, but inconsistent with the token-only rule used for `/health`.
