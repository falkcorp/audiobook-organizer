### Added

- Optional OTLP/gRPC metric push behind four config keys (`otel_metrics_otlp_endpoint`, `otel_metrics_otlp_interval`, `otel_metrics_otlp_insecure`, `telemetry_environment`). It is off by default, never stops the server from starting, and never reuses the trace endpoint; `/metrics` is unchanged. See `deploy/grafana/METRICS-RUNBOOK.md`.
