<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-21-metrics-scrape-alerting.md -->
<!-- version: 1.0.0 -->
<!-- guid: aede36e2-91a9-4adc-a985-7f25769fa3b8 -->
<!-- last-edited: 2026-07-03 -->

# TASK-21 — Scrape /metrics + minimal alerting (OPS-4, OPS-5)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · **Wave:** 2 · **Depends on:** TASK-08

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-21-metrics-scrape-alerting" -b agent/cr-21-metrics-scrape-alerting origin/main
cd "$REPO/.worktrees/cr-21-metrics-scrape-alerting"
git rebase origin/main
```

## Goal

Close OPS-4/OPS-5: `/metrics` is served but nothing scrapes it and there is no
alerting layer at all — OpenAI quota exhaustion, the 69GB cache-warmup memory
bloat, and op wedges were all discovered by a human noticing symptoms, not by
an alert. Deliver a minimal, self-hostable Prometheus scrape config + Alertmanager
rule set committed under `deploy/`, a runbook for installing them on
`<server>`, and (only if genuinely absent after verification) a small gauge
exporting AI-backend reachability. Keep it self-hostable — no SaaS/hosted
monitoring product.

## Background (verify before editing)

- `/metrics` is registered unauthenticated at `internal/server/server_lifecycle.go`
  inside `setupRoutes()`: `s.router.GET("/metrics", gin.WrapH(promhttp.Handler()))`,
  with an accepted-risk comment referencing pen-test finding MED-1 (LAN-only,
  no auth). Do **not** add auth here — that decision is already made and
  documented; this task is scrape+alert, not endpoint hardening.
- `internal/metrics/metrics.go` already registers the metrics you need for
  alerting, so this is mostly config authoring, not new instrumentation:
  - `operations_failed_total{type}` (Counter) — op failure count. Already
    exists; alert on `rate()` of this, do not add a duplicate.
  - `process_memory_alloc_bytes` (Gauge, `SetMemoryAlloc`) — Go-runtime alloc.
  - The Prometheus client library auto-registers `process_resident_memory_bytes`
    and Go-collector metrics (`go_goroutines`, `go_memstats_*`) at package
    `init()` in `prometheus.DefaultRegisterer` — verify with the grep below
    before assuming you need to add RSS yourself.
  - There is **no** metric today for AI-backend (Ollama) reachability. The
    closest existing signal is `EmbeddingClient.SetOllamaAvailable(bool)` in
    `internal/ai/embedding_client.go`, called once from
    `internal/server/server.go` (`ollamaOK := server.toolRegistry.Available("ollama") || config.AppConfig.Embedding.BaseURL != ""; server.embedClient.SetOllamaAvailable(ollamaOK)`)
    — this only sets an in-memory bool, it is not exported as a gauge. Add a
    `NewGaugeVec` (e.g. `ai_backend_available{backend}`, values 0/1) in
    `internal/metrics/metrics.go` with a `SetBackendAvailable(backend string, ok bool)`
    helper, and call it from the same `server.go` call site right alongside the
    existing `SetOllamaAvailable` call (label value `"ollama"`). Keep this
    addition small — one gauge, one setter, one call site. Do not build a
    polling/health-check loop; this task reuses the availability signal that
    already exists at server-init time.
  - `deploy/` currently contains only systemd/launchd unit files and
    `local.conf` (gitignored) — no Prometheus scrape config, no Grafana, no
    Alertmanager, no healthcheck cron anywhere in the repo. Confirm with the
    grep below.
  - `deploy/audiobook-organizer.service` sets `MemoryMax=12G` and
    `GOMEMLIMIT=9GiB` — use these as the basis for the memory alert threshold
    (e.g. alert when `process_resident_memory_bytes` exceeds ~90% of 12G, i.e.
    ~11.1e9 bytes).
  - `docs/system/runbooks.md` already exists (Deploy Runbook, Systemd service
    sections) with a versioned header and a `<server>` production-host
    convention — append a new `## Monitoring & Alerting Runbook` section to it
    rather than creating a separate doc.
  - Prod already runs on `<server>` — target that host as
    `<server>:8484` in the scrape config's `targets:` (adjust the port to
    match whatever `--port` prod actually uses; verify via
    `grep -rn "\-\-port\|ExecStart=" deploy/local.conf 2>/dev/null` if that
    gitignored file exists locally, otherwise default to `8484` per the
    systemd unit's `ExecStart`).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "s.router.GET(\"/metrics\"" internal/server/server_lifecycle.go
  grep -n "func Register\|MustRegister\|NewGaugeVec\|operationFailed\|SetMemoryAlloc" internal/metrics/metrics.go
  grep -n "SetOllamaAvailable" internal/ai/embedding_client.go internal/server/server.go
  grep -rn "prometheus\|grafana\|alertmanager\|scrape" deploy/ 2>/dev/null
  grep -n "MemoryMax\|GOMEMLIMIT" deploy/audiobook-organizer.service
  ```
  Confirm the auto-registered process/go collector metrics exist without any
  in-repo code (this is a client_golang library default, not something this
  repo defines):
  ```bash
  grep -n "NewProcessCollector\|NewGoCollector" $(go env GOPATH)/pkg/mod/github.com/prometheus/client_golang*/prometheus/registry.go 2>/dev/null || echo "vendor path differs; confirm via 'go doc github.com/prometheus/client_golang/prometheus.init' or by curling /metrics on a running instance and grepping for process_resident_memory_bytes"
  ```

## Step-by-step

1. Add the backend-availability gauge (small, additive):
   - In `internal/metrics/metrics.go`, add
     `aiBackendAvailable = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "audiobook_organizer", Name: "ai_backend_available", Help: "1 if the named AI backend was reachable/available at last check, else 0"}, []string{"backend"})`,
     register it in `Register()`, and add
     `func SetBackendAvailable(backend string, ok bool) { v := 0.0; if ok { v = 1.0 }; aiBackendAvailable.WithLabelValues(backend).Set(v) }`.
   - In `internal/server/server.go`, immediately after the existing
     `server.embedClient.SetOllamaAvailable(ollamaOK)` line, add
     `metrics.SetBackendAvailable("ollama", ollamaOK)` (import the metrics
     package if not already imported in that file — check first).
2. Create `deploy/prometheus/scrape-config.yml` — a minimal, commented
   Prometheus `scrape_configs` snippet targeting `<server>:8484` on the
   `/metrics` path, `scrape_interval: 30s`, job name `audiobook-organizer`.
   Include a comment explaining this is a **snippet** to merge into an
   existing `prometheus.yml`, not a standalone runnable file.
3. Create `deploy/prometheus/alert-rules.yml` — Prometheus alerting rules
   (`groups:` format) covering at minimum:
   - `AudiobookOrganizerOpFailuresHigh` — `rate(audiobook_organizer_operations_failed_total[15m]) > <threshold>` (pick a conservative threshold, e.g. `0.1` per second sustained, document the reasoning in a comment), `for: 10m`, severity warning.
   - `AudiobookOrganizerBackendUnavailable` — `audiobook_organizer_ai_backend_available == 0`, `for: 5m`, severity warning. Comment that this covers OPS-4's "OpenAI/local backend down" case.
   - `AudiobookOrganizerMemoryHigh` — `process_resident_memory_bytes > 1.1e10` (~90% of the 12G `MemoryMax` cgroup limit), `for: 10m`, severity critical. Comment referencing `MemoryMax=12G` in `deploy/audiobook-organizer.service`.
   - `AudiobookOrganizerDown` — standard `up{job="audiobook-organizer"} == 0`, `for: 2m`, severity critical (covers "service restarts"/reachability; a restart shows up as a scrape gap here since there's no restart-count metric — call this out as the practical proxy in a comment).
   - `AudiobookOrganizerDiskLow` — `node_filesystem_avail_bytes{mountpoint="/var/lib/audiobook-organizer"} / node_filesystem_size_bytes{mountpoint="/var/lib/audiobook-organizer"} < 0.1` guarded with a comment that this rule requires `node_exporter` to also be scraped (out of scope to install here — call it out as a prerequisite, do not silently assume it exists).
4. Create `deploy/prometheus/README.md` (or extend `deploy/README.md` if one
   exists — check first) documenting: what these two files are, that they are
   snippets/examples (not turnkey), and a one-line pointer to the runbook
   section for the install procedure.
5. Append a `## Monitoring & Alerting Runbook` section to
   `docs/system/runbooks.md` (after the existing sections — do not reorder or
   rewrite existing content) covering:
   - Prerequisite: a Prometheus + Alertmanager install on `<server>` (or a
     separate host that can reach it) — this task does not install Prometheus
     itself, only ships the config to plug into one.
   - How to merge `deploy/prometheus/scrape-config.yml` into that
     Prometheus's `prometheus.yml` and reload (`curl -X POST
     http://localhost:9090/-/reload` or `systemctl reload prometheus`).
   - How to load `deploy/prometheus/alert-rules.yml` as a rule file and wire
     Alertmanager (mention a minimal Alertmanager receiver, e.g. email or a
     self-hosted webhook — explicitly no SaaS like PagerDuty/Opsgenie unless
     the operator already has one).
   - A note that `AudiobookOrganizerDiskLow` requires `node_exporter` as a
     prerequisite, with a one-line pointer to installing it (`apt install
     prometheus-node-exporter` or equivalent) — do not install it as part of
     this task.
