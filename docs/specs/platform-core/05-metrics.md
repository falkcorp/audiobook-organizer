<!-- file: docs/specs/platform-core/05-metrics.md -->
<!-- version: 1.12.0 -->
<!-- guid: 5cdf765d-aec1-4ed7-981a-e2e698afde61 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Metrics and tracing (`core/metrics`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §4, §9;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (OTel v1.47.0, exporter
v0.69.0, client_golang v1.25.0). Normative: [03-architecture](03-architecture.md) v1.14.0 R4 (`metrics` is in
**L0** with `entity`, `lifecycle`, `version`, `log`), R5 (no `prometheus.DefaultRegisterer`, no `otel.Set*Provider`
in core except the opt-in `app.InstallProcessDefaults`; R5 binds core only), §3.6 "Metric name" (one namespace,
`app.Options.MetricNamespace`, default `mediacore`: core series `<MetricNamespace>_<subsystem>_*`, module series
`<MetricNamespace>_*` for an app that ships one module and `<MetricNamespace>_<moduleID>_*` otherwise, decided by
the modules compiled in, not by enablement, §4; semantic-convention names unchanged). Owner decision D10: metric
names may change, and the dashboards and rules in `deploy/` change in the same PR.

## Decisions at a glance

| # | Decision |
|---|---|
| M1 | One `metrics.Provider` per `App` owns a private `*prometheus.Registry`, an OTel `MeterProvider`, a `TracerProvider` and a propagator. All are **injected**; nothing in core names `prometheus.DefaultRegisterer`/`DefaultGatherer` or calls `otel.Set*`/`otel.Get*`, except that `app.InstallProcessDefaults` sets the three OTel globals to this Provider's values for an app that opts in ([05-logging](05-logging.md) §3, MOD4-3). Components receive a `*metrics.Scope`; only the kernel holds the `*Provider`. There is no `metrics.Registry` type and no way to hand the Provider an outside registry. |
| M2 | The authoring API is the **OTel metric API** (`metric.Meter`), continuing ABO's in-flight migration (client_golang family ceiling 64 and falling, `internal/telemetry/testdata/client_golang_baseline.txt`). Prometheus is an exporter bound to the App's registry. client_golang is used directly only inside `core/metrics` (registry, Go/process collectors, promhttp) and through one escape hatch, `Provider.Registerer()`, for third-party and legacy collectors. |
| M3 | Names: core instruments `<namespace>.<subsystem>.<what>` → `<namespace>_<subsystem>_<what>[_unit][_total]`, where the namespace is the app's **metric namespace** `app.Options.MetricNamespace` (default `mediacore`, the name every table of this spec shows; ABO passes `audiobook_organizer`). Module instruments use the same namespace: `<namespace>.<what>` when the app **ships** one module, `<namespace>.<module>.<what>` when it ships two or more. "Ships" means the module list passed to `app.New`, which never changes at run time, so enabling or disabling a module never renames another module's series (SIMP2-13). The Prometheus translation strategy is **pinned** (`UnderscoreEscapingWithSuffixes`), not left to the exporter's process-global default. OTel semantic-convention instruments (`http.server.*`) keep their standard names. **Every Prometheus family has exactly one owner per App (R9-3):** one instrument identity or one collector produces it, whether it was created through a `Scope`, through the App's raw or process-global `MeterProvider`, or registered through `Registerer()` (`ErrFamilyTaken`, §3), so a module family and a core family that translate to one name cannot merge and two emitters can never serve one family. |
| M4 | Cardinality is a contract: closed attribute-key allow-list per scope, bounded value sets only, no identifiers; SDK per-instrument limit 2000; a series golden per scope. |
| M5 | `/metrics` is a core route mounted by the kernel through `Router.Infra(path, "core:metrics.read", h)` on **every** app, with the permission the chain **enforces** (stages 10–13 run for an `Infra` route whose permission is not Public, SEC2-14), outside response compression. For ABO the kernel mounts it in the R2 metrics flip; from R1 until then ABO's gin route serves `Provider.Handler()`. ABO's identity migration stores `core:metrics.read` on every principal that is not `Confined()` and on none that is (05-rbac §6), so the scrape key keeps working (D12 F2 keeps API keys) and a worker-scoped key never scrapes (§6). |
| M6 | **Names may change (D10), and the deploy files follow in the same PR.** A PR that renames a family, changes its labels or deletes it updates every reference in `deploy/grafana/dashboards/*.json` and `deploy/prometheus/*.yml` in the same PR, and `TestDeployReferencedSeriesServed` fails it otherwise (§6, §12). There is no alias, no HEAD golden and no compat recording rule. A family keeps its name where its emitter stays (ABO's books families) or where core's name already equals it under ABO's namespace; a rename that buys nothing is not made. §13 lists every HEAD family that changes. |
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
| Series contract | `internal/telemetry/contract/testdata/series.golden`: **87 series** (74 `audiobook_organizer_*`, 10 `ai_*`, 3 `http_server_*`; an informative count at `748fd787a`); histogram buckets golden; views table; `TestAIDashboardUsesGoldenNames` checks the AI dashboard's names against the golden | `contract/testdata/`, `internal/telemetry/views.go:37-80`, `internal/telemetry/contract/series_contract_test.go:754` |
| HTTP series | `otelgin.Middleware("audiobook-organizer")` on the gin engine records `http_server_*` with gin's `c.FullPath()` as `http_route` | `internal/server/server.go:1668-1673` |
| Process identity | `instanceID` = random ULID per process (hostname-free), package var | `internal/telemetry/resource.go:31` |
| Error handling | rate-limited OTel error handler and value-dropping SDK logger, both process-global | `internal/telemetry/errorhandler.go:121`, `sdklog.go:38-39` |
| `/metrics` | `promhttp.Handler()` (default gatherer); any authenticated credential when auth is on, with worker-only credentials confined to the worker API; excluded from gin gzip after a production scrape failure | `internal/server/server_lifecycle.go:1735-1747`, `internal/server/middleware/auth.go:268-297`, `internal/server/server.go:708-716` |
| Other default-gatherer readers | cache stats handler, cache snapshotter | `internal/server/handlers/cache.go:99`, `internal/server/cache_snapshotter.go:24` |
| Dashboards / rules | 2 Grafana dashboards, 9 alert rules, 1 compat recording rule (`label_replace` of `def_id` → `op_type`), promtool unit tests (`TestPromRules_Promtool`, run when promtool is on `PATH`); they reference 20 `audiobook_organizer_*` names, 7 `ai_*`, and `process_resident_memory_bytes` (from the default registry's process collector) | `deploy/grafana/dashboards/*.json`, `deploy/prometheus/alert-rules.yml`, `deploy/prometheus/recording-rules.yml:14-19`, `deploy/prometheus/alert-rules.test.yml`, `internal/opsmetrics/promrules_test.go` |
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
	AppID            string   // app.Options.AppID, e.g. "abo"; the ServiceName default
	// ServiceName is the resource's service.name (app.Options.ServiceName; default AppID; ABO passes
	// "audiobook-organizer", cmd/root.go:296 @748fd787a). It is also the fixed server.address of the
	// http.server.* series core's stage 2 records, never the request's Host (§5 rule 2; MOD10-2).
	ServiceName      string
	MetricNamespace  string   // app.Options.MetricNamespace, [a-z][a-z0-9_]*; default "mediacore"; ABO: "audiobook_organizer"
	Modules          []string // IDs of the modules compiled into the app (the app.New module list), enabled or not;
	                          // one module => module scopes use MetricNamespace alone (M3). Never the enabled set.
	ServiceVersion   string
	InstanceID       string // per-process random ULID, never a hostname
	Environment      string // deployment.environment
	Prometheus       bool
	OTLP             *OTLPMetrics // nil = no push
	Tracing          *OTLPTracing // nil = no-op TracerProvider
	Redactor         *log.Redactor // from Logging.Redactor(); applied to span names and URL attributes before export
	CardinalityLimit int           // per instrument; 0 = 2000 (SDK default)
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

// Scopes enforce naming. Core subsystems get "<MetricNamespace>.<subsystem>"; modules get MetricNamespace
// (the app ships one module) or "<MetricNamespace>.<module>" (it ships two or more).
func (p *Provider) Core(subsystem string, allowed ...attribute.Key) (*Scope, error)
func (p *Provider) Module(id string, allowed ...attribute.Key) (*Scope, error)

// Scope is what components and modules receive (module.Deps.Metrics, constructor params): *metrics.Scope.
type Scope struct{ /* unexported */ }

func (s *Scope) Prefix() string                 // "mediacore.ops" (default namespace), "audiobook_organizer"
func (s *Scope) Meter() metric.Meter            // validating wrapper over the App's MeterProvider
func (s *Scope) Tracer() trace.Tracer           // tracer named "<prefix>"
func (s *Scope) Propagator() propagation.TextMapPropagator
func (s *Scope) Allowed() []attribute.Key       // declared keys; asserted by metricstest
// View declares a Scope instrument's histogram buckets before the instrument is created: it records them in the
// Provider's view table, which the one view function installed at New consults when the SDK resolves the
// instrument (an SDK view can be set only at construction, so this is a lookup, not a late view; MOD10-6). A
// Scope histogram without one fails ErrInstrumentName.
func (s *Scope) View(instrument string, buckets ...float64) error

var (
	ErrInstrumentName = errors.New("metrics: instrument name violates naming rules")
	ErrScopeTaken     = errors.New("metrics: scope already registered")
	ErrFamilyTaken    = errors.New("metrics: Prometheus family already registered by another instrument or collector")
)
```

**Instrument rules enforced by `Scope.Meter()`** (constructor returns `ErrInstrumentName` and a no-op
instrument, which the module surfaces at registration so boot fails closed): name =
`<scope prefix>.<seg>(.<seg>)*`, segments `[a-z][a-z0-9_]*`; counters must not end in `total`; histograms must
have a unit (`s`, `By`, or `{thing}`) and a declared view (buckets); gauges whose legacy name ends in `_total`
use a bracket unit, exactly as ABO's rules (`meter.go:20-37`). These rules are ABO's, made per-scope.
An instrument created outside any `Scope` (through the raw or the process-global `MeterProvider`) gets no view
from the Provider: it carries its buckets as creation advice (`metric.WithExplicitBucketBoundaries`), which the
SDK honours when no view matches, or the SDK default. ABO's instruments of that kind take their buckets from
HEAD's `telemetry.HistogramBuckets()` table as advice (§13 item 1).

**One family, one owner (R9-3).** Module and core scopes share the app's namespace, so a core family and a
module family could translate to one Prometheus name; and ABO's own code creates instruments through the
process-global provider and registers client_golang collectors through `Registerer()`. The Provider records the
translated family name (`otlptranslator`, the pinned strategy below) of:

- every instrument created on the App's `MeterProvider`, through a `Scope` **or not**: `MeterProvider()` returns a
  wrapper that records `(scope name, scope version, schema URL, instrument name, kind, number type, unit, description)`
  → family for every instrument, the SDK's own instrument identity (meters cached by scope, `sdk/metric@v1.47.0`
  `provider.go:79-98`; instruments by name, description, unit and kind, `meter.go:703-717`; FIN12-7),
  and it is that wrapper `app.InstallProcessDefaults` installs as the OTel global, so ABO's `otel.GetMeterProvider()`
  callers (`opsmetrics.Default()` while it exists, `aidispatch`, …) are seen too;
- every collector registered through `Registerer()`.

A second **owner** of a family is refused with `ErrFamilyTaken`: a different instrument identity (another scope name,
version or schema URL, kind, number type, unit or description) or a collector for a name an instrument holds, and the
reverse. Creating the identical instrument twice in one scope is the OTel SDK's one instrument, not a second owner.
The refused instrument is a no-op (a refused collector is not registered). During boot (through 06 P6) a refusal
fails boot; after boot (an instrument created lazily, as `opsmetrics.Default()` does on first use) it is logged at
error with both owners and raises the health token `metrics_family_taken` (degraded), and CI catches it first
through `TestABOMetricsGatherNoDuplicate` (§6). Two instruments of one scope that the SDK would merge are that
scope's own concern, as today.

**Exporter construction (normative):**
`prometheus.New(prometheus.WithRegisterer(reg), prometheus.WithoutScopeInfo(),
prometheus.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes))` — the three options
verified at exporter v0.69.0 (`config.go:67,120,170`); `reg` is the private registry. It also gets
`collectors.NewGoCollector()` and `collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})`
explicitly (the default registry had them implicitly; ABO's memory alert reads
`process_resident_memory_bytes`). `Handler` wraps `promhttp.HandlerFor(gatherer, opts)` in
`promhttp.InstrumentMetricHandler(reg, …)` so the `promhttp_metric_handler_*` series exist too.
`WithoutScopeInfo` keeps scope labels off `/metrics`, as at HEAD (`internal/telemetry/telemetry.go:446-447`).

**Tracing and HTTP (D10).** `httpapi` traces with `otelhttp` using `WithTracerProvider` and
`WithPropagators` from the Provider, never the globals. Core's stage 2 ([05-http-api](05-http-api.md) §4.2) is the
**only** recorder of the three semconv families (`http_server_request_duration_seconds` and the request and response
body sizes): it creates them once on the `httpapi` meter of `MeterProvider()` with the `httpconv` constructors and
records every request, the legacy gin mount's included, with the matched mux pattern as `http_route` (none for a
request answered before routing, a router 404 or an SPA page) and `Options.ServiceName` as `server_address`. ABO's
`otelgin` leaves the gin engine in R1 (13 §13.5.1). Against HEAD the `http_route` values change form (`{id}` for
`:id`), refusals the perimeter answers before routing carry no route, and `http_server_response_body_size` counts
uncompressed bytes (compression is stage 9, after stage 2); no shipped dashboard or rule reads these families (§13).

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
| Module, app ships one module | `<namespace>.<what>` | `<namespace>_<what>…` | `audiobook_organizer.books` → `audiobook_organizer_books_total` (today's name, unchanged) |
| Module, app ships two or more | `<namespace>.<module>.<what>` | `<namespace>_<module>_<what>…` | `suite.tv.scan.files` → `suite_tv_scan_files_total` |
| Semconv | spec name | translated | `http.server.request.duration` → `http_server_request_duration_seconds` |
| Resource | — | `target_info{service_name,service_version,service_instance_id,deployment_environment,telemetry_sdk_language,telemetry_sdk_name,telemetry_sdk_version}` | `service.name` = `Options.ServiceName` (ABO `audiobook-organizer`), merged with the SDK's `resource.Default()` as HEAD does (`internal/telemetry/resource.go:36-50` @748fd787a) |

**Reading the tables of the other files.** Every 05 file writes core family names with the default namespace,
`mediacore_…`. For an app that passes its own `MetricNamespace`, read that namespace instead (ABO:
`audiobook_organizer_…`). Native apps that keep the default (hello, TV, Movies) share every core family name, so
one set of core dashboards serves them (distinguished by `job`/`service_name`). ABO's core families differ only in
the namespace, so a cross-app dashboard needs a per-app query.

Module names are per app. The single-module rule exists because inserting `books_` disambiguates nothing in an
app whose namespace is already app-specific, while it would rename every books series and every dashboard
reference to them for no gain (SIMP-13). **Trade:** if ABO's books module ever shares a binary with other
modules, its series are renamed then, with the deploy files in the same PR; that is a row of the 11 §11.5
checklist. `Provider.Module` computes the prefix from `Options.Modules`, the modules compiled into the app, so
the rule is mechanical and the segment changes only when a release changes the module list. Keying it on the
*enabled* modules would rename every series of the remaining module when an operator disables one at the next
restart, breaking dashboards (SIMP2-13).

## 5. Cardinality rules (normative)

1. Every scope declares its attribute keys (`Core(…, keys)` / module manifest `Metrics.Attributes`); keys come
   from a shared vocabulary (`outcome`, `kind`, `module`, `provider`, `adapter`, `capability`, `endpoint`,
   `reason`, `class`, `state`, `phase`, `direction`, `cache`, `route`, `method`, `status_class`, …) extended
   per module with review.
2. Values must come from a set that is finite and known at registration: enums, registered task kinds,
   enabled module IDs, configured adapter/endpoint IDs, route templates. **Forbidden as values:** entity IDs,
   task run IDs, user IDs, request IDs, file paths, raw URLs, error strings, titles/names, and any value taken
   from a request header: `server.address` is an app constant, `Options.ServiceName`, never the `Host` header (§3,
   MOD10-2). HEAD's per-run `op_id` gauges (§2) break this rule; they go with HEAD's ops engine in the R2 tasks
   flip (§13 item 3).
3. Per-instrument SDK cardinality limit `core.metrics.cardinality_limit` (default 2000,
   `sdk/metric@v1.47.0/config.go:183`); overflow lands in the SDK's overflow series and fails
   `metricstest` in CI.
4. Histogram buckets are declared per instrument (`Scope.View`, or creation advice for an instrument created
   outside a `Scope`; §3); no exponential histograms by default (ABO pinned this for OTLP,
   `telemetry.go:418-420`).
5. A series golden per scope (`testdata/series.golden`: family, type, label set) is asserted by
   `metricstest.AssertSeries`; changing it is a reviewed diff — the generalization of ABO's contract test.

## 6. `/metrics` ownership

The kernel mounts `Provider.Handler()` at `core.metrics.path` (default `/metrics`, outside `/api`) with
`Router.Infra(path, "core:metrics.read", h)` ([05-http-api](05-http-api.md) §3.5) — permission
**`core:metrics.read`** (R6: explicit). `Infra` routes are inside the chain's authenticated scope: for an `Infra`
route whose permission is not Public, stages 10–13 (authentication through permission) run exactly as for a
native route, so the permission is enforced, not merely recorded; only the probes `/livez` and `/readyz` stay
Public (SEC2-14; `TestInfra_MetricsRequiresScope` in 05-http-api asserts it). It is **excluded from compression
middleware** (promhttp negotiates gzip itself; double gzip broke production scrapes, `server.go:708-716`).
Optional dedicated listener `core.metrics.listen` (e.g. a loopback address) serving only `/metrics`; it requires
the same credential unless the operator sets `core.metrics.listen_public=true` (`ProtectOperator`, logged at warn
on every boot). Two apps in one process each expose their own registry on their own listener or path;
registries are never merged. The path `/metrics` is kept (zero cost; the owner's scrape config names it).

**Who may scrape.** For native role stores, `admin` holds `core:metrics.read` through `core:admin`, and
[05-rbac](05-rbac.md) §4.8 seeds it to `editor` and `viewer`; it is **never** seeded to `fp-worker` or any role
holding a `Confining` scope, which would stop being confined (SEC-3, SEC3-13), so a fingerprint worker has no scrape
access. For ABO, HEAD admits any authenticated credential except a worker-confined one (a key scoped to
`fingerprint.worker` stays confined whatever its owner, as HEAD's `workerOnly` tests the effective set,
`internal/server/middleware/auth.go:266-297` @748fd787a). The R2 identity migration stores `core:metrics.read` on
every migrated role and key whose principal is not `Confined()` (05-rbac §6), so the owner's scrape key keeps
working (D12 F2 keeps API keys) and the admitted set equals HEAD's. A key created after R2 scrapes only if its
scopes include `core:metrics.read`.

**Who serves `/metrics` (MOD4-3).** One registry, the App's, is served from R1:

| Release | Registry | Who serves `/metrics` |
|---|---|---|
| R0 | HEAD's default registry | gin, `promhttp.Handler()`, HEAD's any-credential check |
| R1 | the App's private registry. ABO's legacy client_golang families register through `Provider.Registerer()` and `app.InstallProcessDefaults` points the OTel globals at the App's providers, in the PR that wires `app.New`, so the six global-reading ABO packages keep their series; `cache.go:99` and `cache_snapshotter.go:24` take `Provider.Gatherer()` in the same PR | ABO's **legacy gin route** under `MountLegacy`, unchanged in path and compression exclusion, whose handler becomes `Provider.Handler()`; HEAD's any-credential check until the R2 identity flip, then the manifest's `core:metrics.read` (05-http-api §8.1) |
| R2 metrics flip on | the App's private registry | the kernel's `Router.Infra(core.metrics.path, "core:metrics.read", …)` mount; the legacy gin route is removed in the same PR |

**Series checks (OPS5-6, OPS5-9, OPS6-3).** A live scrape lists only the families that have recorded a sample
since boot: a client_golang `Vec` with no observed label set, an OTel instrument with no data point and the Pebble
collector without a source export nothing (`internal/telemetry/contract/series_contract_test.go:15-18`
@748fd787a), and families tied to configured integrations never appear on a store whose providers are not set up.
So a live scrape cannot be the normative check.

**Normative: the seeded contract test.** ABO's series contract test (HEAD's `internal/telemetry/contract`, kept
in ABO as `internal/telemetrypolicy` from R1) builds the telemetry in its own process, records one sample per row
of its **seeding table**, scrapes, and compares the family set (family, type, label set) with its golden. A PR that
adds, renames or deletes a family changes its seeding row and its golden line in the same PR, and the deploy
files with them (M6). **What it scrapes (OPS7-1):**

- From R1 the test builds the same `metrics.Provider` the kernel builds, through the public test constructor
  `coretest.MetricsProvider(t, opts)` (06 `coretest`): it **boots one zero-module `coretest` harness** (G7's) with
  `opts` (`MetricNamespace`), so the kernel applies its `metrics.Options` defaults exactly as §7 step 1 does, and
  returns that boot's Provider. It scrapes that Provider's `Handler()`, never `promhttp.Handler()`.
- **No prefix is added to `inScopePrefixes`** (today `audiobook_organizer_`, `ai_dispatch_`, `ai_`,
  `http_server_`, `series_contract_test.go:69-71`): ABO passes `audiobook_organizer` as its namespace, so every core
  family of ABO's App is already in scope. A PR that deletes the last `ai_*` family deletes those two entries.
- **Seeding rows for core-owned emitters go through public SDK paths**, because ABO cannot import
  `mediacore/internal/...`: the Pebble collector through a store opened with `coretest`/`storage`, the tasks
  instruments through one `hello.count` run, the AI instruments through a fake endpoint.
- **Seeding row for the kernel's own families (OPS8-10):** the zero-module `coretest` boot inside
  `coretest.MetricsProvider`, whose Provider is the one scraped. `<namespace>_kernel_boot_phase_seconds`
  ([06](06-app-sdk.md) §6.9), `<namespace>_storage_level` and `<namespace>_storage_checkpoint_bytes` are recorded
  by kernel code during a boot, never by the Provider, and a family with no sample exports nothing, so the
  constructor boots rather than only building the Provider. There is one Provider per test, so nothing collides on
  `target_info` (OPS5-14). Core also asserts the same families in its own seeded test, `TestKernelFamiliesGathered`
  (a `kernel` test, §12), so core CI checks them without ABO.
- **The `http_server_*` seeding rows go through production wiring (MOD10-5).** HEAD seeds them through a
  test-local gin engine with its own `otelgin.Middleware` (`series_contract_test.go:265-267,404-417` @748fd787a),
  which would stay green even if production stopped serving them. From R1 the test seeds them through ABO's real
  gin engine mounted by `MountLegacy` on the `coretest` boot, so stage 2 records them; the golden's source column
  for the three rows becomes `httpapi`.
- `TestContractScrapesServedHandler` asserts that the handler under test is the one the `/metrics` mount serves
  (the legacy gin route's handler in R1, the kernel's `Router.Infra` mount from the R2 metrics flip).
- **`TestABOMetricsGatherNoDuplicate` (ABO, from R1; R9-3, MOD9-1).** A seeded test with `coretest` alone cannot
  see a second emitter, because it never builds ABO's own recorders. This one builds the kernel Provider (through
  `coretest.MetricsProvider`) **together with ABO's real recorders on it**: `app.InstallProcessDefaults`, ABO's
  legacy client_golang families registered through `Registerer()`, `opsmetrics.Default()` while the package
  exists, and books' recorders. It drives one task run, one legacy HTTP request on a sampled route through the
  production gin engine, one core-route request and, while ABO records any, one AI call through a fake endpoint.
  Then `Gatherer().Gather()` must return **no error** (two samples of one family with equal label values fail it,
  as client_golang does at a scrape) and no family may have two owners.
- **`TestDeployReferencedSeriesServed` (ABO, from R1).** It reads every PromQL expression in
  `deploy/grafana/dashboards/*.json` and in `deploy/prometheus/alert-rules.yml`, `recording-rules.yml` and
  `alert-rules.test.yml`, extracts each metric name (a `_bucket`, `_count` or `_sum` suffix folded into its
  histogram family), drops the names those files define themselves (`record:` outputs) and the scrape-generated
  `up`, and asserts that every remaining name is a family of the seeded scrape above. It generalises HEAD's
  `TestAIDashboardUsesGoldenNames` (`series_contract_test.go:754`) from the AI dashboard to every dashboard and
  rule file. `promtool test rules deploy/prometheus/alert-rules.test.yml` keeps running as HEAD's
  `TestPromRules_Promtool` does (when promtool is on `PATH`), from the package that owns the rule files' tests once
  `internal/opsmetrics` is deleted, so an alert whose series was renamed fails its unit test too.

The Go collector's `go_goroutines` and `go_memstats_alloc_bytes` are present in every release: HEAD's `/metrics`
serves `promhttp.Handler()` over the default registry, which already carries the Go collector
(`internal/server/server_lifecycle.go:1746-1747` @748fd787a), and the private registry adds it from R1 (OPS6-2).

**Smoke check: the live scrape (optional, D11).** After a deploy a credentialed scrape (a key holding
`core:metrics.read`; for ABO before the R2 identity flip any non-confined credential, as at HEAD) must answer 200
with a non-empty family list (comment lines dropped, label part cut, sorted unique; an unauthenticated scrape gets
401 and would compare nothing). That list must contain a short fixed set of always-on families:
`process_resident_memory_bytes` and `go_goroutines`, plus `<namespace>_build_info` from R2. It is an item of the
optional smoke run, never compared with another build's scrape.

`health` serves no `/metrics` ([05-health](05-health.md) §5H.11 item 3).


## 7. Lifecycle

1. `app.New`, right after logging: `metrics.New` with options from `core.metrics` / `core.tracing` (operator
   layers and defaults), `Options.MetricNamespace` = `app.Options.MetricNamespace` (default `mediacore`),
   `Options.Redactor = Logging.Redactor()`, `Options.Modules` = the IDs of every module passed to `app.New`,
   enabled or not (§4), and `Options.ServiceName` from `app.Options.ServiceName` (06; MOD10-11). The kernel hands
   `httpapi` this Provider's `MeterProvider()` for stage 2 (05-http-api §3.5). If the app's `main` calls
   `app.InstallProcessDefaults`, the OTel globals point at this Provider from then on
   ([05-logging](05-logging.md) §3).
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
| A second owner of one Prometheus family (another instrument identity, through a `Scope`, the raw or the global `MeterProvider`, or a collector through `Registerer()`) | fail-closed during boot (`ErrFamilyTaken`); after boot the second is a no-op, logged at error, health `metrics_family_taken` (degraded) | two owners merge silently or make every scrape fail (M3, R9-3, MOD9-1) |
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
| `core.metrics.otlp.endpoint` | string | `""` (env binding `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`) | no | admin |
| `core.metrics.otlp.interval` | duration | `60s` (env binding `OTEL_METRIC_EXPORT_INTERVAL`, a bare integer in milliseconds as the OTel specification defines it, which ABO's `telemetry.ParseMetricsInterval` implements) | no | none |
| `core.metrics.otlp.insecure` | bool | `false` (env binding `OTEL_EXPORTER_OTLP_METRICS_INSECURE`) | no | admin |
| `core.metrics.environment` | string | `prod` | no | none |
| `core.metrics.cardinality_limit` | int | `2000` | no | none |
| `core.tracing.otlp.endpoint` | string | `""` (env binding `OTEL_EXPORTER_OTLP_ENDPOINT`) | no | admin |
| `core.tracing.otlp.insecure` | bool | `false` | no | admin |
| `core.tracing.sample_ratio` | float [0,1] | `1.0` (parent-based) | no | none |

Endpoints are `ProtectAdmin` because a changed endpoint sends every metric and span (which carry route and host
data) to a host of the actor's choosing (05-config §1.3.7). Telemetry wiring is construction-time; making it live
would require swapping providers under running instruments, which the OTel SDK does not support. The metric
namespace is an `app.Options` value compiled into the app's `main`, never a config key. All keys here are
operational dials. The `OTEL_*` names are the OpenTelemetry ecosystem's own and are these keys' canonical env
bindings (05-config §1.3.6). ABO's HEAD names (`otel_metrics_otlp_endpoint`, `otel_metrics_otlp_interval`,
`otel_metrics_otlp_insecure`, `otel_exporter_otlp_endpoint`, `telemetry_environment`, `resource.go:25`) are rows of
05-config's generated rename table, which the R2 config migration applies once (05-config §1.12.4); core holds none
of them (R1).

## 11. Metrics (of this subsystem)

| Name | Type | Labels |
|---|---|---|
| `mediacore_metrics_otlp_export_failures_total` | counter | `signal` ∈ metrics, traces |
| `mediacore_metrics_span_redactions_total` | counter | `kind` ∈ key, url, path |
| `promhttp_metric_handler_requests_total` | counter | `code` (client_golang standard) |
| `promhttp_metric_handler_errors_total` | counter | `cause` (client_golang standard) |
| `target_info` | gauge | resource attributes (§4: `service_name` = `Options.ServiceName`, plus `resource.Default()`'s `telemetry_sdk_*`) |

## 12. Tests (core must have)

| Test | Asserts |
|---|---|
| `TestNoGlobals` (archtest, R5) | no `prometheus.DefaultRegisterer/DefaultGatherer/MustRegister/Register`, `promhttp.Handler()`, `otel.Set*`/`otel.Get*Provider`/`otel.Tracer`/`otel.Meter` in core, except `otel.SetMeterProvider`, `otel.SetTracerProvider` and `otel.SetTextMapPropagator` inside `app.InstallProcessDefaults` (05-logging §10) |
| `TestGoAndProcessCollectorsPresent` | `go_goroutines`, `process_resident_memory_bytes` in the scrape of a fresh Provider's private registry (MOD4-3) |
| `TestTwoProvidersIsolated` | two Providers register identical instruments without panic; scrapes are disjoint |
| `TestScope_RejectsBadNames` | prefix, `_total` suffix, missing unit/view → `ErrInstrumentName` |
| `TestNamespace_CoreScopesUseAppNamespace` | `MetricNamespace` unset → core scope prefix `mediacore.<subsystem>`; `MetricNamespace` = `audiobook_organizer` → `audiobook_organizer.<subsystem>` and families `audiobook_organizer_<subsystem>_…`; an invalid namespace fails `New` |
| `TestFamilyTaken_ModuleAndCoreShareNamespace` | a single-module app: a module instrument and a core instrument that translate to one family → the second gets `ErrFamilyTaken`; the same for an OTel instrument against a collector registered through `Registerer()`, in both orders; distinct families under the shared namespace register |
| `TestFamilyTaken_ThroughGlobalProvider` | after `app.InstallProcessDefaults`, an instrument created through `otel.GetMeterProvider()` in another scope with a name a core `Scope` instrument holds (the `ops.zombies` case of MOD9-1) → `ErrFamilyTaken`, a no-op instrument, boot fails during boot and `metrics_family_taken` is raised after it; the identical instrument created twice in one scope is one instrument (R9-3) |
| `TestAdviceBucketsOutsideScope` | an instrument created through the raw `MeterProvider` with `WithExplicitBucketBoundaries` gets those `le` values; a `Scope.View` made before creation applies to its Scope instrument (MOD10-6) |
| `TestResource_ServiceNameAndSDKDefaults` | `ServiceName` = `audiobook-organizer` → `target_info{service_name="audiobook-organizer", telemetry_sdk_language, telemetry_sdk_name, telemetry_sdk_version, …}`; unset → `service_name` = `AppID` (MOD10-11) |
| `TestStage2_ServerAddressIgnoresHost` (`httpapi`, with 05-http-api) | two requests to one route with different `Host` headers produce one `server_address` value, `Options.ServiceName` (MOD10-2) |
| `TestModuleNamespace_SingleAndMulti` | one compiled-in module → module scope prefix `MetricNamespace`; two → `<MetricNamespace>.<module>` |
| `TestModuleNamespace_StableUnderEnablement` | two compiled-in modules, one disabled: the other's prefix is still `<MetricNamespace>.<module>`; series names identical before and after the toggle |
| `TestTranslationStrategyPinned` | names identical with the global validation scheme set to legacy and to utf8 |
| `TestTrace_NoApikeyInSpanAttributes` | in-memory exporter behind the redacting wrapper: request `?apikey=…&token=…` and `/api/core/v1/auth/invites/<t>` → no secret in span name or attributes |
| `TestHandler_NoDoubleGzip` | `Accept-Encoding: gzip` through the httpapi stack yields one gzip layer |
| `TestCardinality_OverflowFailsMetricstest` | exceeding the limit is detected |
| `TestOTLPBadEndpointDegrades` | provider builds, health token set |
| `TestShutdownFlushes` | `testing/synctest` + in-memory exporter: last batch exported |
| `metricstest.AssertSeries` (per scope) | series golden for each core scope |
| `TestKernelFamiliesGathered` (a `kernel` test) | one zero-module `coretest` boot: the scrape of that App's `Provider.Handler()` carries every kernel-emitted family (`<namespace>_kernel_boot_phase_seconds`, `<namespace>_storage_level`, `<namespace>_storage_checkpoint_bytes`), matching core's golden (OPS8-10) |

ABO keeps its own series contract test (HEAD's `internal/telemetry/contract`, `internal/telemetrypolicy` from R1),
which scrapes the served `Provider.Handler()` of a Provider built through `coretest.MetricsProvider` with ABO's
namespace (§6, OPS7-1). Its seeded golden is the normative series check (§6, OPS6-3). With it go
`TestContractScrapesServedHandler`, `TestABOMetricsGatherNoDuplicate` (ABO's real recorders plus the kernel
Provider, gathered without error and with one owner per family; R9-3) and `TestDeployReferencedSeriesServed`
(every series the deploy files name is served), all from R1, and HEAD's `TestPromRules_Promtool`. Core's
own `TestKernelFamiliesGathered` asserts the kernel-emitted families in core as well (§6, OPS8-10).

## 13. Migration from ABO

1. **Injection first, installs last (MOD3-3, MOD4-3, MOD4-4).** `internal/telemetry` splits per file:
   - **to core `metrics` in R1:** provider construction, the endpoint validator, the exporter wiring and the
     resource, with the namespace and the scope as constructor options. Core only **returns** providers (R5).
   - **stays in ABO permanently, as books telemetry** (the `internal/telemetry` path kept, or renamed under
     `internal/books`): `attr.go` (the ~30 attribute-key constants: `def_id`, `plugin`, `fixer`, `op_id`, …),
     `views.go` (the bucket table keyed by ABO family names) and `HistogramBuckets()`, the `meter.go` scope helper
     (`Meter(name)`, `MeterFrom`) and `SetVersion`/`Version`. ABO's instruments created outside a `Scope` pass
     their `HistogramBuckets()` entry as creation advice (§3), and the rest feed `Scope.Allowed` and
     `Options.ServiceVersion` at registration, so no ABO vocabulary enters core (R1; MOD10-6).
     `LoadConfig`/`WithMetricsOTLP`/`WithEnvironment`/`ParseMetricsInterval`/`Config` stay in ABO until the R2
     config flip, when the kernel builds `metrics.Options` from `core.metrics`/`core.tracing`; they are deleted when
     their last importer is gone (04 §8.2 lists, informatively, which importers rewire when).
   - **the process-global installs** (`otel.SetTracerProvider`, `telemetry.go:287`; `otel.SetMeterProvider`,
     `:461`; `otel.SetErrorHandler`, `errorhandler.go:121`; `otel.SetLogger`, `sdklog.go:39`; the propagator;
     the default-registry Prometheus exporter) are replaced by `app.InstallProcessDefaults`
     ([05-logging](05-logging.md) §3) and the App's private registry in the R1 PR that wires `app.New` (§6), so
     the six ABO packages that still create instruments through the globals (`internal/ai/telemetry.go`,
     `internal/aidispatch/metrics.go`, `internal/database/memdb_metrics.go`, `internal/dedup/engine.go`,
     `internal/operations/registry/worker.go`, `internal/opsmetrics/opsmetrics.go`) keep their series. A PR
     that builds core's Provider before `app.New` exists installs it from ABO's own telemetry init, and the
     `app.New` PR deletes that code. 04 lists this as a re-implementation, not a verbatim move.

   The three `otel.Tracer` package vars, `otel.GetMeterProvider()` calls and `opsmetrics.Default()` become injected
   scopes when touched. `opsmetrics`' core successor is written fresh in one place, `mediacore/internal/executor`
   ([05-tasks](05-tasks.md)), whose instruments sit on scope `<namespace>.ops` (item 3). ABO's `internal/opsmetrics`
   is deleted with HEAD's ops engine in the R2 tasks flip. **One owner per family:** the PR that gives a HEAD
   family a new emitter deletes the old one in the same change (`ErrFamilyTaken`, R9-3); in particular the PR that
   lands the executor's `ops.*` instruments in ABO's App stops `opsmetrics` from creating the same names (HEAD's
   recorder observes an attribute-less `ops.zombies` 0 on every collection, `opsmetrics.go:214-273` @748fd787a;
   left beside the executor's, it would put two samples of `audiobook_organizer_ops_zombies{}` on every scrape and
   client_golang's `Gather` would fail the whole `/metrics` answer). Books' own gauges register through books' own
   `metrics.Scope`, never through core instrumentation with ABO names.
2. **client_golang families** in `internal/metrics` finish the move to OTel when convenient (the ratchet continues
   to zero). All of them, the five `operations_*` families and the two process gauges included, register through
   `Provider.Registerer()` from the R1 PR that makes the App's registry the served one (§6). `metrics.Register`'s
   `sync.Once` global disappears in the same PR.
3. **Names: every HEAD family that changes (D10).** ABO passes `audiobook_organizer` as its namespace, so books'
   families and the core families whose names already equal HEAD's keep their names. Where HEAD's shape carries
   vocabulary that the native code does not keep, the family takes core's name and labels, and the deploy files
   follow in that PR (M6). 05-tasks 5T.11, 05-storage and 05-ai table the core families named here.

| HEAD family | After the move | PR | Deploy files |
|---|---|---|---|
| `audiobook_organizer_pebble_*` (label `store`) | core `storage`'s Pebble collector, which moves with HEAD's family names, help strings, types and `store` label (`pebble_collector.go:119-146`) and registers `<namespace>_pebble_*`, the same names under ABO's namespace ([05-storage](05-storage.md) tables it) | R1 (storage) | unchanged |
| `audiobook_organizer_ops_runs_total`, `_ops_run_duration_seconds`, `_ops_items_total`, `_ops_inflight`, `_ops_progress_age_seconds`, `_ops_progress_timeout_seconds`, `_ops_zombies`, `_ops_schedule_lag_seconds`, `_ops_schedule_missed_total` | the core executor's and scheduler's instruments in the scope `<namespace>.ops`: the same names under ABO's namespace, with core's label set and values ([05-tasks](05-tasks.md) 5T.11); `def_id` values are the native kinds after the kind-rename map | R2 tasks flip (the executor's first emission in ABO's App) | queries that select by a HEAD `def_id` value are updated through the kind-rename map in the same PR |
| `audiobook_organizer_operations_{started,completed,failed,canceled}_total`, `_operation_duration_seconds` (label `type`) | **deleted** with HEAD's ops engine: core's `<namespace>_ops_runs_total{outcome}` and `<namespace>_ops_run_duration_seconds` carry the same information | R2 tasks flip | the dashboards' and alert rules' `operations_*`/`operation_duration` queries move to the `ops_*` families in that PR |
| `audiobook_organizer_op_items_processed`, `_op_items_total` (labels `op_id`, `op_type`) | **deleted** with HEAD's ops engine (they break §5 rule 2); core's `<namespace>_ops_items_total` replaces them | R2 tasks flip | the `audiobook_organizer:ops_items_by_op_type:sum` recording rule (`recording-rules.yml:14-19`, HEAD's `label_replace` compat rule) and its dashboard panels move to `ops_items_total` in that PR |
| `audiobook_organizer_ops_abandoned_calls` (label `source`) | books' own observable on its own scope for both `source` values (`repairs_resolver`, `retired_versions_stat`; `internal/opsmetrics/opsmetrics.go:370-371` @748fd787a), created in the PR that converts the second of its two sources (05-tasks 5T.8.1); that PR deletes `opsmetrics`' instrument | R2 tasks stream | unchanged |
| `ai_dispatch_*`, `ai_*` (unprefixed) | core `ai`'s families `<namespace>_ai_*` replace them; [05-ai](05-ai.md) §8 maps each HEAD family to its successor | R2 integrations flip | the AI dashboard and the AI alert rules move to `<namespace>_ai_*` in that PR |
| `audiobook_organizer_process_memory_alloc_bytes`, `_process_goroutines` | books' status worker, as at HEAD (`internal/server/server_lifecycle.go:432-433`), registered through `Provider.Registerer()` (item 2) | R1 | unchanged |
| `http_server_*` (semconv) | core's stage 2, for every request, with mux patterns as `http_route` (§3) | R1 (perimeter move) | none reference them |
| every other `audiobook_organizer_<x>` (books, search index, catalog, cache, ABS, review, fixer, metadata fetch, …) | books, unchanged | — | unchanged |

4. **Dashboards and rules** in `deploy/` change only in the PRs of the table above, each with its seeding rows and
   golden lines; `TestDeployReferencedSeriesServed` fails any PR that leaves a reference to a series nobody serves.
   The R2 release note lists every renamed or deleted family with its successor, and the owner reloads the rules
   and dashboards at the R2 deploy (09). The core families an app adds appear in no shipped dashboard until one is
   written for them.

## 14. Open items

1. **OTLP transport.** gRPC is used only by telemetry (`go mod why -m google.golang.org/grpc` →
   `internal/telemetry`), and it is the module that drags in the genproto split that makes `go.work` fail
   (03 §3.2). Measure whether `otlpmetrichttp`/`otlptracehttp` remove gRPC from core's `go list -deps`; if they
   do, make HTTP the only transport (wave-0 experiment, [13-toolchain](13-toolchain.md)).
2. Exemplars (trace-id on histogram buckets) once tracing has a collector; off until then.
3. Whether `core.metrics.environment` belongs in a shared `core.app` section (also used by updater and
   diagnostics).

## Objections to 03

One, against 03's text as it stands before its D10 revision: 03 §3.6 "Metric name" must say what this file uses,
namely one namespace (`app.Options.MetricNamespace`, default `mediacore`) for core series
(`<MetricNamespace>_<subsystem>_*`) and module series (`<MetricNamespace>_*` when the app ships one module,
`<MetricNamespace>_<moduleID>_*` when it ships two or more, counted over the modules the app ships, SIMP2-13),
semantic-convention names unchanged, and that names may change with the deploy files in the same PR (D10). Its
`MetricPrefix`, "ABO keeps its metric names", HEAD-golden, `otelgin`, `HTTPMetricsScope` and `MetricViews` text
goes. The app ID field is `app.Options.AppID` (COR-26).

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

Round 10:

- R10-3 / COR10-3 / MOD10-1 / MOD10-3 — M3, §3 "Tracing and HTTP" (rewritten), §13 `http_server_*` row: the legacy gin mount keeps `otelgin` as at HEAD (tracer off), the sole recorder of every request that reaches gin; core's stage 2 records only core and module routes (an alias of a HEAD route with HEAD's gin template, `Router.SPA` with no route); `httpapi.ReportLegacyRoute` deleted; one owner per series, the `http.server.*` semconv exception to `ErrFamilyTaken` (`TestFamilyTaken_SemconvHTTPTwoScopes`).
- R10-3 / MOD10-4 — §3 rule 3, M6: a request a core stage answers before the mount is recorded under core's scope with no `http_route` (disjoint from every gin series), the one 09 §9.2.1 observable change with its release note.
- R10-3 / MOD10-2 — §3 `Options.ServiceName`, §5 rule 2: `server_address` is the app constant, never `Host`; `TestStage2_ServerAddressIgnoresHost`.
- R10-3 / MOD10-5 — §3, §6, §7: stage 2 records into the served Provider at every release (`app.Options.ServedMeterProvider` at S2 r1–r2); the `http_server_*` seeding rows go through ABO's production gin engine on `MountLegacy`, source column unchanged, so no HEAD golden line changes and the test passes at S2 r2, S5 and S8.
- R10-3 / MOD10-6 (lead naming) — §3 `Options.MetricViews` (from `app.Options.MetricViews`), `Scope.View` as a pre-creation table entry, §5 rule 4, §7, §12 `TestMetricViews_InstalledAtConstruction`, §13 item 1, §6 `TestHistogramBucketsPinned` in every stage's check.
- R10-3 / MOD10-7 (R-C request) — §13 item 1 and table: `ops_abandoned_calls` changes owner in exactly one PR, at T6, for both sources.
- MOD10-11 — §3 `Options.ServiceName`, §4 Resource row, §11, §12 `TestResource_ServiceNameAndSDKDefaults`, §6 golden: `service.name` = `audiobook-organizer` merged with `resource.Default()`; `target_info` in `head_series_identity.golden`.
- COR10-8 / R10-2 — M5, §6, §6 stage table, §13 item 5: one `Router.Infra(path, "core:metrics.read", h)` mount on every app; ABO's non-confined principals hold the scope through `LegacyAmbientScopes`, confined ones never.
- Objections — new open objection: 03 §3.6's "told apart by `otel_scope_name`, as at HEAD" contradicts HEAD's `WithoutScopeInfo`.
- Header — 03 reference v1.14.0.
- integration pass (round 10): objection to 03 resolved (03 v1.14.1 §3.6 matches `WithoutScopeInfo`); §3 rule 1 and §13 `http_server_*` row name no route-report identifier. Already as requested: §13 and 5T.8.1 name the T6 PR in which `ops_abandoned_calls` changes owner for both books sources; `app.Options.MetricViews`, `ServiceName`, `ServedMeterProvider`.

Round 11:

- VER11-1 (R11-1) — M3 and §3 `ErrFamilyTaken`: R10-3's two-scope exception for the `http.server.*` families deleted; the two HTTP recorders share one instrument per family because stage 2 creates them in otelgin's scope (`app.Options.HTTPMetricsScope`) with identical identity; §8 row and §12 `TestFamilyTaken_SemconvHTTPOneInstrument` (replacing `…TwoScopes`) follow.
- VER11-1 (R11-1) — §3 rules 1–3 and "One instrument set, no scope label": the wrapper clears `r.Pattern` before gin (otelgin's fallback), so `NoRoute` and SPA loads keep no route; stage 2 records into the same instruments; the "disjoint by `http_route`" argument deleted; otelgin's chain position cited from 05-http-api §4.2, its one statement; §6 seeding wording, §12 contract-test summary and §13 `http_server_*` row restated; objections paragraph updated.

Final verification:

- FIN12-7 — §3 "One family, one owner": the recorded identity is (scope name, version, schema URL, instrument name, kind, number type, unit, description); the experiment's load-bearing details (typed attributes, exact scope version, identical description) recorded; bucket advice is not part of the identity; `TestFamilyTaken_SemconvHTTPOneInstrument` covers the full tuple.

Plan-phase consistency:

- PC-13 — §6: `TestHistogramBucketsPinned` is part of every stage's series check in every release from S1 on, S2 r1–r3 included; the stage list names where it matters most.

- D10–D12 revision (2026-11-04): M1, M3, M5, M6 rewritten: one namespace (`MetricNamespace`) for core and module series, `MetricPrefix` deleted; names may change, with the deploy files in the same PR (D10); HEAD goldens (`head_series.golden`, `head_series_identity.golden`, `TestHEADSeriesStillPresent`, `TestHistogramBucketsPinned`) and the per-stage golden-diff table deleted; `TestABOMetricsGatherNoDuplicateOrMissing` reduced to `TestABOMetricsGatherNoDuplicate`; new `TestDeployReferencedSeriesServed` (every series in `deploy/grafana` and `deploy/prometheus` is served) beside HEAD's promtool test (§6, §12); `MetricViews`, `PromRegisterer`, `Shared`, `ServedMeterProvider`, the `HTTPMetricsScope` instrument sharing and otelgin co-recording deleted: stage 2 is the only `http.server.*` recorder, out-of-scope instruments carry bucket advice (§3, §5, §7, §8, §12); §6 `/metrics`: the App's private registry served from R1 by the gin route, the kernel's `Infra` mount from the R2 metrics flip, `core:metrics.read` stored by the identity migration instead of `LegacyAmbientScopes`; §10 `OTEL_*` as canonical env bindings, ABO names in the rename table, no window aliases; §13 rewritten as the list of HEAD families that change (operations, op-items and `ai_*` families replaced by core's, `ops_abandoned_calls` hand-over choreography deleted); stage references re-keyed to R0–R2; objection to 03 §3.6 recorded.
