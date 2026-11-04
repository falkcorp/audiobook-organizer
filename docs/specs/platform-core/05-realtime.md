<!-- file: docs/specs/platform-core/05-realtime.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f4f16ae-e4d9-4fd1-a6df-e409e07602ad -->
<!-- last-edited: 2026-11-04 -->

# 05 — Realtime (`core/realtime`)

Evidence commit `748fd787a`. Raw inputs: [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §6,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §5,
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §10.
Related: [05-events](05-events.md) (in-process bus), [05-http-api](05-http-api.md) (route chain),
[05-authn](05-authn.md) §4.12 (principal invalidation), [05-rbac](05-rbac.md) §4.10 (topic permissions),
[05-tasks](05-tasks.md) (task run views).

## 1. Purpose & scope

One server-push channel for every app: a single **SSE hub** per `App` that

- replaces the three implementations in ABO (hub A, the ops-registry stream, the v3 broker);
- is **resumable** with `Last-Event-ID` inside a bounded in-memory window, and says `resync` when
  it cannot resume;
- filters **per subscriber, per event** by permission, and re-checks that the subscriber's
  credential still stands for the life of the stream;
- applies **backpressure** without ever blocking a publisher: slow subscribers lose their oldest
  queued events and are told to resync;
- carries typed, namespaced topics declared by core and modules.

Not in scope: durable delivery (correctness never depends on realtime — 02 decision 11; the
`outbox` is in 05-events), WebSockets (none today, `raw/09` §6; SSE suffices because traffic is
server→client), and the *arr facade's SignalR feed (08-arr-facade may bridge to this hub).

## 2. Current state at HEAD

| Stream | Behaviour | Anchor |
|---|---|---|
| A: `GET /api/events` (`realtime.EventHub`) | `data:` frames only, no `event:`/`id:`; 25 s `: ping`; per-client channel of 100, **drop newest on full** with an info log; optional `?operation=` filter; types `operation.progress/status/log`, `system.status`, `books.changed` (250 ms coalescer); auth = any valid credential, **no permission, no per-user filtering** | `internal/realtime/events.go:19-140,216-296`, `internal/realtime/book_changes.go:18,32`, `internal/server/server_lifecycle.go:1762-1783` |
| B: `GET /api/v1/operations/events` (ops registry bus) | named events `op.created/updated/terminal/log/current_item`; immediate heartbeat; perm `library.view`; no resume | `internal/server/handlers/operations_v2.go:1063-1110`, `internal/operations/registry/bus.go:14-50` |
| C: `GET /api/v3/ops/events` (v3 broker) | `id: <boot-nonce>-<seq>`, `event: run`, 15 s heartbeat; ring 512 for `Last-Event-ID`; per-client queue 128, overflow drops queued and sets `resync`; caps 64 global (503) / 4 per bucket (429, `Retry-After: 5`), buckets = user's session vs user's API keys; per-event re-authorization of Repairs runs and of user-identifying text; closes when the credential stops standing; 250 ms flush coalescing | `internal/server/handlers/opsv3/events.go:33-75,108-178,366-460,486-618`, `internal/server/handlers/opaccess/repairs.go:227,272` |
| Clients | `eventSourceManager` for `/api/events` (backoff 3 s→60 s, gives up after 5 attempts); separate `EventSource` for B; v3 stream not used by the web UI | `web/src/services/eventSourceManager.ts:25-42,95,163`, `web/src/services/api.ts:733-740`, `raw/09` §2.3 |
| Listener | `WriteTimeout: 0` so streams stay open; gzip excludes `/api/events` | `internal/server/server_lifecycle.go:2187-2192`, `raw/09` §2.2 row 7 |

Stream C is the prior art: its resume, resync, caps and per-event re-authorization are adopted
wholesale and generalized from "runs" to topics.

## 3. Go API

Package `github.com/falkcorp/mediacore/realtime` (layer `httpapi`; imports `auth`, `rbac`,
`events`, `log`, `metrics`); implementation `core/internal/sse`.

```go
package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// Topic declares a stream of T. Name follows 03 §3.6 event-topic naming:
// "<moduleID>.<noun>.<verb-past>.v<N>" (e.g. "books.book.changed.v1",
// "core.task.run.v1"). Perm is required (zero Requirement rejected, like R6).
type Topic[T any] struct {
	Name     string
	Perm     rbac.Requirement
	// Visible optionally filters per subscriber (row-level), e.g. "only runs the
	// caller may read". Called on the subscriber's goroutine, must be fast and
	// must not block; it may consult p.Perms.
	Visible  func(ctx context.Context, p *auth.Principal, v T) bool
	// Redact optionally returns a reduced view for subscribers lacking a scope
	// (today's withUserText, internal/server/handlers/opsv3/events.go:609-616).
	Redact   func(p *auth.Principal, v T) T
	// Key extracts an entity key for subscription filters (?key=) and coalescing.
	Key      func(v T) string
	// Coalesce > 0 folds events with the same Key published within the window
	// into the latest one (books.changed used 250 ms).
	Coalesce time.Duration
	// Merge combines coalesced values (default: keep last).
	Merge    func(prev, next T) T
}

// Publisher is returned by Register; Publish never blocks and never fails the
// caller (realtime is notify-only).
type Publisher[T any] interface {
	Publish(ctx context.Context, v T)
}

// Register declares a topic on the hub. Called from module/core registration
// only; errors (bad name, duplicate, zero Perm, foreign namespace) abort boot.
func Register[T any](h Registrar, t Topic[T]) (Publisher[T], error)

// Bridge republishes an in-process events topic (05-events) as a realtime
// topic, so most modules declare one events topic and bridge it.
func Bridge[T any](h Registrar, bus events.Bus, eventsTopic string, t Topic[T]) error

type Registrar interface{ moduleID() string /* unexported: only the kernel builds it */ }

// Hub is created by the kernel, one per App (R5).
type Hub struct{ /* unexported */ }

func NewHub(opts Options, deps Deps) *Hub

type Options struct {
	RingSize          int           // resumable window, events (default 1024)
	ClientBuffer      int           // per-subscriber queue (default 128)
	MaxClients        int           // global open streams (default 256)
	MaxPerBucket      int           // per user per credential class (default 4)
	Heartbeat         time.Duration // default 15s
	WriteTimeout      time.Duration // per-write deadline via http.ResponseController (default 10s)
	RetryAfter        time.Duration // on 429/503 (default 5s)
}

type Deps struct {
	Auth    *auth.Service // Revalidate
	Events  events.Bus    // core.principal.invalidated.v1, core.rbac.changed.v1
	Log     *slog.Logger
	Metrics *metrics.Registry
	Clock   func() time.Time
}

// Handler serves GET /api/core/v1/events (registered by the kernel on CoreGroup
// with Perm rbac.AnyAuthenticated and HandleRaw).
func (h *Hub) Handler() http.Handler

// Projection serves a legacy framing of the same hub (§4.8); used only by the
// books module's legacy aliases.
func (h *Hub) Projection(p LegacyProjection) http.Handler

type LegacyProjection struct {
	Topics  []string                              // topics this legacy stream carries
	Frame   func(topic string, data json.RawMessage) (event string, payload []byte, ok bool)
	WithIDs bool                                  // emit id: lines (only stream C did)
	Heartbeat string                              // ": ping" (A) or ": heartbeat" (B, C)
}

func (h *Hub) Run(ctx context.Context) error // coalescer + invalidation listener; returns on ctx done

// Wire frame (data: line) of the native stream.
type Envelope struct {
	Topic string          `json:"topic"`
	Time  time.Time       `json:"ts"`
	Key   string          `json:"key,omitempty"`
	Data  json.RawMessage `json:"data"`
}

var (
	ErrTooManyClients = errors.New("realtime: too many event streams")          // 503
	ErrTooManyForUser = errors.New("realtime: too many event streams for user") // 429
	ErrUnknownTopic   = errors.New("realtime: unknown topic")                   // 400
	ErrTopicDenied    = errors.New("realtime: topic not permitted")             // 403 (explicit topic only)
)
```

## 4. Behaviour

### 4.1 Subscription

`GET /api/core/v1/events?topic=<pattern>[&topic=…][&key=<entity key>]`

- `topic` patterns: exact (`core.task.run.v1`), module wildcard (`books.*`), or all (`*`, the
  default). Wildcards expand to the topics the principal may see **now**; topics registered
  later in no case appear (topics are fixed at boot).
- An explicitly named topic the principal lacks permission for → 403 `topic_not_permitted`
  (fail closed, visible). Wildcard expansion silently omits topics the principal cannot see.
- `key` narrows to one entity (replaces stream A's `?operation=<id>` and lets the UI follow one
  task run).
- Response: `Content-Type: text/event-stream`, `Cache-Control: no-store`,
  `X-Accel-Buffering: no`; excluded from compression (05-http-api §4.2 stage 8). The first
  frame is `retry: 3000` then `event: core.stream.open.v1` with the stream's effective topic list.

### 4.2 Framing

```
id: <bootNonce>-<seq>
event: <topic>
data: {"topic":"<topic>","ts":"<RFC3339Nano>","key":"<key>","data":{…}}

```

`seq` is a hub-wide `uint64` assigned at publish time; `bootNonce` is 8 random bytes hex,
regenerated each process start (stream C's scheme, `opsv3/events.go:169-194`). Heartbeat: comment
line `: hb` every `Heartbeat`.

### 4.3 Resume

- Every published event (after coalescing) is appended to a ring of `RingSize` entries
  `{seq, topic, key, payload bytes, value any}`.
- `Last-Event-ID` (header, or `?last_event_id=` for clients that cannot set headers) with this
  hub's nonce and a `seq` still in the ring: queue every ring entry after it that passes the
  subscriber's filters, then live events.
- Unknown nonce (server restarted), malformed id, or `seq` older than the ring: send
  `event: core.stream.resync.v1` with `{"reason":"restart"|"gap"}` first; the client refetches
  state over REST. No error status: a resync is normal.

### 4.4 Permission filtering (per subscriber, per event)

For each queued entry the subscriber goroutine checks, in order: topic `Perm` against the
principal's checker (already guaranteed at subscribe time for explicit topics, rechecked when
`core.rbac.changed.v1` bumps the generation); `Visible(ctx, principal, value)`; then `Redact`.
Filtering happens at **send** time, not publish time, so a single published event is evaluated
once per subscriber with that subscriber's current rights, and replayed ring entries are filtered
exactly like live ones (stream C's rule, `opsv3/events.go:569-574`).

### 4.5 Backpressure and caps

- `Publish` cost: marshal once (payload bytes cached in the ring entry), append to ring under a
  mutex, then for each subscriber append to its queue and non-blocking-signal its wake channel
  (capacity 1). No goroutine per publish, no blocking send, no per-publish allocation per
  subscriber beyond a slice append.
- Subscriber queue full (`ClientBuffer`): drop the queued entries, set `resync`; the subscriber
  sends `core.stream.resync.v1{"reason":"overflow"}` and continues live (stream C semantics,
  `opsv3/events.go:417-447`). Stream A's "drop newest silently" (`events.go:131-138`) is not kept.
- Write deadline: before each write the handler sets
  `http.ResponseController.SetWriteDeadline(now + WriteTimeout)`; a client that does not read
  for that long is disconnected (the listener keeps `WriteTimeout: 0`,
  `server_lifecycle.go:2192`, so per-write deadlines are the only bound).
- Caps: `MaxClients` (503 + `Retry-After`) and `MaxPerBucket` per user per credential class
  (session/interactive vs API key — stream C's buckets, `opsv3/events.go:383-406`) (429 +
  `Retry-After`). Auth-disabled principals share one bucket bounded only by `MaxClients`.

### 4.6 Credential lifetime

A stream outlives the request that opened it. The hub listens on `events` for
`core.principal.invalidated.v1` (05-authn §4.12) and `core.rbac.changed.v1`:

- invalidation matching the subscriber's user or credential → send
  `event: core.stream.closed.v1 {"reason":"credential_revoked"}` and end the stream;
- rbac generation change → the subscriber re-resolves its checker on next wake; topics it can no
  longer see are dropped from its set (explicitly named ones end the stream with
  `reason:"permission_revoked"`).
- Additionally every heartbeat calls `auth.Service.Revalidate` (cheap, in-memory generation
  table), covering expiry of the session itself (stream C's `StreamOpen()` check,
  `opaccess/repairs.go:272`).

### 4.7 Core topics

| Topic | Perm | Payload | Replaces |
|---|---|---|---|
| `core.task.run.v1` | `core:tasks.view` + `Visible` = task definition's `View` perm (05-tasks) | run view with progress, workers, current item (`Redact` hides user-identifying text without `core:users.view`) | stream C `run`, stream B `op.*`, stream A `operation.*` |
| `core.task.log.v1` | as above | log line | stream A `operation.log`, B `op.log` |
| `core.system.status.v1` | `core:system.view` | status, shutdown notice | stream A `system.status` (incl. the `system.shutdown` the UI listens for, `raw/13` §10) |
| `core.notify.v1` | `core:self` + `Visible` = recipient is the subscriber | in-app notification | new |
| `books.book.changed.v1` | `books:view` | `{kind: created|updated|deleted, ids[]}`, `Coalesce: 250ms`, `Merge` unions ids | stream A `books.changed` (`book_changes.go:18-32`) |

Modules add their own via `realtime.Register`/`Bridge` (05-events topics are the usual source).

### 4.8 Legacy projections (compatibility window)

The three legacy URLs become projections of the one hub, registered by the `books` module as
aliases with Deprecation/Sunset headers (05-http-api §4.6) for two minor releases:

| Legacy URL | Projection |
|---|---|
| `GET /api/events` | topics `core.task.*`, `core.system.status.v1`, `books.book.changed.v1`; frames `data: {"type":…,"id":…,"timestamp":…,"data":…}` with the old type names; `: ping`; no ids; `?operation=` → `key` |
| `GET /api/v1/operations/events` | `core.task.*` mapped to `event: op.created|op.updated|op.terminal|op.log|op.current_item` |
| `GET /api/v3/ops/events` | `core.task.run.v1` as `event: run`, ids kept, `event: resync` for the resync topic |

Permissions on projections are the native topic permissions (stricter than stream A's "any
credential", which the 2026-09 pen-test gate MED-2 already narrowed to authenticated,
`server_lifecycle.go:1762-1781`).

## 5. Lifecycle

1. Kernel creates the `Hub` after `auth`/`rbac`/`events`; core and modules register topics during
   registration; duplicates/zero perms abort boot.
2. `httpapi` mounts `Handler()` (`HandleRaw`, `RateExempt` not allowed → `RateDefault`, body 0).
3. `App.Run` starts `Hub.Run` (`wg.Go`): coalescer timers, invalidation listener.
4. Shutdown: the kernel calls `Hub.Close` from `http.Server.RegisterOnShutdown` — every stream
   receives `event: core.stream.closed.v1 {"reason":"shutdown"}` and ends, so `Shutdown` is not held
   open by long-lived streams; `Run` returns when its context ends.

## 6. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| Topic registration invalid (name, zero Perm, foreign namespace, duplicate) | boot error | closed | R6 parity |
| Anonymous request | 401 (route is `AnyAuthenticated`) | closed | |
| Explicit topic not permitted | 403 | closed | |
| Unknown topic | 400 | closed | typo visibility |
| Over caps | 503 / 429 + `Retry-After` | closed | resource bound |
| Marshal error on publish | event dropped, `mediacore_realtime_events_dropped_total{reason="marshal"}`, error log once per topic per minute | open | notify-only; never fail the publisher |
| Publish with no subscribers | appended to ring only | — | resume needs it |
| Subscriber queue overflow | drop queue, resync | open (for that subscriber) | publisher must not block |
| Write error / deadline exceeded | end stream | — | client gone or stuck |
| `Visible`/`Redact` panics | recovered per event, event skipped for that subscriber, counted | closed (event withheld) | a buggy filter must not leak or kill the hub |
| `Revalidate` returns error (store down) | keep stream until next heartbeat; end after 3 consecutive failures | open briefly, then closed | short store blips must not drop every stream; persistent failure must not keep a revoked credential streaming |
| Bad `Last-Event-ID` | resync | open | normal |

## 7. Concurrency contract

- `Publish` is safe from any goroutine; O(subscribers) non-blocking work under one mutex
  (ring + subscriber list). Payload marshalling happens before taking the lock.
- Each stream uses exactly its HTTP handler goroutine; no extra goroutine per subscriber.
- Coalescing: one timer per (topic, key) bucket, owned by the hub's `Run` goroutine; bounded by
  `max_coalesce_keys` (10,000) — overflow publishes immediately instead of coalescing.
- Subscriber state (`queue`, `resync`, checker) is guarded by the subscriber's own mutex; the hub
  never holds a subscriber lock while holding the hub lock longer than an append.
- Tests use `testing/synctest` for heartbeats, coalescing and deadlines.

## 8. Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.realtime.ring_size` | int | 1024 | no |
| `core.realtime.client_buffer` | int | 128 | no |
| `core.realtime.max_clients` | int | 256 | yes (new streams) |
| `core.realtime.max_per_bucket` | int | 4 | yes |
| `core.realtime.heartbeat` | duration | 15s | no |
| `core.realtime.write_timeout` | duration | 10s | yes |
| `core.realtime.retry_after` | duration | 5s | yes |
| `core.realtime.max_coalesce_keys` | int | 10000 | no |

## 9. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_realtime_subscribers` | gauge | `kind` (`native`,`legacy_a`,`legacy_b`,`legacy_c`) |
| `mediacore_realtime_events_published_total` | counter | `topic` |
| `mediacore_realtime_events_delivered_total` | counter | `topic` |
| `mediacore_realtime_events_filtered_total` | counter | `topic`, `reason` (`perm`,`visible`) |
| `mediacore_realtime_events_dropped_total` | counter | `reason` (`overflow`,`marshal`,`filter_panic`) |
| `mediacore_realtime_resyncs_total` | counter | `reason` (`restart`,`gap`,`overflow`) |
| `mediacore_realtime_stream_refusals_total` | counter | `reason` (`global_cap`,`bucket_cap`,`denied`) |
| `mediacore_realtime_streams_closed_total` | counter | `reason` (`client`,`shutdown`,`credential_revoked`,`permission_revoked`,`write_timeout`) |
| `mediacore_realtime_ring_oldest_age_seconds` | gauge | — |

## 10. Tests

| Test | Asserts |
|---|---|
| `TestHub_ResumeFromLastEventID` | replay after id, then live, no duplicates |
| `TestHub_ResyncOnRestartNonce` / `TestHub_ResyncOnGap` | |
| `TestHub_SlowSubscriberDoesNotBlockPublisher` | `synctest`: publisher completes N publishes while one subscriber never reads; that subscriber gets `resync` |
| `TestHub_PerEventPermissionFilter` | two subscribers, different scopes, same publish |
| `TestHub_ReplayIsFilteredLikeLive` | |
| `TestHub_StreamClosesOnCredentialRevoke` / `TestHub_RevalidateOnHeartbeat` | |
| `TestHub_PermissionRevokedDropsTopic` | rbac generation bump |
| `TestHub_CapsGlobalAndBucket` | 503/429 + `Retry-After`; session and key buckets independent |
| `TestHub_CoalesceMergesIDs` | `synctest` 250 ms window |
| `TestHub_WriteDeadlineDisconnects` | |
| `TestHub_FilterPanicIsContained` | |
| `TestHub_ShutdownClosesStreams` | `Server.Shutdown` returns within drain deadline with open streams |
| `TestRegister_RejectsZeroPermAndForeignNamespace` | |
| `TestLegacyProjection_FramesGolden` (A, B, C) | byte-for-byte legacy framing from one publish |
| `TestNoGlobals` (archtest) | hub is per-App (R5) |

## 11. Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/realtime/events.go` (hub A) + `book_changes.go` | `core/realtime` hub + `books.book.changed.v1` topic (books module) + legacy projection A |
| `internal/operations/registry/bus.go` + `handlers/operations_v2.go:1063-1110` | `core.task.*` topics published by `core/tasks` + legacy projection B |
| `internal/server/handlers/opsv3/events.go` broker | its algorithm becomes `core/internal/sse`; route becomes projection C |
| `opaccess.Repairs.ForStream`/`StreamOpen` | `Topic.Visible` (books, repairs runs) + `auth.Service.Revalidate` |
| `web/src/services/eventSourceManager.ts`, `openOperationsSSE` (`api.ts:733-740`), `useOperationsStore` SSE | `@mediacore/shell` `useEventStream({topics, key})` on the native endpoint; handles `resync` by invalidating the affected stores; native `EventSource` auto-sends `Last-Event-ID` |

Order: core hub ships with projections first (no client change), then the shell moves to the
native endpoint, then projections sunset after two minor releases.

## 12. Open items

1. Whether the *arr facade needs a SignalR-compatible feed (Bazarr uses it, `raw/16` §5.1) — 08
   decides; it would be another projection of this hub.
2. Ring sizing per topic (a chatty log topic can evict the resumable window of run updates);
   per-topic ring quotas are a possible refinement if `mediacore_realtime_resyncs_total{reason="gap"}`
   shows it in practice.

## Objections to 03

None.

## Out-of-scope defects found

1. Stream A drops the **newest** event when a client's channel is full, logging at info level
   (`internal/realtime/events.go:131-138`), and never tells the client; the UI can show stale
   state indefinitely. Superseded by §4.5 (drop queued + resync).
