<!-- file: deploy/grafana/TRACING-RUNBOOK.md -->
<!-- version: 2.0.0 -->
<!-- guid: 327a1efc-1be7-4428-b607-07958c03abb6 -->
<!-- last-edited: 2026-10-03 -->

# Tracing runbook: Grafana Tempo next to the Loki + Alloy stack

**Status: Tempo is running since 2026-10-03 07:22** (swarm service
`loki_tempo`, image `grafana/tempo:2.10.8`, config
`/mnt/bigdata/config/tempo/tempo.yaml`, datasource file written). Steps 1–4
were executed as corrected below. Step 5 (the app's endpoint) needs the
telemetry fix that ships with this version of the runbook to be deployed
first, and Grafana needs one restart to load the datasource.

## What went wrong on the first run (read before repeating any step)

1. **`docker stack deploy` replaced Loki and Alloy too.** They are tagged
   `latest`; a deploy re-resolves the tag to today's digest, which is a spec
   change, so both tasks were shut down and restarted on newer images (Loki
   was unready for about two minutes, Alloy for about nine while a 500 MB
   image pulled, then replayed its log backlog, which Loki rejects as too
   old). To add or change ONE service of this stack without touching the
   others, pin the others to a digest first, or use
   `docker service update` / `docker service create --network loki_loki` for
   the one service instead of a stack deploy.
2. **The app crash-looped for 75 seconds.** Step 5 below used to say
   `OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4317`. The telemetry package ran
   `url.Parse` on it, which rejects a bare `ip:port`, returned the error, and
   `cmd/root.go` made it fatal. Fixed in the same change as this runbook: a
   trace endpoint that cannot be used now turns tracing off and logs an
   error, and both `http://host:port` and `host:port` are accepted. **Do not
   set the endpoint on a build older than that fix.**
3. **No general sudo on the server.** `chown` to uid 10001 and `sudo tee`
   into `/etc` are not available to the deploy user. Tempo therefore runs as
   uid 1000 (the deploy user) on a directory that user owns, and the app's
   environment goes through `deploy/local.conf` + `make deploy-debug`, the
   one path the sudoers file allows.

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
ssh unimatrixzero.local 'mkdir -p /mnt/bigdata/config/tempo/data && chmod 0775 /mnt/bigdata/config/tempo /mnt/bigdata/config/tempo/data'
ssh unimatrixzero.local 'cat > /mnt/bigdata/config/tempo/tempo.yaml' <<'YAML'
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

storage:
  trace:
    backend: local
    wal:
      path: /var/tempo/wal
    local:
      path: /var/tempo/blocks

YAML
ssh unimatrixzero.local 'ls -la /mnt/bigdata/config/tempo'
```

The official image runs as uid 10001; here the service is started with
`user: "1000:1000"` (step 2) so it can write a directory the deploy user owns
without a `chown` that needs root. Span-metrics (`metrics_generator` with a
remote write to Prometheus) is left out: it needs
`--web.enable-remote-write-receiver` on Prometheus, which is a root change.

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
    image: grafana/tempo:2.10.8
    command: ["-config.file=/etc/tempo/tempo.yaml"]
    user: "1000:1000"
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
service). `2.10.8` was the newest 2.x on 2026-10-03. Tempo 3.x changed the
architecture and this single-binary config has not been tried on it.

## 3. Deploy

```bash
ssh unimatrixzero.local 'docker stack deploy -c /home/jdfalk/loki.yaml loki'
ssh unimatrixzero.local 'docker service ls --format "{{.Name}} {{.Replicas}} {{.Ports}}" | grep ^loki_'
ssh unimatrixzero.local 'docker service logs --tail 30 loki_tempo'
ssh unimatrixzero.local 'curl -s http://127.0.0.1:3200/ready; echo; curl -s http://127.0.0.1:3200/status/version'
```

**`docker stack deploy` re-resolves every image tag.** `loki_loki` and
`loki_alloy` are tagged `latest`, so they ARE replaced (see "What went wrong"
above). Plan for a few minutes without log shipping, or pin them first. Expect `ready`
from `/ready` after ~15 s. If `loki_tempo` is `0/1`, read
`docker service ps --no-trunc loki_tempo` for the error (almost always a
permission problem on `/mnt/bigdata/config/tempo/data`).

## 4. Grafana datasource (provisioned, like Loki)

```bash
ssh unimatrixzero.local 'cat > /etc/grafana/provisioning/datasources/tempo.yaml' <<'YAML'
apiVersion: 1
datasources:
  - name: Tempo
    type: tempo
    uid: tempo
    access: proxy
    url: http://127.0.0.1:3200
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
ssh unimatrixzero.local 'chmod 0664 /etc/grafana/provisioning/datasources/tempo.yaml'
# Needs root; the deploy user has no sudo rule for it. Run it yourself:
#   sudo systemctl restart grafana-server
```

The directory is group-writable through an ACL, so the file can be written
without sudo; Grafana only reads provisioning at start, so the datasource
appears after the restart. `127.0.0.1`, not `localhost`: swarm publishes the
port on IPv4 only and `localhost` resolves to `::1` first. The file written on
2026-10-03 has no `tracesToLogsV2` / `serviceMap` block, because the Loki and
Prometheus datasource uids could not be read without a Grafana login; add them
from Connections → Data sources once known.

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
cat >> deploy/local.conf <<'EOT'

# Tracing to Grafana Tempo (docker swarm service loki_tempo on this host).
# Metrics do not depend on this; removing these lines turns only tracing off.
# A URL: "http" is plaintext gRPC, which is what Tempo listens for on 4317.
Environment=OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317
Environment=OTEL_SERVICE_NAME=audiobook-organizer
EOT
make deploy-debug     # from the primary checkout at 0 0; copies local.conf and restarts
ssh unimatrixzero.local 'journalctl -u audiobook-organizer -n 80 --no-pager | grep -i "OpenTelemetry initialized"'
```

`deploy/local.conf` is gitignored and is the only drop-in the sudoers file
lets the deploy user install; a separate `otel.conf` cannot be copied into
place. The bare form `127.0.0.1:4317` also works on a build with the
telemetry fix, together with `OTEL_EXPORTER_OTLP_TRACES_INSECURE=true`; the
URL form needs no second variable and is also what the OTel SDK expects when
it reads the variable itself.

Expected log line: `OpenTelemetry initialized metrics=true tracing=true endpoint=http://127.0.0.1:4317`.
If instead you see `OpenTelemetry initialized with tracing OFF: …`, the server
is up and only tracing failed; `tracing_error` on that line names the reason.

A restart of prod is pre-authorized, but wait for `memdb warmup published`
(~130 s) before judging anything else.

## 6. Verify end to end

```bash
# Spans arriving? (search the last 15 minutes)
ssh unimatrixzero.local 'curl -s "http://127.0.0.1:3200/api/search?tags=service.name%3Daudiobook-organizer&limit=5&start=$(date -d "-15 min" +%s)&end=$(date +%s)" | jq ".traces[]|{traceID,rootServiceName,rootTraceName,durationMs}"'
# Trigger an op so there is something to see (any cheap op; this one is read-only):
#   POST /api/v1/operations with def_id maintenance.book-atpath-index-verify, or run anything from the UI.
# Tempo's own health:
ssh unimatrixzero.local 'curl -s http://127.0.0.1:3200/metrics | grep -E "^tempo_distributor_spans_received_total|^tempo_ingester_traces_created_total"'
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
# 1. Tracing off in the app: delete the OTEL_* lines from deploy/local.conf, then
make deploy-debug
# 2. Tempo off; Loki and Alloy are not touched by removing one service:
ssh unimatrixzero.local 'docker service rm loki_tempo'
# 3. Datasource (the file is yours to remove; Grafana forgets it at its next restart, which needs root):
ssh unimatrixzero.local 'rm /etc/grafana/provisioning/datasources/tempo.yaml'
# then remove the tempo: block from /home/jdfalk/loki.yaml (or restore the .bak) so the next stack deploy does not recreate it
```

Trace data under `/mnt/bigdata/config/tempo/data` can be deleted once the
service is gone.
