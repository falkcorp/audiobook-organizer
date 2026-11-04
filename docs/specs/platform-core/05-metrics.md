<!-- file: docs/specs/platform-core/05-metrics.md -->
<!-- version: 1.10.1 -->
<!-- guid: 5cdf765d-aec1-4ed7-981a-e2e698afde61 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Metrics and tracing (`core/metrics`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §4, §9;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (OTel v1.47.0, exporter
v0.69.0, client_golang v1.25.0). Normative: [03-architecture](03-architecture.md) v1.12.1 R4 (`metrics` is in
**L0** with `entity`, `lifecycle`, `version`, `log`), R5 (no `prometheus.DefaultRegisterer`, no `otel.Set*Provider`
in core except the opt-in `app.InstallProcessDefaults`; R5 binds core only), §3.6 "Metric name" (core series
`<MetricNamespace>_<subsystem>_*`, `MetricNamespace` = `app.Options.MetricNamespace`, default `mediacore`; module
series `<MetricPrefix>_*` for an app that ships one module and `<MetricPrefix>_<moduleID>_*` otherwise, decided by
the modules compiled in, not by enablement, §4; `MetricPrefix` = `app.Options.MetricPrefix`; semantic-convention
names unchanged; ABO keeps its metric names, A-5).

## Decisions at a glance

| # | Decision |
|---|---|
| M1 | One `metrics.Provider` per `App` owns a `*prometheus.Registry` (private, unless the app supplies a registerer for the S2 r3–S8 bridge, §6), an OTel `MeterProvider`, a `TracerProvider` and a propagator. All are **injected**; nothing in core names `prometheus.DefaultRegisterer`/`DefaultGatherer` or calls `otel.Set*`/`otel.Get*`, except that `app.InstallProcessDefaults` sets the three OTel globals to this Provider's values for an app that opts in ([05-logging](05-logging.md) §3, MOD4-3). Components receive a `*metrics.Scope`; only the kernel holds the `*Provider`. There is no `metrics.Registry` type. |
| M2 | The authoring API is the **OTel metric API** (`metric.Meter`), continuing ABO's in-flight migration (client_golang family ceiling 64 and falling, `internal/telemetry/testdata/client_golang_baseline.txt`). Prometheus is an exporter bound to the App's registry. client_golang is used directly only inside `core/metrics` (registry, Go/process collectors, promhttp) and through one escape hatch, `Provider.Registerer()`, for third-party and legacy collectors. |
| M3 | Names: core instruments `<namespace>.<subsystem>.<what>` → `<namespace>_<subsystem>_<what>[_unit][_total]`, where the namespace is the app's **metric namespace** `app.Options.MetricNamespace` (default `mediacore`, the name every table of this spec shows; ABO passes `audiobook_organizer`, A-5). Module instruments use the app's **metric prefix** `app.Options.MetricPrefix` (ABO: `audiobook_organizer`): `<prefix>.<what>` when the app **ships** one module, `<prefix>.<module>.<what>` when it ships two or more. "Ships" means the module list passed to `app.New`, which never changes at run time, so enabling or disabling a module never renames another module's series (SIMP2-13). The Prometheus translation strategy is **pinned** (`UnderscoreEscapingWithSuffixes`), not left to the exporter's process-global default. OTel semantic-convention instruments (`http.server.*`) keep their standard names. **Every Prometheus family has exactly one owner per App, at every stage (R9-3):** one instrument identity or one collector produces it, whether it was created through a `Scope`, through the App's raw or process-global `MeterProvider`, or registered on the shared registry of the S2 r3–S8 bridge (`ErrFamilyTaken`, §3), so a namespace equal to the module prefix cannot merge two series and two emitters can never serve one family. |
| M4 | Cardinality is a contract: closed attribute-key allow-list per scope, bounded value sets only, no identifiers; SDK per-instrument limit 2000; a series golden per scope. |
| M5 | `/metrics` is a core route mounted by the kernel through `Router.Infra` with permission `core:metrics.read`, which the chain **enforces** (stages 10–13 run for an `Infra` route whose permission is not Public, SEC2-14), outside response compression. For ABO the kernel mounts it at 09 S8; until then ABO's legacy gin route serves it (from S2 r3 `Provider.Handler()` over the default registry). ABO's requirement stays HEAD's any-credential check through S10, on the legacy route and on the kernel's mount alike, because ABO's roles hold legacy atoms only until the identity cutover (A-2; §6). |
| M6 | **ABO keeps every metric name and series identity (A-5, R9-3).** No series is renamed at any stage and there is no alias layer: no gather-time alias, no metric ledger row, no dashboard or rule switch. Every family of ABO's HEAD series golden keeps its name, type, label set **and label values** (series identity: the values HEAD records for the same input), with exactly one owner, through S10 and after; `TestABOMetricsGatherNoDuplicateOrMissing` checks all of it against a HEAD golden (§6, MOD9-1, MOD9-2). When the code that emits a HEAD family moves into a core subsystem, the family keeps its name by one of two rules, chosen per family in §13 item 3: the moved emitter keeps its names under ABO's namespace, or ABO's own code keeps recording the family. Core's own families of that subsystem are additions. Trade: cross-app dashboards need a per-app query (`<namespace>_…`). |
| M7 | Spans are redacted before export with the logging pipeline's `*log.Redactor` (credential query values, invite paths, userinfo), so `apikey`/`token` values never reach a trace backend (SEC-22). |

## 1. Purpose & scope

Give every `App` isolated telemetry: a Prometheus scrape endpoint, optional OTLP metric push, optional OTLP
traces, consistent naming under the app's namespace, enforced cardinality and span redaction. Out of scope: what
each subsystem measures (each 05-* file tables its own metrics), log export ([05-logging](05-logging.md)),
health/readiness ([05-health](05-health.md)).

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
| Series contract | `internal/telemetry/contract/testdata/series.golden`: **87 series** (74 `audiobook_organizer_*`, 10 `ai_*`, 3 `http_server_*`; an informative count at `748fd787a`); histogram buckets golden; views table | `contract/testdata/`, `internal/telemetry/views.go:37-80` |
| Process identity | `instanceID` = random ULID per process (hostname-free), package var | `internal/telemetry/resource.go:31` |
| Error handling | rate-limited OTel error handler and value-dropping SDK logger, both process-global | `internal/telemetry/errorhandler.go:121`, `sdklog.go:38-39` |
| `/metrics` | `promhttp.Handler()` (default gatherer); any authenticated credential when auth is on, with worker-only credentials confined to the worker API; excluded from gin gzip after a production scrape failure | `internal/server/server_lifecycle.go:1735-1747`, `internal/server/middleware/auth.go:268-297`, `internal/server/server.go:708-716` |
| Other default-gatherer readers | cache stats handler, cache snapshotter | `internal/server/handlers/cache.go:99`, `internal/server/cache_snapshotter.go:24` |
| Dashboards / rules | 2 Grafana dashboards, 9 alert rules, 1 compat recording rule (`label_replace` of `def_id` → `op_type`); they reference 20 `audiobook_organizer_*` names, 7 `ai_*`, and `process_resident_memory_bytes` (from the default registry's process collector) | `deploy/grafana/dashboards/*.json`, `deploy/prometheus/alert-rules.yml`, `deploy/prometheus/recording-rules.yml:14-19` |
| Exporter default naming | `exporters/prometheus` picks its translation strategy from the process-global `model.NameValidationScheme` unless set | `<gomodcache>/go.opentelemetry.io/otel/exporters/prometheus@v0.69.0/config.go:103-125` |

Multi-app consequence (`raw/12` §4): a second `Register` panics on duplicate collectors, and two apps would
share one meter/tracer provider and one resource.

## 3. Go API

Package `github.com/falkcorp/mediacore/metrics` (L0). Imports: `prometheus/client_golang` (`prometheus`,
`collectors`, `promhttp`), OTel API (`metric`, `trace`, `propagation`, `attribute`), OTel SDK (`sdk/metric`,
`sdk/trace`, `sdk/resource`), exporters (`exporters/prometheus`, `otlpmetricgrpc`, `otlptracegrpc`),
`github.com/prometheus/otlptranslator`, and `github.com/falkcorp/mediacore/log` (same layer, L0; for
`*log.Redactor` only — `log` does not import `metrics`). No other core package (R4).

```go
package metrics

type Options struct {
	AppID            string   // app.Options.AppID, e.g. "abo"; service.name default
	MetricNamespace  string   // app.Options.MetricNamespace, [a-z][a-z0-9_]*; default "mediacore"; ABO: "audiobook_organizer" (A-5)
	MetricPrefix     string   // app.Options.MetricPrefix, [a-z][a-z0-9_]*; default AppID; ABO: "audiobook_organizer"
	Modules          []string // IDs of the modules compiled into the app (the app.New module list), enabled or not;
	                          // one module => module scopes use MetricPrefix alone (M3). Never the enabled set.
	ServiceVersion   string
	InstanceID       string // per-process random ULID, never a hostname
	Environment      string // deployment.environment
	Prometheus       bool
	OTLP             *OTLPMetrics // nil = no push
	Tracing          *OTLPTracing // nil = no-op TracerProvider
	Redactor         *log.Redactor // from Logging.Redactor(); applied to span names and URL attributes before export
	CardinalityLimit int           // per instrument; 0 = 2000 (SDK default)
	// PromRegisterer, when set, replaces the private registry (app.Options.PromRegisterer, the S2 r3–S8 bridge,
	// §6): the exporter and the core collectors register there, Handler gathers from it (it must also implement
	// prometheus.Gatherer, as a *prometheus.Registry does; New fails otherwise), and the Go and process collectors
	// are not added (the caller's registry has its own). It must be a *Shared (above, R9-3). ABO passes
	// metrics.NewShared(prometheus.DefaultRegisterer) from S2 r3 (the release that deletes its façade's exporter;
	// nil, private and unserved, in S2 r1 and r2, OPS5-14), registers its legacy client_golang families through
	// the same value, and stops at S8. nil = a private registry with the Go and process collectors (every other app).
	PromRegisterer   prometheus.Registerer
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

// Provider is one App's telemetry. Safe for concurrent use. Held by the kernel only.
type Provider struct{ /* unexported */ }

func New(ctx context.Context, o Options) (*Provider, error)

func (p *Provider) MeterProvider() metric.MeterProvider       // the family-checking wrapper (below); also what
                                                              // app.InstallProcessDefaults installs as the global
func (p *Provider) TracerProvider() trace.TracerProvider
func (p *Provider) Propagator() propagation.TextMapPropagator // TraceContext + Baggage
func (p *Provider) Registerer() prometheus.Registerer          // escape hatch for third-party and legacy collectors
func (p *Provider) Gatherer() prometheus.Gatherer
func (p *Provider) Handler() http.Handler                       // /metrics: promhttp on the App registry
func (p *Provider) Shutdown(ctx context.Context) error           // flush OTLP, stop readers; idempotent

// Scopes enforce naming. Core subsystems get "<MetricNamespace>.<subsystem>"; modules get MetricPrefix
// (the app ships one module) or "<MetricPrefix>.<module>" (it ships two or more).
func (p *Provider) Core(subsystem string, allowed ...attribute.Key) (*Scope, error)
func (p *Provider) Module(id string, allowed ...attribute.Key) (*Scope, error)

// Scope is what components and modules receive (module.Deps.Metrics, constructor params): *metrics.Scope.
type Scope struct{ /* unexported */ }

func (s *Scope) Prefix() string                 // "mediacore.ops" (default namespace), "audiobook_organizer"
func (s *Scope) Meter() metric.Meter            // validating wrapper over the App's MeterProvider
func (s *Scope) Tracer() trace.Tracer           // tracer named "<prefix>"
func (s *Scope) Propagator() propagation.TextMapPropagator
func (s *Scope) Allowed() []attribute.Key       // declared keys; asserted by metricstest
func (s *Scope) View(instrument string, buckets ...float64) sdkmetric.View // histogram bucket declaration

var (
	ErrInstrumentName = errors.New("metrics: instrument name violates naming rules")
	ErrScopeTaken     = errors.New("metrics: scope already registered")
	ErrFamilyTaken    = errors.New("metrics: Prometheus family already registered by another instrument or collector")
)

// Shared wraps the registerer an app passes as Options.PromRegisterer for the S2 r3–S8 bridge (06
// app.Options.PromRegisterer). It records the descriptor names of every collector registered through it, and the
// Provider checks its own families against them and the reverse (R9-3). The app registers its own legacy
// collectors on the shared registry through the same value, never through prometheus.MustRegister. New fails when
// Options.PromRegisterer is set and is not a *Shared.
type Shared struct{ /* unexported */ }

func NewShared(reg prometheus.Registerer) *Shared // reg must also be a prometheus.Gatherer
func (s *Shared) Register(c prometheus.Collector) error
func (s *Shared) MustRegister(cs ...prometheus.Collector)
func (s *Shared) Unregister(c prometheus.Collector) bool
```

**Instrument rules enforced by `Scope.Meter()`** (constructor returns `ErrInstrumentName` and a no-op
instrument, which the module surfaces at registration so boot fails closed): name =
`<scope prefix>.<seg>(.<seg>)*`, segments `[a-z][a-z0-9_]*`; counters must not end in `total`; histograms must
have a unit (`s`, `By`, or `{thing}`) and a declared view (buckets); gauges whose legacy name ends in `_total`
use a bracket unit, exactly as ABO's rules (`meter.go:20-37`). These rules are ABO's, made per-scope.

**One family, one owner (A-5, R9-3).** An app may pass the same string as `MetricNamespace` and `MetricPrefix` (ABO
does), so a core family and a module family could translate to one Prometheus name; and from S2 r3 ABO's own code
creates instruments through the process-global provider and registers client_golang collectors on the registry the
App shares. The Provider records the translated family name (`otlptranslator`, the pinned strategy below) of:

- every instrument created on the App's `MeterProvider`, through a `Scope` **or not**: `MeterProvider()` returns a
  wrapper that records `(scope name, scope version, instrument name, kind, unit)` → family for every instrument,
  and it is that wrapper `app.InstallProcessDefaults` installs as the OTel global, so ABO's `otel.GetMeterProvider()`
  callers (`opsmetrics.Default()`, `aidispatch`, …) are seen too;
- every collector registered through `Registerer()`, and, while `Options.PromRegisterer` is set, every collector
  registered through the app's `*metrics.Shared` (above), so registration through the shared registry is covered.

A second **owner** of a family is refused with `ErrFamilyTaken`: a different instrument identity (another scope,
kind or unit) or a collector for a name an instrument holds, and the reverse. Creating the identical instrument
twice in one scope is the OTel SDK's one instrument, not a second owner. The refused instrument is a no-op (a
refused collector is not registered). During boot (through 06 P6) a refusal fails boot; after boot (an instrument
created lazily, as `opsmetrics.Default()` does on first use) it is logged at error with both owners and raises the
health token `metrics_family_taken` (degraded), and CI catches it first through
`TestABOMetricsGatherNoDuplicateOrMissing` (§6). Two instruments of one scope that the SDK would merge are that
scope's own concern, as today.

**Exporter construction (normative):**
`prometheus.New(prometheus.WithRegisterer(reg), prometheus.WithoutScopeInfo(),
prometheus.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes))` — the three options
verified at exporter v0.69.0 (`config.go:67,120,170`); `reg` is the private registry or `Options.PromRegisterer`.
The private registry also gets
`collectors.NewGoCollector()` and `collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})`
explicitly (the default registry had them implicitly; ABO's memory alert reads
`process_resident_memory_bytes`). `Handler` wraps `promhttp.HandlerFor(gatherer, opts)` in
`promhttp.InstrumentMetricHandler(reg, …)` so `promhttp_metric_handler_*` series also survive the move.

**Tracing and HTTP (one emitter, HEAD's series; MOD9-2, R9-3).** `httpapi` instruments with `otelhttp` using
`WithTracerProvider`, `WithMeterProvider` and `WithPropagators` from the Provider — never the globals. From S2 core's
chain stage 2 is the **one** emitter of the `http_server_*` families for every request, `MountLegacy` requests
included, and gin carries no `otelgin` (neither its spans nor its metrics), so no request is recorded twice and no
family has two owners. The semconv names (`http_server_request_duration_seconds`, request and response body size)
and label keys are HEAD's (series golden). The label **values** are HEAD's too: HEAD's `http_route` is gin's route
template (`c.FullPath()`, for example `/api/v1/books/:id`; otelgin v0.72.0), which a `ServeMux` pattern before
routing cannot give. So for a request served through `MountLegacy` the legacy mount reports gin's `c.FullPath()`
to stage 2 after gin has routed it, through the one per-request carrier `httpapi.ReportLegacyRoute(ctx, fullPath)`
that stage 2 puts into the request context (05-http-api §4.2 stage 2, §8.2), and stage 2, which records the series
after the handler returns, records that value as `http_route` (as the request's `otelhttp` metric attribute, or
through its own semconv instruments where `otelhttp` cannot take it; 05-http-api §4.2 decides); native, alias and
`Infra` routes record their `ServeMux` pattern.
`TestLegacySeriesLabelValuesMatchHEAD` (ABO, §12) drives a sampled `:id` legacy route and compares the recorded
values with HEAD's. 05-http-api §4.2, 09 S2 and 13 §13.5.1 state the same.

**Span redaction (normative, SEC-22).** `otelhttp` records the request target and query as span attributes,
and the slog redactor does not see spans. The Provider therefore wraps every span exporter in a redacting
exporter: before export, each span's name and its attributes under `log.URLKeys()` (`url.full`, `url.path`,
`url.query`, `http.target`, `http.url`, …) are passed through `Options.Redactor.String`, and any attribute whose
key is on the redactor's key deny-list is replaced with `[REDACTED]`. The wrapper sits inside the Provider, so no
component can register a span exporter that bypasses it. With tracing off (`Tracing == nil`) there is no exporter
and nothing to redact.

## 4. Naming conventions

| Kind | OTel instrument name | Prometheus family | Example |
|---|---|---|---|
| Core subsystem | `<namespace>.<subsystem>.<what>` | `<namespace>_<subsystem>_<what>[_<unit>][_total]` | `mediacore.ops.runs` → `mediacore_ops_runs_total`; for ABO `audiobook_organizer_ops_runs_total` (the task executor's subsystem scope is `ops` for every app, 05-tasks 5T.11; MOD9-8) |
| Module, app ships one module | `<prefix>.<what>` | `<prefix>_<what>…` | `audiobook_organizer.books` → `audiobook_organizer_books_total` (today's name, unchanged) |
| Module, app ships two or more | `<prefix>.<module>.<what>` | `<prefix>_<module>_<what>…` | `suite.tv.scan.files` → `suite_tv_scan_files_total` |
| Semconv | spec name | translated | `http.server.request.duration` → `http_server_request_duration_seconds` |
| Resource | — | `target_info{service_name,service_version,service_instance_id,deployment_environment}` | — |

**Reading the tables of the other files.** Every 05 file writes core family names with the default namespace,
`mediacore_…`. For an app that passes its own `MetricNamespace`, read that namespace instead (ABO:
`audiobook_organizer_…`). Native apps that keep the default (hello, TV, Movies) share every core family name, so
one set of core dashboards serves them (distinguished by `job`/`service_name`). ABO's core families differ only in
the namespace, so a cross-app dashboard needs a per-app query (the A-5 trade).

Module names are per app. The single-module rule exists because inserting `books_` disambiguates nothing in an
app whose prefix is already app-specific, while it would rename every books series (SIMP-13). **Trade:** if ABO's
books module ever shares a binary with other modules, its series must be renamed then; that is a row of the
11 §11.5 checklist. `Provider.Module` computes the prefix from `Options.Modules`, the modules compiled into the
app, so the rule is mechanical and the segment changes only when a release changes the module list. Keying it on
the *enabled* modules would rename every series of the remaining module when an operator disables one at the
next restart, breaking dashboards (SIMP2-13).

## 5. Cardinality rules (normative)

1. Every scope declares its attribute keys (`Core(…, keys)` / module manifest `Metrics.Attributes`); keys come
   from a shared vocabulary (`outcome`, `kind`, `module`, `provider`, `adapter`, `capability`, `endpoint`,
   `reason`, `class`, `state`, `phase`, `direction`, `cache`, `route`, `method`, `status_class`, …) extended
   per module with review.
2. Values must come from a set that is finite and known at registration: enums, registered task kinds,
   enabled module IDs, configured adapter/endpoint IDs, route templates. **Forbidden as values:** entity IDs,
   task run IDs, user IDs, request IDs, file paths, raw URLs, error strings, titles/names. HEAD's `op_id` gauges
   (§2) are an existing books family, kept as at HEAD under M6; the rule binds new instruments, as HEAD's
   "scrape-only" key rule already does (`attr.go:91-102`).
3. Per-instrument SDK cardinality limit `core.metrics.cardinality_limit` (default 2000,
   `sdk/metric@v1.47.0/config.go:183`); overflow lands in the SDK's overflow series and fails
   `metricstest` in CI.
4. Histogram buckets are declared per instrument (`Scope.View`); no exponential histograms by default
   (ABO pinned this for OTLP, `telemetry.go:418-420`).
5. A series golden per scope (`testdata/series.golden`: family, type, label set) is asserted by
   `metricstest.AssertSeries`; changing it is a reviewed diff — the generalization of ABO's contract test.

## 6. `/metrics` ownership

The kernel mounts `Provider.Handler()` at `core.metrics.path` (default `/metrics`, outside `/api`) with
`Router.Infra(path, "core:metrics.read", h)` ([05-http-api](05-http-api.md) §3.5) — permission
**`core:metrics.read`** (R6: explicit). `Infra` routes are inside the chain's authenticated scope: for an `Infra`
route whose permission is not Public, stages 10–13 (authentication through permission) run exactly as for a
native route, so the permission is enforced, not merely recorded; only the probes `/livez` and `/readyz` stay
Public (SEC2-14; `TestInfra_MetricsRequiresScope` in 05-http-api asserts it). **For an app whose role store is
legacy-backed** (ABO through S10) the kernel mounts it with HEAD's requirement instead: any authenticated
credential, with HEAD's worker-only confinement unchanged (`internal/server/server_lifecycle.go:1735-1747`,
`internal/server/middleware/auth.go:268-297`), because those roles hold legacy atoms only and no legacy atom maps
to `core:metrics.read` before the identity cutover (A-2). It is **excluded from compression middleware**
(promhttp negotiates gzip itself; double gzip broke production scrapes, `server.go:708-716`). Optional
dedicated listener `core.metrics.listen` (e.g. a loopback address) serving only `/metrics`; it requires the
same credential unless the operator sets `core.metrics.listen_public=true` (`ProtectOperator`, logged at
warn on every boot). Two apps in one process each expose their own registry on their own listener or path;
registries are never merged. `/metrics` itself is a permanent path (09 §9.6 row 7).

**Who serves `/metrics` at each stage (MOD4-3).** One registry is scraped at every stage, and the series-name set
never drops:

| Stages | Registry the App's exporter and core instruments use | Who serves `/metrics` | Legacy client_golang families |
|---|---|---|---|
| HEAD–S1 | default registry (ABO's façade installs the S1 core providers, §13) | gin, `promhttp.Handler()` | default registry (`metrics.Register`) |
| S2 r1–r2 | the App's Provider uses a **private, unserved** registry (`PromRegisterer` nil): the S1 façade's exporter is still installed on the default registry, and two exporters on one registry could collide on `target_info` and fail the scrape (OPS5-14). Trade: the kernel's own series (e.g. `<namespace>_kernel_boot_phase_seconds`) are not scraped until r3 | gin, `promhttp.Handler()`, as at S1 | default registry, unchanged |
| S2 r3–S7 | `Options.PromRegisterer` = the default registry, wrapped as a `*metrics.Shared` through which ABO's `metrics.Register` registers its legacy families too (ABO passes `app.Options.PromRegisterer`, 06, in the same release that deletes the façade's exporter; R9-3); `app.InstallProcessDefaults` points the OTel globals at the App's providers, so the six global-reading ABO packages keep their series | ABO's **legacy gin route** under `MountLegacy`, unchanged in path, compression exclusion and HEAD's any-credential check, whose handler becomes `Provider.Handler()` | default registry, unchanged |
| S8 on | the App's private registry (`PromRegisterer` nil; the `app.Options` field is removed, 06) | the kernel's `Router.Infra(core.metrics.path, …)` mount, with HEAD's any-credential requirement for ABO through S10 (above); the legacy gin route is removed in the same PR | moved to `Provider.Registerer()` with their names in the same PR; `cache.go:99` and `cache_snapshotter.go:24` take `Provider.Gatherer()` |

**Series-name checks (OPS5-6, OPS5-9, OPS6-3).** This section is the one statement of every stage's series check;
09 points each stage here. A live scrape lists only the families that have recorded a sample since boot: a
client_golang `Vec` with no observed label set, an OTel instrument with no data point and the Pebble collector
without a source export nothing (`internal/telemetry/contract/series_contract_test.go:15-18` @748fd787a). Two boots
of two builds therefore give different name sets, depending on which code paths ran, and families tied to
configured integrations never appear on a staging copy whose provider secrets are scrubbed (09 §9.3). So a live
scrape cannot be the normative check.

**Normative: the seeded contract test.** ABO's series contract test (HEAD's `internal/telemetry/contract`, kept
in ABO as `internal/telemetrypolicy` from S1, 09 S1) builds the telemetry in its own process, records one sample
per row of its **seeding table**, scrapes, and compares the family set (family, type, label set) with its golden.
From S5 it is joined by the gather test below, which also checks label values.
A PR that adds a family adds its seeding row and its golden line. **No PR through S10 removes or changes a line of
HEAD's golden** (M6); a PR that moves the emitter of a HEAD family keeps that family's seeding row green through the
new emitter (§13 item 3). **What it scrapes (OPS7-1).** HEAD's test scrapes `promhttp.Handler()` on the default
registry (`internal/telemetry/contract/series_contract_test.go:451-456` @748fd787a), which loses every books family
at S8, when they move to the private registry. So:

- **From S2 r3** the test builds the same `metrics.Provider` the kernel builds at that stage, through the public
  test constructor `coretest.MetricsProvider(t, opts)` (06 `coretest`): it **boots one zero-module `coretest`
  harness** (G7's) with `opts` (`MetricNamespace`, `MetricPrefix` and, until S8, `PromRegisterer`), so the kernel
  applies its `metrics.Options` defaults and the stage's `PromRegisterer` exactly as §7 step 1 does, and returns
  that boot's Provider. It scrapes that Provider's `Handler()`, never `promhttp.Handler()`. Before S2 r3 (HEAD–S2 r2)
  the default registry is the served one (table above) and the test keeps HEAD's scrape.
- **No prefix is added to `inScopePrefixes`** (today `audiobook_organizer_`, `ai_dispatch_`, `ai_`,
  `http_server_`, `series_contract_test.go:69-71`): ABO passes `audiobook_organizer` as its namespace, so every core
  family of ABO's App is already in scope.
- **Seeding rows for core-owned emitters go through public SDK paths**, because ABO cannot import
  `mediacore/internal/...`: the Pebble collector through a store opened with `coretest`/`storage`, the tasks
  instruments through one `hello.count` run, the AI instruments through a fake endpoint.
- **Seeding row for the kernel's own families (OPS8-10):** the zero-module `coretest` boot inside
  `coretest.MetricsProvider`, with the stage's `app.Options`, whose Provider is the one scraped.
  `<namespace>_kernel_boot_phase_seconds` ([06](06-app-sdk.md) §6.9), `<namespace>_compat_*` (S2b),
  `<namespace>_storage_level` and `<namespace>_storage_checkpoint_bytes` are recorded by kernel code during a boot,
  never by the Provider, and a family with no sample exports nothing, so the constructor boots rather than only
  building the Provider. There is one Provider per test, so nothing collides on `target_info` (OPS5-14). Core
  also asserts the same families in its own seeded test, `TestKernelFamiliesGathered` (a `kernel` test, §12), so
  core CI checks them without ABO.
- Because the test builds the stage's Provider, **at S8 its registry becomes the private one in the same PR** that
  makes the App's registry private.
- `TestContractScrapesServedHandler` asserts that the handler under test is the one the `/metrics` mount serves at
  that stage (the legacy gin route's handler from S2 r3 to S7, the kernel's `Router.Infra` mount from S8).
- `TestHEADSeriesStillPresent` compares the scrape with HEAD's golden, committed unchanged as
  `testdata/head_series.golden`: every HEAD family is present with its type and label set. It is the M6 check of
  every stage, independent of what later PRs add.
- **`TestABOMetricsGatherNoDuplicateOrMissing` (ABO, from S5; R9-3, MOD9-1, MOD9-2).** A seeded test with
  `coretest` alone cannot see a second emitter, because it never builds ABO's own recorders. This one builds the
  stage's kernel Provider (through `coretest.MetricsProvider`, with the stage's `PromRegisterer` as a
  `*metrics.Shared`) **together with ABO's real recorders on it**: `app.InstallProcessDefaults`, ABO's legacy
  client_golang families registered through the `*metrics.Shared`, `opsmetrics.Default()` while the package exists,
  books' run-event recorder and op-items collector, the v3 loop until T7 and, from S7, books' AI recorder (05-ai §8).
  It drives one adapted executor run, one v3 tick (until T7), one legacy HTTP request on a sampled `:id` route and
  one AI call through a fake endpoint (from S7). Then `Gatherer().Gather()` must return **no error** (two samples
  of one family with equal label values fail it, as client_golang does at a scrape) and the result must equal a
  HEAD golden of **family, type, label names and label values** (`testdata/head_series_identity.golden`, captured
  by the S0 PR from the HEAD build driven with the same seeds), with only the stage's listed additions beside it:
  no family missing, no series identity changed, no family with two owners. It is a done-check of S5, T3, T6, T7,
  S7 and S8 (09).

The stage's done-check is the golden diff that PR shows:

| Stage | Golden diff of the seeded contract test |
|---|---|
| S1, S2 r1, S2 r2 | empty |
| S2 r3 | additions only: the kernel's own families under ABO's namespace, seeded by `coretest.MetricsProvider`'s zero-module boot (OPS8-10); core's `TestKernelFamiliesGathered` asserts the same families in core |
| every later stage through S10 | additions only: the families that stage's new code emits (for example `<namespace>_compat_*` and the storage families at S2b, the new executor families of 05-tasks 5T.11 at S5, `<namespace>_ai_*` at S7, `<namespace>_build_info` at S8, labelled with the binary's version, never the UI bundle's build ID, R5-7). A PR that moves the emitter of a HEAD family (the Pebble collector at S2b, the run and schedule instruments at S5, the AI instruments at S7, the legacy client_golang families' registry at S8) shows **no** diff for that family, and `TestABOMetricsGatherNoDuplicateOrMissing` shows no change of its series identity (§13 item 3) |

