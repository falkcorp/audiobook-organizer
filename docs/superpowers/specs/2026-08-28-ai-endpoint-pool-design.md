<!-- file: docs/superpowers/specs/2026-08-28-ai-endpoint-pool-design.md -->
<!-- version: 1.2.1 -->
<!-- guid: 6d52e9c0-92fd-4193-9b9e-cd19e7d1bf5d -->
<!-- last-edited: 2026-09-01 -->

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
  "id": "win-box-fast",
  "label": "Windows box (RX 7800)",
  "url": "http://win-box.local:8080",
  "capabilities": ["transcription"],
  "role": "active",
  "priority": 10,
  "concurrency": 2,
  "enabled": true,

  "host": "win-box",
  "devices": ["cuda:0"],
  "device_backend": "vulkan",

  "schedule": {
    "timezone": "America/Detroit",
    "windows": [{ "days": "*", "from": "22:00", "to": "06:00" }],
    "drain_grace": "15m"
  }
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
outside the configured safe range. It must also reject a `schedule` with a
non-empty `windows` list and no `timezone`, a `timezone` that is not a loadable
IANA name, a malformed `from`/`to` time, a `device_backend` outside the known set,
and a `host_concurrency` outside the safe range. A rejected value is a
configuration error at the point it is set -- never a defect discovered hours
later when the first job routes. Secrets remain outside this object and stay
environment-authoritative.

### Hardware, devices, and host topology

An endpoint is a URL, but the thing that actually does the work is a piece of
hardware, and the pool currently cannot see it. Three fields close that gap.

`devices` is an ordered list of device identifiers the worker should use, in the
worker's own vocabulary (`cuda:0`, `vulkan:1`, `cpu`, `mps`). An empty list means
"the worker chooses" -- it does NOT mean "no devices". `device_backend` records
the compute backend (`cuda`, `vulkan`, `rocm`, `metal`, `cpu`) so the UI can
explain why an AMD card and an NVIDIA card in the same chassis behave differently.

**Device selection is enforced at the worker, never by the client.** Only the
worker can enumerate its GPUs, and the pool has no way to make a remote process
change device. So `devices` is a DECLARATION, and the pool's job is to detect when
the declaration is false. The transcription worker already reports its resolved
device on `GET /health` (see `scripts/whisper_server.py`, the device-autodetect
work). The health probe must therefore compare the reported device against the
configured `devices` and mark the endpoint unhealthy with an explicit reason on
mismatch.

That check is the point of the field. Without it, an endpoint configured for
`cuda:0` that silently fell back to CPU keeps answering every probe successfully
and simply runs twenty times slower -- a wrong answer with a healthy log line,
which is the failure shape this repository keeps paying for. A declared device
that cannot be verified against a `/health` response must be surfaced as
"unverified", not quietly assumed correct.

`host` is an opaque identifier grouping endpoints that run on the same physical
machine. It exists because per-endpoint `concurrency` silently over-commits a
box: two endpoints pinned to two different GPUs still contend for CPU, RAM, PCIe
bandwidth and disk on one host, so two endpoints each capped at 2 put 4 concurrent
jobs on hardware that may sustain 2. A `host_concurrency` cap applies across every
endpoint sharing a `host` value and is enforced in addition to, not instead of,
each endpoint's own cap. An empty `host` means "this endpoint is alone on its own
host" -- it must never mean "group with every other endpoint that also left it
empty", which would collapse the whole pool under one cap.

**Heterogeneous hardware does not need manual weights.** The natural worry is
that pairing a fast card with a slow one drags throughput down, and that the slow
one should therefore be excluded. Under this design's dispatch model it should
not: capacity is allocated to FREE SLOTS, so a slow endpoint simply completes
fewer items per hour and adding it is a strict throughput gain. Weights are only
required for push or round-robin distribution, which is what actually manufactures
stragglers. Do not add a weight knob; keep dispatch slot-based, and say so in the
implementation so nobody reintroduces round-robin as an "optimization".

The genuine cost of a slow endpoint is confined to the TAIL of a batch: the last
item handed to a slow worker can delay completion long after every fast worker is
idle. The pool therefore observes a throughput estimate per endpoint (EWMA of
completed items per second) and stops assigning new work to endpoints whose
estimate is materially below the pool median once the remaining backlog is smaller
than the number of free fast slots. That estimate is telemetry and a tail rule
ONLY; it must not become a routing weight, because measured-weight routing feeds
back on itself and flaps. An operator who still wants a card excluded outright
already has `enabled: false` and `role: backup`.

