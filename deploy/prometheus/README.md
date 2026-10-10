<!-- file: deploy/prometheus/README.md -->
<!-- version: 1.3.1 -->
<!-- guid: e3f4a5b6-c7d8-4e9f-0a1b-2c3d4e5f6a7b -->
<!-- last-edited: 2026-10-10 -->

# Prometheus scrape config + alert rules (OPS-4, OPS-5)

This directory contains **examples/snippets**, not a turnkey Prometheus
install. audiobook-organizer serves `/metrics`, but nothing in this repo
scrapes it or alerts on it. These files close that gap by giving an operator
something to plug into their own Prometheus + Alertmanager install.

> **`/metrics` now requires authentication.** It was previously anonymous as an
> accepted risk (pen-test MED-1) on two grounds, both of which turned out to be
> wrong: that Prometheus cannot authenticate — it can, via
> [`authorization` / `bearer_token_file` / `basic_auth` / `oauth2`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/) —
> and that the endpoint would be restricted at the network layer instead. That
> restriction was never built; the server listens on `0.0.0.0`, so `/metrics`
> was readable by any host on the LAN without a credential.
>
> Scrape it with an `abk_` API key as a bearer token. `scrape-config.yml` shows
> the exact stanza and how to store the key so rotation needs no config reload.
> Any authenticated identity may read it — the payload is aggregate counters and
> Go runtime stats, with no per-book or per-user labels.

- `scrape-config.yml` — a `scrape_configs:` entry to merge into an existing
  `prometheus.yml`.
- `alert-rules.yml` — a `groups:` rule file covering operation failure
  rate, AI-backend availability, memory pressure (tied to the real 12G
  `MemoryMax` cgroup limit), service down/unreachable, and disk space (the
  disk rule requires `node_exporter`, called out explicitly in the file).
- `recording-rules.yml` — a `groups:` rule file with the
  `audiobook_organizer_compat` group (currently
  `audiobook_organizer:ops_items_by_op_type:sum`, per-operation item totals
  under the old `op_type` label name).

**Dual-rule soak (11-PR4).** Operation metrics now exist as OTel instruments
(`audiobook_organizer_ops_runs_total`, `..._ops_run_duration_seconds`,
`..._ops_items_total`, `..._ops_inflight`, `..._ops_progress_age_seconds`,
`..._ops_progress_timeout_seconds`; note
the plural `ops`, which is not the legacy per-run gauge `op_items_total`).
`alert-rules.yml` carries `AudiobookOrganizerOpFailuresHighV2` and
`AudiobookOrganizerOpStalledV2` beside the two original rules, and the legacy
series are still emitted. Both sets run until 2026-11-09; then the old rules
and the legacy gauges are deleted in a follow-up (see `todo.d/`).

When comparing the two stall rules during the soak, note that they measure
different things. The old rule fires on zero item throughput per run. V2
fires when a def's most idle executing run has not called `UpdateProgress`
for longer than that def's own silence budget
(`..._ops_progress_timeout_seconds`, the effective `ProgressTimeout`), with a
30-minute floor, and a heartbeat that repeats the same value counts as
progress. So V2 asks "did the watchdog fail to stop this run?": a 5-minute
default def pages after 30 minutes of silence, while a LivenessNone def that
declares 2 hours (`scheduler.dedup-llm-review`) stays quiet for those 2 hours. V2 is therefore expected to stay quiet where the old rule fires on
phases that report a constant value, such as the pre-organize database backup
at 0/0 or the iTunes hash pass pinned at total/total. Both rules are
pre-empted by the registry watchdog, which cancels a run that is silent for
longer than its def's `ProgressTimeout` (5 minutes by default, up to 2 hours
for the LivenessNone defs: `scheduler.dedup-llm-review` 2h;
`maintenance.trash-cleanup`, `scheduler.cleanup-old-backups`,
`maintenance.temp-file-cleanup`, AI journal prune and recompact activity digests
1h; `maintenance.library-optimize` 65m, the temp-file-cleanup child's 1h plus
5m; `maintenance.activity-reclaim` and `maintenance.optimize-activity-db`
45m). The old rule can fire inside those budgets on a def that reports a start
marker and then goes quiet; V2 does not, which is an expected soak
difference.

`alert-rules.test.yml` holds `promtool test rules` cases for V2 (a 2h-budget
def silent 60 minutes stays quiet; a default def silent 40 minutes fires). Run
`promtool test rules alert-rules.test.yml` from this directory; the Go test
`internal/opsmetrics` `TestPromRules_Promtool` runs it when `promtool` is on
`PATH`.

Neither file is installed, run, or evaluated by this repo. See
[`docs/system/runbooks.md`](../../docs/system/runbooks.md), section
"Monitoring & Alerting Runbook", for the install/merge/reload procedure.
