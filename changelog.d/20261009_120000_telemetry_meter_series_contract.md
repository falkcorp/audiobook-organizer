### Added

- `telemetry.Meter("<area>")` hands any package an OTel meter (scope `audiobook-organizer/<area>`, versioned), with a single views table (`internal/telemetry/views.go`) declaring the bucket list of every histogram and an attribute-key allowlist (`internal/telemetry/attr.go`).
- A `/metrics` series contract: `internal/telemetry/contract/testdata/series.golden` pins the name, type and label names of all 72 exported families (the 69 `client_golang` families plus the three `http_server_*` histograms otelgin already emits), and `TestSeriesContract` fails, naming the family, when one is renamed, retyped, relabelled, dropped or added without a golden update. The five AI series names planned for 11-PR7 are reserved.

### Changed

- OTel series on `/metrics` no longer carry `otel_scope_name`, `otel_scope_version` or `otel_scope_schema_url` labels; today that affects only otelgin's `http_server_*` histograms, which no dashboard or alert reads.
- The OTel resource reports the real service version (build info, else the linked `main.version`) instead of a hard-coded `0.221.0`, and gains a per-process `service.instance.id` (no hostname) and `deployment.environment`.
