### Added

- Optional OTLP/gRPC metric push behind four config keys (`otel_metrics_otlp_endpoint`, `otel_metrics_otlp_interval`, `otel_metrics_otlp_insecure`, `telemetry_environment`). It is off by default, never stops the server from starting, and never reuses the trace endpoint; `/metrics` is unchanged. See `deploy/grafana/METRICS-RUNBOOK.md`.

### Fixed

- The OTLP metric push no longer inherits the trace exporter's `OTEL_EXPORTER_OTLP_*` environment: the transport, headers and cumulative temporality are pinned, `OTEL_METRIC_EXPORT_INTERVAL` accepts the standard integer-milliseconds form, failed exports are logged through a rate-limited handler, and shutdown is bounded to 5s.

### Changed

- OTLP endpoints (`otel_exporter_otlp_endpoint` and `otel_metrics_otlp_endpoint`) are now validated strictly and rebuilt from scheme, host and port. Accepted forms: `host:port`, `http://host:port`, `https://host:port` (one trailing `/` allowed) and `dns:///host:port`, with a DNS-name or bracketed IPv6 host and a port from 1 to 65535. Every other form is refused with one fixed error-level line and the feature stays off (tracing off, or metric push off); the server still starts. Endpoints that carried a path, query, fragment, userinfo or unusual scheme were accepted before and are now refused. The log line shows only the canonical `scheme://host:port`.
