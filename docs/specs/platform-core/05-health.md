<!-- file: docs/specs/platform-core/05-health.md -->
<!-- version: 1.13.1 -->
<!-- guid: 182e343b-a8da-4dd5-8327-3939173ed75c -->
<!-- last-edited: 2026-11-04 -->

# 05 — Health (`core/health`): liveness, readiness, components, diagnostics, sysinfo

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6, §7, §9;
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) (infra routes); [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) (aidispatch health).
Layer **L4** (03 R4 v1.2.0). `health` (root package) imports only `log`, `metrics` (L0), `config`
(L3) and `lifecycle` (L0); `health/diagnostics` takes `health`'s layer, L4, under 03 R4 (COR6-9) and
imports `tasks` (the bundle is a task) — same layer, no cycle, because `tasks` imports only the `health`
root (for readiness signals), never `health/diagnostics`.

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
| Stop budget: the code assumes a unit `TimeoutStopSec` of 30 s (`server_lifecycle.go:174-178` comment), but the committed unit sets **none**, so systemd's default applies; only `deploy/local.conf.example:33` sets 30. `docker-compose.yml` sets no `stop_grace_period` (Docker's default is 10 s) and the launchd plist sets no `ExitTimeOut`. HTTP drain alone may take 30 s, then the ops drain 10 s and activity flush 5 s | `deploy/audiobook-organizer.service`; `deploy/local.conf.example:30-33`; `docker-compose.yml`; `deploy/com.audiobook-organizer.plist`; `server_lifecycle.go:582,611,178` — the phases can sum to 45 s |

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

// Reporters is the module handle, module.Deps.Health (06 §6.4.4, COR-21): it
// hands out the push Reporter of a component the module itself declared with
// Registrar.Health. The kernel builds one per enabled module.
type Reporters interface {
	// Reporter returns ErrNotOwned for an ID this module did not register,
	// so a module can never report another component's status.
	Reporter(id ComponentID) (Reporter, error)
}

var ErrNotOwned = errors.New("health: component not registered by this module")

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
on it exactly as they wait on the store warm-up today ([05-tasks](05-tasks.md) 5T.4): storage registers
`core.storage` (engine ping), books registers `books.memdb` through `Registrar.Health` and pushes it through
`Deps.Health`, so HEAD's warm-up gating (D44) has a source from the executor's first release. What lands when is in
5H.10.

Core registers: `core.storage` (open + a cheap engine ping, replacing `CountAuthors`),
`core.config`, `core.secrets`, `core.tasks` (executor running; Degraded when paused or a zombie cap
is hit), `core.scheduler`, `core.outbox` (Degraded while any dead letter exists), `core.events`
(Degraded while drops exceed a rate), `core.metrics`, `core.updater` (only when enabled), `core.backup`
(the revocation-journal and archive tokens of [05-backup](05-backup.md)), and one
component per configured integration adapter instance (`core.integrations.<capability>.<instance>`,
reported by `providerhttp` breakers and adapter checks; [05-metadata](05-metadata.md),
[05-download](05-download.md), [05-ai](05-ai.md)). The fixed degraded token `basic_gate_no_password` belongs to the
perimeter: while the Basic gate is enabled and its password is unset, cleared, empty or unreadable,
the gate answers 401 on every non-exempt route and health reports `degraded` with that token. In R0 and release R1
HEAD's hotfixed gate (05-authn §11.1 early fix 5) raises it; from F3, the config flip that moves the gate into
core's stage 8 ([05-http-api](05-http-api.md) §4.2, SEC5-4), stage 8 does. Until `Snapshot` and the probes land
(F8, 5H.10) every source reports it through HEAD's `degraded` token list
(`internal/server/handlers/system/handler.go:222-275`), which keeps serving until then; so do the other tokens
below that exist before F8. `auth_disabled_remote` (warn: auth mode `disabled` while remote-reachable,
05-authn §4.11, SEC4-8) exists from F4, the identity flip. An enabled gate with an empty
**username** raises no token: it keeps HEAD's behaviour (05-http-api §10, SEC6-9), so there is no
`basic_gate_no_username`.

**Advisory tokens owned by other files.** Health has three statuses, so a token another file calls `warn` is a
`Reason` on a `StatusDegraded` result (readiness unaffected: Degraded passes it), and one it calls `info` is a
`Reason` on a `StatusUp` result (no probe or readiness effect; shown on the authenticated endpoints and the
token-only route). Three such tokens; the two on `core.config` exist from F3, when core config gets its DB layer and its retired-name
list: `config_fail_closed_override` on `core.config` (warn: an operator value of an
auth or Basic-gate switch is less restrictive than the stored value, which stays effective; 05-config §1.3.5),
`config_retired_name_ignored` on `core.config` (warn: a retired HEAD env or file name of an unprotected, non-secret
key is set and ignored; the authenticated `Detail` names it and its canonical name; 05-config §1.3.6; a retired name
of a protected or secret key never reaches health, because it refuses boot), and `revocation_journal_colocated` on
`core.backup` (info: the revocation journal shares the store's filesystem device, so a whole-volume snapshot
rollback would revert both; 05-backup §5B.3.6 "Where", SEC6-10). One reason an integration instance component
(`core.integrations.<capability>.<instance>`) reports on a `StatusDegraded` result is `operator_secret_withheld`:
an operator-sourced secret is withheld, and nothing sent, because the effective origin of a destination field
(URL-typed, or host and port) of that instance equals neither the origin of that field's operator value (or the
adapter default, when there is none) nor the instance's `OperatorSecretOrigin`; the result names the withheld
fields, never their values (05-metadata M1.4 step 2; SEC7-3, R8-5, SEC8-2). Every instance has origin binding from
F7, the integrations flip, ABO's migrated instances included.

**Factory-reset tokens (R8-1).** A factory reset is the `KindReset` swap of storage's one restore slot
(05-storage §5S.3.15), so a reset staged or in flight is reported with the swap's own reasons, never a
reset-only token. Two reasons on `core.storage`, both owned by 05-storage §5S.3.15, report its two
non-crash outcomes:

- `factory_reset_failed` (`StatusDegraded`): a rename of the staged reset's set-aside failed; the boot
  moved back what it had moved **before** it journaled `rejected`, and serves the untouched store (COR8-3,
  DATA8-1, R9-6). It is reported while the newest swap journal is that rejected `KindReset` journal, so it
  clears when the next swap is staged. The failure rule is its only source: a `KindReset` journal at
  `set_aside_started` or later is never rejected for expiry or because another core build boots it; that
  build resumes it forward to `set_aside` (05-storage §5S.3.16; DATA9-4), so a reset interrupted by a crash
  loop or a recreated container never leaves an empty DB path served as a fresh install. Restore
  `inflight.json` timing (it now exists from before the first rename of every kind, R9-7) raises no reset-only
  token: `restore_pending` and `restore_inflight` are the one swap's token pair for every kind (05-storage
  §5S.3.15).
- `factory_reset_cleanup_incomplete` (warn, `StatusDegraded`): the post-reset hook recorded `hook_done`
  with per-entry failures (an entry it could not delete, as HEAD warns and continues). The hook never fails
  boot (COR8-4, OPS8-5). The authenticated `Detail` names the count and the paths; the probes and the
  token-only route show the token alone. It is reported while the newest `KindReset` journal's `hook_done`
  record lists failures.

Both exist from release R1, where the staged reset ships (staged only offline, by `storage reset`, until the core
reset route arrives at F4; 05-storage §5S.3.15), before this file's `Snapshot` and probes (F8, 5H.10). Until then they
travel through HEAD's `degraded` token list, through the same fixed-token source it merges today (`getDegraded`,
`handler.go:258-264`), so R1's release note's promise of a health token holds from R1.

### 5H.3.2 Startup steps

