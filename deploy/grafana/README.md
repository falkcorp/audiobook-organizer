<!-- file: deploy/grafana/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: b0b4ed29-c562-4f65-b437-bea04f3dd564 -->
<!-- last-edited: 2026-10-03 -->

# Grafana dashboards + provisioning

Dashboards for the Prometheus metrics audiobook-organizer serves on `/metrics`
(scraped as job `audiobook-organizer`; see
[`../prometheus/scrape-config.yml`](../prometheus/scrape-config.yml)).

| File | What |
|---|---|
| `dashboards/audiobook-organizer-overnight.json` | "Audiobook Organizer — Overnight" (uid `aorg-overnight`): review-page p50/p95, number-leading titles, filename parse by shape, metadata fetch by provider/source, repairs/maintenance op and fixer durations, `books_total`. |
| `provisioning/dashboards/audiobook-organizer.yaml` | A Grafana file-provider pointing at `/var/lib/grafana/dashboards`, for a host with no dashboard provider yet. |
| `TRACING-RUNBOOK.md` | How to stand up Grafana Tempo next to the Loki+Alloy stack and turn on OTLP tracing. **Not done tonight**; tracing stays off in prod. |

## Why the dashboard JSON has no file header

Every other file in this repo carries a `file / version / guid / last-edited`
header. JSON has no comment syntax, and Grafana rejects unknown top-level keys
on import in some versions and silently keeps them in others — either way a
`_header` object would be shipped into Grafana's database and shown to the
operator. `deploy/prometheus/` has no JSON precedent to follow, so the
dashboard JSON is header-free on purpose; its identity lives in the
dashboard's own `uid` (`aorg-overnight`), `version`, and the `description`
field, which names this directory. Bump `version` inside the JSON when you
change it.

## Install on the server

Grafana 12.2.1 runs as a systemd service (`grafana-server`) on the server, and
already provisions dashboards from a directory:

```yaml
# /etc/grafana/provisioning/dashboards/dashboard-provider.yaml (already present)
providers:
  - name: 'local-dashboards'
    type: file
    updateIntervalSeconds: 30
    allowUiUpdates: true
    options:
      path: /mnt/bigdata/config/grafana/dashboards
      foldersFromFilesStructure: true
```

So there is nothing to provision — copy the JSON into that directory and
Grafana picks it up within 30 s. From a checkout on your Mac:

```bash
# 1. Copy the dashboard. A subdirectory becomes a Grafana folder
#    (foldersFromFilesStructure: true).
ssh unimatrixzero.local 'mkdir -p /mnt/bigdata/config/grafana/dashboards/audiobook-organizer'
scp deploy/grafana/dashboards/audiobook-organizer-overnight.json \
    'unimatrixzero.local:/mnt/bigdata/config/grafana/dashboards/audiobook-organizer/'

# 2. Make sure the grafana user can read it (the directory is jdfalk-owned,
#    setgid, mode 7775; files land group-readable, which is enough).
ssh unimatrixzero.local 'ls -l /mnt/bigdata/config/grafana/dashboards/audiobook-organizer/'

# 3. Wait ≤30 s, then open it. Grafana's root_url is behind the media host:
#    https://media.jdfalk.com/grafana/d/aorg-overnight
#    (or http://<server>:3000/d/aorg-overnight on the LAN)
```

The dashboard declares a datasource variable `DS_PROMETHEUS` (type
`datasource`, plugin `prometheus`). On first open pick the server's Prometheus
datasource in the top-left selector; Grafana remembers it per-dashboard. There
is no provisioned Prometheus datasource file on the server
(`/etc/grafana/provisioning/datasources/` has only `loki.yaml`), so the
datasource is whatever was created in the UI — if there is none yet, add one at
Connections → Data sources → Prometheus with URL `http://localhost:9090`.

Updating: overwrite the JSON file; the provider re-reads it. Because
`allowUiUpdates: true`, a dashboard edited in the UI is NOT overwritten until
the file's `version` is higher than the saved one — bump it.

## Install on a host without a dashboard provider

```bash
sudo install -d -o grafana -g grafana /var/lib/grafana/dashboards
sudo install -o grafana -g grafana -m 0644 \
    deploy/grafana/dashboards/audiobook-organizer-overnight.json /var/lib/grafana/dashboards/
sudo install -o root -g grafana -m 0640 \
    deploy/grafana/provisioning/dashboards/audiobook-organizer.yaml /etc/grafana/provisioning/dashboards/
sudo systemctl restart grafana-server
```

## Metrics the dashboard reads

All under the `audiobook_organizer_` namespace; defined in
`internal/metrics/metrics.go` and `internal/metrics/pipeline_metrics.go`.

| Metric | Type | Labels | Wired at |
|---|---|---|---|
| `review_index_request_seconds` | histogram, 0.1–120 s | `view` = `index` \| `full` | `handlers.GetCacheReviewResults` (defer) |
| `number_leading_titles` | gauge | — | `PebbleStore.countPrimaryBooksScan`, same pass as `books_total`; predicate `titleutil.IsNumberLeadingTitle` |
| `filename_parse_total` | counter | `shape`, `outcome` | instrument only; call sites come with the fixer branch |
| `metadata_fetch_total` | counter | `provider`, `source` = `cache_hit` \| `cache_miss` \| `network` \| `error` | cache_hit/miss: `metafetch` fetch-cache checks (`service_fetch.go`, `search_fanout.go`, `source_chain_walk.go`); network/error: `metadata.ProtectedSource.recordOutcome`, `metafetch.lookupASIN` |
| `operation_duration_seconds` | histogram, 0.1 s–24 h | `type` (op def_id) | `registry/worker.go` `recordRunMetrics`, both completion paths |
| `operations_{started,completed,failed,canceled}_total` | counter | `type` | same |
| `fixer_duration_seconds` | histogram, 0.1 s–2 h | `fixer`, `phase` = `plan` \| `apply` | `repairs.RunPlan` / `repairs.RunApply` |
| `books_total`, `search_index_docs_total`, `op_items_processed` | gauge | — / `op_id`,`op_type` | pre-existing |
