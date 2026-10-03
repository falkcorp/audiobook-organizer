<!-- file: deploy/grafana/TRACING-RUNBOOK.md -->
<!-- version: 1.0.0 -->
<!-- guid: 327a1efc-1be7-4428-b607-07958c03abb6 -->
<!-- last-edited: 2026-10-03 -->

# Tracing runbook: Grafana Tempo next to the Loki + Alloy stack

**Status: written, not executed.** Tracing is OFF in prod. Nothing below has
been run on the server; every command is ready to paste, and each step says how
to check it worked before the next.

## What exists today (read 2026-10-03 over ssh)

| Piece | Where | How |
|---|---|---|
| Grafana 12.2.1 | systemd `grafana-server`, `:3000`, `root_url https://media.jdfalk.com/grafana` | datasources provisioned from `/etc/grafana/provisioning/datasources/` (`loki.yaml` only) |
| Prometheus 2.53 | systemd, `:9090` | job `audiobook-organizer` scrapes `https://localhost:8484/metrics` with a bearer token |
| Loki + Alloy | **Docker Swarm** stack `loki` (`docker stack ls`), services `loki_loki` (`:3100`) and `loki_alloy` (`:1514`, `:12345`) | stack file `/home/jdfalk/loki.yaml`; overlay network `loki_loki` (not attachable); config under `/mnt/bigdata/config/{loki,alloy}` |
| audiobook-organizer | systemd `audiobook-organizer`, `:8484` | env in `/etc/systemd/system/audiobook-organizer.service.d/local.conf`; `OTEL_EXPORTER_OTLP_ENDPOINT` is unset |

The app side is already done on the `feat/overnight-metrics` branch:
`internal/telemetry.InitOTEL` starts the OTLP/gRPC span exporter whenever
`OTEL_EXPORTER_OTLP_ENDPOINT` is set (metrics no longer depend on it), and
`internal/operations/registry/worker.go` already opens a span per operation run
via `otel.Tracer("audiobook-organizer/operations")`. Setting the endpoint is
the only app change.

> The exporter is `otlptracegrpc` with `WithEndpoint(host:port)` and **no**
> `WithInsecure()`, so it will try TLS. Tempo below listens plain gRPC on 4317.
> Step 5 covers the two options (set `OTEL_EXPORTER_OTLP_TRACES_INSECURE=true`,
> which the OTel SDK honours, or front Tempo with TLS). Verify in step 6 before
> calling it done.

## 1. Tempo config on the shared config volume

Same layout as Loki: a directory under `/mnt/bigdata/config`, bind-mounted
into the container.

```bash
ssh unimatrixzero.local 'sudo install -d -o 10001 -g 10001 /mnt/bigdata/config/tempo /mnt/bigdata/config/tempo/data'
ssh unimatrixzero.local 'sudo tee /mnt/bigdata/config/tempo/tempo.yaml >/dev/null' <<'YAML'
server:
  http_listen_port: 3200

distributor:
  receivers:
    otlp:
      protocols:
        grpc:
          endpoint: 0.0.0.0:4317
        http:
          endpoint: 0.0.0.0:4318

ingester:
  max_block_duration: 5m

compactor:
  compaction:
    block_retention: 168h   # 7 days of traces; raise once disk use is known

metrics_generator:
  registry:
    external_labels:
      source: tempo
  storage:
    path: /var/tempo/generator/wal
    remote_write:
      - url: http://host.docker.internal:9090/api/v1/write   # requires --web.enable-remote-write-receiver on Prometheus; delete this block if you do not want span-metrics
  traces_storage:
    path: /var/tempo/generator/traces

storage:
  trace:
    backend: local
    wal:
      path: /var/tempo/wal
    local:
      path: /var/tempo/blocks

overrides:
  defaults:
    metrics_generator:
      processors: [service-graphs, span-metrics]
YAML
ssh unimatrixzero.local 'sudo chown -R 10001:10001 /mnt/bigdata/config/tempo && ls -la /mnt/bigdata/config/tempo'
```

Tempo runs as uid 10001 in the official image, same as Loki's data dir here.

## 2. Add Tempo to the `loki` stack file

