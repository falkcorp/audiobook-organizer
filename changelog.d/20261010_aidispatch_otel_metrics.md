### Changed

- The five `ai_dispatch_*` metrics are now recorded through OpenTelemetry instead of the Prometheus client library. `/metrics` shows the same names, types, labels and histogram buckets as before, so dashboards and alerts are unaffected.
