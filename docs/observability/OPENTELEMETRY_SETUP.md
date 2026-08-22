<!-- file: docs/observability/OPENTELEMETRY_SETUP.md -->
<!-- version: 1.0.0 -->
<!-- guid: ada445dd-1dd4-4d9f-8e3b-6318e1f750ec -->
<!-- last-edited: 2026-08-22 -->

# OpenTelemetry Observability Setup

This document covers configuring OpenTelemetry instrumentation for the audiobook-organizer service.

## Quick Start

### Enable OpenTelemetry

Set the OTEL exporter endpoint:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
make run
```

If the endpoint is not configured, OpenTelemetry disables gracefully (no-op mode).

### Local Development with Jaeger

#### Option 1: Docker (Recommended)

```bash
# Start Jaeger all-in-one container
docker run -d \
  --name jaeger \
  -p 4317:4317 \
  -p 16686:16686 \
  otel/jaeger:latest

# Start the service with OTEL enabled
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
make run
```

View traces at `http://localhost:16686`

#### Option 2: Brew (macOS)

```bash
# Install Jaeger
brew install jaeger

# Start Jaeger
jaeger &

# Start the service
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
make run
```

## Environment Variables

### Configuration

- `OTEL_EXPORTER_OTLP_ENDPOINT` — gRPC endpoint for OTLP exporter
  - Example: `http://localhost:4317` (Jaeger default)
  - If not set, instrumentation is disabled (no-op mode)
  - No performance impact when disabled

## Instrumentation

The service instruments the following operations:

### HTTP Handlers (otelgin middleware)
- All HTTP requests are automatically traced
- Captures: method, path, status code, latency
- Metric: request count, duration histogram, status distribution

### Database Operations (ActivityStore)
- `activity_store.record` — log entry written
- `activity_store.query` — logs queried with filtering
- `activity_store.summarize` — logs summarized to digests
- `activity_store.purge` — old logs deleted
- `activity_store.compact_by_day` — daily compaction run
- `activity_store.wipe_all_activity` — all logs deleted
- Other store operations

Attributes: operation name, tier, source, entry counts, error status

### Operations (Background Jobs)
- `operation.run` — each operation execution
- Captures: operation ID, name, plugin, duration, success/failure

### Dedup Engine
- `dedup.check_book` — single-book dedup check
- `dedup.full_scan` — full library scan
- `dedup.purge_stale_candidates` — candidate cleanup
- Attributes: total books, merged candidates, operation latency

### Metrics Endpoint

Access Prometheus metrics at `/metrics`:

```bash
curl http://localhost:8484/metrics
```

Exposed metrics include:
- HTTP request counts and latency
- OTEL instrumentation metrics
- Go runtime metrics

## Architecture

### Components

1. **Config** (`internal/telemetry/config.go`)
   - Reads environment variables
   - Enables/disables OTEL based on configuration

2. **Initialization** (`internal/telemetry/telemetry.go`)
   - Creates OTLP gRPC exporter for traces
   - Creates Prometheus exporter for metrics
   - Sets up TracerProvider and MeterProvider
   - Returns shutdown function for graceful shutdown

3. **Resource** (`internal/telemetry/resource.go`)
   - Metadata about the service (name, version)
   - Used in trace and metric export

4. **Instrumentation**
   - HTTP: `otelgin` middleware for all requests
   - Database: `InstrumentedActivityStorer` wrapper
   - Operations: Spans in operation registry
   - Dedup: Spans in dedup engine methods

### Graceful Shutdown

The shutdown function is deferred in `cmd/root.go`:

```go
otelShutdown, err := telemetry.InitOTEL(context.Background(), cfg)
if err != nil {
    return fmt.Errorf("failed to initialize OpenTelemetry: %w", err)
}
defer otelShutdown(context.Background())
```

This ensures traces and metrics are flushed before the process exits.

## Production Deployment

### Recommended Configuration

```bash
# Set OTEL endpoint to your collector/backend
export OTEL_EXPORTER_OTLP_ENDPOINT=https://your-otel-collector:4317

# Optional: Configure other OTEL settings
export OTEL_SDK_DISABLED=false
export OTEL_METRICS_EXPORTER=otlp
export OTEL_TRACES_EXPORTER=otlp

# Start the service
make deploy
```

### Performance Considerations

- **Disabled mode** (no endpoint): negligible performance impact
- **Enabled mode**: ~2-5% overhead depending on trace sampling
- Default: all traces exported (no sampling configured yet)
- Consider configuring trace sampling for high-traffic scenarios

### Compatibility

- **Jaeger**: Use OTLP gRPC receiver (localhost:4317)
- **Datadog**: Configure Datadog OTEL collector
- **New Relic**: Configure NRRI OTEL collector
- **Lightstep**: OTLP gRPC compatible
- Any OTEL-compliant collector/backend

## Monitoring

### Key Metrics

- `http_server_duration_histogram` — HTTP request latencies
- `http_server_request_count` — HTTP request counts by status
- `activity_store_record_duration` — database write latency
- `operation_duration` — background job execution time

### Key Traces

Look for these span names in Jaeger:

- `http.server.request` — incoming HTTP request
- `activity_store.record` — activity log write
- `dedup.full_scan` — full library dedup scan
- `operation.run` — background operation execution

## Future Enhancements

- [ ] Trace sampling configuration (probabilistic sampler)
- [ ] Custom metrics for business logic (dedup matches, metadata enrichment)
- [ ] Logging integration (send logs to trace backend)
- [ ] Runtime instrumentation (GC pauses, goroutine counts)
- [ ] Custom baggage propagation (user IDs, request context)

## Troubleshooting

### No traces appearing in Jaeger

1. Verify OTEL is enabled:
   ```bash
   env | grep OTEL_EXPORTER_OTLP_ENDPOINT
   ```

2. Verify Jaeger is running:
   ```bash
   curl http://localhost:16686/api/traces 2>/dev/null | jq .
   ```

3. Check service logs for OTEL initialization errors:
   ```bash
   grep -i "opentelemetry\|otel\|telemetry" service.log
   ```

### High memory/CPU usage

- Reduce trace volume with sampling (future: configure probabilistic sampler)
- Verify Jaeger instance has sufficient resources
- Check network connectivity to OTEL collector

### Missing metrics

Ensure `/metrics` endpoint is accessible:
```bash
curl http://localhost:8484/metrics
```

If empty, verify the service is running and receiving traffic.

## References

- OpenTelemetry: https://opentelemetry.io/
- OTLP Specification: https://opentelemetry.io/docs/reference/specification/protocol/
- Jaeger: https://www.jaegertracing.io/
- go.opentelemetry.io: https://pkg.go.dev/go.opentelemetry.io
