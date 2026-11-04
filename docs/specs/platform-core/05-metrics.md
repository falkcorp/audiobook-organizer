<!-- file: docs/specs/platform-core/05-metrics.md -->
<!-- version: 1.8.0 -->
<!-- guid: 5cdf765d-aec1-4ed7-981a-e2e698afde61 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Metrics and tracing (`core/metrics`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §4, §9;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (OTel v1.47.0, exporter
v0.69.0, client_golang v1.25.0). Normative: [03-architecture](03-architecture.md) v1.10.1 R4 (`metrics` is in
**L0** with `entity`, `lifecycle`, `version`, `log`), R5 (no `prometheus.DefaultRegisterer`, no `otel.Set*Provider`
in core except the opt-in `app.InstallProcessDefaults`; R5 binds core only), §3.6 (`mediacore_<subsystem>_*`; module series `<MetricPrefix>_*` for an app that ships one module
and `<MetricPrefix>_<moduleID>_*` otherwise (decided by the modules compiled in, not by enablement, §4), `MetricPrefix` = `app.Options.MetricPrefix`; semantic-convention names
unchanged). The compatibility window is defined once, in
[09-abo-on-core](09-abo-on-core.md) §9.2.1; metric aliases reference it.

## Decisions at a glance

| # | Decision |
|---|---|
| M1 | One `metrics.Provider` per `App` owns a `*prometheus.Registry` (private, unless the app supplies a registerer for the S2 r3–S8 bridge, §6), an OTel `MeterProvider`, a `TracerProvider` and a propagator. All are **injected**; nothing in core names `prometheus.DefaultRegisterer`/`DefaultGatherer` or calls `otel.Set*`/`otel.Get*`, except that `app.InstallProcessDefaults` sets the three OTel globals to this Provider's values for an app that opts in ([05-logging](05-logging.md) §3, MOD4-3). Components receive a `*metrics.Scope`; only the kernel holds the `*Provider`. There is no `metrics.Registry` type. |
| M2 | The authoring API is the **OTel metric API** (`metric.Meter`), continuing ABO's in-flight migration (client_golang family ceiling 64 and falling, `internal/telemetry/testdata/client_golang_baseline.txt`). Prometheus is an exporter bound to the App's registry. client_golang is used directly only inside `core/metrics` (registry, Go/process collectors, promhttp) and through one escape hatch, `Provider.Registerer()`, for third-party and legacy collectors. |
| M3 | Names: core instruments `mediacore.<subsystem>.<what>` → `mediacore_<subsystem>_<what>[_unit][_total]`. Module instruments use the app's **metric prefix** `app.Options.MetricPrefix` (ABO: `audiobook_organizer`): `<prefix>.<what>` when the app **ships** one module, `<prefix>.<module>.<what>` when it ships two or more. "Ships" means the module list passed to `app.New`, which never changes at run time, so enabling or disabling a module never renames another module's series (SIMP2-13). ABO's books series therefore keep their exact names. The Prometheus translation strategy is **pinned** (`UnderscoreEscapingWithSuffixes`), not left to the exporter's process-global default. OTel semantic-convention instruments (`http.server.*`) keep their standard names. |
| M4 | Cardinality is a contract: closed attribute-key allow-list per scope, bounded value sets only, no identifiers; SDK per-instrument limit 2000; a series golden per scope. |
| M5 | `/metrics` is a core route mounted by the kernel through `Router.Infra` with permission `core:metrics.read`, which the chain **enforces** (stages 10–13 run for an `Infra` route whose permission is not Public, SEC2-14), outside response compression. For ABO the kernel mounts it at 09 S8; until then ABO's legacy gin route serves it (from S2 r3 `Provider.Handler()` over the default registry), with HEAD's any-credential check until S4 r2 and `core:metrics.read` enforced by stage 13 from S4 r2 (§6; OPS5-5, SEC5-13). |
| M6 | **Books metric names are unchanged: no rename and no alias layer for them.** Only series that move into core subsystems (tasks, storage, AI) are renamed to `mediacore_*`, and the two process gauges that the core Go collector replaces are served by its runtime families (`metrics.RuntimeFamilies()`, §3; OPS6-2); for those, the old names keep working through **gather-time aliases** (no double instrumentation) registered in 09's compat ledger and removed only by a contract PR, plus shipped recording rules for operators who need longer. |
| M7 | Spans are redacted before export with the logging pipeline's `*log.Redactor` (credential query values, invite paths, userinfo), so `apikey`/`token` values never reach a trace backend (SEC-22). |

## 1. Purpose & scope

Give every `App` isolated telemetry: a Prometheus scrape endpoint, optional OTLP metric push, optional OTLP
traces, consistent naming, enforced cardinality, span redaction, and a compatibility bridge for ABO's series
that move into core subsystems. Out of scope: what each subsystem measures (each 05-* file tables its own
metrics), log export ([05-logging](05-logging.md)), health/readiness ([05-health](05-health.md)).

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

Package `github.com/falkcorp/mediacore/metrics` (L0). Imports: `prometheus/client_golang` (`prometheus`,
`collectors`, `promhttp`), OTel API (`metric`, `trace`, `propagation`, `attribute`), OTel SDK (`sdk/metric`,
`sdk/trace`, `sdk/resource`), exporters (`exporters/prometheus`, `otlpmetricgrpc`, `otlptracegrpc`),
`github.com/prometheus/otlptranslator`, and `github.com/falkcorp/mediacore/log` (same layer, L0; for
`*log.Redactor` only — `log` does not import `metrics`). No other core package (R4).

```go
package metrics

type Options struct {
	AppID            string   // app.Options.AppID, e.g. "abo"; service.name default
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
	// §6): the exporter, the core collectors and the alias source register there, Handler gathers from it (it
	// must also implement prometheus.Gatherer, as a *prometheus.Registry does; New fails otherwise), and the Go
	// and process collectors are not added (the caller's registry has its own). ABO passes
	// prometheus.DefaultRegisterer from S2 r3 (the release that deletes its façade's exporter; nil, private and
	// unserved, in S2 r1 and r2, OPS5-14) and stops at S8. nil = a private registry with the Go and process
	// collectors (every other app).
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

func (p *Provider) MeterProvider() metric.MeterProvider
func (p *Provider) TracerProvider() trace.TracerProvider
func (p *Provider) Propagator() propagation.TextMapPropagator // TraceContext + Baggage
func (p *Provider) Registerer() prometheus.Registerer          // escape hatch for third-party and legacy collectors
func (p *Provider) Gatherer() prometheus.Gatherer
func (p *Provider) Handler() http.Handler                       // /metrics: promhttp on the App registry + aliases
func (p *Provider) Shutdown(ctx context.Context) error           // flush OTLP, stop readers; idempotent

// Scopes enforce naming. Core subsystems get "mediacore.<subsystem>"; modules get MetricPrefix
// (the app ships one module) or "<MetricPrefix>.<module>" (it ships two or more).
func (p *Provider) Core(subsystem string, allowed ...attribute.Key) (*Scope, error)
func (p *Provider) Module(id string, allowed ...attribute.Key) (*Scope, error)

// Scope is what components and modules receive (module.Deps.Metrics, constructor params): *metrics.Scope.
type Scope struct{ /* unexported */ }

func (s *Scope) Prefix() string                 // "mediacore.tasks", "audiobook_organizer"
func (s *Scope) Meter() metric.Meter            // validating wrapper over the App's MeterProvider
func (s *Scope) Tracer() trace.Tracer           // tracer named "<prefix>"
func (s *Scope) Propagator() propagation.TextMapPropagator
func (s *Scope) Allowed() []attribute.Key       // declared keys; asserted by metricstest
func (s *Scope) View(instrument string, buckets ...float64) sdkmetric.View // histogram bucket declaration

// Legacy aliases for series that moved into a core subsystem. Applied at gather time by Handler: matching
// families are copied under the old name with label renames, value maps and an optional label filter.
// No instrument is recorded twice. Never used for module (books) series (M6). New must be a mediacore_* family
// or one of RuntimeFamilies() (OPS6-2); anything else is ErrAliasNotCore. An Alias carries no class
// and no dates: its gate and removal dates come from its ledger row (09 §9.2.9, SIMP2-12). The aliases name
// an app's legacy families, so the app supplies them (app.Options.MetricAliases; core holds no ABO name, R1)
// and their rows are in the app's ledger (app.Options.CompatLedger, SIMP3-3).
type Alias struct {
	New         string                           // Prometheus family name as exported now
	Old         string                           // legacy family name
	Filter      map[string]string                // only series of New with these label values (e.g. outcome=completed)
	DropLabels  []string                         // labels removed after filtering
	LabelRename map[string]string                // new label -> old label
	ValueMap    func(label, value string) string // e.g. task kind "books.library-scan" -> def_id "library-scan"
	Ledger      string                           // its row in the app's ledger (app.Options.CompatLedger, 09 §9.2.9)
}

func (p *Provider) AddAliases(a ...Alias) error

// RuntimeFamilies returns, as a fresh slice (R5: no exported slice), the families of the Go runtime collector that
// core owns and that an Alias may target besides mediacore_*: "go_goroutines" and "go_memstats_alloc_bytes". They
// replace ABO's two process gauges at S8 (§13 item 3). The list grows only by a core PR, never from an app.
func RuntimeFamilies() []string

var (
	ErrInstrumentName = errors.New("metrics: instrument name violates naming rules")
	ErrScopeTaken     = errors.New("metrics: scope already registered")
	ErrAliasConflict  = errors.New("metrics: alias collides with a live family")
	ErrAliasNotCore   = errors.New("metrics: alias source is neither a core-subsystem nor a core runtime family")
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
verified at exporter v0.69.0 (`config.go:67,120,170`); `reg` is the private registry or `Options.PromRegisterer`.
The private registry also gets
`collectors.NewGoCollector()` and `collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})`
explicitly (the default registry had them implicitly; ABO's memory alert reads
`process_resident_memory_bytes`). `Handler` wraps `promhttp.HandlerFor(gatherer, opts)` in
`promhttp.InstrumentMetricHandler(reg, …)` so `promhttp_metric_handler_*` series also survive the move.

**Tracing and HTTP.** `httpapi` instruments with `otelhttp` using `WithTracerProvider`,
`WithMeterProvider` and `WithPropagators` from the Provider — never the globals. The semconv metric names
(`http_server_request_duration_seconds`, request/response body size) are identical to what `otelgin`
emits today (series golden), so moving off gin does not change them.

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
| Core subsystem | `mediacore.<subsystem>.<what>` | `mediacore_<subsystem>_<what>[_<unit>][_total]` | `mediacore.tasks.runs` → `mediacore_tasks_runs_total` |
| Module, app ships one module | `<prefix>.<what>` | `<prefix>_<what>…` | `audiobook_organizer.books` → `audiobook_organizer_books_total` (today's name, unchanged) |
| Module, app ships two or more | `<prefix>.<module>.<what>` | `<prefix>_<module>_<what>…` | `suite.tv.scan.files` → `suite_tv_scan_files_total` |
| Semconv | spec name | translated | `http.server.request.duration` → `http_server_request_duration_seconds` |
| Resource | — | `target_info{service_name,service_version,service_instance_id,deployment_environment}` | — |

Core metric names are identical across ABO, TV and Movies, so one set of core dashboards serves all apps
(distinguished by `job`/`service_name`). Module names are per app. The single-module rule exists because
inserting `books_` disambiguates nothing in an app whose prefix is already app-specific, while it would cost
aliases, recording rules and dashboard edits for 74 series (SIMP-13). **Trade:** if ABO's books module ever shares
a binary with other modules, its series must be renamed then; that is a row of the 11 §11.5 checklist.
`Provider.Module` computes the prefix from `Options.Modules`, the modules compiled into the app, so the rule is
mechanical and the segment changes only when a release changes the module list. Keying it on the *enabled*
modules would rename every series of the remaining module when an operator disables one at the next restart,
breaking dashboards (SIMP2-13).

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

The kernel mounts `Provider.Handler()` at `core.metrics.path` (default `/metrics`, outside `/api`) with
`Router.Infra(path, "core:metrics.read", h)` ([05-http-api](05-http-api.md) §3.5) — permission
**`core:metrics.read`** (R6: explicit). `Infra` routes are inside the chain's authenticated scope: for an `Infra`
route whose permission is not Public, stages 10–13 (authentication through permission) run exactly as for a
native route, so the permission is enforced, not merely recorded; only the probes `/livez` and `/readyz` stay
Public (SEC2-14; HEAD requires an authenticated credential, `internal/server/server_lifecycle.go:1735-1747`,
and `TestInfra_MetricsRequiresScope` in 05-http-api asserts the same after the move). It is **excluded from compression middleware**
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
| S2 r1–r2 | the App's Provider uses a **private, unserved** registry (`PromRegisterer` nil): the S1 façade's exporter is still installed on the default registry, and two exporters on one registry could collide on `target_info` and fail the scrape (OPS5-14). Trade: the kernel's own series (e.g. `mediacore_kernel_boot_phase_seconds`) are not scraped until r3 | gin, `promhttp.Handler()`, as at S1 | default registry, unchanged |
| S2 r3–S7 | `Options.PromRegisterer` = the default registry (ABO passes `app.Options.PromRegisterer`, 06, in the same release that deletes the façade's exporter); `app.InstallProcessDefaults` points the OTel globals at the App's providers, so the six global-reading ABO packages keep their series | ABO's **legacy gin route** under `MountLegacy`, unchanged in path and compression exclusion, whose handler becomes `Provider.Handler()` (gathering that registry plus the gather-time aliases of core-renamed series, which therefore work from the stage that renames them). Credential check: HEAD's any-credential check until S4 r2; **from S4 r2** the route's legacy manifest entry changes from `AnyAuthenticated` to `core:metrics.read`, so stage 13 enforces it, as 09 §9.2.6 records (OPS5-5, SEC5-13; `TestLegacyMetricsRoute_RequiresScopeFromS4`) | default registry, unchanged |
| S8 on | the App's private registry (`PromRegisterer` nil; the `app.Options` field is removed, 06) | the kernel: `Router.Infra(core.metrics.path, "core:metrics.read", Provider.Handler())`; the legacy gin route is removed in the same PR | moved to `Provider.Registerer()` in the same PR; `cache.go:99` and `cache_snapshotter.go:24` take `Provider.Gatherer()` |

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
The PR that renames a family adds the new family's seeding row and the alias's expected family to the golden.
**What it scrapes (OPS7-1).** HEAD's test scrapes `promhttp.Handler()` on the default registry
(`internal/telemetry/contract/series_contract_test.go:451-456` @748fd787a), which never carries the gather-time
aliases (they exist only in `Provider.Handler()`) and loses every books family at S8, when they move to the
private registry. So:

- **From S2 r3** the test builds the same `metrics.Provider` the kernel builds at that stage, through the public
  test constructor `coretest.MetricsProvider` (06 `coretest`; it applies the kernel's `metrics.Options` defaults,
  the stage's `PromRegisterer` and `AddAliases` exactly as §7 steps 1 and 3 do), passing ABO's
  metric-alias rows from the same table ABO's `main` passes as `app.Options.MetricAliases`. It scrapes that
  Provider's `Handler()`, never `promhttp.Handler()`. Before S2 r3 (HEAD–S2 r2) the default registry is the
  served one (table above) and the test keeps HEAD's scrape.
- **The S2 r3 PR adds `mediacore_` to `inScopePrefixes`** (today `audiobook_organizer_`, `ai_dispatch_`, `ai_`,
  `http_server_`, `series_contract_test.go:69-71`), so the kernel's and every renamed core family enter the
  comparison.
- **Seeding rows for core-owned emitters go through public SDK paths**, because ABO cannot import
  `mediacore/internal/...`: the Pebble collector through a store opened with `coretest`/`storage`, the tasks
  instruments through one `hello.count` run, the AI instruments through a fake endpoint.
- **The kernel's own families are not in ABO's golden (OPS8-10).** `mediacore_kernel_boot_phase_seconds`
  ([06](06-app-sdk.md) §6.9), `mediacore_compat_*` (S2b), `mediacore_storage_level` and
  `mediacore_storage_checkpoint_bytes` are recorded by kernel code during a boot, never by the Provider, and
  `coretest.MetricsProvider` builds the Provider without booting, so ABO's test records no sample for them and they
  export nothing there. A second, booted Provider in the same test cannot be added instead: on the default registry
  it would collide with the first on `target_info` (OPS5-14). So core asserts them in its own seeded test,
  `TestKernelFamiliesGathered` (a `kernel` test, §12): one zero-module `coretest` boot (G7's harness) with
  `PromRegisterer` set to a fresh registry, scraping that App's `Provider.Handler()` and comparing the kernel
  families of each stage with core's golden.
- Because the test builds the stage's Provider, **at S8 its registry becomes the private one in the same PR** that
  makes the App's registry private.
- `TestContractScrapesServedHandler` asserts that the handler under test is the one the `/metrics` mount serves at
  that stage (the legacy gin route's handler from S2 r3 to S7, the kernel's `Router.Infra` mount from S8).

The stage's done-check is the golden diff that PR shows:

| Stage | Golden diff of the seeded contract test |
|---|---|
| S1, S2 r1, S2 r2 | empty |
| S2 r3 | empty in ABO's golden; the kernel's own `mediacore_*` families, now gathered, are asserted by core's `TestKernelFamiliesGathered` (OPS8-10) |
| S2b (Pebble), S5 (tasks/ops, T0+T1), the T7 PR (schedule lag and missed), S7 (AI) | only the families renamed in that stage: each new `mediacore_*` family added, and its alias family present under the old name; the alias's ledger row and the `deploy/` dashboard and rule switch ship in the same PR (§13 item 4). At S5 the five `operations_*` families keep their golden lines, now produced by their aliases: the T0+T1 PR deletes their client_golang vectors and replaces their seeding rows with alias rows (§13 item 2, OPS8-1) |
| S8 | only `mediacore_build_info` (09 S8; labelled with the binary's version, never the UI bundle's build ID, R5-7) and the listed additions of that PR. The two retired process gauges keep their golden lines, now produced by their aliases over the Go collector's families: the same PR deletes the gauges and replaces their two seeding rows with alias rows (§13 item 3, OPS7-5). Every aliased family is still present |

The Go collector's `go_goroutines` and `go_memstats_alloc_bytes` are not S8 additions: HEAD's `/metrics` serves
`promhttp.Handler()` over the default registry, which already carries the Go collector
(`internal/server/server_lifecycle.go:1746-1747` @748fd787a), so every stage's scrape has them (OPS6-2).

**Smoke check: the live scrape.** On each stage's deployment a credentialed scrape (a credential that holds
`core:metrics.read`; HEAD: any credential) must answer 200 with a non-empty family list (comment lines dropped,
label part cut, sorted unique; an unauthenticated scrape gets 401 and would compare nothing). That list must
contain a short fixed set of always-on families: `process_resident_memory_bytes` and `go_goroutines`, plus
`mediacore_build_info` from S8. It is never compared with another build's scrape.

`health` serves no `/metrics` ([05-health](05-health.md) §5H.11 item 3).


## 7. Lifecycle

1. `app.New`, right after logging: `metrics.New` with options from `core.metrics` / `core.tracing` (operator
   layers and defaults), `Options.Redactor = Logging.Redactor()`, `Options.Modules` = the IDs of every module
   passed to `app.New`, enabled or not (§4), and `Options.PromRegisterer` from `app.Options.PromRegisterer` (06;
   ABO's S2 r3–S8 bridge, nil at S2 r1–r2, §6). If the app's `main` calls `app.InstallProcessDefaults`, the OTel globals point at this
   Provider from then on ([05-logging](05-logging.md) §3).
   OTLP exporters are built non-blocking; a bad OTLP endpoint is **degraded, not fatal** (ABO learned this the
   hard way, `telemetry.go:52-58`): health token `metrics_otlp_disabled` / `tracing_disabled`.
2. Every component receives a `*Scope` (`module.Deps.Metrics` for modules, [06-app-sdk](06-app-sdk.md); core
   services through their constructors). Instruments are created at construction; a naming error fails boot.
3. The kernel installs the app's legacy aliases for core-subsystem renames (`app.Options.MetricAliases`, §13)
   before listeners open, and registers each with `compat` against its row in the app's ledger
   (`app.Options.CompatLedger`; `compat` is L4; the kernel does the wiring). A row missing from the ledger fails
   registration (`ErrNoLedgerRow`, 09 §9.2.9).
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
| Alias whose `New` is neither a `mediacore_*` family nor one of `RuntimeFamilies()` | fail-closed (`ErrAliasNotCore`) | books names are unchanged; aliases exist only for core renames and the core runtime collector (M6, OPS6-2) |
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
would require swapping providers under running instruments, which the OTel SDK does not support. There is no
`core.metrics.legacy_aliases` switch: alias removal is a contract PR under 09's ledger, never a runtime flag
(09 §9.2.8). All keys here are operational dials. The `OTEL_*` aliases are the only rows of core's ledger
fragment (permanent; 05-config §1.3.6, SIMP4-10). The ABO names in the table (`otel_*`, `telemetry_environment`)
are window aliases ABO supplies through its legacy alias table (`app.Options.LegacyConfig`, 05-config §1.3.3),
with rows in ABO's ledger; core holds none of them (R1).

## 11. Metrics (of this subsystem)

| Name | Type | Labels |
|---|---|---|
| `mediacore_metrics_otlp_export_failures_total` | counter | `signal` ∈ metrics, traces |
| `mediacore_metrics_legacy_alias_families` | gauge | — |
| `mediacore_metrics_span_redactions_total` | counter | `kind` ∈ key, url, path |
| `promhttp_metric_handler_requests_total` | counter | `code` (client_golang standard) |
| `promhttp_metric_handler_errors_total` | counter | `cause` (client_golang standard) |
| `target_info` | gauge | resource attributes |

## 12. Tests (core must have)

| Test | Asserts |
|---|---|
| `TestNoGlobals` (archtest, R5) | no `prometheus.DefaultRegisterer/DefaultGatherer/MustRegister/Register`, `promhttp.Handler()`, `otel.Set*`/`otel.Get*Provider`/`otel.Tracer`/`otel.Meter` in core, except `otel.SetMeterProvider`, `otel.SetTracerProvider` and `otel.SetTextMapPropagator` inside `app.InstallProcessDefaults` (05-logging §10) |
| `TestLegacyMetricsRoute_RequiresScopeFromS4` (ABO, with the legacy manifest) | from S4 r2 a scoped key without `core:metrics.read` gets 403 on `/metrics` and a key or session holding it gets 200; before S4 r2 any credential gets 200, as at HEAD (OPS5-5) |
| `TestProvider_ExternalRegisterer` | with `Options.PromRegisterer` set to a fresh registry that already holds Go and process collectors: no duplicate-registration error, the exporter's families and the aliases appear in `Handler()`'s scrape, nothing lands in a private registry; with it nil the private registry; a registerer that is not a `prometheus.Gatherer` fails `New` carries the Go and process collectors (MOD4-3) |
| `TestTwoProvidersIsolated` | two Providers register identical instruments without panic; scrapes are disjoint |
| `TestScope_RejectsBadNames` | prefix, `_total` suffix, missing unit/view → `ErrInstrumentName` |
| `TestModulePrefix_SingleAndMulti` | one compiled-in module → scope prefix `MetricPrefix`; two → `<MetricPrefix>.<module>` |
| `TestModulePrefix_StableUnderEnablement` | two compiled-in modules, one disabled: the other's prefix is still `<MetricPrefix>.<module>`; series names identical before and after the toggle |
| `TestTranslationStrategyPinned` | names identical with the global validation scheme set to legacy and to utf8 |
| `TestGoAndProcessCollectorsPresent` | `go_goroutines`, `process_resident_memory_bytes` in the scrape |
| `TestAliases_GatherTimeCopy` | old family present with filter, renamed labels and mapped values; no double recording; collision rejected; a source that is neither `mediacore_*` nor in `RuntimeFamilies()` (e.g. a books family) rejected with `ErrAliasNotCore` |
| `TestAlias_GoCollectorTargetAccepted` | on the private registry, aliases `audiobook_organizer_process_goroutines` ← `go_goroutines` and `audiobook_organizer_process_memory_alloc_bytes` ← `go_memstats_alloc_bytes` are accepted by `AddAliases` and both old names appear in `Handler()`'s scrape; `RuntimeFamilies()` returns a fresh slice (OPS6-2) |
| `TestTrace_NoApikeyInSpanAttributes` | in-memory exporter behind the redacting wrapper: request `?apikey=…&token=…` and `/api/core/v1/auth/invites/<t>` → no secret in span name or attributes |
| `TestHandler_NoDoubleGzip` | `Accept-Encoding: gzip` through the httpapi stack yields one gzip layer |
| `TestCardinality_OverflowFailsMetricstest` | exceeding the limit is detected |
| `TestOTLPBadEndpointDegrades` | provider builds, health token set |
| `TestShutdownFlushes` | `testing/synctest` + in-memory exporter: last batch exported |
| `metricstest.AssertSeries` (per scope) | series golden for each core scope |
| `TestKernelFamiliesGathered` (a `kernel` test) | one zero-module `coretest` boot with `PromRegisterer` set to a fresh registry: the scrape of that App's `Provider.Handler()` carries every kernel-emitted family of the stage (`mediacore_kernel_boot_phase_seconds` from S2 r3; `mediacore_compat_*`, `mediacore_storage_level`, `mediacore_storage_checkpoint_bytes` from the stage that lands each), matching core's golden (OPS8-10) |

ABO keeps its own series contract test (HEAD's `internal/telemetry/contract`, `internal/telemetrypolicy` from S1);
from S2 r3 it scrapes the served `Provider.Handler()` of a Provider built through `coretest.MetricsProvider`, with
`mediacore_` in scope (§6, OPS7-1), and its 74 `audiobook_organizer_*` lines change only where §13 retires or moves
a series into core. Its seeded golden is the normative series check of every stage (§6, OPS6-3). Two ABO tests go
with it: `TestContractScrapesServedHandler` (§6), `TestS5_FullAliasSetBoots` (ABO's whole S5 metric-alias table plus
every legacy collector `metrics.Register()` still registers at S5, on the S5 Provider → `AddAliases` returns nil;
OPS8-1) and `TestS8_FullAliasSetBoots` (the same at S8, on the S8 Provider; OPS7-5). Each makes sure no alias
collides with a live family, so the stage's build boots. Core's own `TestKernelFamiliesGathered` covers the
kernel-emitted families (§6, OPS8-10).

## 13. Migration from ABO

1. **Injection first, installs last (MOD3-3, MOD4-3, MOD4-4).** `internal/telemetry` splits per file:
   - **to core `metrics` at S1:** provider construction, the endpoint validator, the exporter wiring and the
     resource. Core only **returns** providers (R5).
   - **stays in ABO permanently, as books telemetry** (the `internal/telemetry` path kept, or renamed under
     `internal/books`): `attr.go` (the ~30 attribute-key constants: `def_id`, `plugin`, `fixer`, `op_id`, …),
     `views.go` (the bucket table keyed by ABO family names) and `Views()`/`HistogramBuckets()`, the `meter.go`
     scope helper (`Meter(name)`, `MeterFrom`) and `SetVersion`/`Version`. They feed `Scope.View`,
     `Scope.Allowed` and `Options.ServiceVersion` at registration, so no ABO vocabulary enters core (R1).
     `LoadConfig`/`WithMetricsOTLP`/`WithEnvironment`/`ParseMetricsInterval`/`Config` stay in ABO too; from S2
     the kernel builds `metrics.Options` from `core.metrics`/`core.tracing` instead (the millisecond rule of
     `ParseMetricsInterval` is the `OTEL_METRIC_EXPORT_INTERVAL` alias's `Convert`, §10), and they are deleted
     when their last importer is gone (04 §8.2 lists which importers rewire when).
   - **the process-global installs** (`otel.SetTracerProvider`, `telemetry.go:287`; `otel.SetMeterProvider`,
     `:461`; `otel.SetErrorHandler`, `errorhandler.go:121`; `otel.SetLogger`, `sdklog.go:39`; the propagator;
     the default-registry Prometheus exporter) stay in the ABO façade at S1, which installs the core providers,
     so the six ABO packages that still create instruments through the globals (`internal/ai/telemetry.go`,
     `internal/aidispatch/metrics.go`, `internal/database/memdb_metrics.go`, `internal/dedup/engine.go`,
     `internal/operations/registry/worker.go`, `internal/opsmetrics/opsmetrics.go`) keep their series and S1's
     series golden holds. **At S2 r3** `app.InstallProcessDefaults` ([05-logging](05-logging.md) §3) takes over
     the OTel installs and the exporter registers on `Options.PromRegisterer` = the default registry (§6); only
     the façade's **install code** is deleted, in that same release (OPS5-14). 04 lists this as a re-implementation, not a verbatim move.

   The three `otel.Tracer` package vars, `otel.GetMeterProvider()` calls and `opsmetrics.Default()` become injected
   scopes when touched. `opsmetrics`' core successor is written fresh in one place, `mediacore/internal/executor`
   ([05-tasks](05-tasks.md)), on scope `mediacore.tasks`; ABO's `internal/opsmetrics` stays in ABO, as opsbridge-side
   code, until its last ABO user is gone (resolution R8-6, MOD8-2). Books' own gauges register through books' own
   `metrics.Scope`, never through core instrumentation with ABO names. The series names survive by one mechanism
   only: the gather-time alias rows of item 3. At S8 `cache.go:99` and `cache_snapshotter.go:24` take `Provider.Gatherer()` (§6).
2. **client_golang families** in `internal/metrics` finish the move to OTel when convenient (the ratchet continues
   to zero). They stay on the default registry, which the App shares from S2 r3 to S8 (§6), and move to
   `Provider.Registerer()` with their current names in the S8 PR that makes the registry private, with two
   exceptions, each deleted in the PR that adds its aliases so that no alias meets a live family of its name (§8):
   - **the five `operations_*` families, in the S5 T0+T1 PR (OPS8-1).** That PR deletes the vectors
     `operationStarted`, `operationCompleted`, `operationFailed`, `operationCanceled` and `operationDuration`
     (`internal/metrics/metrics.go:20-47` @748fd787a, registered in `Register`'s `MustRegister` at `:300-310`), their
     helpers `IncOperationStarted`, `IncOperationCompleted`, `IncOperationFailed`, `IncOperationCanceled` and
     `ObserveOperationDuration` (`:351-358`), and their calls in the v2 worker
     (`internal/operations/registry/worker.go:611,1204,1293-1297`), which stays at its path as opsbridge until T8; it
     replaces their seeding rows with alias rows over the tasks family (item 3). Left registered, they would make
     `AddAliases` refuse the S5 aliases with `ErrAliasConflict`, and the S5 flip build would not boot
     (`TestS5_FullAliasSetBoots`);
   - **the two process gauges, in the S8 PR** (item 3, OPS7-5).

   `metrics.Register`'s `sync.Once` global disappears in the S8 PR.
3. **Names.** The books module scope's prefix is `audiobook_organizer` (single-module rule, M3), so every books
   series keeps its exact name and needs no alias. Only series that move into a core subsystem are renamed; their
   aliases are ledger rows of 09 §9.2.9 and are removed by a contract PR after 09's window, never by a clock.
   Each family is renamed in the stage that moves its emitter, with its alias in the same PR (OPS5-6); the
   moved emitter never keeps the old name in between, and each alias's window W is counted from its own
   release (09 §9.6):

| ABO family (today) | New family | Stage | Alias? |
|---|---|---|---|
| `audiobook_organizer_pebble_*` | `mediacore_storage_pebble_*` | S2b (core `storage` lands; the collector moves) | yes |
| `audiobook_organizer_ops_runs_total`, `_ops_run_duration_seconds`, `_ops_items_total`, `_ops_inflight`, `_ops_progress_age_seconds`, `_ops_progress_timeout_seconds`, `_ops_zombies` (label `def_id`) | `mediacore_tasks_*` (label `kind`, values `books.<def>`) | S5 (T0+T1; the executor's instruments in `mediacore/internal/executor` emit them, item 1, R8-6). The same PR deletes the ABO `opsmetrics` instruments whose only recorder was the v2 engine (`ops.runs`, `ops.run.duration`, `ops.items`, `ops.inflight`, `ops.progress_age`, `ops.progress_timeout`, `ops.zombies`; `opsmetrics.go:216-250` @748fd787a), so no alias meets a live family (05-tasks 5T.11) | yes: name + `kind`→`def_id` + value map from the task-kind alias table (05-tasks) |
| `audiobook_organizer_ops_schedule_lag_seconds`, `_ops_schedule_missed_total` (label `def_id`) | `mediacore_tasks_schedule_lag_seconds`, `_schedule_missed_total` | **T7**: their ABO recorder, the v3 ticker (`v3_schedule.go:444,447` @748fd787a), lives on until then and keeps the ABO emission; the T7 PR deletes the two instruments and adds their aliases (05-tasks 5T.11) | yes, from T7: as the row above |
| `audiobook_organizer_operations_{started,completed,failed,canceled}_total`, `_operation_duration_seconds` (label `type`) | superseded by the tasks family | S5 (the T0+T1 PR deletes the five client_golang vectors in the same PR, item 2, OPS8-1) | yes: one alias per family (dashboards and alerts use them). The four counters `Filter` `mediacore_tasks_runs_total` by outcome; `_operation_duration_seconds` is an alias of the histogram `mediacore_tasks_run_duration_seconds`, never of the runs counter, from which no histogram can be derived; both map `kind`→`type` with the task-kind alias table (05-tasks), and the duration alias carries the tasks histogram's buckets (`TestS5_FullAliasSetBoots`) |
| `audiobook_organizer_op_items_processed`, `_op_items_total` (label `op_id`) | **retired** (forbidden per-run label; progress is served by the tasks API/realtime) | S5 | no alias; kept emitting by the books module's legacy collector for the window under a dated exemption, then deleted |
| `ai_dispatch_*`, `ai_*` (unprefixed legacy) | `mediacore_ai_dispatch_*`, `mediacore_ai_*` | S7 ([05-ai](05-ai.md)) | yes |
| `audiobook_organizer_process_memory_alloc_bytes`, `_process_goroutines` | `go_memstats_alloc_bytes`, `go_goroutines` (Go collector; both in `RuntimeFamilies()`, so `AddAliases` accepts them, OPS6-2; present in every scrape since HEAD) | S8 (private registry with its own Go collector). **The same PR deletes the two gauges** (OPS7-5): `memoryAllocGauge`, `goroutinesGauge`, `SetMemoryAlloc` and `SetGoroutines` (`internal/metrics/metrics.go:188-197,302-303,422-423` @748fd787a) and the two calls in the books status worker (HEAD's ticker, `internal/server/server_lifecycle.go:432-433`); the `books.system.status-ticked.v1` payload keeps its `memory_alloc` and `goroutines` fields, read from `runtime`; the gauges' seeding rows become alias rows. Moved with the other legacy families, they would be live on the private registry and `AddAliases` would refuse the aliases with the same names (§8), so the S8 build would not boot (`TestS8_FullAliasSetBoots`) | yes |
| `http_server_*` (semconv) | unchanged | — | not needed |
| every other `audiobook_organizer_<x>` (books, search index, catalog, cache, ABS, review, fixer, metadata fetch, …), including `audiobook_organizer_ops_abandoned_calls`, which books keeps under its own name through its own observable on its own scope (05-tasks 5T.8.1, 5T.11; R8-6) | **unchanged** | — | none (no rename, no alias layer) |

   The legacy-use gate of 09 §9.2.1 (c) cannot be measured for metric aliases — a scrape reads every family, so
   the server cannot tell which name a dashboard uses; their ledger rows are marked accordingly in 09.
4. **Dashboards and rules** in `deploy/` switch to the new names of the core-subsystem series in the same change
   that adds the aliases, which is the PR of the stage that renames them (item 3) (so the repo's own dashboards never depend on aliases); books series in them are
   untouched. `deploy/prometheus/recording-rules.yml` gains a generated `audiobook_organizer_compat` block mapping
   new → old for the renamed core series only, for operators with external dashboards beyond the window (the file
   already uses this technique, `recording-rules.yml:14-19`).
5. ABO's `/metrics` behaviour (any authenticated credential) is kept for signed-in roles: `admin` holds
   `core:metrics.read` through `core:admin`, and [05-rbac](05-rbac.md) §4.8 seeds it to `editor` and `viewer`.
   It is **never** seeded to `fp-worker` or any role holding a `Confining` scope, which would stop being
   confined (SEC-3, SEC3-13); a fingerprint worker has no scrape access. The seed is native-only data: under
   R4-1 the books legacy role store keeps it in its books-owned extension family, never as an extra string in
   a legacy role row (an extra string there breaks HEAD's worker-only confinement on rollback, SEC4-3;
   [05-rbac](05-rbac.md)). One behaviour change,
   recorded in 09 §9.2.6 and live **from S4 r2** on ABO's legacy route (§6): an API key with explicit scopes reads
   `/metrics` only if its scopes include `core:metrics.read`; existing keys are never widened to include it (HEAD lets any credential scrape,
   `internal/server/server_lifecycle.go:1735-1747`).

## 14. Open items

1. **OTLP transport.** gRPC is used only by telemetry (`go mod why -m google.golang.org/grpc` →
   `internal/telemetry`), and it is the module that drags in the genproto split that makes `go.work` fail
   (03 §3.2). Measure whether `otlpmetrichttp`/`otlptracehttp` remove gRPC from core's `go list -deps`; if they
   do, make HTTP the only transport (wave-0 experiment, [13-toolchain](13-toolchain.md)).
2. Exemplars (trace-id on histogram buckets) once tracing has a collector; off until then.
3. Whether `core.metrics.environment` belongs in a shared `core.app` section (also used by updater and
   diagnostics).

## Objections to 03

None open. SIMP2-13's objection to the 03 §3.6 Metric row is closed: 03 now says "while the app **ships** one
module", counted over the `app.New` module list. 03 adopts the module metric rule of this file (`app.Options.MetricPrefix`, default `AppID`, ABO
`audiobook_organizer`) and keeps semantic-convention names; the app ID field is `app.Options.AppID` (COR-26).

## Out-of-scope defects found

None beyond those listed in [05-logging](05-logging.md) and [05-config](05-config.md).

## Review history

- SIMP-13 — M3, M6, §4, §13: books metric names unchanged, no alias layer for them; single-module prefix rule; core renames only.
- SIMP-7 — M6, §3 `Alias.Ledger`, §7, §10: aliases are 09 ledger rows, removed by contract PR; `RemoveIn` and `core.metrics.legacy_aliases` deleted; usage-gate note for 09.
- SEC-22 — M7, §3 span redaction via redacting exporter with `*log.Redactor`; `TestTrace_NoApikeyInSpanAttributes`.
- SEC-6 — §10 Protection column; endpoints, path and listen are `ProtectAdmin`.
- COR-20 — M1, §3: no `metrics.Registry`; components take `*metrics.Scope`, kernel holds `*Provider`.
- COR-26 — §3 `Options.AppID` = `app.Options.AppID`, `MetricPrefix` = `app.Options.MetricPrefix` (03 v1.3.0 §3.6); objection closed.
- COR-19 — header: R4 v1.3.0 L0; `log` import is same-layer.
- OPS-17 — §10: no runtime alias switch; keys are operational dials.

Round 2:

- SIMP2-13 — header, M3, §3 `Options.Modules`, §4, §7: the module segment is decided by the modules compiled into the app, never the enabled set; tests; new objection to 03 §3.6 wording.
- SEC2-14 — M5, §6: `/metrics` mounted with `Router.Infra(path, "core:metrics.read", h)`, inside the chain's authenticated scope (stages 10–13 enforce it); probes stay Public.
- SIMP2-12 — §3 `Alias`: no class or dates; gate and dates come from the ledger row (already `Ledger`-only; comment made explicit).
- OPS2-21 (alignment with 05-config §1.12.4) — §10: `OTEL_METRIC_EXPORT_INTERVAL` and `OTEL_EXPORTER_OTLP_METRICS_INSECURE` listed as permanent aliases, ABO keys as window aliases.
- Header — 03 v1.4.0.

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): SIMP2-13 objection to 03 §3.6 closed (03 says "ships").

Round 3:

- MOD3-3 — §13 item 1: S1 moves construction only; the global installs stay in ABO's `internal/telemetry` façade until `app.InstallProcessDefaults` at S2; the six global-reading ABO packages named.
- SEC3-13 — §13 item 5: `core:metrics.read` via `core:admin`, seeded to `editor` and `viewer` only, never to `fp-worker` or a `Confining` role; scoped-key change recorded for 09 §9.2.6.
- SIMP3-3 — §3 `Alias`, §7 step 3: aliases supplied by the app (`app.Options.MetricAliases`) with rows in the app's ledger (`app.Options.CompatLedger`).
- SIMP3-9 — §9: periodic reader and batch span processor named as library-internal timers, not tasks.
- R3-1 — §13 item 5: the role seed is stored additively in the legacy role rows; no other change (this file stores no data). (Superseded in round 4 by R4-1: the seed lives in books' extension family.)

Round 4:

- MOD4-3 — M1, M5, §3 `Options.PromRegisterer`, exporter construction, §6 per-stage table (S2–S7: ABO's legacy gin route serves `Provider.Handler()` over the default registry, the OTel globals set by `app.InstallProcessDefaults`; S8: kernel `Router.Infra` mount and private registry, legacy families moved in the same PR; series-name-set done-checks), §7 step 1, §12 `TestNoGlobals` exception and `TestProvider_ExternalRegisterer`, §13 items 1–2.
- MOD4-4 — §13 item 1: per-file split; attribute keys, views, the scope helper and `SetVersion`/`Version` stay in ABO permanently as books telemetry; only the install code is deleted at S2.
- SIMP4-10 — §10: core's ledger fragment holds only the permanent `OTEL_*` rows; ABO's `otel_*` names are app-supplied window aliases.
- SIMP4-2 — no ABO name added: books' bucket and attribute tables stay in ABO (§13 item 1), consistent with "core holds no ABO name".
- R4-1 (SEC4-3) — §13 item 5: the `core:metrics.read` seed lives in books' extension family, not in legacy role rows.

Round 5:

- OPS5-5 / SEC5-13 — M5, §6 table, §13 item 5, §12: option (a): ABO's legacy `/metrics` route keeps HEAD's any-credential check until S4 r2, then its legacy manifest entry is `core:metrics.read`, enforced by stage 13 (matching 09 §9.2.6); `TestLegacyMetricsRoute_RequiresScopeFromS4`.
- OPS5-6 — §6 "Series-name checks", §13 items 3–4: each core-subsystem rename happens in the stage that moves its emitter (Pebble S2b, tasks/ops S5, AI S7, process gauges S8) with its alias and `deploy/` switch in the same PR and a "⊇ previous, renamed families answer under both names" check; S8's check is set(S7) ∪ {`mediacore_build_info`, …}; W counted from each alias's release.
- OPS5-9 — §6: every series-name check scrapes with a credential holding `core:metrics.read` and fails on an empty name set.
- OPS5-14 — §3 `Options.PromRegisterer`, §6 table (new S2 r1–r2 row), §7 step 1, §13 item 1: `PromRegisterer` is set at S2 r3, in the release that deletes the façade's exporter; nil (private, unserved) at S2 r1–r2, whose checks are "200 and ⊇ S1".
- MOD5-5 (reference) — §3 span redaction uses `log.URLKeys()`.

Round 6:

- OPS6-2 — M6, §3 (`Alias` comment, new `metrics.RuntimeFamilies()`, `ErrAliasNotCore` text), §6, §8 row, §12 `TestAliases_GatherTimeCopy` and new `TestAlias_GoCollectorTargetAccepted`, §13 item 3: an alias may target `mediacore_*` or a core runtime-collector family (`go_goroutines`, `go_memstats_alloc_bytes`), so the S8 process-gauge aliases no longer fail boot; the two `go_*` names are dropped from the S8 additions, present since HEAD.
- OPS6-3 — §6 "Series-name checks", §12 closing paragraph: the normative per-stage check is the golden diff of ABO's seeded series contract test (`internal/telemetrypolicy`), with each renamed family's seeding row and alias added in the renaming PR; the live credentialed scrape is only a smoke check (200, non-empty, a fixed always-on set).

Round 7:

- OPS7-1 — §6 "What it scrapes", §12 closing paragraph: from S2 r3 ABO's seeded contract test builds the stage's Provider through `coretest.MetricsProvider` with ABO's alias rows and scrapes the served `Provider.Handler()`, never `promhttp.Handler()`; the S2 r3 PR adds `mediacore_` to `inScopePrefixes`; core-owned emitters are seeded through public SDK paths; the test's registry becomes private in the S8 PR; `TestContractScrapesServedHandler`.
- OPS7-5 — §6 S8 row, §12, §13 items 2–3: the S8 PR deletes `memoryAllocGauge`, `goroutinesGauge`, `SetMemoryAlloc`, `SetGoroutines` and their two status-worker calls together with adding the aliases (payload fields read from `runtime`), and replaces their seeding rows with alias rows; `TestS8_FullAliasSetBoots`.
- Header — 03 reference v1.9.1.

Round 8:

- OPS8-1 — §6 golden-diff row, §12 closing paragraph, §13 items 2–3: the T0+T1 PR deletes the five `operations_*` client_golang vectors, their helpers and their v2-worker calls in the PR that adds their aliases, replacing their seeding rows; `_operation_duration_seconds` aliases the histogram `mediacore_tasks_run_duration_seconds`; `TestS5_FullAliasSetBoots`.
- OPS8-10 — §6 "What it scrapes", S2 r3 golden row, §12: kernel-emitted families leave ABO's golden and are asserted by core's `TestKernelFamiliesGathered` (one zero-module `coretest` boot on a fresh registry). This is the reviewer's alternative, because a second booted Provider on the default registry collides on `target_info` (OPS5-14).
- R8-6 / MOD8-2 — §13 item 1 and the rename table: `opsmetrics`' one core successor is `mediacore/internal/executor`, and the ABO package stays as opsbridge-side code until its last user is gone; books' gauges use books' own scope; gather-time aliases are the one name mechanism.
- R-C request (05-tasks v1.9.0 5T.11) — §13 table: the T0+T1 PR also deletes the v2-only `opsmetrics` instruments; `_ops_schedule_lag_seconds` and `_ops_schedule_missed_total` move to T7; `_ops_abandoned_calls` stays a books series with no alias.
- Header — 03 reference v1.10.1.