6. Bump the file header on every file you touch (version bump + `last-edited`
   date) per `.standards/instructions/file-headers.md`, including
   `internal/metrics/metrics.go`, `internal/server/server.go`, and
   `docs/system/runbooks.md`.

## How to test

```bash
go build ./...
go test ./internal/metrics/... ./internal/server/... -count=1
go vet ./internal/metrics/... ./internal/server/...
# Validate the Prometheus rule file syntax if promtool is available locally
# (skip gracefully if not installed — do not add it as a new dependency):
command -v promtool >/dev/null && promtool check rules deploy/prometheus/alert-rules.yml || echo "promtool not installed locally; rule syntax reviewed by hand"
```

## Acceptance criteria

- [ ] `internal/metrics/metrics.go` exports an `ai_backend_available{backend}`
      gauge and a `SetBackendAvailable` helper, registered in `Register()`.
- [ ] `internal/server/server.go` calls `metrics.SetBackendAvailable("ollama", ollamaOK)`
      at the same point it calls `SetOllamaAvailable`.
- [ ] `deploy/prometheus/scrape-config.yml` and `deploy/prometheus/alert-rules.yml`
      exist, are valid YAML, and (if `promtool` is available) pass
      `promtool check rules`.
- [ ] Alert rules cover: op failure rate, AI-backend availability, memory
      pressure (tied to the real `MemoryMax=12G` limit), service
      down/unreachable, and disk (with the `node_exporter` prerequisite called
      out explicitly, not silently assumed).
