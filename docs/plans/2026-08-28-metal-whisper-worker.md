<!-- file: docs/plans/2026-08-28-metal-whisper-worker.md -->
<!-- version: 1.0.0 -->
<!-- guid: 67ec911b-7d88-4c32-8b76-95eaf8e4ea60 -->
<!-- last-edited: 2026-08-28 -->

# Metal Whisper worker

## Goal

Run an optional Whisper worker on the M1 Max Mac using Apple Metal/MLX while
preserving the audiobook-organizer remote-transcription protocol. The worker
must add capacity only: when it is offline, production jobs remain durable and
queued; it must not re-enable AI parsing or make scans dependent on the Mac.

## Affected files

- `scripts/whisper_mlx_server.py` — add a Mac-only FastAPI service that exposes
  `/health`, `/transcribe`, and `/transcribe-batch` with the exact JSON and
  multipart shapes consumed by `internal/transcribe/remote.go`.
- `scripts/tests/test_whisper_mlx_server.py` — contract tests for health,
  single-file and multi-file result shapes, including per-file failure output.
- `docs/operations/metal-whisper-worker.md` — document local installation,
  loopback-first health check, restricted LAN exposure, and rollback.
- `todo.d/20260828-metal-whisper-worker.md` — preserve the deployment and
  production-wiring follow-up until the Mac service is benchmarked.

## Steps

1. Write failing contract tests from `remote.go`: batch health capability,
   multipart field names, filename-as-result-key, and `{text,error}` response
   values.
2. Implement the MLX server around `mlx_whisper.transcribe`, retaining a loaded
   model across requests and serializing inference to avoid desktop-memory
   contention. Bind loopback by default.
3. Install the Mac-local Python environment, download a small English model,
   and validate one real WAV request plus the batch contract locally.
4. Measure one bounded representative batch and record latency/thermal evidence.
   Do not claim Metal utilization from elapsed time alone.
5. Expose the service only to the private LAN with an explicit bind/firewall
   decision, verify it from production, then add it as a low-concurrency
   optional `WHISPER_ENDPOINTS` member in a separate production-config change.
6. Keep `enable_ai_parsing=false` throughout. Consider enabling a separate
   transcription maintenance operation only after endpoint health is stable.

## Test strategy

- `uv run --with fastapi --with httpx --with pytest pytest scripts/tests/test_whisper_mlx_server.py`
- `curl http://127.0.0.1:<port>/health` and a multipart local WAV canary against
  both `/transcribe` and `/transcribe-batch`.
- Use `GOTOOLCHAIN=go1.26.0 go test ./internal/transcribe -count=1` to confirm
  the existing client contract remains unchanged.
- Benchmark a real short WAV batch and record model, elapsed time, and Mac
  thermal state before LAN or production configuration changes.

## Rollback

Stop the local worker and leave it loopback-only. Remove its endpoint from
`WHISPER_ENDPOINTS` without deleting any queued production work. The existing
remote client treats unavailable endpoints as unavailable capacity rather than
permission to fall back to an in-process server worker.
