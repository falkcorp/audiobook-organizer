### Added

- Optional OTLP/gRPC metric push behind four config keys (`otel_metrics_otlp_endpoint`, `otel_metrics_otlp_interval`, `otel_metrics_otlp_insecure`, `telemetry_environment`). It is off by default, never stops the server from starting, and never reuses the trace endpoint; `/metrics` is unchanged. See `deploy/grafana/METRICS-RUNBOOK.md`.

### Fixed

- The OTLP metric push no longer inherits the trace exporter's `OTEL_EXPORTER_OTLP_*` environment: the transport, headers and cumulative temporality are pinned, `OTEL_METRIC_EXPORT_INTERVAL` accepts the standard integer-milliseconds form, failed exports are logged through a rate-limited handler, and shutdown is bounded to 5s.
