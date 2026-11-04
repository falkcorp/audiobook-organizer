<!-- file: docs/specs/platform-core/05-realtime.md -->
<!-- version: 1.5.0 -->
<!-- guid: 9f4f16ae-e4d9-4fd1-a6df-e409e07602ad -->
<!-- last-edited: 2026-11-04 -->

# 05 — Realtime (`core/realtime`)

Evidence commit `748fd787a`. Raw inputs: [`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §6,
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §5,
[`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md) §10.
Related: [05-events](05-events.md) (in-process bus and **the one topic catalog**), [05-http-api](05-http-api.md)
(route chain, 401 codes), [05-authn](05-authn.md) §4.12 (principal invalidation), [05-rbac](05-rbac.md)
§4.10 (topic permissions), [05-tasks](05-tasks.md) (task run views), [07-ui-shell](07-ui-shell.md) §7.9
(the shell's client). Layers and naming: [03-architecture](03-architecture.md) v1.2.0.

## 1. Purpose & scope

One server-push channel for every app: a single **SSE hub** per `App` that

- replaces the three implementations in ABO (hub A, the ops-registry stream, the v3 broker);
- is **resumable** with `Last-Event-ID` inside a bounded in-memory window, and says "reset" when it
  cannot resume;
- streams the events topics whose catalog entry carries `Realtime` metadata (05-events, SIMP-5): it
  declares **no topics and no permissions of its own**;
- filters **per subscriber, per event** by the topic's catalog permission and `Visible` filter, and
  re-checks that the subscriber's credential still stands for the life of the stream;
- applies **backpressure** without ever blocking a publisher: slow subscribers lose their oldest
  queued events and are told to reset;
- has a client contract (§4.8) that lets a browser combine backoff with resume and see 401, 429, 503
  and SSO redirects.

Not in scope: durable delivery (correctness never depends on realtime — 02 decision 11; the
`outbox` is in 05-events), WebSockets (none today, `raw/09` §6; SSE suffices because traffic is
server→client), and the *arr facade's SignalR feed (08-arr-facade may bridge to this hub).

## 2. Current state at HEAD

| Stream | Behaviour | Anchor |
|---|---|---|
| A: `GET /api/events` (`realtime.EventHub`) | `data:` frames only, no `event:`/`id:`; 25 s `: ping`; per-client channel of 100, **drop newest on full** with an info log; optional `?operation=` filter; types `operation.progress/status/log`, `system.status`, `books.changed` (250 ms coalescer); auth = any valid credential, **no permission, no per-user filtering** | `internal/realtime/events.go:19-140,216-296`, `internal/realtime/book_changes.go:18,32`, `internal/server/server_lifecycle.go:1762-1783` |
| B: `GET /api/v1/operations/events` (ops registry bus) | named events `op.created/updated/terminal/log/current_item`; immediate heartbeat; perm `library.view`; no resume | `internal/server/handlers/operations_v2.go:1063-1110`, `internal/operations/registry/bus.go:14-50` |
| C: `GET /api/v3/ops/events` (v3 broker) | `id: <boot-nonce>-<seq>`, `event: run`, 15 s heartbeat; ring 512 for `Last-Event-ID`; per-client queue 128, overflow drops queued and sets `resync`; caps 64 global (503) / 4 per bucket (429, `Retry-After: 5`), buckets = user's session vs user's API keys; per-event re-authorization of Repairs runs and of user-identifying text; closes when the credential stops standing; 250 ms flush coalescing | `internal/server/handlers/opsv3/events.go:33-75,108-178,366-460,486-618`, `internal/server/handlers/opaccess/repairs.go:227,272` |
| Clients | `eventSourceManager` for `/api/events` (backoff 3 s→60 s by `close()` + `new EventSource(url)`, which never sends `Last-Event-ID`; only `onmessage`; gives up after 5 attempts); separate `EventSource` for B; each tab opens **two** streams; v3 stream not used by the web UI | `web/src/services/eventSourceManager.ts:25-42,95,119,135-146,163`, `web/src/App.tsx:120,137`, `web/src/services/api.ts:733-740`, `raw/09` §2.3 |
| Shutdown banner | the UI watches stream A for `system.shutdown`; every authenticated subscriber receives it | `web/src/App.tsx:137-141` |
| Listener | `WriteTimeout: 0` so streams stay open; gzip excludes `/api/events` | `internal/server/server_lifecycle.go:2187-2192`, `raw/09` §2.2 row 7 |

Stream C is the prior art: its resume, reset, caps and per-event re-authorization are adopted
wholesale and generalized from "runs" to catalog topics.

## 3. Go API

Package `github.com/falkcorp/mediacore/realtime` (layer L6; imports `auth`, `events`,
`compat`, `log`, `metrics`; `rbac` types appear only in comments here, so the import block omits
it, COR4-21); implementation `core/internal/sse`. The route
`GET /api/core/v1/events` is registered by `core/internal/eventhttp` (L6, imported only by
`kernel`, 03 §3.3).

```go
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/compat"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
)

// There is no realtime.Topic and no realtime permission. A topic is streamed
// when its events.TopicSpec has Realtime != nil; its permission is
// TopicSpec.Permission, its row filter TopicSpec.Visible and its redactor
// TopicSpec.Redact (05-events). Redaction is a property of the topic,
// declared by the topic's owner in its catalog entry, core topics included
// (COR2-2): there is no separate redactor registration, so no module can
// attach a redactor to a topic it does not own and no owner can forget one.
//
// events.RedactFunc (declared in 05-events so events imports nothing of
// realtime) reduces a payload for a subscriber lacking a scope (today's
// withUserText, internal/server/handlers/opsv3/events.go:609-616):
//   type RedactFunc func(who rbac.Checker, payload json.RawMessage) json.RawMessage
// It is called on the subscriber's goroutine; it must be fast and must not block.

// Registrar is the module-scoped handle a module receives as
// module.Registrar.Realtime() (06); only the kernel builds it.
type Registrar interface {
	// Projection records a legacy framing of the hub owned by the calling module
	// (COR2-1) and returns its handler at once, during P6. The handler is lazy:
	// it is bound to the hub, whose catalog subscriptions are attached in P7,
	// and answers 503 + Retry-After until the router is built (P8). The module
	// serves it from its own route table (books: three gin manifest routes,
	// §4.9). Errors (duplicate Name, unknown topic, topic without Realtime)
	// abort boot.
	Projection(p LegacyProjection) (http.Handler, error)
}

// Hub is created by the kernel in P5, one per App (R5).
type Hub struct{ /* unexported */ }

func NewHub(opts Options, deps Deps) *Hub

type Options struct {
	RingSize     int           // resumable window, events (default 1024)
	ClientBuffer int           // per-subscriber queue (default 128)
	MaxClients   int           // global open streams (default 256)
	MaxPerBucket int           // native streams per user per credential class (default 16, §4.5)
	MaxLegacy    int           // legacy-projection streams per user per credential class (default 16, §4.5)
	Heartbeat    time.Duration // default 15s
	WriteTimeout time.Duration // per-write deadline via http.ResponseController (default 10s)
	RetryAfter   time.Duration // on 429/503 (default 5s)
}

type Deps struct {
	Auth    *auth.Service  // Revalidate
	Catalog events.Catalog // the one topic catalog (05-events)
	Bus     *events.Bus    // raw subscriptions to Realtime topics; core.principal.invalidated.v1, core.rbac.changed.v1
	Compat  *compat.Ledger // legacy projections are a compat surface ("sse", 09 §9.2.1)
	Log     *slog.Logger
	Metrics *metrics.Scope // "mediacore.realtime" (05-metrics)
	Clock   func() time.Time
}

// Handler serves GET /api/core/v1/events (registered by internal/eventhttp on
// CoreGroup with Perm rbac.AnyAuthenticated and HandleRaw).
func (h *Hub) Handler() http.Handler

// Registrar returns the module-scoped handle for moduleID (kernel only, P6).
func (h *Hub) Registrar(moduleID string) Registrar

type LegacyProjection struct {
	Name      string                                 // "a", "b", "c" (metrics, compat ledger)
	Topics    []string                               // catalog topics this legacy stream carries
	Frame     func(topic string, data json.RawMessage) (event string, payload []byte, ok bool)
	WithIDs   bool                                   // emit id: lines (only stream C did)
	Heartbeat string                                 // ": ping" (A) or ": heartbeat" (B, C)
	OnClose   func(reason string) (event string, payload []byte, ok bool) // e.g. A's system.shutdown frame
}

// Run subscribes to the catalog's Realtime topics and to invalidations, and
// owns the coalescer timers; returns when ctx ends.
func (h *Hub) Run(ctx context.Context) error

// Close sends core.stream.closed.v1{reason:"shutdown"} to every stream and
// ends them; called from http.Server.RegisterOnShutdown (§5). Idempotent.
func (h *Hub) Close(ctx context.Context) error

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

`GET /api/core/v1/events?topic=<pattern>[&topic=…][&exclude=<pattern>…][&key=<entity key>][&last_event_id=<id>]`

- `topic` patterns: exact (`core.task.run-changed.v1`), module wildcard (`books.*`), or all (`*`,
  the default). Wildcards expand to the catalog's Realtime topics the principal may see **now**;
  topics are fixed at boot. `exclude` removes topics from the expansion (same pattern grammar); the
  shell's leader stream uses `topic=*&exclude=core.task.log-appended.v1` (§4.8 item 7, FE2-21).
- An explicitly named topic the principal lacks permission for → 403 `topic_not_permitted`
  (fail closed, visible). Wildcard expansion silently omits topics the principal cannot see.
- `key` narrows to one entity (replaces stream A's `?operation=<id>` and lets the UI follow one
  task run).
- Refusals happen **before** the stream starts, as ordinary problem+json responses (05-http-api
  §4.3): 401 with one of the session-gone codes, 403, 400, 429/503 with `Retry-After`. A
  fetch-based client sees them (§4.8).
- Response: `Content-Type: text/event-stream`, `Cache-Control: no-store`,
  `X-Accel-Buffering: no`; excluded from compression: 05-http-api §4.2 stage 9 skips `HandleRaw`
  streams and every `text/event-stream` response, and the three legacy projection routes also carry
  `LegacyRoute.NoCompress` (HEAD excludes `/api/events` from gzip, `internal/server/server.go:1590-1606`;
  COR3-6). The first frames
  are `retry: 3000` and the control frame `core.stream.opened.v1` with the stream's effective topic
  list.

### 4.2 Framing

Native stream (FE-8): **no `event:` line**. Every frame, data and control alike, is an unnamed
message, so a plain `EventSource` fallback receives all of them on `onmessage` and the topic is read
from the envelope:

```
id: <bootNonce>-<seq>
data: {"topic":"<topic>","ts":"<RFC3339Nano>","key":"<key>","data":{…}}

```

`seq` is a hub-wide `uint64` assigned at publish time; `bootNonce` is 8 random bytes hex,
regenerated each process start (stream C's scheme, `opsv3/events.go:169-194`). Heartbeat: comment
line `: hb` every `Heartbeat`.

Control frames use reserved topics in core's namespace that are not in the events catalog and are
delivered to every subscriber regardless of permission: `core.stream.opened.v1` (`{topics}`),
`core.stream.reset.v1` (`{reason: "restart"|"gap"|"overflow"}` — the client refetches state over
REST), `core.stream.closed.v1` (`{reason: "shutdown"|"credential_revoked"|"permission_revoked"}`).
Control frames carry no `id:` line, so they never move the resume point.

### 4.3 Resume

- Every delivered catalog event (after coalescing) is appended to a ring of `RingSize` entries
  `{seq, topic, key, payload bytes}`.
- `Last-Event-ID` (header, or `?last_event_id=` for clients that cannot set headers) with this
  hub's nonce and a `seq` still in the ring: queue every ring entry after it that passes the
  subscriber's filters, then live events.
- Unknown nonce (server restarted), malformed id, or `seq` older than the ring: send
  `core.stream.reset.v1` with `{"reason":"restart"|"gap"}` first. No error status: a reset is
  normal.

### 4.4 Permission filtering (per subscriber, per event)

For each queued entry the subscriber goroutine checks, in order: the topic's catalog `Permission`
against the principal's checker (`Checker.Satisfies`, so confinement and `Sensitive` apply, 05-rbac
§4.4 — already guaranteed at subscribe time for explicit topics, rechecked when
`core.rbac.changed.v1` bumps the generation); the catalog `Visible(ctx, checker, envelope)`; then
the catalog's `TopicSpec.Redact`, declared by the topic's owner. Filtering happens at **send** time, not publish time, so a single
published event is evaluated once per subscriber with that subscriber's current rights, and
replayed ring entries are filtered exactly like live ones (stream C's rule, `opsv3/events.go:569-574`).

Task topics are `AnyAuthenticated` with `Visible` = the definition's `Perms.View` (05-events), so a
books-only user sees live progress for exactly the runs REST shows (COR-20); v1.0.0 additionally
required `core:tasks.view`.

### 4.5 Backpressure and caps

- Publish path: the hub holds one raw bus subscription per Realtime topic; the payload is the bus's
  JSON (encoded once per publish, 05-events). A Realtime topic is therefore always a bus topic: the
  hub never sees a `Durable` (outbox) topic, and 05-events rejects `Realtime` on a `Durable` entry at
  registration ("declare a companion bus topic"), so no topic boots that silently streams nothing
  (COR2-17). Under a mutex it appends to the ring, then for each
  subscriber appends to its queue and non-blocking-signals its wake channel (capacity 1). No
  goroutine per publish, no blocking send, no per-publish allocation per subscriber beyond a slice
  append.
- `RealtimeMeta.Coalesce > 0`: events with the same `Key` within the window fold into the latest
  (books' book-change topic uses 250 ms, as `book_changes.go:32` does); deleted supersedes updated,
  per 05-events' coalescing rule.
- Subscriber queue full (`ClientBuffer`): drop the queued entries, send
  `core.stream.reset.v1{"reason":"overflow"}`, continue live (stream C semantics,
  `opsv3/events.go:417-447`). Stream A's "drop newest silently" (`events.go:131-138`) is not kept.
- Write deadline: before each write the handler sets
  `http.ResponseController.SetWriteDeadline(now + WriteTimeout)`; a client that does not read
  for that long is disconnected (the listener keeps `WriteTimeout: 0`,
  `server_lifecycle.go:2192`, so per-write deadlines are the only bound).
- **Caps (FE-9).** `MaxClients` (503 + `Retry-After`) is global. Per user per credential class
  (session/interactive vs API key — stream C's buckets, `opsv3/events.go:383-406`) the native
  stream allows `MaxPerBucket` (default **16**, up from v1.0.0's 4) and legacy projections a
  separate `MaxLegacy` bucket (default 16), both 429 + `Retry-After`. Today each tab opens two
  legacy streams (`App.tsx:120,137`), and the same user's phone and desktop share a bucket; with a
  cap of 4 the third tab had dead realtime. The shell opens **one native stream per browser per
  app** (tab leader election, §4.8 item 7, 07 §7.9), which keeps HTTP/1.1 deployments clear of the
  browser's six connections per host. The election must work where that limit actually bites:
  HTTPS listeners negotiate HTTP/2 (`server_lifecycle.go:903-1078`), so the six-connection limit is
  a **plain-HTTP** LAN problem, and `navigator.locks` (Web Locks) exists only in secure contexts
  (HTTPS or loopback). The caps therefore assume the election but do not depend on it: in the
  last-resort per-tab mode (§4.8 item 7) a browser holds at most one stream per visible tab plus one
  keyed log stream, well inside `MaxPerBucket` (16). Auth-disabled principals share one bucket
  bounded only by `MaxClients`.

### 4.6 Credential lifetime

A stream outlives the request that opened it. The hub listens on `events` for
`core.principal.invalidated.v1` (05-authn §4.12) and `core.rbac.changed.v1`:

- invalidation matching the subscriber's user or credential → send
  `core.stream.closed.v1 {"reason":"credential_revoked"}` and end the stream;
- rbac generation change → the subscriber re-resolves its checker on next wake; topics it can no
  longer see are dropped from its set (explicitly named ones end the stream with
  `reason:"permission_revoked"`).
- Additionally every heartbeat calls `auth.Service.Revalidate` (cheap, in-memory generation
  table), covering expiry of the session itself (stream C's `StreamOpen()` check,
  `opaccess/repairs.go:272`).

### 4.7 Streamed core topics

Declared in the events catalog by their owners (05-events); listed here because the shell and the
legacy projections consume them.

| Topic | Owner | Permission (catalog) | Payload | Replaces |
|---|---|---|---|---|
| `core.task.run-changed.v1` | 05-tasks | `AnyAuthenticated` + `Visible` = definition `View` | run view with progress, workers, current item; the topic's own `TopicSpec.Redact` = `tasks.RedactRunView`, declared by `tasks`, blanks `RunView.Current` (every worker's current-item label) and any run error text unless the subscriber holds `core:users.view` — the same function runs in the `GET /api/core/v1/tasks/runs[/{id}]` serializer, so REST and stream agree (COR2-2) | stream C `run`, stream B `op.created/updated/current_item/terminal`, stream A `operation.progress/status` |
| `core.task.log-appended.v1` | 05-tasks | as above | log line | stream A `operation.log`, B `op.log` |
| `core.system.status-changed.v1` | 05-health | `core:system.status` (seeded to `editor`/`viewer`, 05-rbac §4.8) | readiness and maintenance tokens only | the native successor of stream A's `system.status`; projection A keeps HEAD's frame from books' ticker topic for the whole window (§4.9, COR4-7) |
| `books.book.changed.v1` | books | `books:view` | `{kind: created|updated|deleted, id}`, `Key` = book ID, `Coalesce: 250ms` | stream A `books.changed` (`book_changes.go:18-32`) |
| `books.system.status-ticked.v1` | books (from 09 S6) | `books:view` | HEAD's 5-second payload unchanged, `{books, folders, memory_alloc, goroutines, timestamp}` (`internal/server/server_lifecycle.go:404-441`), published by a books worker that takes over HEAD's ticker | stream A `system.status`, for the whole window: projection A's only source of that frame (§4.9, COR4-7, COR5-9) |

**Successor scope of HEAD's gate (COR2-2).** HEAD withholds run error text and current-item labels
from callers without `users.manage`, because a label can name a user (`opsv3/events.go:609-616`,
`view.go:59-64`). The canonical successor is `core:users.view`: 05-rbac §6.1 maps `users.manage` to
`core:users.manage`, which implies `core:users.view`, and by default only `admin` and holders of
the `users.manage` expansion hold it. A custom role holding only `core:users.view` already sees the
user list, so seeing a username inside a label discloses nothing more.

The shutdown notice is not a topic: every subscriber, whatever its permissions, receives
`core.stream.closed.v1{reason:"shutdown"}` from `Hub.Close`, and the shell drives its shutdown
backdrop from it (FE-10). v1.0.0's `core.notify.v1` is gone: the notify inbox is deferred
(SIMP-10), and when it returns it is a catalog topic like the others.

### 4.8 Client contract (FE-8)

The shell's realtime transport (07 §7.9) is a `fetch()` + `ReadableStream` SSE reader, because
native `EventSource` cannot do what the UI needs: it sends `Last-Event-ID` only on its own fixed
`retry:` reconnect (so backoff and resume exclude each other), and on a non-200 response or a
cross-origin SSO redirect it fails with no status. The server guarantees, and the client relies on:

1. **Visible refusals.** Every refusal is an HTTP status with a problem+json body before the first
   byte of the stream (§4.1); 429 and 503 carry `Retry-After`. 401 codes are the session-gone codes
   of 05-http-api §4.3, which the client routes through the shell's single 401 rule (07).
2. **Resume on every reconnect.** The client sends the last seen `id` as `Last-Event-ID` (header) on
   every reconnect it schedules; `?last_event_id=` is accepted for clients that cannot set headers.
   The server replays from the ring or sends `core.stream.reset.v1`.
3. **Backoff.** The client reconnects with 3 s doubling to 60 s and jitter, and never sooner than a
   `Retry-After`; there is no "give up after 5" (today's manager, `eventSourceManager.ts:163`):
   realtime is a hint, so the client keeps trying and shows a status chip.
4. **SSO redirect (normative, FE2-20).** The request uses `redirect: "manual"`. Any response with
   `type === "opaqueredirect"` (status 0) is an SSO bounce and is raised as the API client's
   `ApiAuthRedirectError` (05-http-api §7). `response.redirected` is never consulted: with
   `redirect: "manual"` it is never true, and with the default `follow` a cross-origin SSO redirect
   fails CORS as a `TypeError` before any flag can be read. 07 §7.9.2 refers to this item.
5. **No named events.** All frames arrive as unnamed messages (§4.2), so the same parser serves a
   plain `EventSource` fallback.
6. **Reset handling.** On `core.stream.reset.v1` the client invalidates the affected stores and
   refetches over REST; on `core.stream.closed.v1` it reconnects with backoff (shutdown: shows the
   backdrop first).
7. **One stream per browser, also over plain HTTP (FE2-2, FE2-21).** Tabs elect one leader per app
   that holds the stream and fans frames out to the other tabs over a `BroadcastChannel`:
   - **Election.** Web Locks (`navigator.locks.request`) when `navigator.locks` exists (secure
     contexts); otherwise a `BroadcastChannel` heartbeat election, which works in insecure contexts:
     the leader announces every 2 s, followers take over after 3 missed beats, ties are broken by the
     lowest tab ID. A leader releases leadership on `pagehide` and on `freeze` (a frozen page would
     otherwise keep its lock or its claim and starve the followers), and a background leader's
     throttled timers are covered by the 3-beat takeover.
   - **Last resort.** If neither API exists, each tab opens its own stream only while it is visible
     and closes it on `visibilitychange` to hidden (refetching over REST when it becomes visible
     again), so a plain-HTTP browser never holds more than one stream per visible tab.
   - **Subscription set.** The leader subscribes `topic=*&exclude=core.task.log-appended.v1`.
     `useTopic` keys filter on the client. Log lines are not on the shared stream (with `*` every
     visible run's log would reach every tab and evict the resumable window, §12 item 2): the tab
     that shows a run's log opens its own keyed stream
     (`topic=core.task.log-appended.v1&key=<run id>`, counted in the same bucket) or reads
     `GET /api/core/v1/tasks/runs/{id}/log` over REST.
   - **Test.** 07's `tabs.spec.ts` runs against a non-loopback address (the server bound to the
     runner's LAN interface), where `isSecureContext` is false, and asserts the `BroadcastChannel`
     election and a single open stream.

### 4.9 Legacy projections and migration timing (compatibility window)

The three legacy URLs become projections of the one hub. They are **not** `httpapi.Alias`es (an
alias re-exposes a native route of the caller's own group with the same handler; a projection has
its own handler and the native stream lives on `CoreGroup`, COR2-1, SEC2-25). The `books` module
obtains each handler from `Registrar.Realtime().Projection(p)` during P6 and serves it from the
**gin legacy manifest** at the three legacy paths (`Perm: rbac.AnyAuthenticated`, as for the native
stream; `OwnAuth: false`; per-topic permissions are applied per event inside the hub, §4.4). They are
recorded in the compat ledger as surface `sse` (09 §9.2.1), their use is counted in
`mediacore_compat_legacy_use_total{surface="sse",name=<projection>}`, and a contract PR removes them
once the window and the zero-use gate allow. No clock removes them.

| Legacy URL | Projection |
|---|---|
| `GET /api/events` | `core.task.*`, `books.book.changed.v1` and `books.system.status-ticked.v1` (topics by stage below); frames `data: {"type":…,"id":…,"timestamp":…,"data":…}` with the old type names; `: ping`; no ids; `?operation=` → `key`; `OnClose("shutdown")` emits today's `system.shutdown` frame to every subscriber |
| `GET /api/v1/operations/events` | the task topics mapped to `event: op.created|op.updated|op.terminal|op.log|op.current_item` |
| `GET /api/v3/ops/events` | `core.task.run-changed.v1` as `event: run`, ids kept, `event: resync` for the reset control frame |

**Projection A's topics exist at the stage that binds them (COR4-7).** The hub binds projections at
P7 and an unknown topic aborts boot, so projection A names only topics declared in that build's
catalog:

| Stages | Projection A topics | `system.status` frame |
|---|---|---|
| S6–S7 | `core.task.*` (05-tasks, S5), `books.book.changed.v1` (books' `BookChangeCoalescer` rewritten to publish it, 09 S6), `books.system.status-ticked.v1` | from `books.system.status-ticked.v1`: a books catalog topic (`Realtime`, permission `books:view`) published every 5 s by a books worker that takes over HEAD's ticker, payload unchanged `{books, folders, memory_alloc, goroutines, timestamp}` (`internal/server/server_lifecycle.go:404-441`) |
| S8 onward | the same | unchanged: `core.system.status-changed.v1` (05-health's component registry, S8) is **not** added to projection A, so stream A's `system.status` payload never changes and S8 adds no legacy frame type and no compat-ledger row; the topic is on the native stream only. This file owns the rule; 05-health and 09 S6 state the same (COR5-9) |

`TestProjectionTopicsExistAtStage` runs P7 with the S6 catalog and with the S8 catalog.

Permissions on projections are the native topic permissions (stricter than stream A's "any
credential", which the 2026-09 pen-test gate MED-2 already narrowed to authenticated,
`server_lifecycle.go:1762-1781`).

**Order (FE-10), one sequence in all files:** 09 S6 ships the hub with the three projections and
**no client change** (the UI keeps `eventSourceManager` on the legacy URLs; projection streams use
the `MaxLegacy` bucket). The shell-adoption stage (07, 09 S9) moves the UI to the native endpoint
with the client of §4.8. Projections are then removed by a contract PR under the compat window.
Changing only the URL earlier would break every consumer, because native frames are envelopes with
new topic names.

**What feeds the legacy streams, per stage (FE2-3).** The task executor moves at S5 but the hub and
its projections arrive at S6, so one stage needs an explicit feed:

| Stages | Source of run events | Legacy streams A, B, C | UI (Activity page, `useOperationsStore`) |
|---|---|---|---|
| S0–S4 | ABO's operations registry and v3 runner | HEAD's three implementations, unchanged | unchanged |
| S5 | `core/tasks` publishes `core.task.run-changed.v1` / `core.task.log-appended.v1` on the bus | still HEAD's implementations, fed by a **books-side bridge** (the one form for every file, COR3-9, FE3-12): a bus subscriber on `core.task.*`, registered by `books` with `Registrar.Subscribe` at S5, translates each core task event into today's `registry.EventHub` publish (stream B), hub A's `Send*` (`operation.*`) and the v3 broker's `run` events (stream C); core never imports ABO (R1), so the bridge lives in `books`. **HEAD's per-event filters keep working (SEC3-9):** the bridge forwards only events of kinds that have a `LegacyDefID` (05-tasks §5T.3), under that ID, so HEAD's def-ID filters (`opEventFilter`, `handlers/operations_v2.go:1084-1104`; stream C's re-check, `opsv3/events.go:569-618`) see exactly what they see today — core kinds HEAD's streams show today keep appearing under their legacy IDs, and a kind without a `LegacyDefID` (no older build shows it) is never forwarded; before publishing to stream A, which any authenticated credential reads, it applies `tasks.RedactRunView` with a nil checker (labels and error text blanked) | the Activity page and `useOperationsStore` keep their hand-written calls on the legacy paths and the legacy streams, so REST and stream use one vocabulary (07 §7.11, FE3-6); S5 done-check: "streams A, B and C show live progress on S5" |
| S6 | the same bus topics | the hub's three projections replace HEAD's implementations; the S5 bridge, `registry.EventHub`, hub A and the v3 broker are deleted in the S6 PR | unchanged (legacy URLs, `MaxLegacy` bucket) |
| S9 | the same | projections kept for external consumers until their contract PR | native REST and the native stream move **together** (FE-10) |

## 5. Lifecycle

1. **P5** (06 §6.5.1): the kernel creates the `Hub` after `auth`/`rbac`/`events` (COR2-1).
2. **P6**: each module's `Registrar.Realtime().Projection` records its projections and returns lazy
   handlers; books puts them in its gin manifest (§4.9). Redaction needs no registration: it is in
   the catalog entries the modules declare (§3).
3. **P7**: after the events catalog is frozen, the hub subscribes to every Realtime topic and binds
   the recorded projections; errors (unknown topic, duplicate projection) abort boot.
4. **P8**: `kernel` mounts `Handler()` through `internal/eventhttp` (`HandleRaw`, `RateDefault`, body
   0); the projection handlers stop answering 503.
5. `App.Run` starts `Hub.Run` (`wg.Go`): raw bus subscriptions, coalescer timers, invalidation
   listener.
6. Shutdown: the kernel calls `Hub.Close` from `http.Server.RegisterOnShutdown` — every stream
   receives `core.stream.closed.v1 {"reason":"shutdown"}` (projection A: `system.shutdown`) and
   ends, so `Shutdown` is not held open by long-lived streams; `Run` returns when its context ends.

## 6. Error semantics

| Path | Behaviour | Fail | Why |
|---|---|---|---|
| Projection invalid (unknown topic, topic without `Realtime`, duplicate name) | boot error | closed | |
| Projection handler called before P8 | 503 + `Retry-After` | closed | the hub is not yet serving (COR2-1) |
| Anonymous request or failed credential | 401 problem with the stage-13 code (route is `AnyAuthenticated`) | closed | visible to the fetch client (§4.8) |
| Confined principal (worker key; facade keys once `arrfacade` lands) | 403 (fails `AnyAuthenticated`, 05-rbac §4.10) | closed | |
| Explicit topic not permitted | 403 `topic_not_permitted` | closed | |
| Unknown topic | 400 `unknown_topic` | closed | typo visibility |
| Over caps | 503 / 429 problem + `Retry-After` | closed | resource bound |
| Undecodable bus payload | event dropped, `mediacore_realtime_events_dropped_total{reason="decode"}`, error log once per topic per minute | open | notify-only; never fail the publisher |
| Publish with no subscribers | appended to ring only | — | resume needs it |
| Subscriber queue overflow | drop queue, reset | open (for that subscriber) | publisher must not block |
| Write error / deadline exceeded | end stream | — | client gone or stuck |
| `Visible`/redactor panics | recovered per event, event skipped for that subscriber, counted | closed (event withheld) | a buggy filter must not leak or kill the hub |
| `Revalidate` returns error (store down) | keep stream until next heartbeat; end after 3 consecutive failures | open briefly, then closed | short store blips must not drop every stream; persistent failure must not keep a revoked credential streaming |
| Bad `Last-Event-ID` | reset | open | normal |

## 7. Concurrency contract

- The publish path is safe from any goroutine; O(subscribers) non-blocking work under one mutex
  (ring + subscriber list). Payload bytes come from the bus already encoded.
- Each stream uses exactly its HTTP handler goroutine; no extra goroutine per subscriber.
- Coalescing: one timer per (topic, key) bucket, owned by the hub's `Run` goroutine; bounded by
  `max_coalesce_keys` (10,000) — overflow delivers immediately instead of coalescing.
- Subscriber state (`queue`, `reset`, checker) is guarded by the subscriber's own mutex; the hub
  never holds a subscriber lock while holding the hub lock longer than an append.
- Tests use `testing/synctest` for heartbeats, coalescing and deadlines.

## 8. Config keys

`Protection` levels are defined by 05-config.

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.realtime.ring_size` | int | 1024 | no | none |
| `core.realtime.client_buffer` | int | 128 | no | none |
| `core.realtime.max_clients` | int | 256 | yes (new streams) | none |
| `core.realtime.max_per_bucket` | int | 16 | yes | none |
| `core.realtime.max_legacy_per_bucket` | int | 16 | yes | none |
| `core.realtime.heartbeat` | duration | 15s | no | none |
| `core.realtime.write_timeout` | duration | 10s | yes | none |
| `core.realtime.retry_after` | duration | 5s | yes | none |
| `core.realtime.max_coalesce_keys` | int | 10000 | no | none |

## 9. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_realtime_subscribers` | gauge | `kind` (`native`,`legacy_a`,`legacy_b`,`legacy_c`) |
| `mediacore_realtime_events_published_total` | counter | `topic` |
| `mediacore_realtime_events_delivered_total` | counter | `topic` |
| `mediacore_realtime_events_filtered_total` | counter | `topic`, `reason` (`perm`,`visible`) |
| `mediacore_realtime_events_dropped_total` | counter | `reason` (`overflow`,`decode`,`filter_panic`) |
| `mediacore_realtime_resets_total` | counter | `reason` (`restart`,`gap`,`overflow`) |
| `mediacore_realtime_stream_refusals_total` | counter | `reason` (`global_cap`,`bucket_cap`,`legacy_cap`,`denied`) |
| `mediacore_realtime_streams_closed_total` | counter | `reason` (`client`,`shutdown`,`credential_revoked`,`permission_revoked`,`write_timeout`) |
| `mediacore_realtime_ring_oldest_age_seconds` | gauge | — |

Legacy projection use is counted by `compat` (`mediacore_compat_legacy_use_total{surface="sse"}`).

## 10. Tests

| Test | Asserts |
|---|---|
| `TestHub_ResumeFromLastEventID` | replay after id, then live, no duplicates |
| `TestHub_ResumeAfterScheduledReconnect` | a client that reconnects itself after a backoff with `Last-Event-ID` (header and query forms) replays from the last id (FE-8) |
| `TestHub_NativeFramesUnnamed` | no `event:` line on the native stream; an `EventSource`-style parser gets every frame via `onmessage` |
| `TestHub_RefusalsAreStatusesBeforeStream` | 401/403/429/503 arrive as problem+json with `Retry-After` where applicable, no `text/event-stream` body |
| `TestHub_ResetOnRestartNonce` / `TestHub_ResetOnGap` | |
| `TestHub_SlowSubscriberDoesNotBlockPublisher` | `synctest`: publisher completes N publishes while one subscriber never reads; that subscriber gets `reset{overflow}` |
| `TestHub_PerEventPermissionFilter` | two subscribers, different scopes, same publish; permission read from the events catalog |
| `TestHub_TaskTopicVisibleWithoutTasksView` | books-only user receives run updates for definitions whose `View` they hold (COR-20) |
| `TestHub_ReplayIsFilteredLikeLive` | |
| `TestHub_StreamClosesOnCredentialRevoke` / `TestHub_RevalidateOnHeartbeat` | |
| `TestHub_PermissionRevokedDropsTopic` | rbac generation bump |
| `TestHub_CapsGlobalBucketAndLegacy` | 503/429 + `Retry-After`; session and key buckets independent; legacy projections in their own bucket (FE-9) |
| `TestHub_CoalesceLatestPerKey` | `synctest` 250 ms window |
| `TestHub_WriteDeadlineDisconnects` | |
| `TestHub_FilterPanicIsContained` | |
| `TestHub_ShutdownClosesStreams` | `Server.Shutdown` returns within drain deadline with open streams; every subscriber, any permission, gets `closed{shutdown}` (FE-10) |
| `TestRunRedaction_SameOnStreamAndREST` | a subscriber without `core:users.view` receives `core.task.run-changed.v1` with `Current` blanked, and `GET /tasks/runs/{id}` returns the same blanked view; with `core:users.view` both carry it (COR2-2) |
| `TestProjection_LazyHandler503UntilServing` | a projection handler obtained in P6 answers 503 before P8 and streams after (COR2-1) |
| `TestProjection_RejectsUnknownOrNonRealtimeTopic` | |
| `TestHub_ExcludePattern` | `topic=*&exclude=core.task.log-appended.v1` carries no log lines; a keyed log stream carries one run's lines (FE2-21) |
| `TestS5Bridge_LegacyStreamsShowCoreRuns` (books, S5 only) | a run started on the core executor at S5 appears on streams A, B and C in today's framing, under its `LegacyDefID` (FE2-3, COR3-9) |
| `TestS5Bridge_RepairsRunHiddenFromViewer` (books, S5 only) | a Repairs run is not shown on stream B to a `library.view` subscriber without `users.manage`, stream C re-checks it, a kind without `LegacyDefID` reaches no stream, and stream A carries the run with its labels blanked (SEC3-9) |
| `TestProjectionTopicsExistAtStage` | P7 binds projection A with the S6 catalog (no `health` status topic) and with the S8 catalog; stream A's `system.status` frame keeps HEAD's payload at both (COR4-7) |
| `TestLegacyProjection_FramesGolden` (A, B, C) | byte-for-byte legacy framing from one publish, including A's `system.shutdown` |
| `TestNoGlobals` (archtest) | hub is per-App (R5) |

## 11. Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/realtime/events.go` (hub A) + `book_changes.go` | `core/realtime` hub + books' `books.book.changed.v1` catalog topic (`Realtime{Coalesce: 250ms}`) + legacy projection A |
| `internal/operations/registry/bus.go` + `handlers/operations_v2.go:1063-1110` | `core.task.run-changed.v1` / `core.task.log-appended.v1` published by `core/tasks` + legacy projection B |
| `internal/server/handlers/opsv3/events.go` broker | its algorithm becomes `core/internal/sse`; route becomes projection C |
| `opaccess.Repairs.ForStream`/`StreamOpen` | the catalog `Visible` of the repairs topics (books) + `auth.Service.Revalidate` |
| `withUserText` (`opsv3/events.go:609-616`) | `tasks.RedactRunView` as the `TopicSpec.Redact` of `core.task.run-changed.v1` and the same function in the REST run serializer (§4.7) |
| `web/src/services/eventSourceManager.ts`, `openOperationsSSE` (`api.ts:733-740`), `useOperationsStore` SSE | unchanged until the shell stage (fed by the S5 bridge, then the S6 projections, §4.9; the legacy projection routes carry `NoCompress`, §4.1); then `@mediacore/shell`'s `useTopic<T>(topic, onEvent, {key})` and `useResync(handler)` (07 §7.4.2, §7.9; one signature, FE2-20) over the fetch-based reader of §4.8, one stream per browser per app |

## 12. Open items

1. Whether the *arr facade needs a SignalR-compatible feed (Bazarr uses it, `raw/16` §5.1) — 08
   decides; it would be another projection of this hub.
2. Ring sizing per topic (a chatty log topic can evict the resumable window of run updates);
   per-topic ring quotas are a possible refinement if `mediacore_realtime_resets_total{reason="gap"}`
   shows it in practice.

## Objections to 03

None. (The control-frame names `core.stream.opened|reset|closed.v1` follow 03 §3.6's
`<noun>.<verb-past>.v<N>` grammar; they are reserved in core's namespace and are not catalog topics.)

## Out-of-scope defects found

1. Stream A drops the **newest** event when a client's channel is full, logging at info level
   (`internal/realtime/events.go:131-138`), and never tells the client; the UI can show stale
   state indefinitely. Superseded by §4.5 (drop queued + reset).

## Review history

Round 1 (2026-11-04), findings applied to this file:

- SIMP-5 — realtime declares no topics or permissions; it streams events-catalog topics with `Realtime` metadata; `Register`/`Bridge`/`Topic[T]` removed; owner-only `Redact` hook (§1, §3, §4.4, §4.7).
- COR-20 — `*events.Bus`, `events.Catalog`, `*metrics.Scope` in `Deps`; `Hub.Close` added; task topics mapped through the catalog (`core.task.run-changed.v1`, `core.task.log-appended.v1`, names from R-C); topic permission `AnyAuthenticated` + `Visible` = definition `View`; topic names per 03 §3.6 (§3, §4.2, §4.4, §4.7).
- FE-8 — native stream has no `event:` lines; refusals are statuses before the stream; client contract for fetch-based reader with backoff + resume, SSO redirect, 401 routing (§4.1, §4.2, §4.8).
- FE-9 — `max_per_bucket` 16; legacy projections in their own bucket; one stream per browser per app assumed (§4.5, §8).
- FE-10 — one migration order (S6 projections, no client change; shell stage moves to native); shutdown backdrop from `core.stream.closed.v1`; `useTopic`/`useResync` naming (§4.7, §4.9, §11).
- SIMP-7 — projections are a compat surface removed only by a contract PR; compat counter (§4.9, §9).
- SIMP-10 — `core.notify.v1` dropped with the deferred inbox (§4.7).
- SEC-8 — system status topic requires `core:system.status`, not `core:system.view` (§4.7).
- SEC-3 / SEC-5 — confined principals refused on the `AnyAuthenticated` stream (§6).
- COR-18 / cross-cluster (R-A) — route registered by `internal/eventhttp` (§3, §5).
- COR-19 — layer L6 per 03 v1.2.0 (§3).
- Cross-cluster (R-E) — Protection column (§8).

Round 2 (2026-11-04), findings applied to this file:

- COR2-1 — `realtime.Registrar.Projection` returns a lazy handler (503 until P8); hub built in P5, projections recorded in P6 and bound in P7; books serves them as gin manifest routes, not aliases (§3, §4.9, §5, §6, §10).
- COR2-2 — "books' redactor" deleted; redaction is the topic's catalog `TopicSpec.Redact` (`tasks.RedactRunView`), set by `tasks` on `core.task.run-changed.v1` and reused by the REST serializer; successor scope `core:users.view` stated; `Registrar.Redact` removed (§3, §4.4, §4.7, §10, §11).
- COR2-17 — Realtime topics are bus topics; Durable+Realtime rejected by 05-events (§4.5).
- SEC2-25 — projections described as gin manifest routes wrapping the hub, not aliases (§4.9).
- FE2-2 — election: Web Locks, else `BroadcastChannel` heartbeat election, else per-visible-tab streams; release on `pagehide`/`freeze`; caps sized for plain HTTP; non-loopback e2e (§4.5, §4.8).
- FE2-3 — per-stage feed table: S5 books-side bridge into the legacy hubs, deleted at S6; UI stays on aliases until S9 (§4.9, §10).
- FE2-20 — normative manual-redirect detection; one `useTopic<T>(topic, onEvent, {key})` signature (§4.8, §11).
- FE2-21 — leader subscribes `*` minus `core.task.log-appended.v1` via `exclude=`; per-run keyed log stream or REST (§4.1, §4.8, §10).
- SIMP2-6 — facade keys referred to as deferred (§6).

Round 3 (2026-11-04), findings applied to this file:

- COR3-6 — the native stream and the three legacy projection routes are never compressed (`text/event-stream` rule and `LegacyRoute.NoCompress`, 05-http-api stage 9) (§4.1, §11).
- COR3-9 / FE3-12 — the S5 bridge is a books bus subscriber on `core.task.*` registered with `Registrar.Subscribe`, feeding streams A, B and C; S5 done-check names all three (§4.9, §10).
- COR3-13 — `realtime` import block completed (`errors`, `log/slog`, `compat`, `metrics`) (§3).
- COR3-15 — the "Successor scope" paragraph moved below the §4.7 table (§4.7).
- SEC3-9 — the S5 bridge forwards only kinds with a `LegacyDefID`, under that ID, so HEAD's per-event filters apply; never a kind without one; stream A gets `RedactRunView` with a nil checker; `TestS5Bridge_RepairsRunHiddenFromViewer` (§4.9, §10).
- FE3-6 — the S5–S9 UI keeps its hand-written calls on the legacy paths (§4.9).

Round 4 (2026-11-04), findings applied to this file:

- COR4-7 — projection A's topics are stage-dependent: `books.system.status-ticked.v1` (books worker, HEAD's 5-second payload) feeds `system.status` from S6; `core.system.status-changed.v1` is never added to projection A; `TestProjectionTopicsExistAtStage` (§4.7, §4.9, §10).
- COR4-21 — unused `rbac` import dropped from the code block and the package description; blank line inside the §4.7 table removed (§3, §4.7).

Round 5 (2026-11-04), findings applied to this file:

- COR5-9 — COR4-7 residue: `books.system.status-ticked.v1` added to §4.7's table; the S8 row states no legacy frame type and no compat-ledger row is added, and that 05-health and 09 S6 follow this rule (§4.7, §4.9).
