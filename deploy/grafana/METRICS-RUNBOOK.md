<!-- file: deploy/grafana/METRICS-RUNBOOK.md -->
<!-- version: 1.3.0 -->
<!-- guid: 5a2c8e71-3d94-4b60-8f17-c9e0a4d63b25 -->
<!-- last-edited: 2026-10-10 -->

# Metrics runbook: optional OTLP push next to `/metrics`

**`/metrics` remains the primary metrics surface.** Prometheus scrapes it as
before and nothing here changes it. The OTLP reader is an optional push copy
of the same OpenTelemetry instruments, for a collector that wants them pushed.
It is off by default.

Properties, in the order they matter in an incident:

- **Off by default.** With `otel_metrics_otlp_endpoint` empty no OTLP reader
  exists and no outbound connection is made.
- **Never fatal.** A malformed endpoint, an unreachable collector or a failing
  export costs the OTLP copy only. The server starts and `/metrics` keeps
  serving. A malformed endpoint is reported once, at start-up, as an
  error-level line: `OpenTelemetry initialized with the OTLP metric push OFF`,
  with an `otlp_metrics_error` attribute (URL userinfo is redacted). A
  failing export is logged by a rate-limited handler: the first error, then at
  most one line per 10 minutes (`OpenTelemetry export error (rate limited)`,
  with a `suppressed_since_last` count). The handler is process-wide, so it
  also covers trace-export errors.
- **No fallback.** The trace endpoint (`otel_exporter_otlp_endpoint`) is never
  reused for metrics. Setting only the trace endpoint leaves metric push off.
- gRPC only, cumulative temporality, no scope labels.
- **Isolated from the trace exporter's environment.** The OTel SDK reads the
  generic `OTEL_EXPORTER_OTLP_*` variables, which the trace exporter also
  uses. The metric push pins what matters, so none of them can change it:
  the transport (see "Transport rule"), headers (always empty, so a trace
  collector's `OTEL_EXPORTER_OTLP_HEADERS` bearer token is never sent to the
  metric host), and temporality (always cumulative, whatever
  `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE` says).

## The four keys

| Config key | Environment variable | Default | Meaning |
|---|---|---|---|
| `otel_metrics_otlp_endpoint` | `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | empty (off) | OTLP/gRPC collector |
| `otel_metrics_otlp_interval` | `OTEL_METRIC_EXPORT_INTERVAL` | `60s` | Push period. The OTel standard form, an integer in milliseconds (`60000`), or a Go duration (`60s`); clamped to 5s..1h, an unparsable value becomes 60s |
| `otel_metrics_otlp_insecure` | `OTEL_EXPORTER_OTLP_METRICS_INSECURE` | `false` | Plaintext gRPC to a bare `host:port` or `dns:///` target |
| `telemetry_environment` | none | `prod` | `deployment.environment` resource attribute on both surfaces |

These keys take effect only from the environment or the config file. A value
set through the UI or API is saved in `config_blob` and does **not** take
effect: telemetry reads its configuration before the database is loaded.

Accepted endpoint forms:

- `http://collector.example.invalid:4317`: plaintext gRPC.
- `https://collector.example.invalid:4317`: TLS.
- `collector.example.invalid:4317` or `dns:///collector.example.invalid:4317`:
  TLS, unless `otel_metrics_otlp_insecure` is true.

Anything after `host:port` that could carry a secret is dropped from either
OTLP endpoint at parse time: userinfo (`user:pass@`), `?query`, `#fragment`
and, for `http(s)` URLs, the path. OTLP/gRPC authenticates with headers and
credentials only and the SDK ignores the path, so nothing functional changes;
a `dns:///` target keeps its path because that is the target name. A single
start-up warning names the config key and what was dropped, never the value.
The start-up line shows the endpoint as `scheme://host:port` (attribute
`endpoint`, plus `otlp_metrics_endpoint` when the push is on); an endpoint that
does not parse is shown as `(invalid)`. Error text is also scrubbed of URL
userinfo, query and fragment as a backstop.

A URL needs both a host and a port; `http://host` is rejected and logged.

### Transport rule

One rule, applied to every form: an `http://` URL is plaintext and an
`https://` URL is TLS (the URL decides; the insecure key is not consulted); a
bare `host:port` or `dns:///` target is TLS unless `otel_metrics_otlp_insecure`
is true. The choice is pinned with explicit credentials, so the generic
`OTEL_EXPORTER_OTLP_ENDPOINT` / `_INSECURE` variables cannot downgrade a TLS
endpoint, and `OTEL_EXPORTER_OTLP_CERTIFICATE` / `_CLIENT_CERTIFICATE` cannot
turn an explicit plaintext one back into TLS. The consequences:

- TLS uses the system root CAs. `OTEL_EXPORTER_OTLP_CERTIFICATE`, the client
  certificate variables and their `_METRICS_` forms are **not honoured** for
  metrics. For a private CA put it in the system trust store or set
  `SSL_CERT_FILE` / `SSL_CERT_DIR` for the service.
- Metric headers are not supported yet: both `OTEL_EXPORTER_OTLP_HEADERS` and
  `OTEL_EXPORTER_OTLP_METRICS_HEADERS` are ignored. A collector that needs an
  auth header cannot be used until that is added.
- Compression and timeout variables are still read by the SDK.

## Turn it on

1. Stand up a collector with an OTLP/gRPC receiver, for example at
   `192.0.2.20:4317`.
2. Set `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://192.0.2.20:4317` in the
   service environment (or `otel_metrics_otlp_endpoint` in the config).
3. Restart. The start-up log line reads `OpenTelemetry initialized` with
   `otlp_metrics=true`.

## Confirm both surfaces agree

1. Scrape `/metrics` and pick a counter, for example
   `curl -s http://127.0.0.1:8484/metrics | grep <series>` (use your own port).
2. Look the same series up in the collector's backend. Names carry the same
   instrument names; the push copy also has the resource attributes
   `service.name`, `service.version`, `service.instance.id` and
   `deployment.environment`, which `/metrics` carries on `target_info`.
3. Counters are cumulative on both, so the values converge after one push
   interval; the OTLP copy can lag by up to `otel_metrics_otlp_interval`.

## Turn it off

Clear `otel_metrics_otlp_endpoint` (unset the environment variable) and
restart. No code revert is needed and `/metrics` is untouched.

## Troubleshooting

- **No data at the collector.** The gRPC dial is non-blocking, so an
  unreachable collector is not a start-up error. Failed exports appear as
  `OpenTelemetry export error (rate limited)` lines: the first, then at most
  one per 10 minutes with a `suppressed_since_last` count. Check reachability
  and TLS: a `host:port` endpoint without `otel_metrics_otlp_insecure=true`
  uses TLS and will fail against a plaintext receiver.
- **Error line at start-up with `otlp_metrics_error`.** The endpoint was
  malformed; fix the value. The server is running with `/metrics` only.
- **Interval seems ignored.** A bare number is milliseconds (`30` is 30ms, clamped up to 5s; use `30000`). Values below 5s or above 1h are clamped; the
  start-up line carries `otlp_metrics_interval_note` when that happened.