Edit `/home/jdfalk/loki.yaml` (back it up first) and add a third service on
the same overlay network, so Alloy can reach it as `tempo` and the host
publishes 4317/4318/3200.

```bash
ssh unimatrixzero.local 'cp /home/jdfalk/loki.yaml /home/jdfalk/loki.yaml.bak.$(date +%Y%m%d-%H%M%S)'
ssh unimatrixzero.local 'python3 - <<"PY"
import re
p="/home/jdfalk/loki.yaml"
s=open(p).read()
block="""
  tempo:
    image: grafana/tempo:2.6.1
    command: ["-config.file=/etc/tempo/tempo.yaml"]
    user: "10001:10001"
    environment:
      TZ: America/Detroit
    networks:
      - loki
    ports:
      - 3200:3200
      - 4317:4317
      - 4318:4318
    volumes:
      - type: bind
        source: /mnt/bigdata/config/tempo
        target: /etc/tempo
      - type: bind
        source: /mnt/bigdata/config/tempo/data
        target: /var/tempo
      - type: bind
        source: /etc/localtime
        target: /etc/localtime
        read_only: true
    extra_hosts:
      - "host.docker.internal:host-gateway"
"""
assert "  tempo:" not in s, "tempo already present"
s=s.replace("\nnetworks:\n  loki:", block+"\nnetworks:\n  loki:")
open(p,"w").write(s)
print("ok")
PY'
ssh unimatrixzero.local 'docker stack config -c /home/jdfalk/loki.yaml >/dev/null && echo "stack file valid"'
```

Pin the image tag (Loki/Alloy use `latest`; do not copy that for a new
service). Check <https://github.com/grafana/tempo/releases> and replace `2.6.1`
with the current release before deploying.

## 3. Deploy

```bash
ssh unimatrixzero.local 'docker stack deploy -c /home/jdfalk/loki.yaml loki'
ssh unimatrixzero.local 'docker service ls --format "{{.Name}} {{.Replicas}} {{.Ports}}" | grep ^loki_'
ssh unimatrixzero.local 'docker service logs --tail 30 loki_tempo'
ssh unimatrixzero.local 'curl -s http://localhost:3200/ready; echo; curl -s http://localhost:3200/status/version'
```

`docker stack deploy` on an existing stack only (re)creates the services whose
spec changed, so `loki_loki` and `loki_alloy` are untouched. Expect `ready`
from `/ready` after ~15 s. If `loki_tempo` is `0/1`, read
`docker service ps --no-trunc loki_tempo` for the error (almost always a
permission problem on `/mnt/bigdata/config/tempo/data`).

## 4. Grafana datasource (provisioned, like Loki)

```bash
ssh unimatrixzero.local 'sudo tee /etc/grafana/provisioning/datasources/tempo.yaml >/dev/null' <<'YAML'
apiVersion: 1
datasources:
  - name: Tempo
    type: tempo
    uid: tempo
    access: proxy
    url: http://localhost:3200
    isDefault: false
    editable: true
    jsonData:
      tracesToLogsV2:
        datasourceUid: loki
        spanStartTimeShift: "-5m"
        spanEndTimeShift: "5m"
        filterByTraceID: true
      serviceMap:
        datasourceUid: prometheus
      nodeGraph:
        enabled: true
YAML
ssh unimatrixzero.local 'sudo chown root:grafana /etc/grafana/provisioning/datasources/tempo.yaml && sudo chmod 0640 /etc/grafana/provisioning/datasources/tempo.yaml'
ssh unimatrixzero.local 'sudo systemctl restart grafana-server && sleep 5 && curl -s http://localhost:3000/api/health'
```

Check the uids first: `tracesToLogsV2.datasourceUid` and
`serviceMap.datasourceUid` must match the real Loki and Prometheus datasource
uids (`curl -s -u admin:... http://localhost:3000/api/datasources | jq '.[]|{name,uid}'`,
or Connections → Data sources in the UI). The Loki one is not set in
`loki.yaml`, so Grafana generated it; copy it from the API output or add
`uid: loki` to `loki.yaml` and restart.

