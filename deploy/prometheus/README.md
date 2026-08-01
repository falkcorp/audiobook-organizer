<!-- file: deploy/prometheus/README.md -->
<!-- version: 1.1.0 -->
<!-- guid: e3f4a5b6-c7d8-4e9f-0a1b-2c3d4e5f6a7b -->
<!-- last-edited: 2026-08-01 -->

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

Neither file is installed, run, or evaluated by this repo. See
[`docs/system/runbooks.md`](../../docs/system/runbooks.md), section
"Monitoring & Alerting Runbook", for the install/merge/reload procedure.
