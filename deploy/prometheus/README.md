<!-- file: deploy/prometheus/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: e3f4a5b6-c7d8-4e9f-0a1b-2c3d4e5f6a7b -->
<!-- last-edited: 2026-07-03 -->

# Prometheus scrape config + alert rules (OPS-4, OPS-5)

This directory contains **examples/snippets**, not a turnkey Prometheus
install. audiobook-organizer serves `/metrics` (unauthenticated,
LAN-only — see the accepted-risk comment in
`internal/server/server_lifecycle.go` referencing pen-test finding MED-1),
but nothing in this repo scrapes it or alerts on it. These files close that
gap by giving an operator something to plug into their own
Prometheus + Alertmanager install.

- `scrape-config.yml` — a `scrape_configs:` entry to merge into an existing
  `prometheus.yml`.
- `alert-rules.yml` — a `groups:` rule file covering operation failure
  rate, AI-backend availability, memory pressure (tied to the real 12G
  `MemoryMax` cgroup limit), service down/unreachable, and disk space (the
  disk rule requires `node_exporter`, called out explicitly in the file).

Neither file is installed, run, or evaluated by this repo. See
[`docs/system/runbooks.md`](../../docs/system/runbooks.md), section
"Monitoring & Alerting Runbook", for the install/merge/reload procedure.
