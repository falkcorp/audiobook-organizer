### Added

#### Observability for the overnight run: parse, metadata fetch, review page, number-leading titles, fixer durations, Grafana dashboard

New Prometheus instruments in `internal/metrics` (all `audiobook_organizer_*`):
`filename_parse_total{shape,outcome}` (instrument + bounded label constants; call
sites arrive with the number-leading-titles fixer), `metadata_fetch_total{provider,source}`
wired at every fetch-cache check in `internal/metafetch` (`cache_hit`/`cache_miss`) and at
the live provider call in `metadata.ProtectedSource` plus the by-hand ASIN lookup
(`network`/`error`), `review_index_request_seconds{view}` observed around
`GET /audiobooks/metadata/cache/review`, `number_leading_titles` computed in the same
scan as `books_total` using the new shared predicate `titleutil.IsNumberLeadingTitle`
with its exported allowlist `titleutil.LegitNumberTitles`, and
`fixer_duration_seconds{fixer,phase}` around `repairs.RunPlan`/`RunApply` because
`operation_duration_seconds{type}` pools every fixer under `repairs.plan`/`repairs.apply`.

`deploy/grafana/` holds the "Audiobook Organizer — Overnight" dashboard
(`dashboards/audiobook-organizer-overnight.json`, uid `aorg-overnight`), a file
provider, install notes for the server's existing dashboard directory, and
`TRACING-RUNBOOK.md`: ready-to-run commands for adding Grafana Tempo to the Loki+Alloy
Swarm stack and pointing the app at it (not executed; tracing stays off).

### Fixed

#### Operation lifecycle metrics were defined but never observed

`operations_{started,completed,failed,canceled}_total` and
`operation_duration_seconds` had helpers in `internal/metrics` that nothing called, so
the `deploy/prometheus` alert rules on them could never fire. `registry/worker.go` now
records every finished run attempt on both the in-process and subprocess paths, keyed
by op def_id, and the duration buckets were widened from 50 ms–3.4 s to 0.1 s–24 h.

#### OTel meter provider no longer gated behind the trace endpoint

`telemetry.InitOTEL` returned a no-op whenever `OTEL_EXPORTER_OTLP_ENDPOINT` was empty
(the production default), which also skipped the Prometheus-exporting meter provider.
Metrics and tracing now initialize independently (`Config.MetricsEnabled`,
`Config.TracingEnabled`); the exporter is registered once per process with an
idempotent shutdown.