The Go collector's `go_goroutines` and `go_memstats_alloc_bytes` are present at every stage: HEAD's `/metrics`
serves `promhttp.Handler()` over the default registry, which already carries the Go collector
(`internal/server/server_lifecycle.go:1746-1747` @748fd787a), and the private registry adds it from S8 (OPS6-2).

**Smoke check: the live scrape.** On each stage's deployment a credentialed scrape (for ABO through S10 any
credential, as at HEAD; a native app's credential holds `core:metrics.read`) must answer 200 with a non-empty family
list (comment lines dropped, label part cut, sorted unique; an unauthenticated scrape gets 401 and would compare
nothing). That list must contain a short fixed set of always-on families: `process_resident_memory_bytes` and
`go_goroutines`, plus `<namespace>_build_info` from S8. It is never compared with another build's scrape.

`health` serves no `/metrics` ([05-health](05-health.md) §5H.11 item 3).


## 7. Lifecycle

1. `app.New`, right after logging: `metrics.New` with options from `core.metrics` / `core.tracing` (operator
   layers and defaults), `Options.MetricNamespace` = `app.Options.MetricNamespace` (default `mediacore`),
   `Options.Redactor = Logging.Redactor()`, `Options.Modules` = the IDs of every module passed to `app.New`,
   enabled or not (§4), and `Options.PromRegisterer` from `app.Options.PromRegisterer` (06; ABO's S2 r3–S8
   bridge, nil at S2 r1–r2, §6). If the app's `main` calls `app.InstallProcessDefaults`, the OTel globals point at
   this Provider from then on ([05-logging](05-logging.md) §3).
   OTLP exporters are built non-blocking; a bad OTLP endpoint is **degraded, not fatal** (ABO learned this the
   hard way, `telemetry.go:52-58`): health token `metrics_otlp_disabled` / `tracing_disabled`.
