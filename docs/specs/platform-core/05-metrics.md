<!-- file: docs/specs/platform-core/05-metrics.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5cdf765d-aec1-4ed7-981a-e2e698afde61 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Metrics and tracing (`core/metrics`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §4, §9;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (OTel v1.47.0, exporter
v0.69.0, client_golang v1.25.0). Normative: [03-architecture](03-architecture.md) R4 (`metrics` is in the
lowest layer), R5 (no `prometheus.DefaultRegisterer`, no `otel.Set*Provider`), §3.6 (`mediacore_<subsystem>_*`,
`<app>_<moduleID>_*`). Compat window rule: [02-decision-record](02-decision-record.md) §2.5 Q5.

## Decisions at a glance

| # | Decision |
|---|---|
| M1 | One `metrics.Provider` per `App` owns a private `*prometheus.Registry`, an OTel `MeterProvider`, a `TracerProvider` and a propagator. All are **injected**; nothing in core touches `prometheus.DefaultRegisterer`/`DefaultGatherer` or `otel.Set*`/`otel.Get*`. |
| M2 | The authoring API is the **OTel metric API** (`metric.Meter`), continuing ABO's in-flight migration (client_golang family ceiling 64 and falling, `internal/telemetry/testdata/client_golang_baseline.txt`). Prometheus is an exporter bound to the App's registry. client_golang is used directly only inside `core/metrics` (registry, Go/process collectors, promhttp) and through one escape hatch, `Provider.Registerer()`, for third-party collectors. |
| M3 | Names: core instruments `mediacore.<subsystem>.<what>` → `mediacore_<subsystem>_<what>[_unit][_total]`; module instruments `<app>.<module>.<what>` → `<app>_<module>_…`. The Prometheus translation strategy is **pinned** (`UnderscoreEscapingWithSuffixes`), not left to the exporter's process-global default. OTel semantic-convention instruments (`http.server.*`) keep their standard names. |
| M4 | Cardinality is a contract: closed attribute-key allow-list per scope, bounded value sets only, no identifiers; SDK per-instrument limit 2000; a series golden per module. |
| M5 | `/metrics` is a core route mounted by the kernel, permission `core:metrics.read`, outside response compression. |
| M6 | ABO's dashboard names keep working for two minor releases through **gather-time aliases** (no double instrumentation), plus shipped recording rules for operators who need longer. |

## 1. Purpose & scope

Give every `App` isolated telemetry: a Prometheus scrape endpoint, optional OTLP metric push, optional OTLP
traces, consistent naming, enforced cardinality, and a compatibility bridge for ABO's existing series.
Out of scope: what each subsystem measures (each 05-* file tables its own metrics), log export
([05-logging](05-logging.md)), health/readiness ([05-health](05-health.md)).

## 2. Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| client_golang collectors | ~34 package-level collectors, every one with literal `Namespace: "audiobook_organizer"`; registered once with `prometheus.MustRegister` on the **default registry** | `internal/metrics/metrics.go:10-60`, `Register` `metrics.go:300-326` (MustRegister at `:302,:311`) |
| Pebble collector | `prometheus.NewDesc(prometheus.BuildFQName("audiobook_organizer", "", name), …)` | `internal/metrics/pebble_collector.go:119-146` |
| Per-run label | `op_items_processed` / `op_items_total` gauges labelled `op_id` (one series per run, deleted at run end) | `metrics.go:260-284,430-442` |
| OTel meter provider | built once per process, Prometheus exporter on the default registry, `WithoutScopeInfo`, then `otel.SetMeterProvider` | `internal/telemetry/telemetry.go:378,446-461` |
| OTel tracer provider | OTLP/gRPC batcher, `otel.SetTracerProvider`; tracing off in production (no collector) | `telemetry.go:284-287`, `telemetry.go:36-38` |
| Global lookups | `otel.Tracer("audiobook-organizer/…")` package vars in `ai`, `dedup`, `operations/registry`; `otel.GetMeterProvider()` 5 calls in 4 files; `opsmetrics.Default()` (global recorder) 9 calls in 5 files | `internal/ai/telemetry.go:27`, `internal/dedup/engine.go:40`, `internal/operations/registry/worker.go:30`, `internal/opsmetrics/opsmetrics.go:290-300` |
| Scope helper | `telemetry.Meter(name)` → scope `audiobook-organizer/<name>` on the global provider; `MeterFrom(mp, …)` for injection | `internal/telemetry/meter.go:17-53` |
| Naming contract | dotted lowercase `audiobook_organizer.<area>.<what>`; counters never end in `_total`; histograms set a unit; `ai_dispatch.`/`ai.` families are unprefixed legacy exceptions | `meter.go:20-37` |
| Attribute allow-list | 30 instrument keys; `op_id` and HTTP semconv keys are "scrape-only" (closed to new instruments) | `internal/telemetry/attr.go:91-102` |
| Series contract | `internal/telemetry/contract/testdata/series.golden`: **87 series** (74 `audiobook_organizer_*`, 10 `ai_*`, 3 `http_server_*`); histogram buckets golden; views table | `contract/testdata/`, `internal/telemetry/views.go:37-80` |
| Process identity | `instanceID` = random ULID per process (hostname-free), package var | `internal/telemetry/resource.go:31` |
| Error handling | rate-limited OTel error handler and value-dropping SDK logger, both process-global | `internal/telemetry/errorhandler.go:121`, `sdklog.go:38-39` |
| `/metrics` | `promhttp.Handler()` (default gatherer); any authenticated credential when auth is on; excluded from gin gzip after a production scrape failure | `internal/server/server_lifecycle.go:1735-1747`, `internal/server/server.go:708-716` |
| Other default-gatherer readers | cache stats handler, cache snapshotter | `internal/server/handlers/cache.go:99`, `internal/server/cache_snapshotter.go:24` |
| Dashboards / rules | 2 Grafana dashboards, 9 alert rules, 1 compat recording rule (`label_replace` of `def_id` → `op_type`); they reference 20 `audiobook_organizer_*` names, 7 `ai_*`, and `process_resident_memory_bytes` (from the default registry's process collector) | `deploy/grafana/dashboards/*.json`, `deploy/prometheus/alert-rules.yml`, `deploy/prometheus/recording-rules.yml:14-19` |
| Exporter default naming | `exporters/prometheus` picks its translation strategy from the process-global `model.NameValidationScheme` unless set | `<gomodcache>/go.opentelemetry.io/otel/exporters/prometheus@v0.69.0/config.go:103-125` |

Multi-app consequence (`raw/12` §4): a second `Register` panics on duplicate collectors, and two apps would
share one meter/tracer provider and one resource.

## 3. Go API

Package `github.com/falkcorp/mediacore/metrics`. Imports: `prometheus/client_golang` (`prometheus`,
`collectors`, `promhttp`), OTel API (`metric`, `trace`, `propagation`, `attribute`), OTel SDK (`sdk/metric`,
`sdk/trace`, `sdk/resource`), exporters (`exporters/prometheus`, `otlpmetricgrpc`, `otlptracegrpc`),
`github.com/prometheus/otlptranslator`. No other core package (R4).

```go
package metrics

type Options struct {
	AppID            string // "abo"; service.name default
	AppPrefix        string // module metric prefix, [a-z][a-z0-9_]*; ABO: "audiobook_organizer"
	ServiceVersion   string
	InstanceID       string // per-process random ULID, never a hostname
	Environment      string // deployment.environment
	Prometheus       bool
	OTLP             *OTLPMetrics // nil = no push
	Tracing          *OTLPTracing // nil = no-op TracerProvider
	CardinalityLimit int          // per instrument; 0 = 2000 (SDK default)
	Logger           *slog.Logger
}

type OTLPMetrics struct {
	Endpoint string        // host:port or URL, validated like ABO's endpoint.go
	Insecure bool
	Interval time.Duration // clamped to [5s, 1h], default 60s (ABO telemetry.go:310-312)
}

type OTLPTracing struct {
	Endpoint    string
	Insecure    bool
	SampleRatio float64 // parent-based ratio sampler; 1.0 = ABO's current behaviour (SDK default sampler)
}

// Provider is one App's telemetry. Safe for concurrent use.
type Provider struct{ /* unexported */ }

func New(ctx context.Context, o Options) (*Provider, error)

func (p *Provider) MeterProvider() metric.MeterProvider
func (p *Provider) TracerProvider() trace.TracerProvider
func (p *Provider) Propagator() propagation.TextMapPropagator // TraceContext + Baggage
func (p *Provider) Registerer() prometheus.Registerer          // escape hatch for third-party collectors
func (p *Provider) Gatherer() prometheus.Gatherer
func (p *Provider) Handler() http.Handler                       // /metrics: promhttp on the App registry + aliases
func (p *Provider) Shutdown(ctx context.Context) error           // flush OTLP, stop readers; idempotent

// Scopes enforce naming. Core subsystems get "mediacore.<subsystem>"; modules "<AppPrefix>.<module>".
func (p *Provider) Core(subsystem string, allowed ...attribute.Key) (*Scope, error)
func (p *Provider) Module(id string, allowed ...attribute.Key) (*Scope, error)

type Scope struct{ /* unexported */ }

func (s *Scope) Prefix() string                 // "mediacore.tasks", "audiobook_organizer.books"
func (s *Scope) Meter() metric.Meter            // validating wrapper over the App's MeterProvider
func (s *Scope) Tracer() trace.Tracer           // tracer named "<prefix>"
func (s *Scope) Allowed() []attribute.Key       // declared keys; asserted by metricstest
func (s *Scope) View(instrument string, buckets ...float64) sdkmetric.View // histogram bucket declaration

// Legacy aliases (compat window). Applied at gather time by Handler: matching families are copied under
// the old name with label renames/value maps. No instrument is recorded twice.
type Alias struct {
	New         string                         // Prometheus family name as exported now
	Old         string                         // legacy family name
	LabelRename map[string]string              // new label -> old label
	ValueMap    func(label, value string) string // e.g. task kind "books.library-scan" -> def_id "library-scan"
	RemoveIn    string                         // release that drops it; logged at startup
}

func (p *Provider) AddAliases(a ...Alias) error

var (
	ErrInstrumentName = errors.New("metrics: instrument name violates naming rules")
	ErrScopeTaken     = errors.New("metrics: scope already registered")
	ErrAliasConflict  = errors.New("metrics: alias collides with a live family")
)
```

**Instrument rules enforced by `Scope.Meter()`** (constructor returns `ErrInstrumentName` and a no-op
instrument, which the module surfaces at registration so boot fails closed): name =
`<scope prefix>.<seg>(.<seg>)*`, segments `[a-z][a-z0-9_]*`; counters must not end in `total`; histograms must
have a unit (`s`, `By`, or `{thing}`) and a declared view (buckets); gauges whose legacy name ends in `_total`
use a bracket unit, exactly as ABO's rules (`meter.go:20-37`). These rules are ABO's, made per-scope.

**Exporter construction (normative):**
`prometheus.New(prometheus.WithRegisterer(reg), prometheus.WithoutScopeInfo(),
prometheus.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes))` — the three options
verified at exporter v0.69.0 (`config.go:67,120,170`). The registry also gets
`collectors.NewGoCollector()` and `collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})`
explicitly (the default registry had them implicitly; ABO's memory alert reads
`process_resident_memory_bytes`). `Handler` wraps `promhttp.HandlerFor(gatherer, opts)` in
`promhttp.InstrumentMetricHandler(reg, …)` so `promhttp_metric_handler_*` series also survive the move.

**Tracing and HTTP.** `httpapi` instruments with `otelhttp` using `WithTracerProvider`,
`WithMeterProvider` and `WithPropagators` from the Provider — never the globals. The semconv metric names
(`http_server_request_duration_seconds`, request/response body size) are identical to what `otelgin`
emits today (series golden), so moving off gin does not change them.

## 4. Naming conventions

| Kind | OTel instrument name | Prometheus family | Example |
|---|---|---|---|
| Core subsystem | `mediacore.<subsystem>.<what>` | `mediacore_<subsystem>_<what>[_<unit>][_total]` | `mediacore.tasks.runs` → `mediacore_tasks_runs_total` |
| Module | `<app>.<module>.<what>` | `<app>_<module>_<what>…` | `audiobook_organizer.books.scan.files` → `audiobook_organizer_books_scan_files_total` |
| Semconv | spec name | translated | `http.server.request.duration` → `http_server_request_duration_seconds` |
| Resource | — | `target_info{service_name,service_version,service_instance_id,deployment_environment}` | — |

Core metric names are therefore identical across ABO, TV and Movies, so one set of core dashboards serves all
apps (distinguished by `job`/`service_name`).

## 5. Cardinality rules (normative)

1. Every scope declares its attribute keys (`Core(…, keys)` / module manifest `Metrics.Attributes`); keys come
   from a shared vocabulary (`outcome`, `kind`, `module`, `provider`, `adapter`, `capability`, `endpoint`,
   `reason`, `class`, `state`, `phase`, `direction`, `cache`, `route`, `method`, `status_class`, …) extended
   per module with review.
2. Values must come from a set that is finite and known at registration: enums, registered task kinds,
   enabled module IDs, configured adapter/endpoint IDs, route templates. **Forbidden as values:** entity IDs,
   task run IDs, user IDs, request IDs, file paths, raw URLs, error strings, titles/names.
3. Per-instrument SDK cardinality limit `core.metrics.cardinality_limit` (default 2000,
   `sdk/metric@v1.47.0/config.go:183`); overflow lands in the SDK's overflow series and fails
   `metricstest` in CI.
4. Histogram buckets are declared per instrument (`Scope.View`); no exponential histograms by default
   (ABO pinned this for OTLP, `telemetry.go:418-420`).
5. A series golden per scope (`testdata/series.golden`: family, type, label set) is asserted by
   `metricstest.AssertSeries`; changing it is a reviewed diff — the generalization of ABO's contract test.

## 6. `/metrics` ownership

The kernel mounts `Provider.Handler()` at `core.metrics.path` (default `/metrics`, outside `/api`) through
`httpapi` with permission **`core:metrics.read`** (R6: explicit), **excluded from compression middleware**
(promhttp negotiates gzip itself; double gzip broke production scrapes, `server.go:708-716`). Optional
dedicated listener `core.metrics.listen` (e.g. a loopback address) serving only `/metrics`; it requires the
same credential unless the operator sets `core.metrics.listen_public=true` (`ProtectOperator`, logged at
warn on every boot). Two apps in one process each expose their own registry on their own listener or path;
registries are never merged.

## 7. Lifecycle

1. `app.New`, right after logging: `metrics.New` with options from `core.metrics` / `core.tracing` (operator
   layers). OTLP exporters are built non-blocking; a bad OTLP endpoint is **degraded, not fatal** (ABO
   learned this the hard way, `telemetry.go:52-58`): health token `metrics_otlp_disabled` /
   `tracing_disabled`.
2. Every component receives a `*Scope` (`module.Deps.Metrics` for modules, [06-app-sdk](06-app-sdk.md)).
   Instruments are created at construction; a naming error fails boot.
3. The kernel installs legacy aliases supplied by app modules (ABO's books module) before listeners open.
4. `App.Run` shutdown: `Provider.Shutdown` after the HTTP server stops and before logging closes; it flushes
   the last OTLP batch within the shutdown budget.

## 8. Error semantics

| Path | Mode | Why |
|---|---|---|
| Prometheus exporter/registry construction fails | fail-closed (boot) | programming error; scrape is the primary signal |
| OTLP metric/trace exporter cannot be built | fail-open, degraded health token | telemetry must not crash-loop the app (`telemetry.go:52-58`) |
| OTLP export errors at runtime | fail-open, rate-limited log, counter | collector outages are routine |
| Instrument name/unit/view violation | fail-closed at construction (`ErrInstrumentName`) | contract violations caught at boot and in CI |
| Duplicate scope | fail-closed (`ErrScopeTaken`) | two owners for one name |
| Alias collides with a live family | fail-closed at `AddAliases` | would corrupt a live series |
| Cardinality limit reached | fail-open (overflow series), test failure in CI | data loss for one instrument only |
| Gather error in `/metrics` | `promhttp.ContinueOnError`: partial scrape + `promhttp_metric_handler_errors_total` | one bad collector must not blank the scrape |
| Unauthorized scrape | 401/403 | R6 |

## 9. Concurrency contract

`Provider`, `Scope`, instruments, tracer and handler are safe for concurrent use (OTel SDK and client_golang
guarantees). Observable-gauge callbacks must be non-blocking and bounded (read an atomic or a snapshot; never
I/O) — ABO's in-flight gauge callback pattern (`opsmetrics.go:440`) is the model. `Shutdown` is idempotent and
bounded by `ctx`. No goroutines except the SDK's periodic reader and batch span processor, both stopped by
`Shutdown`.

## 10. Config keys

| Key | Type | Default | Hot reload? |
|---|---|---|---|
| `core.metrics.prometheus.enabled` | bool | `true` | no |
| `core.metrics.path` | string | `/metrics` | no |
| `core.metrics.listen` | string (host:port) | `""` (main listener) | no |
| `core.metrics.listen_public` | bool | `false` (`ProtectOperator`) | no |
| `core.metrics.otlp.endpoint` | string | `""` (aliases: `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, permanent; ABO `otel_metrics_otlp_endpoint`) | no |
| `core.metrics.otlp.interval` | duration | `60s` | no |
| `core.metrics.otlp.insecure` | bool | `false` | no |
| `core.metrics.environment` | string | `prod` (ABO `telemetry_environment`, `resource.go:25`) | no |
| `core.metrics.cardinality_limit` | int | `2000` | no |
| `core.metrics.legacy_aliases` | bool | `true` during the window, then the key is removed | no |
| `core.tracing.otlp.endpoint` | string | `""` (aliases: `OTEL_EXPORTER_OTLP_ENDPOINT`, permanent; ABO `otel_exporter_otlp_endpoint`) | no |
| `core.tracing.otlp.insecure` | bool | `false` | no |
| `core.tracing.sample_ratio` | float [0,1] | `1.0` (parent-based) | no |

Telemetry wiring is construction-time; making it live would require swapping providers under running
instruments, which the OTel SDK does not support.

## 11. Metrics (of this subsystem)

| Name | Type | Labels |
|---|---|---|
| `mediacore_metrics_otlp_export_failures_total` | counter | `signal` ∈ metrics, traces |
| `mediacore_metrics_legacy_alias_families` | gauge | — |
| `promhttp_metric_handler_requests_total` | counter | `code` (client_golang standard) |
| `promhttp_metric_handler_errors_total` | counter | `cause` (client_golang standard) |
| `target_info` | gauge | resource attributes |

## 12. Tests (core must have)

| Test | Asserts |
|---|---|
| `TestNoGlobals` (archtest, R5) | no `prometheus.DefaultRegisterer/DefaultGatherer/MustRegister/Register`, `promhttp.Handler()`, `otel.Set*`/`otel.Get*Provider`/`otel.Tracer`/`otel.Meter` in core |
| `TestTwoProvidersIsolated` | two Providers register identical instruments without panic; scrapes are disjoint |
| `TestScope_RejectsBadNames` | prefix, `_total` suffix, missing unit/view → `ErrInstrumentName` |
| `TestTranslationStrategyPinned` | names identical with the global validation scheme set to legacy and to utf8 |
| `TestGoAndProcessCollectorsPresent` | `go_goroutines`, `process_resident_memory_bytes` in the scrape |
| `TestAliases_GatherTimeCopy` | old family present with renamed labels and mapped values; no double recording; collision rejected |
| `TestHandler_NoDoubleGzip` | `Accept-Encoding: gzip` through the httpapi stack yields one gzip layer |
| `TestCardinality_OverflowFailsMetricstest` | exceeding the limit is detected |
| `TestOTLPBadEndpointDegrades` | provider builds, health token set |
| `TestShutdownFlushes` | `testing/synctest` + in-memory exporter: last batch exported |
| `metricstest.AssertSeries` (per scope) | series golden for each core scope |

## 13. Migration from ABO

1. **Injection first.** `internal/telemetry`'s mechanism (`InitOTEL`, views, endpoint validation, resource,
   error handler) moves into `core/metrics` (03 §3.2.1 E2 already moves the package for the build experiment);
   process-global installs move to `app.InstallProcessDefaults` ([05-logging](05-logging.md) §3). The three
   `otel.Tracer` package vars, `otel.GetMeterProvider()` calls and `opsmetrics.Default()` become injected
   scopes; `opsmetrics` becomes core `tasks` instrumentation ([05-tasks](05-tasks.md)) on scope
   `mediacore.tasks`. `cache.go:99` and `cache_snapshotter.go:24` take `Provider.Gatherer()`.
2. **client_golang families** in `internal/metrics` finish the move to OTel (the ratchet continues to zero);
   until then they register on `Provider.Registerer()` — never the default registry. `metrics.Register`'s
   `sync.Once` global disappears.
3. **Renames with aliases** (window: two minor releases after the release that renames; `RemoveIn` logged):

| ABO family (today) | New family | Alias? |
|---|---|---|
| `audiobook_organizer_ops_runs_total`, `_ops_run_duration_seconds`, `_ops_items_total`, `_ops_inflight`, `_ops_progress_age_seconds`, `_ops_progress_timeout_seconds`, `_ops_schedule_lag_seconds`, `_ops_schedule_missed_total`, `_ops_zombies`, `_ops_abandoned_calls` (label `def_id`) | `mediacore_tasks_*` (label `kind`, values `books.<def>`) | yes: name + `kind`→`def_id` + value map from the task-kind alias table (05-tasks) |
| `audiobook_organizer_operations_{started,completed,failed,canceled}_total`, `_operation_duration_seconds` (label `type`) | superseded by the tasks family | yes for the window (dashboards and alerts use them), computed from `mediacore_tasks_runs_total` by outcome |
| `audiobook_organizer_op_items_processed`, `_op_items_total` (label `op_id`) | **retired** (forbidden per-run label; progress is served by the tasks API/realtime) | no alias; kept emitting by the books module's legacy collector for the window under a dated exemption, then deleted |
| `audiobook_organizer_pebble_*` | `mediacore_storage_pebble_*` | yes |
| `audiobook_organizer_process_memory_alloc_bytes`, `_process_goroutines` | `go_memstats_alloc_bytes`, `go_goroutines` (Go collector) | yes |
| `ai_dispatch_*`, `ai_*` (unprefixed legacy) | `mediacore_ai_dispatch_*`, `mediacore_ai_*` | yes |
| `http_server_*` (semconv) | unchanged | not needed |
| every other `audiobook_organizer_<x>` (books, search index, catalog, cache, ABS, review, fixer, metadata fetch, …) | `audiobook_organizer_books_<x>`; a leading `books_` in `<x>` is not repeated, so `audiobook_organizer_books_total` keeps its exact name | yes where the name changes |

4. **Dashboards and rules** in `deploy/` are updated to the new names in the same change that adds the
   aliases (so the repo's own dashboards never depend on aliases); `deploy/prometheus/recording-rules.yml`
   gains a generated `audiobook_organizer_compat` block mapping new → old for operators with external
   dashboards beyond the window (the file already uses this technique, `recording-rules.yml:14-19`).
5. ABO's `/metrics` behaviour (any authenticated credential) is preserved by granting `core:metrics.read` to
   all three ABO seed roles in the role mapping ([09-abo-on-core](09-abo-on-core.md), [05-rbac](05-rbac.md)).

## 14. Open items

1. **OTLP transport.** gRPC is used only by telemetry (`go mod why -m google.golang.org/grpc` →
   `internal/telemetry`), and it is the module that drags in the genproto split that makes `go.work` fail
   (03 §3.2). Measure whether `otlpmetrichttp`/`otlptracehttp` remove gRPC from core's `go list -deps`; if they
   do, make HTTP the only transport (wave-0 experiment, [13-toolchain](13-toolchain.md)).
2. Exemplars (trace-id on histogram buckets) once tracing has a collector; off until then.
3. Whether `core.metrics.environment` belongs in a shared `core.app` section (also used by updater and
   diagnostics).

## Objections to 03

1. §3.6 gives two metric patterns only. OTel **semantic-convention** instruments (`http.server.*`, and any
   future `rpc.*`/`db.*`) must keep their standard names to stay compatible with stock dashboards and with what
   ABO exports today (`series.golden` lines for `http_server_*`). Proposed amendment: add a third row
   "semconv instruments: spec name, unprefixed".
2. §3.6 example `mediacore_tasks_runs_total` is consistent with this file; the module pattern
   `<app>_<moduleID>_*` needs `<app>` defined. This spec defines it as `app.Options.MetricPrefix`, passed to
   `metrics.Options.AppPrefix` (`[a-z][a-z0-9_]*`; ABO: `audiobook_organizer`, keeping the leading prefix of every
   existing ABO series). 03
   should name that option or point here.

## Out-of-scope defects found

None beyond those listed in [05-logging](05-logging.md) and [05-config](05-config.md).
