### Added

- `internal/opsmetrics`: OTel instruments for operation runs. The v2 registry now
  records `audiobook_organizer_ops_runs_total{def_id,outcome}`,
  `audiobook_organizer_ops_run_duration_seconds{def_id,outcome}`,
  `audiobook_organizer_ops_items_total{def_id,outcome}` and two observable gauges,
  `audiobook_organizer_ops_inflight{def_id,plugin}` and
  `audiobook_organizer_ops_progress_age_seconds{def_id,plugin}` (seconds since
  the most idle executing run last reported progress), both read from the
  registry's running set at scrape time and unregistered on shutdown. Items are
  counted per progress phase, so a multi-phase op whose later phases restart at
  0 still counts them, and a resumed attempt does not count its checkpoint
  again. `def_id` comes from a
  closed set filled by `RegisterOp`; an unregistered id is recorded as `other`,
  and no per-run id is ever a label.
- `deploy/prometheus`: alert rules `AudiobookOrganizerOpFailuresHighV2` (per
  `def_id`) and `AudiobookOrganizerOpStalledV2` (on progress age, so a phase
  that heartbeats a constant value is not reported as stalled) and a
  `recording-rules.yml` file. They run
  beside the two old rules for a 30-day soak (until 2026-11-09). The legacy
  `operations_*_total`, `operation_duration_seconds`, `op_items_processed` and
  `op_items_total` series are still emitted unchanged.