2. Every component receives a `*Scope` (`module.Deps.Metrics` for modules, [06-app-sdk](06-app-sdk.md); core
   services through their constructors). Instruments are created at construction; a naming error or a taken
   family name fails boot.
3. `App.Run` shutdown: `Provider.Shutdown` after the HTTP server stops and before logging closes; it flushes
   the last OTLP batch within the shutdown budget.

## 8. Error semantics

| Path | Mode | Why |
|---|---|---|
| Prometheus exporter/registry construction fails | fail-closed (boot) | programming error; scrape is the primary signal |
| OTLP metric/trace exporter cannot be built | fail-open, degraded health token | telemetry must not crash-loop the app (`telemetry.go:52-58`) |
| OTLP export errors at runtime | fail-open, rate-limited log, counter | collector outages are routine |
| Instrument name/unit/view violation | fail-closed at construction (`ErrInstrumentName`) | contract violations caught at boot and in CI |
| Duplicate scope | fail-closed (`ErrScopeTaken`) | two owners for one name |
| A second owner of one Prometheus family (another instrument identity, through a `Scope`, the raw or the global `MeterProvider`, or a collector through `Registerer()` or the app's `*metrics.Shared`) | fail-closed during boot (`ErrFamilyTaken`); after boot the second is a no-op, logged at error, health `metrics_family_taken` (degraded) | two owners merge silently or make every scrape fail (M3, R9-3, MOD9-1) |
| Redactor nil while tracing is on | fail-closed (boot) | spans would export credentials |
| Cardinality limit reached | fail-open (overflow series), test failure in CI | data loss for one instrument only |
| Gather error in `/metrics` | `promhttp.ContinueOnError`: partial scrape + `promhttp_metric_handler_errors_total` | one bad collector must not blank the scrape |
| Unauthorized scrape | 401/403 | R6 |

## 9. Concurrency contract

`Provider`, `Scope`, instruments, tracer and handler are safe for concurrent use (OTel SDK and client_golang
guarantees). Observable-gauge callbacks must be non-blocking and bounded (read an atomic or a snapshot; never
I/O) — ABO's in-flight gauge callback pattern (`opsmetrics.go:440`) is the model. The redacting span exporter is
stateless (the `Redactor` is immutable). `Shutdown` is idempotent and bounded by `ctx`. No goroutines except the
SDK's periodic reader (every `core.metrics.otlp.interval`) and batch span processor, both stopped by `Shutdown`.
They are library-internal timers, not durable work, so they are not tasks ([05-tasks](05-tasks.md) §5T.3.8
"What is not a task", SIMP3-9).

## 10. Config keys

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.metrics.prometheus.enabled` | bool | `true` | no | none |
| `core.metrics.path` | string | `/metrics` | no | admin |
| `core.metrics.listen` | string (host:port) | `""` (main listener) | no | admin |
| `core.metrics.listen_public` | bool | `false` | no | operator |
| `core.metrics.otlp.endpoint` | string | `""` (aliases: `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, permanent; ABO `otel_metrics_otlp_endpoint`, window) | no | admin |
| `core.metrics.otlp.interval` | duration | `60s` (aliases: `OTEL_METRIC_EXPORT_INTERVAL`, permanent, a bare integer is milliseconds as in ABO's `telemetry.ParseMetricsInterval`; ABO `otel_metrics_otlp_interval`, window) | no | none |
| `core.metrics.otlp.insecure` | bool | `false` (aliases: `OTEL_EXPORTER_OTLP_METRICS_INSECURE`, permanent; ABO `otel_metrics_otlp_insecure`, window) | no | admin |
| `core.metrics.environment` | string | `prod` (ABO `telemetry_environment`, `resource.go:25`) | no | none |
| `core.metrics.cardinality_limit` | int | `2000` | no | none |
| `core.tracing.otlp.endpoint` | string | `""` (aliases: `OTEL_EXPORTER_OTLP_ENDPOINT`, permanent; ABO `otel_exporter_otlp_endpoint`, window) | no | admin |
| `core.tracing.otlp.insecure` | bool | `false` | no | admin |
| `core.tracing.sample_ratio` | float [0,1] | `1.0` (parent-based) | no | none |

Endpoints are `ProtectAdmin` because a changed endpoint sends every metric and span (which carry route and host
data) to a host of the actor's choosing (05-config §1.3.7). Telemetry wiring is construction-time; making it live
would require swapping providers under running instruments, which the OTel SDK does not support. The metric
namespace and prefix are `app.Options` values compiled into the app's `main`, never config keys. All keys here are
operational dials. The `OTEL_*` aliases are the only rows of core's ledger fragment (permanent; 05-config §1.3.6,
SIMP4-10). The ABO names in the table (`otel_*`, `telemetry_environment`) are window aliases ABO supplies through
its legacy alias table (`app.Options.LegacyConfig`, 05-config §1.3.3), with rows in ABO's ledger; core holds none
of them (R1). Their values decode with HEAD's per-type semantics (05-config §1.3.6, A-7).