```go
type StepClass uint8

const (
	Fatal    StepClass = iota + 1 // error stops the process before it serves
	Degraded                      // error logged, Token recorded, startup continues
)

type StartupStep struct {
	Name   string    // literal used at the Run call site
	Class  StepClass
	Token  string    // fixed token for Degraded steps; empty for Fatal
	Reason string    // the review record for the class
}

// Startup is the successor of internal/startup: a per-App table (core rows
// + module rows from Registrar.StartupSteps, 06 §6.4.3) instead of a package var.
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
| `GET /api/core/v1/health` | `core:system.manage` (SEC-8: detail text, durations and versions are operator data; today's `/system/status` needs `settings.manage`, `wire_system_routes.go:28`) | 200 | full `Snapshot` with redacted `Detail`, check durations, versions |
| `GET /api/core/v1/system/info` | `core:system.manage` (SEC-8) | 200 | `sysinfo.Info` |
| `GET /api/core/v1/system/status` | `core:system.status` (readiness tokens only; seeded to editor/viewer if the shell shows a status banner, 05-rbac) | 200 | `{ready, not_ready[]}` — the `/readyz` body, authenticated |
| `POST /api/core/v1/diagnostics` | `core:system.manage` | 202 + run ref | starts `core.diagnostics.export` |
| `GET /api/core/v1/diagnostics/{run}` | route `Perm` `core:system.manage` with `Guards.Interactive`; the handler **also** requires that the caller is the run's actor or holds `core:admin` (SEC2-13) | 200 zip / 403 / 404 / 410 expired | bundle, re-filtered at download time (5H.3.5); every download audited as `core.diagnostics.download` (05-rbac §7.1) |

**Status topic.** From **F8** (with the probes and routes, 5H.10), `health` declares
`core.system.status-changed.v1` in the one catalog ([05-events](05-events.md)); no earlier build
declares it. Bus only, `Realtime{Coalesce: 1s}`, `Permission` `core:system.status`
([05-rbac](05-rbac.md) §4.8 seeds it to `editor` and `viewer`). Its payload is exactly the
`GET /api/core/v1/system/status` body, `{ready, not_ready[]}` with fixed `<component>:<reason>`
tokens — never `Detail`, durations, versions or counts. It is published when `Ready()` or the set of
not-ready tokens changes, and once with `ready:false` when shutdown starts.

**The status topic is native-stream only (COR4-7, COR5-9).** Its readers are the shell's status banner and any
native-stream subscriber holding `core:system.status` ([05-realtime](05-realtime.md) §4.7). HEAD's 5-second
`system.status` frame on HEAD's `/api/events` stream (`{books, folders, memory_alloc, goroutines, timestamp}`,
`internal/server/server_lifecycle.go:404-441`) is library data, not readiness: it is deleted with HEAD's event streams
at F6 (05-realtime), and whatever dashboard ticker books keeps is a books topic, never this one.

**Probe paths are exact (FE2-6).** The liveness path is `/livez` everywhere (05-http-api uses the same
name). `/livez` and `/readyz` are `Router.Infra` exact patterns, and their first segments, with
`health` and `metrics`, are in `Router.NonSPA()` ([05-http-api](05-http-api.md) §4.10), so a variant
such as `/livez/`, `/READYZ` or `/health/` gets a 404, never `200 text/html` that a monitor would read
as healthy (today's `nonSPASegments` rule, `internal/server/spa_fallback.go:74-83`). Until the shell
owns `/` (F9: in R2 if the shell is ready, else in R3; 09), gin's SPA fallback answers unmatched paths from that
static list, which has no `livez` or `readyz`; the PR that adds the probes (F8) makes gin's fallback refuse the same
segments, so the rule holds from the probes' first release (FE3-13).

**Auth-off reach (SEC2-21).** `GET /api/core/v1/health`, `/system/info` and the diagnostics routes need
`core:system.manage`, which is `AuthRequired` (05-rbac §6.1), so with auth off they answer 401 where
HEAD's `settings.manage` routes passed every request through (`wire_admin_role.go:18-22`). This is a
deliberate tightening — detail text, versions, host data and bundles stay behind a credential (SEC-8)
— stated in R2's release note. The token-only `GET /api/core/v1/system/status` (`core:system.status`, not `AuthRequired`) and the public
probes keep their auth-off reach.

Unauthenticated bodies follow today's rule (`handler.go:223-228`): booleans, fixed tokens,
milliseconds — never error text, versions, counts or paths. ABO's `/health` shape (`status`,
`ready`, `memdb_ready`, `warmup_ms`, `degraded`, always 200) is **kept** (OPS-27): the Docker `HEALTHCHECK`, the
deploy and rollback scripts (`scripts/deploy-wait-ready.sh` waits on its `"ready":true`) and operators' uptime
monitors read it, and keeping it costs one route. It is served by the **books** module's layer reading this
registry ([09-abo-on-core](09-abo-on-core.md)), so core carries no audiobook field. HEAD's two other paths for the
same handler, `/api/health` and `/api/v1/health` (`internal/server/server_lifecycle.go:1749-1755`), are **deleted**
by F8's PR: the SPA (`/api/v1/health`) and the first-party scripts that call `/api/health` (`scripts/e2e_test.sh`,
`scripts/test-api-endpoints.py`, `scripts/api_examples.sh`, the demo-recording scripts) switch to `/health` or
`/readyz` in that same PR, never in a later one (09 §9.4 R2 flips, §9.6 row 3).

### 5H.3.4 Supervisor signals

The kernel uses `core/internal/sdnotify` (from `internal/sdnotify`) through one `health.Supervisor`
per `App`: `READY=1` once listeners are up and startup completed — **not** after module warm-up
(current deliberate choice, `server_lifecycle.go:335-339`); `STATUS=` with the not-ready tokens while
not ready; `WATCHDOG=1` at half of `WATCHDOG_USEC` when the unit sets `WatchdogSec`, driven by the
same heartbeat as `/livez` (new: today there is no watchdog ping); `STOPPING=1` at shutdown start.

`EXTEND_TIMEOUT_USEC` has exactly **two** uses (OPS2-17), both only when `NOTIFY_SOCKET` is set (Docker
and launchd have no equivalent):

1. **Startup steps** — kept from HEAD (`internal/database/startup_notify.go:15-27`,
   `internal/sdnotify/sdnotify.go:46-53`, `internal/search/register.go:55`), which the committed unit
   relies on (`deploy/audiobook-organizer.service:26-31`: `TimeoutStartSec=300` sits above the measured
   warm-up, and long steps push it out). Right before a startup step that makes real progress — store
   open (WAL replay), each migration step or chunk group of 05-storage §5S.3.5 ("Systemd budget"),
   the pre-migration checkpoint, and a search-index open (`storage/index`, 05-storage §5S.3.8) — the
   step calls
   `Supervisor.ExtendStart(step)`, which extends the start deadline to at least
   `StartupStepBudget` (10 min, HEAD's value) from that moment. It is called per step, **never from a
   timer**, so a step that hangs is still killed when its own budget runs out. Lower layers do not import
   `health`: the kernel hands storage and the search-index opener `ExtendStart` as a plain
   `func(step string)`, and hands the same func to modules as `module.Deps.ExtendStart` (06 §6.4.4,
   from R1). Below layer 6 an ABO package never imports `module` or `app` (03 §3.5.1, MOD4-18), so
   ABO's search code that stays in ABO (`internal/search/register.go:55`) does not call
   `Deps` itself: its `IndexService` takes an injected `func(step string)`, which `books.Module()`
   wires from `Deps.ExtendStart`, and so keeps extending the start deadline after `internal/sdnotify`
   moves to core (04 §8.3). Code that runs **before** `app.New` and so has no `Deps` — until R1's storage
   flip, ABO's `serve` opens the store and runs books' migrations first (`internal/database/startup_notify.go`) —
   receives the stateless `app.ExtendStart` (06 §6.4.5) the same way, as a `func(step string)` that
   `serve` injects, never by importing `app`: the same `EXTEND_TIMEOUT_USEC` write with the same
   per-step budget, reading `NOTIFY_SOCKET` per call (no package state, R5), never from a timer, a
   no-op without the socket and a failed write logged, never returned. From R1's storage flip core storage
   opens the store and calls the kernel's func itself; the first R2 boot's migration steps (05-storage) extend it
   per step like any other.
2. **Stop** — the tasks executor's one bounded extension for an in-flight `Uninterruptible` run
   ([05-tasks](05-tasks.md) 5T.4), at most `core.tasks.shutdown_extend_max` beyond the stop budget.

A deploy or rollback script must therefore not treat a long, extending start as a failure, and it can
only see the unit where the unit runs: both recipes run `scripts/deploy-wait-ready.sh` on the
operator's machine against a URL (`Makefile:916`; the script takes only `BASE_URL [TIMEOUT]`,
`scripts/deploy-wait-ready.sh:8-13`), so a local `systemctl` would query the wrong host (OPS3-9). R1
therefore changes the signature to `deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]` and, with
`SSH_HOST`, polls `ssh $SSH_HOST systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus $UNIT`:
`activating` is progress (a startup that keeps sending `EXTEND_TIMEOUT_USEC` is not failing),
`failed`/`inactive` or a rising `NRestarts` fails at once (a crash loop is reported in seconds), and
`active` (`READY=1`) starts `DEPLOY_READY_TIMEOUT`, which bounds only the wait for `"ready":true`. A
failure whose `ExecMainStatus` is 78 is reported by name: the release refused the store, either because a
later release already migrated it (an R0 or R1 binary over an R2 store) or because it is below the release's
upgrade floor (R3 over a pre-R2 store, naming R2 as the release to install first; 05-storage owns both refusals),
so the operator does not chase a crash; with `RestartPreventExitStatus=78` the unit only reads `failed`, so
without the status the script could not tell the two apart (OPS6-5). `scripts/tests/test_deploy_wait_ready.py`
covers it. Without `SSH_HOST` (Docker, launchd) it keeps URL-only polling with a documented larger
timeout; launchd relaunches a 78 exit like any unsuccessful exit (throttled), so there the refusal shows
only in the log.

```go
// Supervisor is the kernel's supervisor-signal handle (one per App, R5).
type Supervisor struct{ /* unexported */ }

// StartupStepBudget is the start time one progressing startup step asks for.
const StartupStepBudget = 10 * time.Minute

// ExtendStart: startup use (1). No-op without NOTIFY_SOCKET; a failure is
// logged and never fails startup.
func (s *Supervisor) ExtendStart(step string)

