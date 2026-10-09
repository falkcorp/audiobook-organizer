<!-- file: docs/proposals/2026-10-holistic/11-metrics-strategy-otel-prometheus.md -->
<!-- version: 1.1.0 -->
<!-- guid: b7aecfb3-f5ea-482f-80e4-3e5cc0453ff1 -->
<!-- last-edited: 2026-10-09 -->

# 11: Metrics strategy, OTel and Prometheus (decision D14d)

Owner's requirement, verbatim (09-owner-decisions.md, D14d): *"I don't care how it gets there, I just want otel and prometheus metrics available. So figure out the best way forward that gives us the most versatility for the future so we won't have to do major refactors."* Accepted direction: new instrumentation uses the OTel metric API; add an optional OTLP metric exporter as a second reader alongside the Prometheus reader; existing `client_golang` metrics migrate opportunistically; delete the unused `telemetry.MetricsHandler` placeholder.

Round-2 author `r5`. Planning only. Evidence anchors are `file:line` at commit `ebda30d47`.

## 1. Summary

- Today one OTel `MeterProvider` exists with a single Prometheus reader that registers into the **default** `client_golang` registry (`internal/telemetry/telemetry.go:174-191`), and `/metrics` serves that registry through `promhttp.Handler()` behind `RequireAuth` when auth is on (`server_lifecycle.go:1305-1310`). So OTel instruments and `client_golang` series already land on the same scrape. Metrics are always on; only tracing is gated on an endpoint (`telemetry/config.go:25-31`).
- **69 `client_golang` series families** are registered (D14d said "about 25"): 34 in `internal/metrics/metrics.go`, 5 in `pipeline_metrics.go`, 25 in `pebble_collector.go` (a custom `Collector`), 5 in `internal/aidispatch/metrics.go`. Measured with the grep in F3. The five aidispatch names carry **no** `audiobook_organizer_` prefix.
- **Exactly one OTel metric producer exists:** `otelgin.Middleware` (`server.go:566`). `otel.Meter` is called nowhere except the unused `GlobalMeter()` (`telemetry.go:199-201`). The ground is clear for one convention.
- The OTel Prometheus exporter can keep every existing name unchanged: counters get `_total`, histograms get the unit suffix (`_seconds`) plus `_bucket/_sum/_count`, gauges keep the literal name (so `books_total`, a gauge named with `_total`, survives if the instrument is named that way). The two things that would change label sets are `otel_scope_name`/`otel_scope_version` and the `target_info` series; the spec turns scope labels off so PromQL `by (...)` clauses and the four alert expressions keep their shape.
- Histogram buckets do **not** carry over by default (the SDK's default boundaries are 0, 5, 10, 25 … 10000). Every migrated histogram needs an explicit `View` with today's bucket list (`metrics.go:48,236`; `pipeline_metrics.go:104,126`; `aidispatch/metrics.go:44`), enforced by a test that every histogram instrument has a view entry.
- One cardinality defect to fix on migration: `op_items_processed{op_id,op_type}` and `op_items_total{op_id,op_type}` (`metrics.go:275-284`) carry a per-run label (one series per op run, forever). The ops v3 instruments (05 R15) replace them with `def_id` (a closed set validated at startup) and the `OpStalled` alert is rewritten.
- The 10-03 Tempo incident (a bad trace endpoint crash-looped prod; `telemetry.go:45-51`) sets the rule for the metric exporter too: a broken OTLP metric endpoint costs the OTLP copy and nothing else, never startup.
- `resource.go:19` hard-codes `service.version "0.221.0"`; it should come from build info.
- 7 PRs: PR 1 `telemetry.Meter` + views table + delete `metrics_handler.go` + the `/metrics` series contract test (S); PR 2 the OTLP metric reader, off by default (M); PR 3 first migration proof on aidispatch (S); PR 4 ops v3 instruments package that 05 PR 2/11 consume, and the `op_id` label fix (M); PR 5 a `client_golang` constructor ratchet in `make ci` (S); PR 6 pipeline metrics migration as the dashboard-pinned proof (S); PR 7 AI call metrics and traces, which keeps and wires `internal/ai/telemetry.go` (D55, M).

## 2. Findings

| ID | Finding | Evidence | Confidence | Impact |
|---|---|---|---|---|
| F1 | `initMetrics` builds `prometheus.New()` (default options: default registry, scope labels on, `target_info` on, no namespace) once per process and sets it global; `InitOTEL` returns the error (fatal in `cmd/root.go:277-280`). Tracing errors are non-fatal by design since 10-03. | `telemetry.go:42-70,174-191`; `cmd/root.go:276-281` | high | PR 2 adds a second reader to the same provider; must be non-fatal |
| F2 | `/metrics` = `promhttp.Handler()` on the default registry; credential required when `EnableAuth`; scraper uses an `abk_` key (`deploy/prometheus/scrape-config.yml`). | `server_lifecycle.go:1299-1310` | high | Unchanged by this spec |
| F3 | 69 series families: `grep -c "prometheus.New\(Counter\|Gauge\|Histogram\)" internal/metrics/metrics.go` → 34; `pipeline_metrics.go` → 5; `newPebbleDesc(` in `pebble_collector.go` → 25; `aidispatch/metrics.go` → 5. | the greps | high | The contract golden has 69 entries (plus `_bucket/_sum/_count` expansions) |
| F4 | aidispatch names have no `Namespace`: `ai_dispatch_requests_total{capability,endpoint,outcome}`, `ai_dispatch_inflight{endpoint}`, `ai_dispatch_failover_total{capability,endpoint,class}`, `ai_dispatch_no_capable_total{capability}`, `ai_dispatch_slot_wait_seconds{endpoint}` (exp buckets 1 ms ×4 ×10). | `internal/aidispatch/metrics.go:22-45` | high | Name map keeps them bare; a recording-rule shim is offered if the owner wants the prefix |
| F5 | Only OTel producer: `otelgin.Middleware("audiobook-organizer")`. No `otel.Meter(` call sites besides `GlobalMeter()`. | `server.go:566`; `grep -rn "otel.Meter(\|Int64Counter(" internal cmd \| grep -v _test` → 1 hit, telemetry.go | high | No existing OTel naming to reconcile |
| F6 | `MetricsHandler` creates a new exporter per request and writes two placeholder lines; zero references. | `telemetry/metrics_handler.go:30-49`; `grep -rn MetricsHandler --include='*.go' .` → only its own file | high | Delete in PR 1 |
| F7 | Dashboard references 15 names (`metadata_fetch_total`, `review_index_request_seconds_{bucket,count}`, `number_leading_titles`, `filename_parse_total`, `books_total`, `search_index_docs_total`, `operations_{failed,completed,canceled}_total`, `operation_duration_seconds_bucket`, `op_items_processed`, `fixer_duration_seconds_bucket`, `cache_{hits,misses}_total`); alerts use `operations_failed_total`, `ai_backend_available`, `op_items_processed`, plus `process_resident_memory_bytes`, `up`, `node_filesystem_*`. | `deploy/grafana/dashboards/audiobook-organizer-overnight.json`; `deploy/prometheus/alert-rules.yml:31,54,72,88,105,148` | high | These 15 + 3 names are the "must not drift" set |
| F8 | Histogram buckets are explicit and non-default: operation duration 18 buckets to 86400 s; cache get exponential 500 ns; review index 0.1-120 s; fixer 0.1-7200 s; aidispatch slot wait exp. | `metrics.go:48,236`; `pipeline_metrics.go:104,126`; `aidispatch/metrics.go:44` | high | Views table in PR 1 |
| F9 | Per-run labels: `op_items_processed{op_id,op_type}`, `op_items_total{op_id,op_type}` gauges; `operation_deprecated_def_id_total{alias,entry}` is bounded. | `metrics.go:275-284,104-108` | high | Cardinality rule C1; replaced in PR 4 |
| F10 | Pebble collector is a hand-written `prometheus.Collector` with `Describe/Collect` over `Metrics()` and a `store` label (+ `level`). | `pebble_collector.go:114-160` | high | Stays on `client_golang`; OTel equivalent is 25 observable instruments with one callback (L); not in this plan |
| F11 | Config: one telemetry key, `otel_exporter_otlp_endpoint` (env `OTEL_EXPORTER_OTLP_ENDPOINT`), default "", used only for traces. | `config.go:1282-1284,2386,2406,2950` | high | PR 2 adds metric keys |
| F12 | Module versions: `otel v1.46.0`, `sdk/metric v1.46.0`, `exporters/prometheus v0.68.0`, `otlptracegrpc v1.46.0`, `otelgin v0.71.0`, `client_golang v1.24.1`. No `otlpmetricgrpc` module yet. | `go.mod:22,29-37` | high | PR 2 adds `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.46.0` |
| F13 | `NewResource` merges `resource.Default()` with `service.name` and a hard-coded `service.version`. | `telemetry/resource.go:13-23` | high | PR 1 reads version from `debug.ReadBuildInfo` / ldflags |
| F14 | otelgin v0.71 can emit `http.server.request.duration` (→ `http_server_request_duration_seconds`) through the global meter; whether it does in this build was **not measured** (no prod scrape allowed). | `server.go:566`; go.mod:29 | low | Verified by the contract test's first run (it will list it if present) |
| F15 | 05 R15 wants queue depth, in-flight, items, errors, zombies, fenced writes, checkpoint age, schedule lag; 05 PR 2 lists `internal/metrics/metrics.go` as a file it touches and 05 PR 11 a Grafana operations dashboard. | `05-operations-v3.md:200,415,424` | high | PR 4 here supplies the instruments so 05 PR 2 writes OTel, not `client_golang` (D14d item 1) |
| F16 | `internal/ai/telemetry.go` (47 lines) is **kept, not deleted (owner answer to D55)**, and PR 7 wires it. Today it has an unused `aiTracer` plus `WithOpenAISpan` and a no-op `RecordOpenAIMetric` placeholder with zero callers (`grep -rn "WithOpenAISpan\|RecordOpenAIMetric" internal cmd pkg` finds only the file itself). Seven chat call sites in `openai_parser.go` (`:420, :482, :588, :685, :756, :1081, :1203`), one in `metadata_llm_review.go:142`, and the embeddings call `embedding_client.go:428` make every direct AI request; batch submissions are `embedding_batch.go:71` and `openai_batch.go:110, :277, :440`. **No code reads `completion.Usage` or `resp.Usage`** (`grep -n "Usage" internal/ai/*.go` finds none outside comments), so token counts are available from the SDK response but unrecorded. The routed path wraps these in `aidispatch.Call` (`pool_routing.go:158, :224`), whose 5 `ai_dispatch_*` series know the endpoint but not model, task or tokens. | `internal/ai/telemetry.go:1-47`; call sites above; `aidispatch/dispatch.go:328` | high | PR 7 |

## 3. Proposed specification

### 3.1 One helper: `telemetry.Meter`

```go
// internal/telemetry/meter.go (new)
package telemetry

// Meter returns the meter for a package. name is the Go package path suffix
// ("aidispatch", "operations/registry", "plugins/deluge"); the scope is
// "audiobook-organizer/<name>" and is NOT exported as a label (scope labels
// are off, §3.3), so it only identifies the instrument in OTLP consumers.
func Meter(name string) metric.Meter {
	return otel.GetMeterProvider().Meter("audiobook-organizer/" + name,
		metric.WithInstrumentationVersion(Version()))
}

// Instrument naming (enforced by TestInstrumentNames over the golden):
//   - dotted, lowercase, prefixed: "audiobook_organizer.<area>.<what>"
//     (the exporter turns dots into underscores: audiobook_organizer_<area>_<what>);
//   - counters are nouns ("...removed"), never "..._total" — the exporter appends it;
//   - histograms name the quantity ("...duration") and set WithUnit("s"), which
//     yields "<name>_seconds"; sizes use WithUnit("By") -> "_bytes";
//   - gauges whose legacy Prometheus name ends in "_total" (books_total,
//     search_index_docs_total, import_paths_total) keep that literal name:
//     the exporter adds no suffix to a gauge; and they use a bracket unit
//     ("{book}"), never "1", which would append "_ratio";
//   - the aidispatch family is the one unprefixed legacy exception (F4).
```

`GlobalMeter() any` and `GlobalTracer() any` (typed `any`) are deleted with the placeholder; `Meter` and `otel.Tracer` replace them.

### 3.2 Name map, `client_golang` → OTel (Grafana-compatible)

The rule gives a Prometheus name identical to today's for every family. The map is the test golden, not prose; the table shows the shapes.

| Today (`client_golang`) | OTel instrument | unit | Exporter output |
|---|---|---|---|
| `audiobook_organizer_operations_started_total{type}` CounterVec | Int64Counter `audiobook_organizer.operations.started` | `{operation}` | `audiobook_organizer_operations_started_total{type}` |
| `audiobook_organizer_operation_duration_seconds{type}` HistogramVec, 18 buckets | Float64Histogram `audiobook_organizer.operation.duration` + View | `s` | `..._operation_duration_seconds_{bucket,sum,count}{type}` |
| `audiobook_organizer_books_total` Gauge | Int64ObservableGauge `audiobook_organizer.books_total` | `{book}` | `audiobook_organizer_books_total` (no suffix added to gauges) |
| `audiobook_organizer_cache_size{cache}` GaugeVec | Int64UpDownCounter or ObservableGauge `audiobook_organizer.cache.size` | `{entry}` | `audiobook_organizer_cache_size{cache}` |
| `audiobook_organizer_cache_get_duration_seconds{cache}` exp buckets | Float64Histogram + View (ExponentialBuckets(5e-7,4,10) copied as explicit boundaries) | `s` | unchanged |
| `ai_dispatch_requests_total{capability,endpoint,outcome}` | Int64Counter `ai_dispatch.requests` (legacy exception, no prefix) | `{request}` | `ai_dispatch_requests_total{...}` |
| `ai_dispatch_inflight{endpoint}` Gauge | Int64UpDownCounter `ai_dispatch.inflight` | `{request}` | `ai_dispatch_inflight{endpoint}` |
| `op_items_processed{op_id,op_type}` | **replaced**, see §3.7 | — | recording-rule shim for 30 days |
| `pebble_*{store}` (25) | not migrated (F10) | — | unchanged |

Recording-rule shim (only where a name must change, today only `op_items_processed`):

```yaml
# deploy/prometheus/recording-rules.yml (new, PR 4)
groups:
  - name: audiobook_organizer_compat
    rules:
      - record: audiobook_organizer_op_items_processed
        expr: sum by (op_type) (audiobook_organizer_ops_items_total)
```

### 3.3 Exporter options, resource and readers

```go
// telemetry.go, PR 1/2
promExporter, err := prometheus.New(
	prometheus.WithoutScopeInfo(),   // no otel_scope_name/version labels: label sets stay identical to client_golang
	// target_info stays: one extra series carrying the resource; harmless and useful for joins.
)
readers := []metric.Option{metric.WithReader(promExporter), metric.WithResource(NewResource(cfg.ServiceName))}
if cfg.MetricsOTLPEndpoint != "" {
	exp, err := otlpmetricgrpc.New(ctx, otlpEndpointOption(cfg.MetricsOTLPEndpoint)) // same parser as traces, renamed
	if err != nil { /* log at error, continue without OTLP: never fatal (10-03 rule) */ }
	else {
		readers = append(readers, metric.WithReader(metric.NewPeriodicReader(exp,
			metric.WithInterval(cfg.MetricsOTLPInterval))))  // cumulative temporality (default), matches Prometheus
	}
}
readers = append(readers, Views()...)   // §3.4
provider := metric.NewMeterProvider(readers...)
```

Resource attributes: `service.name`, `service.version` (from `debug.ReadBuildInfo().Main.Version`, falling back to the ldflags `version` var), `service.instance.id` (hostname-free: a per-process ULID, so the public repo and dashboards never carry a hostname), `deployment.environment` (new key `telemetry_environment`, default `"prod"`; the sandbox sets `"sandbox"`).

Config keys (all new; `internal/config/config.go` next to `otel_exporter_otlp_endpoint`):

| Key | Env | Default | Meaning |
|---|---|---|---|
| `otel_metrics_otlp_endpoint` | `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | `""` (off) | OTLP/gRPC collector for metrics. Same three forms as the trace endpoint. Empty = Prometheus reader only. No fallback to the trace endpoint (explicit, after 10-03). |
| `otel_metrics_otlp_interval` | `OTEL_METRIC_EXPORT_INTERVAL` | `60s` | Periodic reader interval. |
| `otel_metrics_otlp_insecure` | `OTEL_EXPORTER_OTLP_METRICS_INSECURE` | `false` | Plaintext gRPC for a bare `host:port`. |
| `telemetry_environment` | — | `prod` | `deployment.environment` resource attribute. |

`metrics_enabled` stays implicit (always on), as `LoadConfig` documents.

### 3.4 Views: histogram buckets are declared, not defaulted

```go
// internal/telemetry/views.go (new)
var histogramBuckets = map[string][]float64{
	"audiobook_organizer.operation.duration":        {0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400},
	"audiobook_organizer.cache.get.duration":        explicit(prometheus.ExponentialBuckets(0.0000005, 4, 10)),
	"audiobook_organizer.review_index.request.duration": {0.1, 0.25, 0.5, 1, 2, 3, 5, 10, 20, 30, 60, 120},
	"audiobook_organizer.fixer.duration":            {0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200},
	"ai_dispatch.slot_wait":                         explicit(prometheus.ExponentialBuckets(0.001, 4, 10)),
	"audiobook_organizer.deluge.rpc.duration":       {0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	"audiobook_organizer.ops.schedule_lag":          {1, 5, 15, 60, 300, 900, 3600, 21600, 86400},
}
func Views() []metric.Option // one metric.NewView per entry (ExplicitBucketHistogram)
```

`TestEveryHistogramHasAView` creates every instrument the binary declares (the golden) against a test provider and fails on any histogram missing from the map, so a new histogram cannot ship with default boundaries.

### 3.5 Cardinality rules

- C1: an attribute value set must be **closed and small**: enumerations (`outcome`, `class`, `state`, `phase`, `provider`, `kind`), plugin names (≈12), fixer ids (≈22), def ids (≈234, validated at startup, the documented upper bound). Never: op/run ids, book/file ids, paths, user ids, URLs, free text, timestamps.
- C2: at most 4 attributes per instrument; at most 1,000 series per family (def_id × outcome stays under it).
- C3: attribute keys come from `internal/telemetry/attr.go` constants (`attr.Outcome`, `attr.DefID`, ...), so a new key is a reviewed addition; a test enumerates keys used in the golden against the allowlist.
- C4: `endpoint` on aidispatch is the LLM pool host role name (bounded by the pool), kept.
- C5: AI series (PR 7) use `provider` (2 values), `model` (the configured model names, a handful), `task` (closed enum of 9) and never `endpoint`/`node` on a histogram: the pool can grow and `node × model × task × 12 buckets` multiplies fast. Per-node latency stays in traces, where the node is a span attribute.

### 3.6 Migration policy: move on touch

- A PR that edits a function recording a `client_golang` series moves **that whole family** (constructor, every call site, its registration) to OTel in the same PR, and deletes the `client_golang` constructor. Never both at once: two collectors with one name make `promhttp` return 500 (`collected metric ... was collected before`), which the contract test catches in CI.
- The PR's only visible change on `/metrics` is none: the contract test's golden is unchanged. If a name must change, the PR adds the recording rule (§3.2) and updates the dashboard/alert in the same PR.
- `internal/metrics` keeps its exported `Inc*/Set*/Observe*` functions as the call-site API while families migrate; the package becomes a façade over `telemetry.Meter("metrics")` instruments and is deleted when empty (except the pebble collector, which stays as a `client_golang` `Collector` and is registered from `internal/metrics/pebble_collector.go` indefinitely).
- No big-bang: the ratchet (PR 5) only forbids a **rise** in `client_golang` constructors; the baseline is 69.

### 3.7 Ops v3 metrics (05 R15) as OTel instruments

`internal/opsmetrics` (new, PR 4), one `Meter("operations")`, consumed by `internal/operations/registry` (05 PR 2) and the v3 runner (05 PR 5):

| Instrument | Kind | Attributes | Replaces |
|---|---|---|---|
| `audiobook_organizer.ops.queue_depth` | observable gauge | `kind`, `priority` | — |
| `audiobook_organizer.ops.inflight` | observable gauge | `kind`, `plugin` | — |
| `audiobook_organizer.ops.runs` | counter | `def_id`, `outcome` ∈ {completed, failed, canceled, timed_out, dropped} | `operations_{started,completed,failed,canceled}_total{type}` (type = def id today; one counter with outcome; `started` is `runs{outcome="started"}`) |
| `audiobook_organizer.ops.run.duration` | histogram `s` + view | `def_id`, `outcome` | `operation_duration_seconds{type}` |
| `audiobook_organizer.ops.items` | counter | `def_id`, `outcome` ∈ {ok, failed, skipped} | `op_items_processed{op_id}` (gauge per run → counter per def) |
| `audiobook_organizer.ops.fenced_writes` | counter | `def_id` | — |
| `audiobook_organizer.ops.zombies` | observable gauge | — | — |
| `audiobook_organizer.ops.checkpoint_age` | observable gauge `s` | `def_id` | — |
| `audiobook_organizer.ops.schedule_lag` | histogram `s` + view | `def_id` | — |
| `audiobook_organizer.ops.schedule_missed` | counter | `def_id` | — |

Alert rewrites in `deploy/prometheus/alert-rules.yml` (PR 4): `OpFailuresHigh` → `rate(audiobook_organizer_ops_runs_total{outcome="failed"}[15m]) > 0.1` with the old expression kept as a second rule for 30 days (dual-write period: both families are emitted by the registry during the soak, which is the one sanctioned exception to "never both at once", because the names differ); `OpStalled` → `sum(audiobook_organizer_ops_inflight) > 0 and rate(audiobook_organizer_ops_items_total[30m]) == 0`.

### 3.8 Traces

What exists: `otelgin` server spans on every request (`server.go:566`) and an OTLP/gRPC span exporter + batch tracer provider when `otel_exporter_otlp_endpoint` is set (`telemetry.go:138-154`); no collector in prod (`deploy/grafana/TRACING-RUNBOOK.md`); a bad endpoint is logged, never fatal. Recommendation: keep as is; no new trace work in this plan. The metric endpoint is a separate key (§3.3) so a collector can receive metrics without traces or the reverse. When a collector exists, the one trace addition worth making is a span per op run in the v3 runner (`def_id`, `op_id` as span attributes, where high cardinality is fine), which is 05's call.

### 3.9 AI call metrics and traces (PR 7, owner decision D55)

Owner decision D55: keep `internal/ai/telemetry.go` and add AI metrics instead of deleting it. **Prometheus compatibility is a hard requirement (owner, restated):** every AI instrument below is an OTel instrument on the existing `MeterProvider` and is exported on the **existing `/metrics` scrape endpoint** through the **existing OTel Prometheus reader** (`telemetry.go:174-191`), with Prometheus-conventional names (`_total` on counters, `_seconds` plus `_bucket/_sum/_count` on the histogram, explicit buckets). Grafana and the Prometheus scraper read them exactly as they read every other series today: same job, same auth, same dashboards stack, no new endpoint and no new scrape config. OTLP push (PR 2) stays an optional second reader, off by default, that receives the same instruments; turning it on or off never changes `/metrics`.

**Where AI calls are made** (F16): all direct calls go through the OpenAI-compatible Go SDK (`Chat.Completions.New`, `Embeddings.New`, `Batches.New`), pointed at OpenAI or, via base URL, at a local OpenAI-compatible server (Ollama and similar); there is no second client library. The routed path reaches the same SDK calls through `aidispatch.Call`.

**Instruments** (meter `telemetry.Meter("ai")`; the `ai_` prefix is a second unprefixed exception beside `ai_dispatch_*`, extending the F4 rule so AI series group together in Grafana):

| Instrument (exported name) | Type | Labels | Notes |
|---|---|---|---|
| `ai_requests_total` | counter | `provider`, `model`, `task`, `outcome` | one per attempt that reaches the SDK. `outcome` in `ok`, `error`, `timeout`, `canceled`. A retry is a new attempt. |
| `ai_request_duration_seconds` | histogram | `provider`, `model`, `task` | wall time of the SDK call. Buckets (view): `0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300` (the embedding per-attempt timeout is 30 s; local models on a busy pool run to minutes). |
| `ai_tokens_total` | counter | `provider`, `model`, `task`, `direction` | `direction` in `input`, `output`. Source: `completion.Usage.PromptTokens` / `CompletionTokens` (chat) and `resp.Usage.PromptTokens` (embeddings, input only); **today no code reads these fields but the SDK response carries them**. A reply with all-zero usage (some local servers omit it) records nothing rather than a false zero. Batch-API tokens arrive in the batch result file, not the submit call, so they are out of scope for PR 7 (note in `AI-REFERENCE.md`). |
| `ai_failures_total` | counter | `provider`, `model`, `task`, `reason` | `reason` in `rate_limit`, `quota` (`isPermanentQuota429`), `unreachable` (`isUnreachableError`), `timeout`, `permanent` (`isPermanentAIError`), `reply_parse` (`*ReplyParseError`), `count_mismatch` (`*ResultCountError`), `other`. Always incremented together with `ai_requests_total{outcome="error"|"timeout"}`, so failure ratio is `failures / requests` with matching labels. |
| `ai_parse_results_total` | counter | `task`, `accepted` | one per **item** of a parse (filename, audiobook, cover art). `accepted="true"`: the model returned a non-nil result and the scan phase merged at least one field (`ai_batch_phase.go:236-260`). `accepted="false"`: the model returned nothing for the item (nil entry, journalled as "had nothing" by `scanner/ai_parse_journal.go`) or the whole batch was rejected as a quality failure (`*ReplyParseError`/`*ResultCountError`, which are never retried on a peer: `pool_routing.go:141-170`). A grep of `ai_batch_phase.go` and `ai_parse_async.go` finds **no confidence gate** (`ParsedMetadata.Confidence` is carried, not compared), so "accepted" means usable, not high-confidence; adding a `confidence` label is a follow-up only if the owner introduces a gate. |

`task` is a closed enum of 9: `filename_parse`, `audiobook_parse`, `batch_parse`, `cover_art`, `cover_text`, `author_dedup_review`, `author_discovery`, `metadata_review`, `embed`. `provider` is a closed pair, `openai` (base URL empty or the OpenAI host) or `local` (any other OpenAI-compatible base URL). `model` is the configured model name (`filename_parse_model`, `metadata_review_model`, the endpoint's `chat_model`, the pinned embed model): a handful. All four keys come from `attr.go` (C3).

**Cardinality.** `node`/`endpoint` is **not** a label on any AI series, and above all not on the duration histogram: 12 buckets × nodes × models × 9 tasks grows with the pool (the pool is documented as growable, never hard-coded). Worst case today is about 2 providers × ~6 models × 9 tasks × 12 buckets, under C2's 1,000-series cap; the test counts it. Per-node latency and the chosen node stay in traces (span attributes `ai.node`, `ai.endpoint_id`, where high cardinality is fine) and in the existing `ai_dispatch_*` series.

**Relation to the 5 existing `ai_dispatch_*` series (PR 3 migrates them to OTel with identical names; neither is renamed).** Keep both: `ai_dispatch_*` answers routing questions (which endpoint, slot wait, failover, no capable endpoint) and has no model, task or token view; `ai_*` answers call-quality questions (latency, tokens, failure reason, parse acceptance) and deliberately drops `endpoint`. A dispatch attempt that reaches the SDK produces one `ai_dispatch_requests_total` and one `ai_requests_total`; a refused call (`ai_dispatch_no_capable_total`) never reaches the SDK and produces no `ai_*` series. No double counting because they measure different layers; the dashboard shows them side by side.

**Traces.** `WithOpenAISpan` is not OpenAI-specific (the same call reaches local servers), so PR 7 **renames it `WithAISpan`** and makes it generic over the result type (the current `any` return forces a type assertion at every site). It is wired at the same choke point as the metrics (`observe`), at the 9 sites above, so each AI request has a span named `ai.<task>` with attributes `ai.provider`, `ai.model`, `ai.task`, `ai.node`, token counts as attributes on completion, and `RecordError` plus `error=true` on failure. The span nests under the caller's context, so a `library.ai-parse` op shows its model calls as children when the existing OTLP trace exporter is on (no new trace config; a bad endpoint stays non-fatal). `RecordOpenAIMetric` (a documented placeholder) is deleted, replaced by the real instruments. Free text (filenames, prompts, replies) is never a span attribute or label (public-repo and privacy hygiene).

**Grafana panels** (new `audiobook-organizer-ai.json`, all `/metrics`-sourced):
- AI request rate by task and outcome: `sum by (task, outcome) (rate(ai_requests_total[5m]))`
- AI failure ratio by task: `sum by (task) (rate(ai_failures_total[15m])) / sum by (task) (rate(ai_requests_total[15m]))`
- Failures by reason (stacked): `sum by (reason) (rate(ai_failures_total[15m]))`
- Latency p50/p95/p99 by task: `histogram_quantile(0.95, sum by (le, task) (rate(ai_request_duration_seconds_bucket[5m])))`
- Latency p95 by model: `histogram_quantile(0.95, sum by (le, model) (rate(ai_request_duration_seconds_bucket[5m])))`
- Tokens per minute by direction and model: `sum by (model, direction) (rate(ai_tokens_total[5m])) * 60`
- Tokens per accepted parse (cost proxy): `sum(rate(ai_tokens_total{task=~".*parse.*"}[1h])) / sum(rate(ai_parse_results_total{accepted="true"}[1h]))`
- Parse accepted ratio by task: `sum by (task) (rate(ai_parse_results_total{accepted="true"}[1h])) / sum by (task) (rate(ai_parse_results_total[1h]))`
- Dispatch beside call quality: `ai_dispatch_inflight`, `rate(ai_dispatch_failover_total[15m])` and `ai_dispatch_slot_wait_seconds` p95 on the same row.

**Alert suggestion** (`deploy/prometheus/alert-rules.yml`): `AIFailureRatioHigh`: `sum by (task) (rate(ai_failures_total{reason!~"reply_parse|count_mismatch"}[15m])) / sum by (task) (rate(ai_requests_total[15m])) > 0.2 and sum by (task) (rate(ai_requests_total[15m])) > 0.01` for 15m, severity warning. Reply-quality failures are excluded because the scan phase's split-and-retry handles them by design; a second, lower-severity `AIParseAcceptedLow` on the accepted ratio is optional. Thresholds are a starting point to tune after a week of data.

## 4. Implementation plan

| PR | Title | Files | Tests | Rollback | Size | Depends on |
|---|---|---|---|---|---|---|
| 1 | `telemetry.Meter`, views, version, delete placeholder, series contract | `internal/telemetry/meter.go` (new), `views.go` (new), `attr.go` (new), `resource.go` (version from build info), `telemetry.go` (`WithoutScopeInfo`, `WithResource`, views), `metrics_handler.go` (**delete only if 01 P6 has not landed; 01 P6 owns it, 08 R22**), `series_contract_test.go` (new), `testdata/series.golden` (new: every `# TYPE` line from an in-test `promhttp` scrape after `metrics.Register()` + `aidispatch` init + a Pebble store; 69 families), `docs/AI-REFERENCE.md` | contract test fails on any missing or added family unless the golden is updated in the same PR; `TestEveryHistogramHasAView`; `TestAttributeKeysAllowlisted`; `TestNoScopeLabels`; `TestReservedAISeries` (the PR 7 names `ai_requests_total`, `ai_request_duration_seconds{_bucket,_sum,_count}`, `ai_tokens_total`, `ai_failures_total`, `ai_parse_results_total` are listed in `testdata/series_reserved.txt`: no `client_golang` constructor may claim them before PR 7 lands, and PR 7 moves them from reserved into the golden) | revert; no runtime behaviour change except the two labels, which no dashboard uses | S | — |
| 2 | OTLP metric reader, off by default | `internal/telemetry/{telemetry,config}.go` (`initMetrics(cfg)`, `otlpEndpointOption` shared with traces), `internal/config/config.go` (4 keys, viper defaults, env binds), `cmd/root.go`, `go.mod`/`go.sum` (`otlpmetricgrpc v1.46.0`), `deploy/grafana/METRICS-RUNBOOK.md` (new), `web/src/components/settings/*` (read-only display of the keys, optional) | periodic reader with `metric.NewManualReader` stand-in: resource attrs present, cumulative temporality; endpoint parse table (reuses the trace test); **a failing exporter does not fail `InitOTEL`** | keys default off; revert | M | 1 |
| 3 | Migrate `internal/aidispatch/metrics.go` | `internal/aidispatch/metrics.go` (5 families → `Meter("aidispatch")`), `internal/aidispatch/*_test.go`, `internal/telemetry/views.go` (slot_wait entry) | golden unchanged (names identical); view test | revert | S | 1 |
| 4 | `internal/opsmetrics` + `op_id` fix + alert/recording rules | `internal/opsmetrics/*` (new), `internal/operations/registry/{worker,registry}.go` (emit v3 instruments alongside the old counters for the soak), `internal/metrics/metrics.go` (delete `op_items_processed/op_items_total` after the soak: follow-up), `deploy/prometheus/{alert-rules,recording-rules}.yml`, `deploy/prometheus/README.md`, `deploy/grafana/dashboards/audiobook-organizer-overnight.json` (OpStalled panel) | golden gains the ops families; alert YAML parse test; registry test asserts `ops.runs{outcome}` increments per state | revert; recording rule keeps the old name alive | M | 1; feeds 05 PR 2 and PR 11 |
| 5 | `client_golang` ratchet in `make ci` | `internal/telemetry/ratchet_test.go` (new: counts `prometheus.New(Counter\|Gauge\|Histogram\|Summary)` constructors under `internal/` and `pkg/`, fails on a rise over `testdata/client_golang_baseline.txt` = 69; a bot PR lowers the baseline, per D47), `Makefile` (`ci` target) | the test is the gate; a fixture PR proves it fails on +1 | revert | S | 1 |
| 6 | Migrate `internal/metrics/pipeline_metrics.go` (dashboard-pinned proof) | `internal/metrics/pipeline_metrics.go` (5 families; 4 are on the dashboard), call sites in `internal/filename`, `internal/metafetch`, `internal/server` (review index), `internal/repairs/engine.go` (fixer duration), `views.go` | golden unchanged; dashboard JSON unchanged | revert | S | 1, 3 |
| 7 | **AI call metrics and traces** (D55: keep and wire `ai/telemetry.go`) | `internal/ai/telemetry.go` (replace the placeholder: `WithOpenAISpan` renamed **`WithAISpan`** and made generic, `RecordOpenAIMetric` deleted, new `observe(ctx, task, provider, model, fn)` helper that owns the span, the timer and the counters), `internal/ai/ai_metrics.go` (new: the 5 instruments on `telemetry.Meter("ai")`, provider/outcome/reason classifiers reusing `isPermanentAIError`, `isPermanentQuota429`, `isUnreachableError`, `*ReplyParseError`, `*ResultCountError` from `retry.go`/`openai_parser.go`), call sites `internal/ai/openai_parser.go` (`:420, :482, :588, :685, :756, :1081, :1203`), `internal/ai/metadata_llm_review.go:142`, `internal/ai/embedding_client.go:428`, parse-acceptance counting in `internal/scanner/ai_batch_phase.go` (the `resolved`/`aiMeta == nil` loop at `:231-240`) and `internal/scanner/ai_parse_async.go` (single-book path), `internal/telemetry/views.go` (`ai.request.duration` buckets), `internal/telemetry/testdata/series.golden` (gains the 5 AI families, which leave `series_reserved.txt`), `deploy/grafana/dashboards/audiobook-organizer-ai.json` (new), `deploy/prometheus/alert-rules.yml` (`AIFailureRatioHigh`), `docs/AI-REFERENCE.md`, `internal/ai/telemetry_test.go` | see §3.9: in-memory `ManualReader` + `tracetest.SpanRecorder` tests per call site; outcome and reason table test over the real error types; a fake completion with `Usage{PromptTokens, CompletionTokens}` asserts `ai_tokens_total`; a fake with zero usage records no token series; golden gains exactly the 5 families; `TestEveryHistogramHasAView`; a scrape-through-`promhttp` test asserts the AI names appear on `/metrics` with `_total`/`_seconds` suffixes and no `otel_scope_*` labels; `TestGuard_NoAIBackendCallsOutsideTheDispatcher` (aidispatch) still passes | revert; additive (new series and spans only, no existing name changes); `WithAISpan` has no callers outside the PR | M | 1, 4 (shares `views.go`, `alert-rules.yml`, `AI-REFERENCE.md`); 3 for the dispatch-series note |

Order: 1 → 2 ‖ 3 ‖ 5 → 4 → **7** → 6 (7 lands right after 4: it edits the same `views.go`, `alert-rules.yml` and `AI-REFERENCE.md`; 6 is independent of it). PR 4 must land before 05 PR 2 starts, or 05 PR 2 writes `client_golang` and the ratchet blocks it. The remaining 29 `internal/metrics` families (34 − 5 in PR 6) move on touch; the pebble collector does not move.

## 5. Risks and what must not break

- **Double registration.** A family on both `client_golang` and OTel with one name makes `/metrics` return 500 for the whole scrape, which takes every alert dark at once. Rule §3.6 (move the whole family, delete the old) and the contract test (a duplicate shows as a scrape error in-test) cover it. The one sanctioned overlap (PR 4 soak) uses different names.
- **Name drift breaking `alert-rules.yml` and the dashboard.** The 15 + 3 names in F7 are in the golden; `promtool check rules` (if present in CI) or a YAML parse test runs on every change to `deploy/prometheus/*`. A renamed family ships its recording rule in the same PR.
- **Scope labels.** `WithoutScopeInfo()` is set in PR 1, before any instrument exists, so no query ever sees `otel_scope_name`. Turning it on later is a label-set change and needs the same treatment as a rename.
- **`_ratio` and `_total` suffixes.** Unit `"1"` appends `_ratio`; a counter named with `_total` is double-suffixed by some exporter versions and warned on by others. The naming test rejects unit `"1"` and any counter name ending in `_total`.
- **Default histogram buckets.** Covered by the views test; an `operation.duration` histogram with 10000-second-capped default buckets would silently flatten the 86400-second tail the overnight dashboard shows.
- **OTLP exporter at startup.** Never fatal (F1 rule); a wrong endpoint logs once at error level and the Prometheus reader keeps serving. Its gRPC dial is non-blocking by default; the periodic reader's export timeout (30 s default) bounds a hung collector.
- **Cardinality.** `def_id` is the largest set (≈234) and is validated at startup; a run id never becomes an attribute (C1 test).
- **AI series are on the same scrape as everything else.** A regression in PR 7 (a name clash, an unbounded label) would take the whole `/metrics` response down like any double registration, so the reserved-name test (PR 1), the golden (PR 7) and the cardinality test guard it; the Prometheus-conventional suffixes are asserted by a `promhttp` scrape test, not trusted to the exporter.
- **Go runtime duplicates.** `process_memory_alloc_bytes` and `process_goroutines` duplicate `client_golang`'s Go collector; leave them until touched, then drop them in favour of the standard `go_*` series (dashboard does not use them).
- **The pebble collector stays** on `client_golang`; the ratchet counts constructors, not `NewDesc`, so it is neutral to it.
- **otelgin's own metrics** (F14): if present, they join the golden on PR 1's first run and are then pinned like everything else.

## 6. Dependencies

- **05 (ops v3):** PR 2 ("timed_out status and lifecycle metrics") and PR 11 (Grafana operations dashboard) consume `internal/opsmetrics` from PR 4 here instead of adding `client_golang` series to `internal/metrics/metrics.go`. The v3 runner (05 PR 5) records `ops.items`, `ops.fenced_writes`, `ops.checkpoint_age`, `ops.schedule_lag`, `ops.schedule_missed`.
- **10 (Deluge cleanup):** PR 4 there uses `telemetry.Meter("plugins/deluge")` and the views table entry for `deluge.rpc.duration`.
- **06 (Go/Node):** the deploy-wide observability lane (06 P3 pprof, P4 flight recorder) is independent; the `service.version` resource attribute reads the same ldflags var 06 sets.
- **07:** readiness signals (07 R2-R4) are a natural `ops.inflight`-style observable gauge (`audiobook_organizer.readiness{signal}`), added when R2 lands, not here.
- **D47 (ratchets fail only on a rise, a bot lowers the baseline):** PR 5 follows that shape.

## 7. Open questions for the owner

| # | Question | Recommended answer |
|---|---|---|
| Q1 | Scope labels (`otel_scope_name/version`) on OTel series: on or off? | **Off** (`WithoutScopeInfo`). Label-set parity with today's queries is worth more than per-scope filtering, which OTLP consumers get from the scope field anyway. |
| Q2 | OTLP metrics protocol: gRPC only, or also http/protobuf? | **gRPC only now**, matching the trace exporter and the single endpoint parser. Add http/protobuf if a collector that lacks gRPC shows up. |
| Q3 | Temporality: cumulative or delta? | **Cumulative.** It is what Prometheus, Mimir and Grafana expect, and it keeps both readers reporting the same numbers. |
| Q4 | Should the metrics endpoint fall back to `otel_exporter_otlp_endpoint` when unset? | **No.** One endpoint turned on by accident put prod in a crash loop on 10-03; metrics stay off until their own key is set. |
| Q5 | Migrate the Pebble collector to OTel observables? | **No**, unless an OTLP consumer needs Pebble internals. It is 25 families behind one callback; the Prometheus reader serves it today and the ratchet is neutral to it. |
| Q6 | Prefix the five aidispatch names with `audiobook_organizer_` while migrating them? | **No.** Keep them bare (zero dashboard/alert change). If the owner wants uniformity, PR 3 can add a 30-day recording-rule shim and rename; the cost is one more rule file to retire. |
| Q7 | When does `internal/metrics` get deleted? | When its last `client_golang` family (other than the pebble collector) has moved on touch. No date; the ratchet baseline is the progress meter, and a `todo.d` fragment lists the 29 remaining families after PR 6. |