## 11. Metrics (of this subsystem)

| Name | Type | Labels |
|---|---|---|
| `mediacore_metrics_otlp_export_failures_total` | counter | `signal` ∈ metrics, traces |
| `mediacore_metrics_span_redactions_total` | counter | `kind` ∈ key, url, path |
| `promhttp_metric_handler_requests_total` | counter | `code` (client_golang standard) |
| `promhttp_metric_handler_errors_total` | counter | `cause` (client_golang standard) |
| `target_info` | gauge | resource attributes |

## 12. Tests (core must have)

| Test | Asserts |
|---|---|
| `TestNoGlobals` (archtest, R5) | no `prometheus.DefaultRegisterer/DefaultGatherer/MustRegister/Register`, `promhttp.Handler()`, `otel.Set*`/`otel.Get*Provider`/`otel.Tracer`/`otel.Meter` in core, except `otel.SetMeterProvider`, `otel.SetTracerProvider` and `otel.SetTextMapPropagator` inside `app.InstallProcessDefaults` (05-logging §10) |
| `TestProvider_ExternalRegisterer` | with `Options.PromRegisterer` set to a `*metrics.Shared` over a fresh registry that already holds Go and process collectors: no duplicate-registration error, the exporter's families appear in `Handler()`'s scrape, nothing lands in a private registry; a registerer that is not a `*metrics.Shared`, or whose registry is not a `prometheus.Gatherer`, fails `New`; with it nil the private registry carries the Go and process collectors (MOD4-3) |
| `TestTwoProvidersIsolated` | two Providers register identical instruments without panic; scrapes are disjoint |
| `TestScope_RejectsBadNames` | prefix, `_total` suffix, missing unit/view → `ErrInstrumentName` |
| `TestNamespace_CoreScopesUseAppNamespace` | `MetricNamespace` unset → core scope prefix `mediacore.<subsystem>`; `MetricNamespace` = `audiobook_organizer` → `audiobook_organizer.<subsystem>` and families `audiobook_organizer_<subsystem>_…`; an invalid namespace fails `New` (A-5) |
| `TestFamilyTaken_SharedNamespaceAndPrefix` | `MetricNamespace` = `MetricPrefix`: a module instrument and a core instrument that translate to one family → the second gets `ErrFamilyTaken`; the same for an OTel instrument against a collector registered through `Registerer()`, in both orders; distinct families under the shared prefix register (A-5) |
| `TestFamilyTaken_ThroughGlobalProvider` | after `app.InstallProcessDefaults`, an instrument created through `otel.GetMeterProvider()` in another scope with a name a core `Scope` instrument holds (the `ops.zombies` case of MOD9-1) → `ErrFamilyTaken`, a no-op instrument, boot fails during boot and `metrics_family_taken` is raised after it; the identical instrument created twice in one scope is one instrument (R9-3) |
| `TestFamilyTaken_SharedRegisterer` | a client_golang collector registered through `*metrics.Shared` with a family name a Provider instrument holds → refused, and the reverse order → `ErrFamilyTaken`; disjoint names register (R9-3) |
| `TestModulePrefix_SingleAndMulti` | one compiled-in module → scope prefix `MetricPrefix`; two → `<MetricPrefix>.<module>` |
| `TestModulePrefix_StableUnderEnablement` | two compiled-in modules, one disabled: the other's prefix is still `<MetricPrefix>.<module>`; series names identical before and after the toggle |
| `TestTranslationStrategyPinned` | names identical with the global validation scheme set to legacy and to utf8 |
| `TestGoAndProcessCollectorsPresent` | `go_goroutines`, `process_resident_memory_bytes` in the scrape |
| `TestTrace_NoApikeyInSpanAttributes` | in-memory exporter behind the redacting wrapper: request `?apikey=…&token=…` and `/api/core/v1/auth/invites/<t>` → no secret in span name or attributes |
| `TestHandler_NoDoubleGzip` | `Accept-Encoding: gzip` through the httpapi stack yields one gzip layer |
| `TestCardinality_OverflowFailsMetricstest` | exceeding the limit is detected |
| `TestOTLPBadEndpointDegrades` | provider builds, health token set |
| `TestShutdownFlushes` | `testing/synctest` + in-memory exporter: last batch exported |
| `metricstest.AssertSeries` (per scope) | series golden for each core scope |
| `TestKernelFamiliesGathered` (a `kernel` test) | one zero-module `coretest` boot with `PromRegisterer` set to a fresh registry: the scrape of that App's `Provider.Handler()` carries every kernel-emitted family of the stage (`<namespace>_kernel_boot_phase_seconds` from S2 r3; `<namespace>_compat_*`, `<namespace>_storage_level`, `<namespace>_storage_checkpoint_bytes` from the stage that lands each), matching core's golden (OPS8-10) |

