<!-- file: deploy/grafana/METRICS-RUNBOOK.md -->
<!-- version: 1.5.0 -->
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
  with an `otlp_metrics_error` attribute. A
  failing export is logged by a rate-limited handler keyed by error class (the
  message with digits normalised): each distinct error is logged at least once
  per 10 minutes and repeats of the same one are counted
  (`OpenTelemetry error (rate limited)`, with a `suppressed_since_last` count).
  At most 32 classes are tracked, then new ones share one bucket. The handler
  is process-wide, so it also covers trace-export errors.
- **No fallback.** The trace endpoint (`otel_exporter_otlp_endpoint`) is never
  reused for metrics. Setting only the trace endpoint leaves metric push off.
- gRPC only, cumulative temporality, no scope labels.
- **Isolated from the trace exporter's environment.** The OTel SDK reads the
  generic `OTEL_EXPORTER_OTLP_*` variables, which the trace exporter also
  uses. The metric push pins what matters, so none of them can change it:
  the transport (see "Transport rule"), headers (always empty, so a trace
  collector's `OTEL_EXPORTER_OTLP_HEADERS` bearer token is never sent to the
  metric host), temporality (always cumulative, whatever
  `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE` says) and histogram
  aggregation (the SDK default, whatever
  `OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION` says).

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

Accepted endpoint forms (the same set for `otel_metrics_otlp_endpoint` and
`otel_exporter_otlp_endpoint`; the scheme is case-insensitive and surrounding
whitespace is trimmed):

- `collector.example.invalid:4317` or `[2001:db8::1]:4317`: bare `host:port`,
  TLS unless `otel_metrics_otlp_insecure` is true.
- `http://collector.example.invalid:4317`: plaintext gRPC.
- `https://collector.example.invalid:4317`: TLS. For `http(s)` one trailing
  `/` is allowed.
- `dns:///collector.example.invalid:4317`: TLS unless
  `otel_metrics_otlp_insecure` is true.

The host is a DNS name (letters, digits, `.` and `-`, not starting or ending
with `.` or `-`, at most 253 bytes) or a bracketed IPv6 address. **The port is
required** and must be a number from 1 to 65535.

**Everything else is refused, not repaired**: userinfo or any `@`, `?`, `#`,
`%`, any path other than that one trailing `/`, whitespace inside the value,
and any other scheme. A refused metrics endpoint turns the OTLP push OFF and a
refused trace endpoint turns tracing OFF, each with one error-level start-up
line carrying a fixed message that names the config key and quotes none of the
value (`... is not a valid OTLP/gRPC endpoint (expected host:port,
http(s)://host:port or dns:///host:port)`); the server keeps running. The
endpoint is rebuilt from the validated scheme, host and port, and that
canonical string is both what is dialled and what the start-up line shows
(`endpoint`, plus `otlp_metrics_endpoint` when the push is on; `(invalid)` for
a refused one). OTLP/gRPC needs nothing else, and credentials belong in
headers or TLS, never in the endpoint. Third-party error text that reaches the
error handler is additionally scrubbed of URL userinfo, path, query and
fragment as a last line of defence.

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
- Still follows the environment, on purpose (benign: they change encoding and
  deadlines, not where data goes or what is sent):
  `OTEL_EXPORTER_OTLP_[METRICS_]COMPRESSION`, `OTEL_EXPORTER_OTLP_[METRICS_]TIMEOUT`
  and `OTEL_METRIC_EXPORT_TIMEOUT`.
- Pinned (the environment cannot change them): transport and TLS material,
  headers, temporality and histogram aggregation.
- **A malformed `OTEL_EXPORTER_OTLP_HEADERS` is printed verbatim.** The OTel
  SDK's internal logger reports a header value it cannot parse, token
  included, through a path that bypasses this service's error handler and
  redaction. Keep that variable well-formed (`key=value,key2=value2`), or
  unset when not needed, and treat a log line mentioning it as sensitive.

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
  `OpenTelemetry error (rate limited)` lines: each distinct error at least
  once, then at most one per 10 minutes per error with a
  `suppressed_since_last` count. Check reachability
  and TLS: a `host:port` endpoint without `otel_metrics_otlp_insecure=true`
  uses TLS and will fail against a plaintext receiver.
- **Error line at start-up with `otlp_metrics_error`.** The endpoint was
  malformed; fix the value. The server is running with `/metrics` only.
- **Interval seems ignored.** A bare number is milliseconds (`30` is 30ms, clamped up to 5s; use `30000`). Values below 5s or above 1h are clamped; the
  start-up line carries `otlp_metrics_interval_note` when that happened.