- [ ] `docs/system/runbooks.md` has a new "Monitoring & Alerting Runbook"
      section with install/reload steps; existing sections are untouched.
- [ ] `go build ./...`, targeted `go test`, and `go vet` are all clean.
- [ ] File headers bumped on every changed file.
- [ ] No SaaS monitoring dependency introduced; everything is self-hostable
      config/docs.

This task ships config and docs only — it does not install Prometheus/Alertmanager
on `<server>` itself and does not require prod access. No owner-greenlight
gate is needed beyond normal PR review (no prod-data mutation, no dry-run
report required).

## Commit message

```
feat(observability): add Prometheus scrape config + alert rules for /metrics (OPS-4, OPS-5)

/metrics has been served since server_lifecycle.go registered promhttp.Handler,
but nothing scrapes it and there is no alerting layer — OpenAI quota exhaustion,
the 69GB cache-warmup bloat, and op wedges were all discovered by a human
noticing symptoms, not an alert. Add a minimal self-hostable Prometheus
scrape-config snippet and alert rules (op failures, AI-backend availability,
memory vs the 12G MemoryMax limit, service down, disk) under deploy/, a small
ai_backend_available gauge to make backend reachability alertable, and a
runbook section for installing them.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-21-metrics-scrape-alerting
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/metrics/metrics.go` already exports a backend-availability gauge
(grep for `ai_backend_available` or any `*_available` GaugeVec) and
`deploy/prometheus/` already contains a scrape config and alert rules, this
task is done — verify with:
```bash
grep -n "available" internal/metrics/metrics.go
ls deploy/prometheus/ 2>/dev/null
grep -n "Monitoring & Alerting Runbook" docs/system/runbooks.md
```
Rollback = revert the commit. The new gauge and metrics call are purely
additive (no existing metric renamed or removed), so reverting cannot break
any existing scrape or dashboard. The `deploy/prometheus/` files and runbook
section are new, standalone additions with no effect on the running service
until an operator actually installs Prometheus and loads them.