ABO keeps its own series contract test (HEAD's `internal/telemetry/contract`, `internal/telemetrypolicy` from S1);
from S2 r3 it scrapes the served `Provider.Handler()` of a Provider built through `coretest.MetricsProvider` with
ABO's namespace and prefix (§6, OPS7-1). Its seeded golden is the normative series check of every stage (§6,
OPS6-3), and every HEAD line in it stays unchanged (M6). Four ABO tests go with it:
`TestContractScrapesServedHandler`, `TestHEADSeriesStillPresent`, `TestABOMetricsGatherNoDuplicateOrMissing` (§6,
from S5: ABO's real recorders plus the kernel Provider, gathered without error and equal to a HEAD golden of
family, label and series identity; R9-3) and `TestLegacySeriesLabelValuesMatchHEAD` (§3, from S2: one request to a
sampled `:id` legacy route and, from S5, one adapted run; the `http_route` value of
`http_server_request_duration_seconds` and the `def_id`/`plugin` values of `ops_inflight` equal HEAD's; MOD9-2). Core's own `TestKernelFamiliesGathered`
asserts the kernel-emitted families in core as well (§6, OPS8-10).

## 13. Migration from ABO

1. **Injection first, installs last (MOD3-3, MOD4-3, MOD4-4).** `internal/telemetry` splits per file:
   - **to core `metrics` at S1:** provider construction, the endpoint validator, the exporter wiring and the
     resource, with the namespace and the scope as constructor options (09 S1). Core only **returns** providers (R5).
   - **stays in ABO permanently, as books telemetry** (the `internal/telemetry` path kept, or renamed under
     `internal/books`): `attr.go` (the ~30 attribute-key constants: `def_id`, `plugin`, `fixer`, `op_id`, …),
     `views.go` (the bucket table keyed by ABO family names) and `Views()`/`HistogramBuckets()`, the `meter.go`
     scope helper (`Meter(name)`, `MeterFrom`) and `SetVersion`/`Version`. They feed `Scope.View`,
     `Scope.Allowed` and `Options.ServiceVersion` at registration, so no ABO vocabulary enters core (R1).
     `LoadConfig`/`WithMetricsOTLP`/`WithEnvironment`/`ParseMetricsInterval`/`Config` stay in ABO too; from S2
     the kernel builds `metrics.Options` from `core.metrics`/`core.tracing` instead (the millisecond rule of
     `ParseMetricsInterval` is the `OTEL_METRIC_EXPORT_INTERVAL` alias's `Convert`, §10), and they are deleted
     when their last importer is gone (04 §8.2 lists, informatively, which importers rewire when).
   - **the process-global installs** (`otel.SetTracerProvider`, `telemetry.go:287`; `otel.SetMeterProvider`,
     `:461`; `otel.SetErrorHandler`, `errorhandler.go:121`; `otel.SetLogger`, `sdklog.go:39`; the propagator;
     the default-registry Prometheus exporter) stay in the ABO façade at S1, which installs the core providers,
     so the six ABO packages that still create instruments through the globals (`internal/ai/telemetry.go`,
     `internal/aidispatch/metrics.go`, `internal/database/memdb_metrics.go`, `internal/dedup/engine.go`,
     `internal/operations/registry/worker.go`, `internal/opsmetrics/opsmetrics.go`) keep their series and S1's
     series golden holds. **At S2 r3** `app.InstallProcessDefaults` ([05-logging](05-logging.md) §3) takes over
     the OTel installs and the exporter registers on `Options.PromRegisterer` = the default registry (§6); only
     the façade's **install code** is deleted, in that same release (OPS5-14). 04 lists this as a
     re-implementation, not a verbatim move.

   The three `otel.Tracer` package vars, `otel.GetMeterProvider()` calls and `opsmetrics.Default()` become injected
   scopes when touched. `opsmetrics`' core successor is written fresh in one place, `mediacore/internal/executor`
   ([05-tasks](05-tasks.md)), whose HEAD-named instruments sit on scope `<namespace>.ops` (item 3); ABO's `internal/opsmetrics` stays in ABO, as
   opsbridge-side code, until its last ABO user is gone (04 §8.6 records it as planning information). **It shrinks in
   the T0+T1 PR (R9-3, MOD9-1):** from that PR `opsmetrics.New` creates and observes only `ops.abandoned_calls` (for
   its `RegisterAbandoned` callers until T3 and T6); it no longer creates `ops.runs`, `ops.run.duration`,
   `ops.items`, `ops.inflight`, `ops.progress_age`, `ops.progress_timeout`, `ops.zombies`, `ops.schedule_lag` or
   `ops.schedule_missed`, which have exactly one owner from then on, core's executor and scheduler. HEAD's recorder
   observes an attribute-less `ops.zombies` 0 on every collection from scope `audiobook-organizer/operations`
   (`opsmetrics.go:214-273` @748fd787a); left beside the executor's, it would put two samples of
   `audiobook_organizer_ops_zombies{}` on every scrape and client_golang's `Gather` would fail the whole
   `/metrics` answer. Until T7 ABO's v3 loop records its ticks' lag and folded fires into the core scheduler's two
   instruments through the tasks client (the due time and fold count of a tick it starts; 05-tasks 5T.11 defines the
   call, cross-cluster), never through instruments of its own, so `def_id` comes from core's `StoredKind` and no
   ABO `RegisterDefID` is needed. Books' own
   gauges register through books' own `metrics.Scope`, never through core instrumentation with ABO names. HEAD's
   series names survive because each HEAD family keeps an emitter with its name (item 3), never through an alias.
2. **client_golang families** in `internal/metrics` finish the move to OTel when convenient (the ratchet continues
   to zero). They stay on the default registry, which the App shares from S2 r3 to S8 (§6), and all of them, the
   five `operations_*` families and the two process gauges included, move to `Provider.Registerer()` with their
   current names in the S8 PR that makes the registry private. `metrics.Register`'s `sync.Once` global disappears
   in the same PR.
3. **Names: how each HEAD family keeps its name (A-5).** ABO sets `MetricPrefix` and `MetricNamespace` to
   `audiobook_organizer`, so every books series keeps its exact name. Where the code that emits a HEAD family moves
   into a core subsystem, one of two rules keeps the name, and nothing is renamed:
   - **(a) The moved emitter keeps its names.** Code that moves verbatim keeps its family names, with the
     namespace in place of HEAD's literal, so under ABO's namespace they are HEAD's names. A native app sees them
     under the default namespace.
   - **(b) ABO keeps recording the family.** A family whose HEAD shape carries app vocabulary that the moved code
     does not keep (the labels `type` or `op_id`, or HEAD's unprefixed `ai_`/`ai_dispatch_` groups) stays an ABO
     emission. The ABO
     code that wraps the core subsystem for books records it, with HEAD's name, labels and values, from what that
     code sees and, for a reading only the core engine has, from the subsystem's public API. It records through
     ABO's kept `internal/telemetry` helper on the App's `MeterProvider`, the path HEAD's code uses today
     (`meter.go:17-53`; the unprefixed groups are ABO's own sanctioned exceptions, `meter.go:20-37`), so
     `Scope.Meter()`'s prefix rule is not bent and no ABO name enters core (R1). The core subsystem's own families
     (for example `<namespace>_ai_*`) are additions beside them.

| HEAD family | Emitter after the move | Stage of the move | Rule |
|---|---|---|---|
| `audiobook_organizer_pebble_*` (label `store`) | core `storage`'s Pebble collector, which moves with HEAD's family names, help strings, types and `store` label (`pebble_collector.go:119-146`) and registers `<namespace>_pebble_*` ([05-storage](05-storage.md) tables it) | S2b | (a) |
| `audiobook_organizer_ops_runs_total`, `_ops_run_duration_seconds`, `_ops_items_total`, `_ops_inflight`, `_ops_progress_age_seconds`, `_ops_progress_timeout_seconds`, `_ops_zombies` (labels `def_id`, `outcome`, `plugin`) | the core executor alone, whose instruments keep HEAD's names in the scope `<namespace>.ops` that the app's namespace supplies (`opsmetrics.go:216-250` @748fd787a), so under ABO's namespace they are HEAD's families with HEAD's label names **and values**: `def_id` is the `StoredKind` (an adapted kind's HEAD def ID), and `plugin` is, for an adapted kind, the HEAD plugin ID opsbridge supplies beside `LegacyDefID` (an app-supplied value, so no ABO name enters core), else the owning module ID; ABO's `opsmetrics` stops creating them in the same PR (item 1; R9-3, MOD9-1, MOD9-2); [05-tasks](05-tasks.md) 5T.11 tables them | S5 (T0+T1) | (a) |
| `audiobook_organizer_operations_{started,completed,failed,canceled}_total`, `_operation_duration_seconds` (label `type`) | books, through a subscriber of the run events on the bus that records them on books' side with HEAD's names and `type` values (05-tasks 5T.11) | S5 (T0+T1) | (b) |
| `audiobook_organizer_op_items_processed`, `_op_items_total` (labels `op_id`, `op_type`) | books' legacy collector, set from the same run events (05-tasks 5T.11) | S5 (T0+T1) | (b) |
| `audiobook_organizer_ops_schedule_lag_seconds`, `_ops_schedule_missed_total` (label `def_id`) | the core scheduler's instruments in the `<namespace>.ops` scope under HEAD's names, the one owner from T0+T1; until T7 ABO's v3 ticker (`v3_schedule.go:444,447` @748fd787a) reports its ticks into them through the tasks client and creates no instrument (item 1; 05-tasks 5T.11) | S5 (T0+T1) | (a) |
| `audiobook_organizer_ops_abandoned_calls` (label `source`) | ABO's shrunk `opsmetrics` until T3/T6, then books' own observable on its own scope (05-tasks 5T.8.1): one owner at a time, the T3/T6 PR deleting the old one | T3, T6 | (b) |
| `ai_dispatch_*`, `ai_*` (unprefixed) | ABO's recorder around the AI calls books makes and over the core `ai` subsystem's public API; [05-ai](05-ai.md) names the recorder of each family. `MetricNamespace` does not touch them: the recorder creates the instruments `ai_dispatch.*` and `ai.*` under exactly HEAD's names (`internal/aidispatch/metrics.go:54-66`, `meter.go:20-37`) on the App's `MeterProvider`, outside any `Scope`, so the namespace is never prepended and the families stay byte-identical; the core `ai` subsystem's own families are `<namespace>_ai_*` beside them, and `ErrFamilyTaken` still guards every name | S7 | (b) |
| `audiobook_organizer_process_memory_alloc_bytes`, `_process_goroutines` | books' status worker, as at HEAD (`internal/server/server_lifecycle.go:432-433`); the gauges move to `Provider.Registerer()` with the other legacy families (item 2) | S8 | unchanged |
| `http_server_*` (semconv) | `httpapi`'s stage-2 `otelhttp`, the one emitter for every request; for a `MountLegacy` request the legacy mount reports gin's `c.FullPath()` back through `httpapi.ReportLegacyRoute` after gin has routed it, and stage 2 records that value as `http_route` once the handler returns, so the label keeps HEAD's values (§3; 05-http-api §4.2 stage 2, §8.2); gin's `otelgin` is removed in the same PR (MOD9-2, R9-3) | S2 | (a) |
| every other `audiobook_organizer_<x>` (books, search index, catalog, cache, ABS, review, fixer, metadata fetch, …) | books, unchanged | — | — |

   A family under rule (b) is recorded from the PR that moves its HEAD emitter, so the seeded test's golden shows
   no diff for it in that PR (§6).
4. **Dashboards and rules** in `deploy/` are unchanged: no stage switches a name and no compat recording-rule
   block is generated. The core families an app adds are new and appear in no shipped dashboard.
5. **Who may scrape.** For ABO, HEAD's behaviour (any authenticated credential; a fingerprint worker's worker-only
   credential stays confined) holds through S10 on the legacy route and on the kernel's mount (§6). ABO's roles
   hold legacy atoms only until the identity cutover, so no ABO role or key carries `core:metrics.read` before it,
   and no scrape key needs replacing (A-2). For native role stores, `admin` holds `core:metrics.read` through
   `core:admin`, and [05-rbac](05-rbac.md) §4.8 seeds it to `editor` and `viewer`; it is **never** seeded to
   `fp-worker` or any role holding a `Confining` scope, which would stop being confined (SEC-3, SEC3-13), so a
   fingerprint worker has no scrape access.

## 14. Open items

1. **OTLP transport.** gRPC is used only by telemetry (`go mod why -m google.golang.org/grpc` →
   `internal/telemetry`), and it is the module that drags in the genproto split that makes `go.work` fail
   (03 §3.2). Measure whether `otlpmetrichttp`/`otlptracehttp` remove gRPC from core's `go list -deps`; if they
   do, make HTTP the only transport (wave-0 experiment, [13-toolchain](13-toolchain.md)).
2. Exemplars (trace-id on histogram buckets) once tracing has a collector; off until then.
3. Whether `core.metrics.environment` belongs in a shared `core.app` section (also used by updater and
   diagnostics).

## Objections to 03

None open. 03 v1.11 §3.6 "Metric name" adopts this file's namespace rule (`<MetricNamespace>_<subsystem>_*`,
`app.Options.MetricNamespace`, default `mediacore`; A-5) and leaves to this file how each moved series keeps its
exact name (§13 item 3). The module rule (`app.Options.MetricPrefix`, default `AppID`, ABO `audiobook_organizer`,
counted over the modules the app ships, SIMP2-13) and the semantic-convention names are as 03 states; the app ID
field is `app.Options.AppID` (COR-26).

## Out-of-scope defects found

None beyond those listed in [05-logging](05-logging.md) and [05-config](05-config.md).

## Review history

- SIMP-13 — M3, M6, §4, §13: books metric names unchanged, no alias layer for them; single-module prefix rule; core renames only. (Core renames superseded by A-5: nothing is renamed.)
- SIMP-7 — M6, §3 `Alias.Ledger`, §7, §10: aliases are 09 ledger rows, removed by contract PR; `RemoveIn` and `core.metrics.legacy_aliases` deleted; usage-gate note for 09. (Superseded by A-5: no metric aliases.)
- SEC-22 — M7, §3 span redaction via redacting exporter with `*log.Redactor`; `TestTrace_NoApikeyInSpanAttributes`.
- SEC-6 — §10 Protection column; endpoints, path and listen are `ProtectAdmin`.
- COR-20 — M1, §3: no `metrics.Registry`; components take `*metrics.Scope`, kernel holds `*Provider`.
- COR-26 — §3 `Options.AppID` = `app.Options.AppID`, `MetricPrefix` = `app.Options.MetricPrefix` (03 v1.3.0 §3.6); objection closed.
- COR-19 — header: R4 v1.3.0 L0; `log` import is same-layer.
- OPS-17 — §10: no runtime alias switch; keys are operational dials.

Round 2:

- SIMP2-13 — header, M3, §3 `Options.Modules`, §4, §7: the module segment is decided by the modules compiled into the app, never the enabled set; tests; new objection to 03 §3.6 wording.
- SEC2-14 — M5, §6: `/metrics` mounted with `Router.Infra(path, "core:metrics.read", h)`, inside the chain's authenticated scope (stages 10–13 enforce it); probes stay Public.
- SIMP2-12 — §3 `Alias`: no class or dates; gate and dates come from the ledger row. (Superseded by A-5.)
- OPS2-21 (alignment with 05-config §1.12.4) — §10: `OTEL_METRIC_EXPORT_INTERVAL` and `OTEL_EXPORTER_OTLP_METRICS_INSECURE` listed as permanent aliases, ABO keys as window aliases.
- Header — 03 v1.4.0.

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): SIMP2-13 objection to 03 §3.6 closed (03 says "ships").

