<!-- file: deploy/grafana/METRICS-RUNBOOK.md -->
<!-- version: 1.0.0 -->
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
  export costs the OTLP copy only. The server starts, `/metrics` keeps
  serving, and the only trace of a configuration problem is one error-level
  log line: `OpenTelemetry initialized with the OTLP metric push OFF`, with an
  `otlp_metrics_error` attribute.
- **No fallback.** The trace endpoint (`otel_exporter_otlp_endpoint`) is never
  reused for metrics. Setting only the trace endpoint leaves metric push off.
- gRPC only, cumulative temporality, no scope labels.

## The four keys

| Config key | Environment variable | Default | Meaning |
|---|---|---|---|
| `otel_metrics_otlp_endpoint` | `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | empty (off) | OTLP/gRPC collector |
| `otel_metrics_otlp_interval` | `OTEL_METRIC_EXPORT_INTERVAL` | `60s` | Push period, a Go duration; clamped to 5s..1h, an unparsable value becomes 60s |
| `otel_metrics_otlp_insecure` | `OTEL_EXPORTER_OTLP_METRICS_INSECURE` | `false` | Plaintext gRPC to a bare `host:port` |
| `telemetry_environment` | none | `prod` | `deployment.environment` resource attribute on both surfaces |

Accepted endpoint forms:

- `http://collector.example.invalid:4317`: plaintext gRPC.
- `https://collector.example.invalid:4317`: TLS.
- `collector.example.invalid:4317` or `dns:///collector.example.invalid:4317`:
  TLS, unless `otel_metrics_otlp_insecure` is true (bare `host:port` only).

A URL needs both a host and a port; `http://host` is rejected and logged.

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

- **No data at the collector, no error logged.** The gRPC dial is
  non-blocking, so an unreachable collector is not a start-up error. Check
  reachability and TLS: a `host:port` endpoint without
  `otel_metrics_otlp_insecure=true` uses TLS and will fail against a
  plaintext receiver.
- **Error line at start-up with `otlp_metrics_error`.** The endpoint was
  malformed; fix the value. The server is running with `/metrics` only.
- **Interval seems ignored.** Values below 5s or above 1h are clamped; the
  start-up line carries `otlp_metrics_interval_note` when that happened.
