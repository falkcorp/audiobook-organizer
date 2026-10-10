- [ ] **OPSMETRICS-SOAK-CLEANUP** After the soak ends on 2026-11-09, delete the legacy
      per-run gauges `op_items_processed` / `op_items_total` and their
      `metrics.SetOpProgress` / `metrics.ClearOpProgress` helpers
      (`internal/metrics/metrics.go`), the old alerts
      `AudiobookOrganizerOpFailuresHigh` and `AudiobookOrganizerOpStalled`
      (`deploy/prometheus/alert-rules.yml`), and the first target of the
      "In-flight op progress" panel in the overnight dashboard. First run
      `grep -rn 'op_items_processed\|op_items_total' deploy internal` and
      confirm nothing else reads them. Replacement series (11-PR4):
      `audiobook_organizer_ops_items_total`, `audiobook_organizer_ops_runs_total`.
