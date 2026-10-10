### Added

- `internal/opsmetrics`: OTel instruments for operation runs. The v2 registry now
  records `audiobook_organizer_ops_runs_total{def_id,outcome}`,
  `audiobook_organizer_ops_run_duration_seconds{def_id,outcome}`,
  `audiobook_organizer_ops_items_total{def_id,outcome}` and the observable gauge
  `audiobook_organizer_ops_inflight{def_id,plugin}` (read from the registry's
  running set at scrape time, unregistered on shutdown). `def_id` comes from a
  closed set filled by `RegisterOp`; an unregistered id is recorded as `other`,
  and no per-run id is ever a label.
- `deploy/prometheus`: alert rules `AudiobookOrganizerOpFailuresHighV2` and
  `AudiobookOrganizerOpStalledV2` and a `recording-rules.yml` file. They run
  beside the two old rules for a 30-day soak (until 2026-11-09). The legacy
  `operations_*_total`, `operation_duration_seconds`, `op_items_processed` and
  `op_items_total` series are still emitted unchanged.
