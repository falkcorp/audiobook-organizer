<!-- file: docs/superpowers/specs/2026-08-28-ai-endpoint-pool-design.md -->
<!-- version: 1.0.1 -->
<!-- guid: 6d52e9c0-92fd-4193-9b9e-cd19e7d1bf5d -->
<!-- last-edited: 2026-08-28 -->

# AI endpoint pool design

## Goal

Replace the scalar local-AI endpoint settings with one capability-aware pool of
endpoints. Operators can mark each endpoint as active or backup, set a
priority and bounded concurrency, see current health, and safely use Mac
Ollama alongside future Whisper workers without losing compatibility with the
current configuration.

## Scope and non-goals

This design covers local HTTP endpoints for embeddings, LLM requests, and
transcription. It preserves the existing paid OpenAI modes and does not make a
local endpoint a prerequisite for scans or metadata work.

It does not start a Mac Whisper worker, enable broad transcription, or move
existing queued work. Those are separate rollout decisions after the worker
passes its existing remote-transcription contract and a real benchmark.

## Configuration model

Add `ai_endpoints` to `Config` as an ordered list of `AIEndpoint` values:

```json
{
  "id": "mac-ollama",
  "label": "Mac Ollama",
  "url": "http://mac-ollama.local:11434/v1",
  "capabilities": ["embedding", "llm"],
  "role": "active",
  "priority": 10,
  "concurrency": 2,
  "enabled": true
}
```

`id` is stable and unique. `label` is display-only. `url` is normalized before
comparison and never includes credentials. Capabilities are one or more of
`embedding`, `llm`, and `transcription`. `role` is exactly `active` or
`backup`; unknown roles are rejected. `priority` orders eligible endpoints
within a role (lower first), while `concurrency` is a positive, bounded cap on
in-flight work. Disabled endpoints remain configured and visible but are never
probed or selected.

The API must reject duplicate IDs, duplicate `(capability, normalized URL)`
entries, empty capability lists, invalid URLs, invalid roles, and concurrency
outside the configured safe range. Secrets remain outside this object and stay
environment-authoritative.

## Routing and failure behavior

Routing is capability-specific. For each request, the endpoint manager builds
the eligible pool from healthy enabled endpoints for that capability.

1. Select all healthy `active` endpoints, ordered by priority.
2. Allocate requests across the active pool subject to each endpoint's
   concurrency cap. Higher-priority endpoints receive capacity first; equally
   prioritized endpoints round-robin. As backlog exceeds preferred capacity,
   lower-priority active endpoints participate.
3. Use `backup` endpoints only when no healthy active endpoint has capacity,
   or every active endpoint is unhealthy/cooling down. Backups are likewise
   priority-ordered and concurrency-capped.
4. A transport failure immediately marks that endpoint unavailable and retries
   the request on an eligible peer when that operation is safely retryable.
   Capacity pressure alone is not a failure.

Embedding routing is pinned by model and dimension: a request can only use an
endpoint advertising the configured embedding model. The selected model is
recorded with its vectors. A model/dimension change emits the existing
re-embedding-required signal rather than silently mixing vector spaces.

LLM work can retry another matching LLM endpoint. Transcription keeps the
existing batch-level safety rule: when every endpoint is unavailable, return a
transport error and leave the page durable/queued; do not write a synthetic
per-book transcription error.

## Health manager and status API

Add one process-local `AIEndpointManager` owned by server wiring. It probes
enabled endpoints at startup and on a fixed cadence with jitter. It also
reprobes immediately after a request failure. Probe contracts are explicit:

- Ollama embedding/LLM: `GET /api/tags` against the normalized host base URL;
  report model availability as well as reachability.
- Transcription: `GET /health`; require the capabilities needed by the current
  remote-transcription client.

Each endpoint has an in-memory status: `unknown`, `healthy`, `unhealthy`, or
`cooling_down`, plus last success, last failure, failure count, available
capacity, and a redacted reason. Health state is operational telemetry, not
configuration: restart returns it to `unknown` and probes again. Status APIs
must never return authorization headers, query secrets, or raw response bodies.

Expose a read-only authenticated endpoint for settings/UI status and include
the selected endpoint ID in operation logs. The Settings UI shows endpoint
role, priority, concurrency, capabilities, and live status; it permits
editing the configuration but not manually overriding health.

## Compatibility and migration

Existing configuration remains accepted for at least one release:

- `embedding.base_url` and `ai_backend.local_base_url` seed one active Ollama
  endpoint with embedding and/or LLM capability as dictated by the effective
  modes and configured models.
- `whisper_endpoints` seed transcription endpoints preserving label, priority,
  kind, and concurrency. If absent, `whisper_remote_url` seeds one active
  transcription endpoint.
- Explicit non-local OpenAI modes remain outside the local endpoint pool and
  retain their present behavior.

Migration runs on load, is idempotent, and does not overwrite a non-empty
`ai_endpoints` list. During the compatibility release, writes update the new
list and retain legacy read shims so older clients keep a coherent response.
The UI marks legacy fields as migrated/read-only rather than presenting two
conflicting writable sources of truth. A later removal requires a separate
versioned migration and release note.

## Rollout

1. Ship the model, validation, migration, manager, and status endpoint behind
   the existing current scalar behavior, with migration tests.
2. Configure only the already-proven Mac Ollama endpoint as active for
   embeddings and LLMs. Confirm real production embedding and LLM requests,
   operation logs, and health status.
3. Ship Settings UI editing/status. Add a second active endpoint only after a
   capability-specific canary proves it works.
4. Build and benchmark the separate Mac Whisper worker. Only then add it as a
   low-concurrency transcription endpoint and observe a single queued-job
   canary before resuming any bulk transcription work.

Rollback is configuration-first: disable or remove the new endpoint, leaving
queued jobs untouched. If the pool implementation misbehaves, reverting its
PR restores scalar routing; legacy fields are retained specifically for this
path.

## Verification

- Unit-test endpoint validation, normalization, legacy migration/idempotence,
  active/backup selection, capacity limits, cooldown, and retry boundaries.
- Contract-test Ollama and Whisper probes with `httptest` servers, including
  malformed responses and timeouts.
- Test that no config/status response exposes secrets.
- Test the server API and Settings UI role toggle, ordering, and health view.
- Run `GOTOOLCHAIN=go1.26.0 go test ./internal/config ./internal/ai
  ./internal/transcribe ./internal/server/... -count=1`, focused frontend
  tests, and `GOTOOLCHAIN=go1.26.0 make ci` before deployment.
- Before each deploy, inspect the production operation timeline. Do not restart
  while scan, import, organize, metadata apply/write-back, or equivalent major
  operations are active.