Round 3:

- MOD3-3 — §13 item 1: S1 moves construction only; the global installs stay in ABO's `internal/telemetry` façade until `app.InstallProcessDefaults` at S2; the six global-reading ABO packages named.
- SEC3-13 — §13 item 5: `core:metrics.read` via `core:admin`, seeded to `editor` and `viewer` only, never to `fp-worker` or a `Confining` role.
- SIMP3-3 — §3 `Alias`, §7 step 3: aliases supplied by the app with rows in the app's ledger. (Superseded by A-5.)
- SIMP3-9 — §9: periodic reader and batch span processor named as library-internal timers, not tasks.
- R3-1 — §13 item 5: the role seed stored additively in the legacy role rows. (Superseded in round 4 by R4-1, then by A-2.)

Round 4:

- MOD4-3 — M1, M5, §3 `Options.PromRegisterer`, exporter construction, §6 per-stage table (S2–S7: ABO's legacy gin route serves `Provider.Handler()` over the default registry, the OTel globals set by `app.InstallProcessDefaults`; S8: kernel `Router.Infra` mount and private registry, legacy families moved in the same PR; series-name-set done-checks), §7 step 1, §12 `TestNoGlobals` exception and `TestProvider_ExternalRegisterer`, §13 items 1–2.
- MOD4-4 — §13 item 1: per-file split; attribute keys, views, the scope helper and `SetVersion`/`Version` stay in ABO permanently as books telemetry; only the install code is deleted at S2.
- SIMP4-10 — §10: core's ledger fragment holds only the permanent `OTEL_*` rows; ABO's `otel_*` names are app-supplied window aliases.
- SIMP4-2 — no ABO name added: books' bucket and attribute tables stay in ABO (§13 item 1), consistent with "core holds no ABO name".
- R4-1 (SEC4-3) — §13 item 5: the `core:metrics.read` seed in books' extension family. (Superseded by A-2: ABO roles hold legacy atoms only.)

Round 5:

- OPS5-5 / SEC5-13 — ABO's legacy `/metrics` route required `core:metrics.read` from S4 r2. (Superseded by A-2: HEAD's any-credential check through S10.)
- OPS5-6 — §6 "Series-name checks", §13 items 3–4: each core-subsystem rename in the stage that moves its emitter. (Superseded by A-5: every stage's check is "additions only".)
- OPS5-9 — §6: every series-name check scrapes with a credential and fails on an empty name set.
- OPS5-14 — §3 `Options.PromRegisterer`, §6 table (new S2 r1–r2 row), §7 step 1, §13 item 1: `PromRegisterer` is set at S2 r3, in the release that deletes the façade's exporter; nil (private, unserved) at S2 r1–r2.
- MOD5-5 (reference) — §3 span redaction uses `log.URLKeys()`.

Round 6:

- OPS6-2 — §6: `go_goroutines` and `go_memstats_alloc_bytes` are present since HEAD. (`RuntimeFamilies()` and the process-gauge aliases superseded by A-5.)
- OPS6-3 — §6 "Series-name checks", §12 closing paragraph: the normative per-stage check is the golden diff of ABO's seeded series contract test (`internal/telemetrypolicy`); the live credentialed scrape is only a smoke check (200, non-empty, a fixed always-on set).

Round 7:

- OPS7-1 — §6 "What it scrapes", §12 closing paragraph: from S2 r3 ABO's seeded contract test builds the stage's Provider through `coretest.MetricsProvider` and scrapes the served `Provider.Handler()`, never `promhttp.Handler()`; core-owned emitters are seeded through public SDK paths; the test's registry becomes private in the S8 PR; `TestContractScrapesServedHandler`.
- OPS7-5 — the S8 process-gauge deletion with its boot check. (Superseded by A-5: the gauges stay.)
- Header — 03 reference v1.9.1.

Round 8:

- OPS8-1 — the T0+T1 deletion of the five `operations_*` vectors with its boot check. (Superseded by A-5: the families keep an ABO recorder.)
- OPS8-10 — §6 "What it scrapes", S2 r3 golden row, §12: kernel-emitted families are asserted by core's `TestKernelFamiliesGathered` (one zero-module `coretest` boot on a fresh registry), because a second booted Provider on the default registry collides on `target_info` (OPS5-14).
- R8-6 / MOD8-2 — §13 item 1: `opsmetrics`' one core successor is `mediacore/internal/executor`, and the ABO package stays as opsbridge-side code until its last user is gone; books' gauges use books' own scope.
- R-C request (05-tasks v1.9.0 5T.11) — §13 table: `_ops_schedule_lag_seconds` and `_ops_schedule_missed_total` move at T7; `_ops_abandoned_calls` stays a books series.
- Header — 03 reference v1.10.1.
- integration pass (round 8): §6 aligned with 06's `coretest.MetricsProvider(t, opts)`, which boots one zero-module harness; `TestKernelFamiliesGathered` stays as core's own check of the same families (OPS8-10, R-G request).

Advisor:

- A-5 (ADV-S4) — header, M3, M6, §1, §3 (`Options.MetricNamespace` from `app.Options.MetricNamespace`; the alias type, its registration method, the runtime-family list and the two alias errors deleted), §4, §6, §7, §8, §10, §11, §12, §13: ABO keeps every metric name; core families use the app's namespace; the renames at S2b, S5, S7 and S8, gather-time aliases, metric ledger rows, dashboard and rule switches, the compat recording-rule block, the alias-collision boot rules (OPS7-5, OPS8-1), their two full-alias-set boot tests, the two alias tests and the alias-families gauge deleted; §13 item 3 states per family how its HEAD name is kept (moved emitter under the namespace, the executor's `<namespace>.ops` scope, or a books recorder: a bus subscriber for the five `operations_*` families, R-C request); new `ErrFamilyTaken` with `TestFamilyTaken_SharedNamespaceAndPrefix`, `TestNamespace_CoreScopesUseAppNamespace` and ABO's `TestHEADSeriesStillPresent`; the seeded contract test keeps scraping the served handler.
- A-2 (ADV-S1) — M5, §6, §13 item 5: ABO's `/metrics` keeps HEAD's any-credential check through S10 (legacy route and kernel mount), because ABO roles hold legacy atoms only; the S4 r2 switch to `core:metrics.read`, its route test and the extension-family seed deleted; native role stores keep the `core:metrics.read` seed.
- A-4 (ADV-S3) — §13 item 1: 04 §8.2 and §8.6 cited as informative planning rows, not as gates.
- ADV-11 — §2: the series-golden count marked informative at `748fd787a`.
- Header — 03 reference v1.11.1; Objections updated (03 v1.11 §3.6 adopts the namespace rule).
- integration pass (advisor): §13 op-items row labels are `op_id`, `op_type` (`internal/metrics/metrics.go:275-284`).