// ExtendStop: stop use (2); granted at most once per shutdown, capped by
// core.tasks.shutdown_extend_max; false when not under systemd.
func (s *Supervisor) ExtendStop(d time.Duration) bool
```

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
	Can     func(rbac.Permission) bool
}

// Contributor is registered by a module (Registrar.Diagnostics, an addition to 06).
type Contributor interface {
	Sections() []Section
}
```

(The `Writer` interface has `JSON` and `File`; generic JSONL is the free function `WriteJSONL`
because Go methods cannot have type parameters.)

**Download authorizes the credential, not only the user (SEC2-13).** The start-time `Viewer` decides
what the bundle may contain; the download re-checks the presenting credential. The route needs
`core:system.manage` with an interactive method (so a scrape key scoped `core:metrics.read`, a
delegated temp-login session or any non-interactive key of the actor gets 403), and the handler also
requires the run's actor or `core:admin`. The `core.manifest` section records each section's `Perm`,
and the handler streams the zip with every section directory omitted whose `Perm` the **download-time**
checker does not satisfy — the effective view is start-time `Viewer` ∩ download-time checker — so an
admin demoted after the run started cannot fetch what they could no longer read.

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
`core.diagnostics.retention` by the task `core.diagnostics.prune`
(`Schedule: tasks.ScheduleFromConfig("core.diagnostics.schedules.prune")`, default every 1 h; the key
lives in the owner's own namespace, 05-tasks 5T.3.8, R2-7).

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
`internal/sysinfo` unchanged in behaviour; the package as a whole is a re-implementation in `health` (04 §8.6,
R7-8), because its `RecentOperations` reader of HEAD's v2 run rows stays books' (§5H.10 table).

## 5H.4 Lifecycle and stop budget

The boot phases are defined once, in [06](06-app-sdk.md) §6.5.1 (R2-8), and the start/stop order in
06 §6.5.2. `health` writes nothing to storage during boot.

1. `app.New`: the kernel creates the `Supervisor` before the first phase (so storage open and every
   migration step of P2/P4 can call `ExtendStart`), and the `Registry` and `Startup` in P5; core and
   module components/steps register in P6–P7.
2. `App.Run`: startup steps run in table order; listeners open; `READY=1`; checks start (one
   goroutine per component with a `Probe`, each on its own interval with jitter; push-only
   components cost nothing). Probes and the heartbeat are in-process ticks, not tasks (05-tasks
   5T.3.8 lists them): their intervals are below the scheduler's 1-minute poll. Module checks call
   module code, so they start only after module hooks have started and stop before module hooks stop
   (06 §6.5.2 lists them among the core components that invoke module callbacks; COR2-15).
3. Shutdown: readiness flips to not-ready **first** (load balancers and the SPA reconnect logic
   see 503 before connections drop), `STOPPING=1`, then the kernel runs the stop order of
   [06](06-app-sdk.md) §6.5.2 (COR-8) with deadlines carved from **one** budget.

**No boot writer (A-8).** `health` writes no storage key and no state-directory file at any boot
phase, and the supervisor signals it sends (`READY=1`, `STATUS=`, `EXTEND_TIMEOUT_USEC`, `WATCHDOG=1`)
are not durable, so it adds no entry to 05-storage's crash matrix. The diagnostics bundle files are
written inside the `core.diagnostics.export` run, after boot.

**One budget key (OPS-13).** `core.shutdown.budget` is the only total; it is defined here and 06
references it. 06's `core.lifecycle.shutdown_budget` and per-hook `core.lifecycle.stop_timeout` are
replaced by this key and the phase slices below (a hook's stop gets the hooks phase's remaining time).

| Phase (06 order) | Budget | Today |
|---|---|---|
| total `core.shutdown.budget` | 25 s; **8 s** when `sysinfo.Info.Container` is set and no layer sets the key (Docker's default grace is 10 s; OPS2-23) | phases unbounded as a sum (up to 45 s) |
| listeners / HTTP drain | `min(core.shutdown.http_drain=10s, remaining)` | 30 s (`server_lifecycle.go:582`) — a behaviour change R1's release note lists |
| scheduler stop, tasks drain | `min(core.tasks.shutdown_drain=10s, remaining)` | 10 s (`:611`) |
| `Bus.Close` | `min(core.events.close_drain=2s, remaining)` | — |
| workers, module hooks, realtime | remainder, split in that order | activity flush 5 s (`:178`) |
| storage close | always runs, after the cap if need be (Pebble's WAL keeps committed batches) | — |

**The budget must fit the supervisor's stop timeout (OPS-13).** The 25 s default assumes the
supervisor waits at least 30 s before SIGKILL. That is not true today for the committed artefacts
(5H.2): the committed systemd unit sets no `TimeoutStopSec`, Docker's default grace is 10 s with no
`stop_grace_period` in compose, and the launchd plist sets no `ExitTimeOut`. R1, which moves
the lifecycle into the kernel, therefore ships `TimeoutStopSec=60` in the committed unit,
`stop_grace_period: 60s` in `docker-compose.yml` together with `ABO_CORE_SHUTDOWN_BUDGET=25s` (the
`<APP>_CORE_SHUTDOWN_BUDGET` variable, 05-config §1.3.6, read by core config from F3; in release R1 ABO reads the same
variable itself and hands the kernel the value through `app.Options.R1Values`, with the listener and perimeter values,
05-config §1.12.1; without it a container keeps the 8 s default,
because an image cannot carry a stop timeout and plain `docker run` grants 10 s), `ExitTimeOut` 60 in
the plist, and a documented `docker run --stop-timeout 60`; an operator who cannot raise the
supervisor's timeout lowers `core.shutdown.budget` below it. **The supervisor timeout is sized for the
slowest build that may run under it, not for core's budget (OPS9-11).** After a rollback (R1 to R0 by a binary
swap, or R2 to R1 by a restore) the same unit and compose file run the older build, and R0's stop is HEAD's: it
sleeps 0.5 s, drains HTTP for up to 30 s and only then
closes its stores (`internal/server/server_lifecycle.go:572,582`), so a long download or stream holds the
drain at its cap, and a 30 s timeout would SIGKILL an R0 binary before its stores close and its
operations are stamped. 60 s covers that drain plus the store close; HEAD's own unit sets no timeout, so
systemd's 90 s default applies there. Core's own budget stays 25 s, well inside it. The same unit sets `KillMode=mixed`:
under the default control-group mode systemd sends SIGTERM to every process of the unit at once, so
subprocess-mode runs (own process group, `internal/operations/registry/subprocess.go:225,328`) would
die at t = 0 instead of being stamped by the executor (OPS2-23). Check for R1:
`systemctl stop` during a running task — in-process and subprocess mode — stamps it `interrupted`
within the budget, and the journal shows no SIGKILL. It needs systemd running the committed unit, which
neither Docker nor an ordinary CI container provides by default, so it runs, with R1's
"12-minute fake migration under the unit is not killed" check of 5H.9, in 09's `unit-lifecycle` job, whose
environment (systemd as PID 1, no network for the fixture store), privileges, image preparation and triggers 09
§9.2.1 owns (R9-9, OPS9-6, OPS10-7). The only way past the **stop** budget is the tasks
executor's one bounded `ExtendStop` under systemd (5H.3.4 use 2); the startup extensions of use 1
concern the start timeout only.

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
| Bundle download by the actor's narrow key, delegated session or non-interactive method | closed: 403 | the credential, not only the user, is authorized (SEC2-13) |
| Section whose `Perm` the download-time checker no longer satisfies | closed per section: omitted from the streamed zip, listed as omitted | demotion after the run started (SEC2-13) |
| `ExtendStart` / `ExtendStop` notify write fails | open: logged, startup/shutdown continue | a supervisor signal must never fail the process (HEAD rule) |
| sysinfo platform call fails | open: field zero | informational |

## 5H.6 Concurrency contract

`Registry`, `Reporter`, `Startup` are goroutine-safe; `Snapshot`/`Ready` are lock-free reads of an
atomically swapped state. Checks run on per-component goroutines (bounded by the static component
set; kernel `lifecycle.Component`s, not tasks), never concurrently with themselves; a check's context
is canceled on its timeout and on shutdown. Diagnostics run inside one task with sections written
sequentially (the zip writer is not concurrent); section producers may use `tasks.RunItems`
internally.

## 5H.7 Config keys

`Protection` levels are 05-config's (§1.3.1; SEC2-8); `—` means `ProtectNone`.

| Key | Type | Default | Hot reload | Protection |
|---|---|---|---|---|
| `core.health.check_interval` | duration | 30s | yes | — |
| `core.health.check_timeout` | duration | 5s | yes | — |
| `core.health.live_stall` | duration | 60s | yes | — |
| `core.shutdown.budget` | duration | 25s; 8s in a container when unset (must stay below the supervisor's stop timeout, 5H.4) | **no** (restart-bound; the same value in 06, OPS2-23) | — |
| `core.shutdown.http_drain` | duration | 10s | yes | — |
| `core.diagnostics.schedules.prune` | `tasks.ScheduleConfig` | `{enabled: true, every: 1h}` | yes | — |
| `core.diagnostics.dir` | path | `diagnostics` beside the DB (relative values resolve against the DB's parent, as `core.backup.dir`; there is no data-dir key, 05-config §1.7) | no | `ProtectAdmin` (a server path, 05-config `FieldServerPath`) |
| `core.diagnostics.retention` | duration | 24h | yes | — |
| `core.diagnostics.log_lines` | int | 5000 | yes | — |
| `core.diagnostics.task_runs` | int | 200 | yes | — |
| `core.diagnostics.max_file_bytes` | int | 64 MiB | yes | — |

## 5H.8 Metrics

Names are shown under the default namespace `mediacore`; an app's `app.Options.MetricNamespace`
replaces it (06; 05-metrics owns the naming rule). HEAD emits none of these series.

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
| `TestProbes_VariantsAreNotSPA` (`/livez/`, `/READYZ`, `/health/`, `/metrics/` → 404, never `text/html`) | FE2-6 |
| `TestReadyz_FlipsBeforeHTTPDrain` | drain order |
| `TestProbes_BodyHasOnlyTokens` (fuzz Reason/Detail with secrets and paths) | no leak |
| `TestCheck_PanicAndTimeoutBecomeDown` (synctest) | isolation |
| `TestRegistry_Wait_UnblocksOnUp` (`books.memdb` pushed Up through `Deps.Health` releases a waiting `Wait`) | tasks readiness, COR4-8 |
| `TestStartup_UnknownStepRefused`, `archtest.StartupSteps` self-test | table discipline |
| `TestHeartbeat_StallFailsLivezAndStopsWatchdog` (synctest) | liveness |
| `TestStopBudget_PhasesNeverExceedTotal` (synctest; each phase sleeps past its slice) | budget |
| `TestDiagnostics_SecretsNeverSerialised` (config with secret refs; scan zip bytes for the secret values) | typed redaction |
| `TestDiagnostics_PatternRedaction` (URLs with credentials, bearer headers, invite paths) | pattern redaction |
| `TestDiagnostics_ViewerFiltersSections`, `TestDiagnostics_DownloadOtherActor404` | permission |
| `TestDiagnostics_NarrowKeyOfActor403`, `TestDiagnostics_DemotedActor403` (role removed after start: affected sections omitted or 403) | SEC2-13 |
| `TestStartup_ExtendPerStepNeverTimer` (fake notify socket: one `EXTEND_TIMEOUT_USEC` per progressing step, none while a step hangs) and R1's check "a 12-minute fake migration under the unit is not killed" (in 09's `unit-lifecycle` job, 5H.4; R9-9, OPS10-7) | OPS2-17, OPS9-6 |
| `TestStopBudget_ContainerDefault8s` | OPS2-23 |
| `TestDiagnostics_PathsConfinedToRoot` | `os.Root` |
| `TestNoGlobals_Health` (R5 AST) | no globals |

## 5H.10 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/startup` (`Table` var, `Steps`) | `health.Startup`; core rows (store, telemetry→metrics, secure state dir, key guard, encryption→secrets, config load/validate, registration gates, http) in core; `searchindex` (token `search_index`) becomes a `books` Degraded step |
| `handlers/system.HealthCheck` and the three `/health` routes | core `/livez`, `/readyz`; ABO's `/health` shape kept, served by the `books` layer from the registry (OPS-27); `/api/health` and `/api/v1/health` deleted by F8 (5H.3.3) |
| `CountAuthors` store probe | `core.storage` check (engine ping) |
| memdb warm-up readiness | component `books.memdb` (push) |
| `plugin.Registry.HealthCheckAll` + `/system/status` `plugin_health` | adapter components; `/system/status` stays a `books` route (it is mostly library data) and reads component states |
| `aidispatch.defaultHealth` global (`health.go:55`) | instance owned by the AI integration, reporting components ([05-ai](05-ai.md)) |
| `internal/sdnotify` | `core/internal/sdnotify` (+ watchdog ping) |
| `internal/sysinfo` host parts (memory, uptime, runtime) and service status | `health/sysinfo`, a **re-implementation**, not a move (04 §8.6 row, R7-8). `service.go` is a **divided file** (MOD8-5): only its host and runtime status is re-implemented in `health`; the rest stays in `internal/sysinfo` at its path. What stays: `RecentOperations` (`internal/sysinfo/service.go:77-99` @748fd787a), which converts HEAD's v2 run rows into v1 `database.Operation` for the dashboard and has no import-path-only replacement, so it stays books' dashboard code and is re-pointed at `tasks` run records in F5, when the tasks migration M4 converts HEAD's runs (05-tasks); `CollectSystemStatus`' library and import fields (`config.AppConfig.RootDir`, `database.ImportPath`, `DashboardStats`, `:197-210`); and the v1 `OperationLog` helpers (`:287-340`) |
| `internal/sysinfo` library parts, `DashboardService` | `books` dashboard; `GetHealthCheckResponse` deleted (no caller) |
| `internal/diagnostics` zip/section machinery | `health/diagnostics` |
| SlimBook/authors/series/version-groups/iTunes/missing-fields sections, `BuildBatchJSONL` | `books` `Contributor` sections; the AI batch builder goes with [05-ai](05-ai.md) |
| op `diagnostics.export` | `core.diagnostics.export`, declared in core with no ABO ID: `diagnostics.export` → `core.diagnostics.export` is a row of books' kind-rename map, which the tasks migration M4 applies once to HEAD's stored runs, so no op ID lands in a `core/` file (05-tasks §5T.8.2; 03 rule R1(c); MOD6-6); `diagnostics.ai-analyze` stays `books` |

**Landing by release** (09 assigns the releases; this is what `health` needs in each):

| Release | What of `health` exists | Why then |
|---|---|---|
| R1 | the flight recorder, rewritten onto a `*slog.Logger` (04 §8.1); `Supervisor` (`ExtendStart` behind `Deps.ExtendStart` and the stateless `app.ExtendStart`; `READY=1`/`STATUS=`/`STOPPING=1` from the kernel) | the flight recorder is a leaf with no ABO import; `internal/sdnotify` moves with the kernel lifecycle and all five importers are rewired then (04) |
| R2, tasks flip (F5) | the readiness core: `ComponentID`, `Result`, `Reporter`, `Reporters`, `Registry` (`Register`, `Ready`, `Wait`), `Registrar.Health` and `Deps.Health`, and the components `core.storage` and `books.memdb` | the tasks executor dispatches on `Ready` signals from its first release (05-tasks 5T.4 step 2, COR4-8; 06 §6.4.10) |
| R2, backup-health-metrics flip (F8) | everything else: `Snapshot`, the other components, the probes and `/api/core/v1/health`, the `/system/*` routes, the status topic, `Startup` and `Registrar.StartupSteps`, the watchdog ping, diagnostics, sysinfo; `/api/health` and `/api/v1/health` deleted, their callers switched in the same PR | 06 §6.4.10 lands `StartupSteps` and `Diagnostics` there, and every core service family moves in R2 |

## 5H.11 Open items

1. Should `/readyz` require module warm-up (e.g. `books.memdb`) or only core? This spec says modules
   decide per component via `Readiness`; ABO's deploy currently waits on `/health` `ready`, so `books.memdb`
   is Readiness=true to keep that behaviour.
2. Ring-buffer size and format for `core.logs` depend on [05-logging](05-logging.md).
3. Whether `/metrics` stays unauthenticated, and who serves it in each release (gin from the default
   registry until F8, the kernel's `Provider.Handler()` from F8), are decided in
   [05-metrics](05-metrics.md) (MOD4-3). `health` serves no `/metrics`; it only keeps `metrics` among
   the non-SPA segments (5H.3.3). The `core.metrics` bundle section lands with diagnostics at F8 and
   renders the App registry.
4. (Closed: 06 §6.5.2 takes its deadlines from `core.shutdown.budget`; `Registrar.StartupSteps` and
   `Registrar.Diagnostics` use `health.StartupStep` as here.)
5. (Closed: 05-rbac defines `core:system.status` (`ClassView`) and seeds it to `editor`/`viewer`, SEC-8.)

## Out-of-scope defects found

1. Shutdown phases can exceed the supervisor's stop timeout: HTTP drain up to 30 s (`server_lifecycle.go:582`)
   followed by the ops drain up to 10 s (`:611`) and the activity flush 5 s (`:178`), while the
   committed unit sets no `TimeoutStopSec`, Docker's default grace is 10 s and compose sets none. The
   code comment (`server_lifecycle.go:174-177`) assumes 30 s, which only `deploy/local.conf.example:33`
   provides. A slow HTTP drain can lead to SIGKILL before the ops registry stamps interrupted runs.
   Fixed by design in 5H.4; also fixable in ABO alone by carving one budget and shipping the timeouts.
2. `/system/status` returns plugin health as raw `err.Error()` strings (`handlers/system/handler.go:288-298`);
   authenticated, but inconsistent with the token-only rule used for `/health`.

## Review history

Round 1 findings applied to this file (v1.1.0):

- COR-8 — shutdown phases follow 06 §6.5.2's single order, with `Bus.Close` after the tasks drain.
- COR-19 — layer stated as L4.
- OPS-13 — premise corrected (committed unit, compose and plist set no stop timeout); one budget key defined here; supervisor timeouts and done-check required of 09 S2; HTTP drain change flagged.
- OPS-27 — ABO `/health` shape is permanent, not a compat-window alias.
- SEC-8 — detailed health and system info require `core:system.manage`; token-only `core:system.status` route.
- SIMP-6 — diagnostics prune scheduled through `ScheduleFromConfig`; probes and heartbeat stated as in-process ticks.
- COR-16 (alignment) — `Step` renamed `StartupStep` to match 06.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-G request (COR-21) — `health.Reporters` (module handle, `ErrNotOwned`) defined for `module.Deps.Health`.
- integration pass: R-D request (SEC-8) — `core.system.status-changed.v1` declared here with permission `core:system.status` and a token-only payload, as 05-realtime §4.7 consumes it.
- integration pass: Open items 4 and 5 closed (06 §6.5.2; 05-rbac `core:system.status`).

Round 2:

- COR2-14 / SEC2-25 — `Viewer.Can` takes `rbac.Permission` (no `rbac.Scope`).
- COR2-15 (reference) — module health checks start after and stop before module hooks (06 §6.5.2).
- FE2-6 — one liveness path `/livez`; probe first segments plus `health`/`metrics` are non-SPA; variant test.
- OPS2-17 — `EXTEND_TIMEOUT_USEC` has two uses: per-step startup extension (HEAD's 10 min, never on a timer, `Supervisor.ExtendStart` passed to storage/indexer as a func) and the executor's stop extension; deploy-wait note.
- OPS2-23 — `core.shutdown.budget` restart-bound; `KillMode=mixed` in the S2 unit and subprocess-mode stop check; 8 s container default with `ABO_CORE_SHUTDOWN_BUDGET=25s` in compose.
- R2-7 — diagnostics prune schedule key `core.diagnostics.schedules.prune` in the owner's namespace.
- R2-8 — lifecycle references 06 §6.5.1; `Supervisor` exists before P2.
- SEC2-8 — Protection column in 5H.7 (`core.diagnostics.dir` is `ProtectAdmin`).
- SEC2-13 — diagnostics download: `core:system.manage` + interactive, actor-or-admin, sections re-filtered at download time; tests.
- SEC2-21 — auth-off tightening of health/info/diagnostics stated, recorded in 09 §9.2.6.

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): §5H.3.4: `ExtendStart` also reaches books' database and search code that stays in ABO during S2–S7 via `module.Deps.ExtendStart`; search-index open cites `storage/index`, not 05-indexer.
- integration pass (round 2): `core.diagnostics.dir` defaults to `diagnostics` beside the DB (no data-dir key); diagnostics downloads audited as `core.diagnostics.download`.

Round 3:

- R3-1 — "a mirror rebuild" removed from the startup steps that call `ExtendStart` (no mirrors during the extraction).
- OPS3-9 — deploy-wait note: the script runs on the operator's machine; 09 S0's `BASE_URL TIMEOUT [SSH_HOST UNIT]` signature polls the unit over ssh (`activating` = progress, `failed`/`inactive`/rising `NRestarts` = immediate failure, `active` starts `DEPLOY_READY_TIMEOUT`); URL-only without `SSH_HOST`.
- FE3-13 — probe-path rule stated to hold during S8 too: gin's SPA fallback refuses `livez`/`readyz` from the probes' first release.
- R3-2 — verified: this file specifies no release-promotion gating.

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): §5H.3.4: code that runs before `app.New` (ABO's S2 `serve`: store open and books' migrations) uses the stateless `app.ExtendStart` (06 §6.4.5); search keeps `module.Deps.ExtendStart`.

Round 4:

- COR4-8 — the readiness core (`ComponentID`, `Reporter`, `Reporters`, `Registry.Register`/`Ready`/`Wait`, `Registrar.Health`, `Deps.Health`; `core.storage`, `books.memdb`) lands with `tasks` at S5; "Landing by stage" table in 5H.10; test reworded.
- COR4-7 — the status topic is declared from S8 only; before S8 projection A carries books' status frame; the S8 payload change is recorded in the compat ledger.
- MOD4-18 — ABO's search and store-open code receive `ExtendStart` as an injected `func(step string)` (`IndexService`, `serve`), never by importing `module` or `app` below layer 6.
- MOD4-3 — open item 3: `/metrics` ownership per stage is 05-metrics'; `health` serves none; the `core.metrics` bundle section exists from S8.

Round 5:

- COR5-9 (COR4-7 residue) — 5H.3.3 "Stream A's `system.status` frame" is the authoritative statement: the status topic is native-stream only and never added to projection A; stream A's frame keeps HEAD's payload at every stage (books' `books.system.status-ticked.v1` from S6); no compat-ledger row; the "from S8 the frame's payload changes" sentence deleted (supersedes the round-4 COR4-7 line's compat-ledger clause).
- integration pass (round 5): §5H.3.1 component registry — the perimeter's fixed degraded token `basic_gate_no_password` (from S3 core stage 8, before S3 ABO's hotfixed gate through HEAD's token list; R5-1(c), SEC5-4, 05-authn §11.1 early fix 5).

Round 6:

- MOD6-6 — 5H.10: `core.diagnostics.export` carries no ABO op ID in core; books supplies `diagnostics.export` through `app.Options.LegacyTaskAliases` (05-tasks 5T.8.2).
- OPS6-5 — 5H.3.4: the wait script reads `ExecMainStatus` and reports exit 78 (upgrade-floor refusal) by name, covered by `test_deploy_wait_ready.py`; launchd relaunches a 78 exit.
- COR6-9 — header: `health/diagnostics` takes `health`'s layer (L4) under 03 R4.
- OPS6-6 — verified: this file already uses `basic_gate_no_password` (5H.3.1); no change.
- integration pass (round 6): §5H.3.1 registers `core.backup` (S8) and states the advisory tokens owned by other files: `config_legacy_name_shadowed` on `core.config` (warn = `StatusDegraded`, SEC6-1) and `revocation_journal_colocated` on `core.backup` (info = `StatusUp` with a reason, SEC6-10); no `basic_gate_no_username`, because 05-http-api keeps the empty Basic username at HEAD parity (SEC6-9).

Round 7:

- SEC7-12 — 5H.3.1: `auth_disabled_remote` and `config_legacy_name_shadowed` are carried by HEAD's `degraded` token list from S4 r2 until S8, beside `basic_gate_no_password`.
- SEC7-3 (cross-cluster request, R-F) — 5H.3.1 lists the integration instance reason `operator_secret_withheld` (05-metadata M1.4 step 2).
- integration pass (round 7): §5H.3.6 and §5H.10: `internal/sysinfo` is a re-implementation in `health` (04 §8.6), its `RecentOperations` reader of HEAD's v2 run rows staying books' (R-H request); SEC7-12 tokens checked: already present.

Round 8:

- R8-1 / COR8-3 / COR8-4 / DATA8-1 / OPS8-5 — 5H.3.1 "Factory-reset tokens": `factory_reset_failed` and `factory_reset_cleanup_incomplete` on `core.storage`, carried by HEAD's `degraded` token list from S2b until S8; a staged or in-flight reset uses the one swap's reasons, with no reset-only token.
- SEC8-9 — advisory token `config_narrowing_owed` on `core.config` (warn), carried by HEAD's token list until S8.
- R8-5 / SEC8-2 (R-F request) — `operator_secret_withheld` is raised when a destination's origin matches neither the operator or adapter-default origin nor the instance's `OperatorSecretOrigin`.
- MOD8-5 — 5H.10 `internal/sysinfo` row: `service.go` is a divided file; host and runtime status → `health`, while `RecentOperations`, the library and import fields and the `OperationLog` helpers stay in `internal/sysinfo`.

Advisor (2026-11-04):

- A-2 / ADV-S1 — `config_narrowing_owed` (and its `operator-names.json` write) deleted with the narrowing writes; `operator_secret_withheld` scoped to native-backed installs, a legacy-backed instance keeping HEAD's behaviour; `config_legacy_name_shadowed` restated as HEAD's precedence (stored value wins); advisory tokens now two.
- A-2 / A-14 — checked: no narrowing, canonical-only, extension-suspension or usage-gate token remains in the normative text.
- A-5 / ADV-S4 — 5H.8: names under the default namespace, replaced by `app.Options.MetricNamespace`; no ABO series renamed.
- A-8 / ADV-4 — 5H.4 "No boot writer": `health` writes nothing durable at boot, so it adds no crash-matrix entry.

Round 9:

- OPS9-11 — 5H.4: the S2 committed unit ships `TimeoutStopSec=60`, compose `stop_grace_period: 60s`, the plist `ExitTimeOut` 60 and `docker run --stop-timeout 60`, sized for the slowest build that may run under them after a rollback (HEAD's 30 s drain plus store close); core's budget stays 25 s.
- R9-9 / OPS9-6 — 5H.4 and 5H.9: the S2 `systemctl stop` done-check and the S0 12-minute migration check run in the systemd-enabled `--network none` CI container 09 §9.3 defines.
- R9-6 / R9-7 / DATA9-4 — 5H.3.1 `factory_reset_failed`: the move-back precedes `rejected`; a reset journal at `set_aside_started` or later is resumed forward, never rejected for expiry or by another build, so the failure rule is the token's only source; `inflight.json` timing raises no health token.

Round 10:

- R10-7 / OPS10-3 — 5H.3.1: `legacy_config_keys` stated as an advisory token on `core.config` (warn, for the whole of W from S3, carried by HEAD's `degraded` list until S8; advisory tokens now three); new paragraph "the soak allow-list": the soak gate reads health at record time (no Critical component Down, no token outside 09's committed `ci/soak_health_allow.txt`), and the list holds `auto_update_paused`, `legacy_config_keys`, `config_legacy_name_shadowed`, `auth_disabled_remote`, `revocation_journal_colocated` and the `core.integrations.*` reasons.
- SEC10-11 (consistency with 05-config) — 5H.3.1: a name a shipped deploy file sets unconditionally, at that file's default, raises no `config_legacy_name_shadowed` token and is reported at info.
- OPS10-7 (consistency with 09) — 5H.4 and 5H.9: the stop and 12-minute checks run in 09's `unit-lifecycle` job, whose environment, privileges and triggers 09 §9.3 owns, instead of naming a container this file does not own.
- D10–D12 revision (2026-11-04): stages re-keyed to releases (R1: flight recorder and `Supervisor`; R2: the rest).
  Deleted the tokens `legacy_config_keys` and `config_legacy_name_shadowed` (the retired-name boot refusal of
  05-config §1.3.6 replaces them), the soak allow-list paragraph (no soaks), the stream-A `system.status`
  projection paragraph and `TestProjectionTopicsExistAtStage` (the legacy streams go in R2), the legacy-backed
  exception to `operator_secret_withheld`, the `TestLegacyRoleReachUnchanged` reference, the `LegacyTaskAliases`
  route for `diagnostics.export` (now a row of the kind-rename map applied by M4) and "carries the legacy families"
  in open item 3. Rewritten: 5H.3.1 (one landing statement; `basic_gate_no_password` through HEAD's token list in R0
  and R1 only; factory-reset tokens through HEAD's list in R1), the advisory tokens (new on `core.config`:
  `config_fail_closed_override` and `config_retired_name_ignored`), 5H.3.3 (`/health` kept, `/api/health` and
  `/api/v1/health` deleted in R2 with their first-party callers switched), 5H.3.4 (the exit-78 message covers both
  refusals: a store a later release migrated, and R3's floor), 5H.4 (rollback means R1 to R0 by binary swap or R2 to
  R1 by restore; the 60 s supervisor timeout is sized for R0's HEAD stop), 5H.10 "Landing by release".
  Consistency pass: R2 landings keyed to the flips (readiness core F5, the rest and the alias deletion with their callers F8,
  advisory tokens F3, origin binding F7), the swap token pair and the R1 budget path re-pointed to 05-storage §5S.3.15 and
  05-config §1.12.1, and `unit-lifecycle` to 09 §9.2.1.