### Availability windows

Some hardware may run at any hour; some is a desktop somebody uses during the
day; some should only be touched overnight. `schedule` expresses that.

`schedule.windows` is a list of `{days, from, to}` entries in
`schedule.timezone`. A window whose `to` is earlier than its `from` crosses
midnight. **`timezone` is required whenever `windows` is non-empty and must be an
IANA name**, never a fixed UTC offset and never the process's local time: a
server's local zone changes with the machine, and a fixed offset is wrong for half
the year. DST is the specific trap -- in a zone with a spring-forward transition a
02:00-03:00 window occurs zero times on one day of the year and twice on another,
so window evaluation must be defined in terms of absolute instants rather than
naive wall-clock arithmetic, and the tests must include both transition days.

Being outside a window is **not** a health condition. It gets its own state,
`off_schedule`, and must not increment failure counters, trigger cooldown, or
appear as an incident. Conflating the two would make a correctly-configured
night-only endpoint look like a chronically failing one, and would let a real
outage hide inside expected quiet hours.

When a window closes, in-flight work **drains**: the endpoint accepts no new
assignments and is given `drain_grace` to finish what it holds, after which
outstanding items are returned to the queue and reassigned. Hard-stopping at the
boundary would discard partial transcription work; ignoring the boundary would
defeat the purpose of the setting on somebody's desktop.

If every endpoint for a capability is `off_schedule`, work queues durably and
waits. It must not error and must not fall back to a paid remote provider
implicitly -- that turns a scheduling preference into a surprise bill. This
mirrors the existing transcription rule: leave the page durable and queued rather
than writing a synthetic per-item failure.

### Serialization and zero-value semantics

The pool is persisted as part of the config blob, so `AIEndpoint`'s wire shape is
load-bearing rather than incidental.

`omitempty` does not mean the same thing under encoding/json v1 and v2. Under v2 a
`false` bool and a `0` int are *emitted* rather than dropped, so `enabled`,
`priority`, and `concurrency` change shape between the two. Use `omitzero`, not
`omitempty`, for any field whose zero value must not be written, and never rely on
"the field is absent" to signal a default.

Zero must never be load-bearing as a disable switch. Validation rejects
`concurrency` outside the safe range, but validation only guards the API path: the
config blob is written by full-struct marshal, so a transient zero that reaches
the stored struct becomes a permanent silent kill switch that a `viper.SetDefault`
will not undo. This repo has already taken a production outage of exactly this
shape (`chapter_consolidation_threshold_min = 0` disabling chapter consolidation
library-wide). Therefore:

- Migration seeds explicit values for every field it creates; it never leaves
  `concurrency`, `priority`, or `enabled` to be filled in by a zero value later.
- Loading treats an out-of-range or zero `concurrency` on an otherwise valid
  endpoint as "apply the documented default and log it", not as "no capacity".
- A round-trip test asserts that marshalling and reloading a pool under
  `GOEXPERIMENT=jsonv2` preserves `enabled: false`, `priority: 0`, and the
  configured concurrency exactly.

The hardware and schedule fields each add a zero value that would be catastrophic
if read literally, so each is pinned here rather than left to the reader:

- **An absent or empty `schedule` means ALWAYS AVAILABLE, never "no windows, so
  never runs".** This is the single most dangerous default in this design. Every
  endpoint migrated from the legacy configuration will deserialize with an empty
  schedule, so the "never" reading would silently disable the entire pool on
  upgrade while every endpoint still reported itself healthy.
- **An empty `devices` list means "the worker chooses", not "no device".** The
  same reasoning: migrated endpoints have no device list and must keep working.
- **An empty `host` means "alone on its own host", not "shares a host with every
  other endpoint that left it empty".** The literal grouping reading would put the
  whole pool under a single `host_concurrency` cap the moment one was configured.
- `drain_grace` of zero means "use the documented default and log it", consistent
  with the `concurrency` rule above; zero must not mean "kill in-flight work
  instantly".

Round-trip tests cover each of these explicitly, asserting that a pool written
with the field absent reloads as the permissive meaning and not the restrictive
one.

## Routing and failure behavior

Routing is capability-specific. For each request, the endpoint manager builds
the eligible pool from healthy enabled endpoints for that capability.