Round 9:

- R9-3 / MOD9-1 — M3, §3 (`MeterProvider()` wrapper, `metrics.Shared`, extended `ErrFamilyTaken`, `metrics_family_taken`), §6, §8, §12, §13 item 1 and table: every family has exactly one owner at every stage; the T0+T1 PR shrinks ABO's `opsmetrics` to `ops.abandoned_calls`, so the executor and core scheduler alone own the run and schedule families (no duplicate `ops_zombies{}` sample); `ErrFamilyTaken` covers instruments made through the raw and global provider and collectors registered through the shared registry; `TestFamilyTaken_ThroughGlobalProvider`, `TestFamilyTaken_SharedRegisterer`.
- R9-3 / MOD9-2 — M6, §3 "Tracing and HTTP", §6, §12, §13 table: series identity (label values) is part of the contract; stage 2 is the one `http_server_*` emitter with gin's route template as `http_route` for legacy requests and `otelgin` removed; `plugin` keeps HEAD's values for adapted kinds; `TestABOMetricsGatherNoDuplicateOrMissing` (ABO's real recorders plus the kernel Provider, gathered without error, against a HEAD golden of family, label and series identity) and `TestLegacySeriesLabelValuesMatchHEAD`.
- MOD9-8 — §3 `Scope.Prefix` and §4 examples use the executor's `ops` scope (`<ns>_ops_runs_total`); §6 golden row names 05-tasks' new executor families at S5 and the schedule instruments' move at S5.
- Header — 03 reference v1.12.1.
- integration pass (round 9): §3 "Tracing and HTTP" and §13 `http_server_*` row: stage 2 takes `http_route` from the legacy mount's `httpapi.ReportLegacyRoute(ctx, c.FullPath())` carrier (05-http-api §4.2, §8.2); the resolved cross-cluster note on 13 removed.