## 5. Point audiobook-organizer at Tempo

The app reads `OTEL_EXPORTER_OTLP_ENDPOINT` (bound in `internal/config` to
`otel_exporter_otlp_endpoint`). Add it to the systemd drop-in that already
holds the prod env, then restart.

```bash
ssh unimatrixzero.local 'sudo tee /etc/systemd/system/audiobook-organizer.service.d/otel.conf >/dev/null' <<'EOT'
# Tracing to Grafana Tempo (docker swarm service loki_tempo, published on the host).
# Metrics do not depend on this; removing the file turns only tracing off.
[Service]
Environment=OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4317
# Tempo listens plain gRPC; the exporter defaults to TLS without this.
Environment=OTEL_EXPORTER_OTLP_TRACES_INSECURE=true
Environment=OTEL_SERVICE_NAME=audiobook-organizer
EOT
ssh unimatrixzero.local 'sudo systemctl daemon-reload && sudo systemctl restart audiobook-organizer'
ssh unimatrixzero.local 'journalctl -u audiobook-organizer -n 50 --no-pager | grep -i "OpenTelemetry initialized"'
```

Expected log line: `OpenTelemetry initialized metrics=true tracing=true endpoint=127.0.0.1:4317`.

A restart of prod is pre-authorized, but wait for `memdb warmup published`
(~130 s) before judging anything else.

If `OTEL_EXPORTER_OTLP_TRACES_INSECURE` turns out not to be honoured by the
pinned SDK version (check `go.mod`: `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`
and its changelog), the code fix is one option in
`internal/telemetry/telemetry.go`'s `initTracing`:
`otlptracegrpc.WithInsecure()` behind a config flag. Do that in a PR; do not
point prod at a TLS-less collector with an exporter that insists on TLS and
call the silence "working".

## 6. Verify end to end

```bash
# Spans arriving? (search the last 15 minutes)
ssh unimatrixzero.local 'curl -s "http://localhost:3200/api/search?tags=service.name%3Daudiobook-organizer&limit=5&start=$(date -d "-15 min" +%s)&end=$(date +%s)" | jq ".traces[]|{traceID,rootServiceName,rootTraceName,durationMs}"'
# Trigger an op so there is something to see (any cheap op; this one is read-only):
#   POST /api/v1/operations with def_id maintenance.book-atpath-index-verify, or run anything from the UI.
# Tempo's own health:
ssh unimatrixzero.local 'curl -s http://localhost:3200/metrics | grep -E "^tempo_distributor_spans_received_total|^tempo_ingester_traces_created_total"'
```

In Grafana: Explore → Tempo → Search, service `audiobook-organizer`. A
trace per operation run (`operations/<def_id>` spans from `worker.go`) is
what success looks like.

## 7. Alloy (optional, later)

Alloy can receive OTLP from other hosts (the Macs, llm1) and forward to Tempo
without opening 4317 to them directly. Append to
`/mnt/bigdata/config/alloy/config.alloy`:

```hcl
otelcol.receiver.otlp "default" {
  grpc { endpoint = "0.0.0.0:4319" }
  output { traces = [otelcol.exporter.otlp.tempo.input] }
}
otelcol.exporter.otlp "tempo" {
  client {
    endpoint = "tempo:4317"
    tls { insecure = true }
  }
}
```

and publish `4319:4319` on the `alloy` service in `loki.yaml`, then
`docker stack deploy` again. Not needed for the server's own app.

## Rollback

```bash
ssh unimatrixzero.local 'sudo rm /etc/systemd/system/audiobook-organizer.service.d/otel.conf && sudo systemctl daemon-reload && sudo systemctl restart audiobook-organizer'
ssh unimatrixzero.local 'docker service rm loki_tempo'          # traces stop; Loki/Alloy untouched
ssh unimatrixzero.local 'sudo rm /etc/grafana/provisioning/datasources/tempo.yaml && sudo systemctl restart grafana-server'
# then remove the tempo: block from /home/jdfalk/loki.yaml (or restore the .bak) so the next stack deploy does not recreate it
```

Trace data under `/mnt/bigdata/config/tempo/data` can be deleted once the
service is gone.