0. Drop every endpoint that is `off_schedule` for the current instant. This
   happens BEFORE health is considered, so a closed window never looks like an
   outage and never consumes a retry.
1. Select all healthy `active` endpoints, ordered by priority.
2. Allocate requests across the active pool subject to each endpoint's
   concurrency cap AND the `host_concurrency` cap shared by every endpoint with
   the same `host`. Higher-priority endpoints receive capacity first; equally
   prioritized endpoints round-robin. As backlog exceeds preferred capacity,
   lower-priority active endpoints participate.
2a. Assignment fills FREE SLOTS -- a worker receives its next item when it
   finishes one. This is what lets fast and slow hardware coexist without weights
   (see "Hardware, devices, and host topology"). Do not replace it with a
   round-robin or hash assignment.
2b. Tail rule: once the remaining backlog for a capability is smaller than the
   number of free slots on at-or-above-median-throughput endpoints, stop assigning
   new work to materially slower endpoints, so one slow worker cannot hold a batch
   open after the fast ones are idle. Work already in flight is not cancelled.
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

Each endpoint has an in-memory status: `unknown`, `healthy`, `unhealthy`,
`cooling_down`, or `off_schedule`, plus last success, last failure, failure count,
available capacity, observed throughput, the device the worker actually reported,
and a redacted reason. `off_schedule` is orthogonal to health: an endpoint outside
its window retains its last known health state and is simply not eligible, and it
must never accrue failures or enter cooldown for being asleep.

The transcription probe additionally compares the device reported by `/health`
against the configured `devices` and marks the endpoint unhealthy on mismatch,
with the reported and expected values in the reason. An endpoint whose worker does
not report a device is shown as "device unverified" rather than assumed correct --
a silent CPU fallback answers every probe successfully while running orders of
magnitude slower, and that must be visible. Health state is operational telemetry, not
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
  transcription endpoint. Migrated endpoints receive an EXPLICIT always-available
  schedule, an explicit empty `devices` list meaning "worker chooses", and a
  `host` derived from the URL's host component so that two endpoints already
  pointing at the same machine are grouped correctly from day one. Seeding these
  explicitly rather than leaving them absent is what keeps the permissive
  zero-value reading from being load-bearing.
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
- Round-trip the pool under `GOEXPERIMENT=jsonv2` and assert `enabled: false`,
  `priority: 0`, and a configured `concurrency` all survive marshal/reload.
- Test the server API and Settings UI role toggle, ordering, and health view,
  including the device, host, throughput, and schedule columns.
- Test schedule evaluation across a DST spring-forward and fall-back day in a
  zone that observes them, asserting a 02:00-03:00 window behaves correctly when
  that wall-clock hour occurs zero times and when it occurs twice.
- Test that an `off_schedule` endpoint is excluded from routing WITHOUT
  incrementing its failure count or entering cooldown, and that it returns to
  service at the window boundary without a probe storm.
- Test that a closed window drains in-flight work within `drain_grace` and
  requeues the remainder rather than discarding it.
- Test that when every endpoint for a capability is off-schedule, work queues
  durably and no paid remote provider is used implicitly.
- Test that a `/health` response reporting a device other than the configured one
  marks the endpoint unhealthy, and that a response reporting no device marks it
  "device unverified" rather than healthy.
- Test that `host_concurrency` bounds the SUM of in-flight work across endpoints
  sharing a host, and that an empty `host` does not group unrelated endpoints.
- Test the tail rule: with one fast and one deliberately slow endpoint and a
  backlog smaller than the fast endpoint's free slots, no new work is assigned to
  the slow endpoint.
- Round-trip a pool with `schedule`, `devices`, and `host` ABSENT and assert each
  reloads as the permissive meaning (always available / worker chooses / alone on
  its host), not the restrictive one.
- Run `GOTOOLCHAIN=go1.27.1 go test ./internal/config ./internal/ai
  ./internal/transcribe ./internal/server/... -count=1`, focused frontend
  tests, and `make ci` (the Makefile exports the toolchain pin) before
  deployment.
  `GOEXPERIMENT=jsonv2` is not optional: the Makefile exports it (`Makefile:11`)
  and CI sets it, so a bare `go test` would exercise encoding/json v1 while
  production runs v2 — and v1/v2 disagree on exactly the field shapes this
  design introduces. See "Serialization" above.
- Before each deploy, inspect the production operation timeline. Do not restart
  while scan, import, organize, metadata apply/write-back, or equivalent major
  operations are active.
