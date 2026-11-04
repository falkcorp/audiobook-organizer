<!-- file: docs/specs/platform-core/05-events.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6dbdef8e-e3f3-4088-bdb9-ba37a706165d -->
<!-- last-edited: 2026-11-04 -->

# 05 — Events (`core/events`) and Outbox (`core/outbox`)

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §5,
[`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md) §2 and §7.3,
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md) §2 (adversarial attack 2), [`raw/06-tasks-ops.md`](../../plans/platform-core/raw/06-tasks-ops.md) §5.
Decision basis: [02](02-decision-record.md) decision 11 ("bus is notification-only; correctness uses
durable ops or an outbox") and gate **G4**. Layer 3 (R4); `outbox` imports `tasks` (its relay is a
task), `tasks` imports `events` (it publishes run events); neither imports the other back.

## 5E.1 Purpose & scope

Two packages with deliberately different guarantees:

| | `events` | `outbox` |
|---|---|---|
| Guarantee | **notify-only**: best effort, in-process, may drop | **durable, at-least-once**, survives crash, committed atomically with the state change that caused it |
| Use for | UI refresh, cache invalidation, metrics, realtime frames, "something changed" hints | any cross-module or external effect whose loss would leave state wrong (G4) |
| Never use for | anything correctness-critical | high-frequency UI chatter |

Out of scope: SSE transport, resumable streams and per-viewer filtering ([05-realtime](05-realtime.md)
subscribes to `events`); notification adapters ([05-notify](05-notify.md) consumes from either).

## 5E.2 Current state at HEAD

Three unrelated buses, none durable (`raw/12` §5):

| Bus | Anchor | Shape | Delivery | Defects relative to the target |
|---|---|---|---|---|
| Plugin lifecycle `EventBus` | `internal/plugin/events.go:60-110` | 12 audiobook `EventType` strings (`:18-31`); `Event{Type, Timestamp, BookID, UserID, Data map[string]any}` (`:34-40`) | `Publish` spawns **one goroutine per handler per event**, passes the **publisher's ctx**, recovers panics (`:85-103`) | unbounded goroutines under load; handler sees a request ctx that may already be canceled; no drop accounting; untyped payload. Design Q1 had decided "bounded channel, bus lifecycle ctx" (`raw/07` §2, §7.3) and was not implemented. |
| Ops `EventHub` | `internal/operations/registry/bus.go:29-101` | `Event{Name, Payload any}`; names `op.created` (`registry.go:1287`), `op.terminal` (`:1332`), `op.updated` (`dispatcher.go:549`, `reporter_db.go:464`), `op.log` (`reporter_db.go:603`), `op.current_item` (`reporter_db.go:776`) | 64-slot channel per subscriber (`bus.go:76`), drop on full **silently** (`bus.go:53-58`) | no metric for drops; untyped payload |
| UI `realtime.EventHub` | `internal/realtime/events.go:78-216` | SSE transport with `Send*` helpers (`:146-198`) and `BookChangeCoalescer` (`book_changes.go:32`) | 100-slot channel per client (`events.go:49`), drop logged at **Info** per event (`events.go:133-137`) | a transport doubling as a bus; log spam under load |

Subscribers of the plugin bus: `internal/dedup/lifecycle.go:94` (`book.imported`), the webhook plugin
(`internal/plugins/webhook/plugin.go:79`). Publishers: 11 files (handlers, iTunes service,
quarantine; grep at HEAD). Op definitions can declare `Triggers` (`types.go:174`), but the only one,
`deluge.path-update` → `book.relocated` (`internal/plugins/deluge/path_update.go:49-53`), is dead:
nothing publishes `book.relocated` and the registry only persists trigger names (`registry.go:882,2330`).

Outbox prior art: one tag write-back outbox existed, "never wired" and since removed
(`internal/server/itunes_writeback_cleanup.go:15-23`, family `pref:_system:outbox:writeback:`
`internal/database/keyfamilies.go:242`). Today correctness-critical follow-ups are done by enqueuing a
v2 op directly from the handler, outside the storage batch that changed state, so a crash between
the two loses the follow-up.

## 5E.3 Go API — `events`

```go
package events // github.com/falkcorp/mediacore/events

// Topic names a typed event stream. Name grammar (03 §3.6):
//   "<moduleID>.<noun>.<verb-past>.v<N>", segments [a-z][a-z0-9-]*, N >= 1.
// e.g. "books.book.imported.v1", "core.task.finished.v1".
// A Topic value is inert until its module declares it (Registrar.Topic, 06 §6.4.3).
type Topic[T any] struct{ name string }

func NewTopic[T any](name string) Topic[T]
func (t Topic[T]) Name() string
func (t Topic[T]) Spec() TopicSpec // for Registrar.Topic; carries the JSON Schema of T

type TopicSpec struct {
	Name   string
	Schema json.RawMessage // generated from T; published in the module manifest for raw subscribers
}

type Envelope[T any] struct {
	ID      string    // ULID, unique per publish
	Topic   string
	Time    time.Time
	Key     string    // entity key for coalescing/filtering ("" = none)
	Actor   string    // user ID, "system", or "task:<run-id>"
	RunID   string    // originating task run, if any
	TraceID string    // from the publisher's span, for correlation only
	Payload T
}

type RawEnvelope struct {
	ID, Topic, Key, Actor, RunID, TraceID string
	Time    time.Time
	Payload json.RawMessage
}

// Bus is one per App (R5: never a package global). Constructed by the kernel.
type Bus struct{ /* unexported */ }

// Publish never blocks on subscribers and never returns an error: it is
// notify-only. ctx is read for actor/run/trace values only; it is NOT handed
// to handlers. Publishing to a topic no enabled module declared is counted
// (publish_rejected) and logged once per topic.
func Publish[T any](b *Bus, ctx context.Context, t Topic[T], key string, payload T)

type Handler[T any] func(ctx context.Context, e Envelope[T])
type RawHandler func(ctx context.Context, e RawEnvelope)

// Subscribe registers a typed subscriber. Allowed for the declaring module's
// own topics and for core.* topics (whose payload types live in core).
func Subscribe[T any](b *Bus, t Topic[T], name string, h Handler[T], opts ...SubOption) (*Subscription, error)

// SubscribeRaw is how a module subscribes to ANOTHER module's topic without
// importing it (R3): by name, with a JSON payload validated against the
// declared schema version. The JSON form is encoded once per publish, lazily,
// only when a raw subscriber exists.
func SubscribeRaw(b *Bus, topic, name string, h RawHandler, opts ...SubOption) (*Subscription, error)

type SubOption func(*subConfig)

func QueueSize(n int) SubOption               // default core.events.queue_size (256); max 65536
func DropOldest() SubOption                   // default policy is DropNewest (keep queued, reject incoming)
func KeyFilter(f func(key string) bool) SubOption
func Coalesce(window time.Duration) SubOption // deliver at most one event per Key per window (latest wins)

type Subscription struct{ /* unexported */ }

func (s *Subscription) Close()
func (s *Subscription) Name() string

func (b *Bus) Close(ctx context.Context) error // stop accepting, drain queues until ctx, then drop and count
```

Subscription names are `<moduleID>.<name>` and unique per bus (kernel-checked); they are the
`subscriber` metric label.

## 5E.4 Go API — `outbox`

```go
package outbox // github.com/falkcorp/mediacore/outbox

// Topic is a durable stream. Same name grammar as events topics; the two
// namespaces are separate (an outbox topic is not automatically published on
// the bus).
type Topic[T any] struct {
	name       string
	partitions int
}

func NewTopic[T any](name string, opts ...TopicOption) Topic[T]

type TopicOption func(*topicConfig)

func Partitions(n int) TopicOption // ordering domains; default 16; fixed for the topic's lifetime

type Outbox struct{ /* unexported; one per App, kernel-held */ }

// Writer is the module-scoped staging handle (module.Deps.Outbox, 06 §6.4.4).
type Writer struct{ /* unexported: the Outbox plus the calling module's ID */ }

// Stage writes msg into b. It is committed if and only if b commits; a
// discarded batch leaves nothing. Stage binds the outbox keyspace to b
// (storage.Batch.Bind / In, 05-storage §5S.3.2) and writes only c/outbox/
// inside it, so R8 holds even though b was opened by a module.
func Stage[T any](w Writer, b *storage.Batch, t Topic[T], key string, payload T, opts ...StageOption) error

type StageOption func(*stageConfig)

func IdempotencyKey(k string) StageOption // default: the message's ULID (no enqueue-side dedupe)
func NotBefore(t time.Time) StageOption   // delayed delivery

type Message[T any] struct {
	Seq            uint64
	Topic          string
	Key            string
	IdempotencyKey string
	Attempt        int // 1 on first delivery
	EnqueuedAt     time.Time
	Payload        T
}

// HandlerFunc is at-least-once: it may see a message again after a crash or a
// failed ack. It must be idempotent, keyed on IdempotencyKey.
type HandlerFunc[T any] func(ctx context.Context, m Message[T]) error

// TxHandlerFunc is for consumers whose effect is a write to the same store:
// the relay opens b, the handler stages its writes, and the relay commits them
// together with the cursor advance — effectively once.
type TxHandlerFunc[T any] func(ctx context.Context, b *storage.Batch, m Message[T]) error

// Handler is a consumer declaration, registered at app construction through
// Registrar.OutboxHandler (06 §6.4.3), never at runtime. Build it with one of
// the generic constructors; consumer names are "<moduleID>.<name>".
type Handler struct{ /* unexported */ }

func Consume[T any](t Topic[T], consumer string, h HandlerFunc[T], opts ...ConsumerOption) Handler
func ConsumeTx[T any](t Topic[T], consumer string, h TxHandlerFunc[T], opts ...ConsumerOption) Handler

// ConsumeIntoTask turns each message into a task run of kind, started with
// IdempotencyKey = consumer + ":" + seq, so a redelivery never starts a
// second run (tasks.StartRequest.IdempotencyKey, 05-tasks 5T.3.10). This is
// how event-triggered tasks are declared (it replaces OperationDef.Triggers).
func ConsumeIntoTask[T any](t Topic[T], consumer string, kind tasks.Kind,
	params func(Message[T]) (any, tasks.ModeRequest, error), opts ...ConsumerOption) Handler

type ConsumerOption func(*consumerConfig)

func Unordered() ConsumerOption                     // no per-key head-of-line blocking
func MaxAttempts(n int) ConsumerOption              // default core.outbox.max_attempts (20)
func Backoff(min, max time.Duration) ConsumerOption // default 1s .. 5m, full jitter
func FromBeginning() ConsumerOption                 // a NEW consumer starts at the oldest retained message instead of "now"

var (
	ErrUnknownTopic = errors.New("outbox: topic not declared")
	ErrPermanent    = errors.New("outbox: permanent failure") // wrap to dead-letter immediately
)
```

**Requirement on [05-storage](05-storage.md).** Joining a module's batch is already provided
(`Batch.Bind`/`Batch.In`, §5S.3.2). The gap-safe horizon (5E.5.2) additionally needs a resolution
hook, `(*storage.Batch).OnResolve(func(committed bool))`, called once on `Commit` (with the outcome)
or on `Close` of an uncommitted batch. 05-storage does not list it yet (open item 3).

## 5E.5 Semantics

### 5E.5.1 Bus delivery

| Property | Contract | Replaces |
|---|---|---|
| Queue | One bounded FIFO and **one goroutine per subscription**; `Publish` does a non-blocking send per subscriber | per-event goroutines (`internal/plugin/events.go:90-101`) |
| Drop policy | Full queue: `DropNewest` (default) or `DropOldest`; every drop increments `mediacore_events_dropped_total{topic,subscriber,reason}`; at most one WARN per subscriber per minute | silent drop (`bus.go:53-58`), Info log per event (`realtime/events.go:133-137`) |
| Panic isolation | Each handler call is wrapped in `recover`; the panic is counted, logged with stack once per minute per subscriber, and the subscription keeps running | per-goroutine recover (`events.go:93-97`) |
| Handler context | The bus lifecycle context (canceled by `Bus.Close` after the drain budget), never the publisher's | publisher ctx (`events.go:98`) |
| Ordering | Per subscription, events from one publishing goroutine arrive in publish order. No order across concurrent publishers, no order across subscriptions | — |
| Publish timing | Publish **after** the storage commit that made the change, never inside a batch callback; a crash between commit and publish loses only a notification, by design | — |
| Coalescing | `Coalesce(window)` keeps the latest event per `Key` within the window (deleted supersedes updated, created stays created — current `BookChangeCoalescer` rule, `book_changes.go:25-31`, generalised by an optional `Merge` hook) | `BookChangeCoalescer` |
| Disabled modules | Subscribing (raw) to a topic of a disabled module is allowed and receives nothing; WARN at boot (G1: a disabled module contributes no events) | — |
| Versioning | An incompatible payload change is a **new topic** (`.v2`); the publisher dual-publishes `.v1` and `.v2` for the compat window (two minor releases, 02 §2.5 Q5) | — |

### 5E.5.2 Outbox delivery

Storage (`c/outbox/` core keyspace):

| Key | Value |
|---|---|
| `c/outbox/m/<topic>/<pp>/<seq20>` | message (`pp` = partition, 2 digits; `seq20` zero-padded) |
| `c/outbox/cur/<consumer>/<topic>/<pp>` | highest acked seq |
| `c/outbox/try/<consumer>/<topic>/<pp>` | `{seq, attempt, next_at, last_error_token}` for the head message |
| `c/outbox/dead/<consumer>/<topic>/<seq20>` | dead-lettered message + error token + attempts |
| `c/outbox/idem/<topic>/<key>` | seq; dedupe of explicit `IdempotencyKey`s within `core.outbox.idempotency_ttl` |
| `c/outbox/meta/seq` | allocator high-water mark |

| Property | Contract |
|---|---|
| Atomicity | `Stage` writes the message into the caller's batch; commit = durable, discard = nothing. Same Pebble, same batch (no two-phase anything). |
| Sequence | A per-`Outbox` monotonic counter seeded at boot from `meta/seq` and the highest message key. Seq is reserved at `Stage` time. |
| Gap-safe horizon | Two batches can commit out of seq order. The relay never advances any cursor past `min(reserved-but-unresolved seq) − 1`; reservations resolve through `OnResolve` (commit or discard). A crash drops all reservations together with their uncommitted messages, so the horizon is exact on restart. |
| Partitioning / order | `partition = fnv1a32(key) % partitions`. Per (consumer, partition), messages are delivered one at a time in seq order. A failing head message blocks later messages of that partition for that consumer (head-of-line) — the cost of per-key order. `Unordered()` consumers skip HOL blocking and process a partition with up to `core.outbox.unordered_parallelism` in flight. |
| At-least-once | A message is redelivered until its consumer acks (handler returned nil and the cursor write committed). Crash after a handler's external effect but before the ack → redelivery; handlers dedupe on `IdempotencyKey`. `ConsumeTx` commits effect + cursor in one batch: effectively once. |
| Retry | Exponential backoff with full jitter between `Backoff(min,max)`; `ErrPermanent` or `MaxAttempts` reached → dead letter, cursor advances, `core.outbox` health component Degraded while any dead letter exists. |
| Idempotency at enqueue | Only for explicit `IdempotencyKey`: a second `Stage` with the same key within the TTL is a no-op that returns nil (the first message stands). |
| Retention | A message is deleted when every consumer registered for its topic has acked it (GC pass in the relay, batched). Dead letters stay until an operator retries or discards them. A topic with no registered consumer keeps messages for `core.outbox.orphan_retention` (7 d) then drops them with a counted metric (fail-open: a removed consumer must not grow the store forever). |
| New consumer | Starts at "now" (cursor = current horizon) unless `FromBeginning()`. |

### 5E.5.3 The relay is a core task

The relay is the task kind `core.outbox.relay` ([05-tasks](05-tasks.md)): Shape Task,
`Resume: ResumeFromZero` (all progress lives in cursors, so restarting from zero is exact),
`ConcurrencyKey: core.outbox.relay`, `Priority: tasks.PrioritySystem` (dispatched on the reserved
system workers so a saturated executor cannot starve it), `Schedule: Every(time.Minute).OnStart()`
(folds into the active run, so it only *ensures* the relay is running), `ReadOnly` effects plus a
`Writes(Res("core","outbox"))` declaration. One run drains for at most `core.outbox.relay_rotate`
(1 h) and then succeeds, so history shows bounded runs and a wedged consumer cannot keep one run
alive forever. A post-commit wake (`OnResolve(committed=true)`) nudges the running relay; with no
relay running it starts one (rate-limited to 1/s). Per-consumer work inside the run uses
`tasks.RunItems` over partitions with `tasks.Fixed(min(partitions, core.outbox.relay_workers))`.
An admin can cancel the run; the next schedule tick restarts it.

### 5E.5.4 What goes on which channel (G4 review rule)

A change is **correctness-critical** if, when its follow-up is lost, persisted state is wrong or
an external system diverges (download client path, a media-server library, a module's derived
index that nothing rebuilds). Those use `outbox`. Everything else uses `events`. The PR review
checklist for modules ([06-app-sdk](06-app-sdk.md)) carries this rule; `archtest` cannot decide
it, so it is a review gate, not a test.

## 5E.6 Lifecycle

- `app.New`: kernel constructs `Bus` and `Outbox`; modules declare bus topics (`Registrar.Topic`),
  outbox topics (`Registrar.OutboxTopic`, an addition to 06), and consumers (`Registrar.OutboxHandler`);
  bus subscriptions are made in the module's start hook through `Deps.Events`. The kernel validates
  names, uniqueness, module ownership and raw-subscription targets. Errors fail boot.
- `App.Run`: bus subscription goroutines start before the executor (so `core.task.*` events from the
  resume sweep are seen); the relay is started by its schedule's `OnStart`.
- Shutdown (inside the stop budget, [05-health](05-health.md) 5H.4): HTTP drain → executor drain (the
  relay run is canceled with `ErrShutdown` and acks nothing further) → `Bus.Close` with
  `core.events.close_drain` (2 s) → storage close. Undelivered bus events are dropped and counted;
  undelivered outbox messages are durable and resume next boot.

## 5E.7 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Undeclared topic at `Publish` | open: dropped, counted, logged once | notify-only; never break the publisher |
| Undeclared/invalid topic or duplicate subscriber at registration | closed: boot fails | programmer error, caught by `coretest` |
| Raw subscription to a topic no module declares | closed: boot fails; to a **disabled** module's topic: open (WARN, receives nothing) | G1 |
| Subscriber queue full | open: drop + metric | notify-only |
| Handler panic | open: recovered, counted, subscription continues | isolation |
| Raw JSON encode failure | open: raw subscribers get a drop with `reason="encode"` | typed subscribers unaffected |
| `outbox.Stage` on undeclared topic | closed: `ErrUnknownTopic`; the caller's batch should be discarded | a correctness message must not vanish |
| Batch discarded after `Stage` | closed (nothing persisted, reservation released) | atomicity |
| Handler error | retried with backoff; after `MaxAttempts` or `ErrPermanent` → dead letter + health Degraded | at-least-once without poison-pill stalls beyond the limit |
| Cursor write fails after handler success | open: message redelivered | at-least-once |
| Relay run canceled/crashed | open: next schedule tick restarts it; no data lost | durability is in storage |
| Message older than `orphan_retention` with no consumer | open: dropped, counted | bounded storage |

## 5E.8 Concurrency contract

- `Bus`, `Subscription`, `Outbox` are goroutine-safe. `Publish` is O(subscribers) non-blocking sends.
- Goroutines: one per bus subscription (bounded by the static subscription set), plus the relay's
  bounded pool inside one task run. No per-event goroutine anywhere.
- Handlers run on their subscription goroutine; a slow handler delays only its own queue.
- The outbox counter and reservation set are per-`Outbox` fields guarded by one mutex (R5).

## 5E.9 Config keys

| Key | Type | Default | Hot reload |
|---|---|---|---|
| `core.events.queue_size` | int | 256 | no (new subscriptions only) |
| `core.events.close_drain` | duration | 2s | yes |
| `core.outbox.max_attempts` | int | 20 | yes |
| `core.outbox.backoff_min` / `backoff_max` | duration | 1s / 5m | yes |
| `core.outbox.relay_workers` | int | 4 | yes (next run) |
| `core.outbox.relay_rotate` | duration | 1h | yes (next run) |
| `core.outbox.unordered_parallelism` | int | 4 | yes |
| `core.outbox.idempotency_ttl` | duration | 7d | yes |
| `core.outbox.orphan_retention` | duration | 7d | yes |

## 5E.10 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_events_published_total` | counter | `topic` |
| `mediacore_events_publish_rejected_total` | counter | `topic` |
| `mediacore_events_delivered_total` | counter | `topic`, `subscriber` |
| `mediacore_events_dropped_total` | counter | `topic`, `subscriber`, `reason` (`queue_full`, `encode`, `closed`) |
| `mediacore_events_handler_panics_total` | counter | `subscriber` |
| `mediacore_events_queue_depth` | gauge | `subscriber` |
| `mediacore_events_handler_seconds` | histogram | `subscriber` |
| `mediacore_outbox_staged_total` | counter | `topic` |
| `mediacore_outbox_delivered_total` | counter | `topic`, `consumer` |
| `mediacore_outbox_retries_total` | counter | `topic`, `consumer` |
| `mediacore_outbox_dead_letters` | gauge | `consumer` |
| `mediacore_outbox_pending` | gauge | `consumer` |
| `mediacore_outbox_lag_seconds` | gauge | `consumer` (age of the oldest unacked message) |
| `mediacore_outbox_orphans_dropped_total` | counter | `topic` |

`topic`, `subscriber` and `consumer` are closed sets (declared at boot), so cardinality is bounded.

## 5E.11 HTTP

| Route | Permission |
|---|---|
| `GET /api/core/v1/events/topics` (declared topics + schemas) | `core:system.view` |
| `GET /api/core/v1/outbox/consumers` (lag, pending, dead count) | `core:system.view` |
| `GET /api/core/v1/outbox/dead`, `POST /dead/{consumer}/{seq}/retry`, `DELETE /dead/{consumer}/{seq}` | `core:system.manage` (audited) |

## 5E.12 Tests

| Test | Proves |
|---|---|
| `TestTopicName_Grammar` | naming |
| `TestBus_SlowSubscriberDropsAndCounts`, `TestBus_DropOldestKeepsNewest` | bounded queue, drop metric |
| `TestBus_PanicIsolated_SubscriptionSurvives` | panic isolation |
| `TestBus_HandlerContextIsBusNotPublisher` (publisher ctx canceled before delivery) | Q1 decision honoured |
| `TestBus_PerPublisherFIFO` | ordering |
| `TestBus_CoalesceLatestPerKey` (synctest) | coalescing |
| `TestBus_CloseDrainsWithinBudget` (synctest) | shutdown |
| `TestBus_NoGoroutinePerEvent` (goroutine count stable under 1e5 publishes) | fix of `events.go:90-101` |
| `TestSubscribeRaw_CrossModuleWithoutImport`, `TestSubscribeRaw_DisabledModuleWarns` | R3, G1 |
| `TestOutbox_StageCommitsWithBatch`, `TestOutbox_DiscardLeavesNothing` | atomicity |
| `TestOutbox_GapSafeHorizon` (two batches commit out of seq order; no message skipped) | horizon |
| `TestOutbox_RedeliveryAfterCrashBeforeAck` (crash injection) | at-least-once |
| `TestOutbox_TxHandlerEffectivelyOnce` | ConsumeTx |
| `TestOutbox_PerKeyOrderAndHeadOfLine`, `TestOutbox_UnorderedNoHOL` | ordering |
| `TestOutbox_DeadLetterAfterMaxAttempts` (synctest backoff), `TestOutbox_RetryDeadLetter` | retry |
| `TestOutbox_IdempotencyKeyDedupe` | enqueue dedupe |
| `TestOutbox_ConsumeIntoTaskStartsOnce` (redeliver same seq → one run) | task triggers |
| `TestOutbox_RelayRestartsAfterCancel` | relay as task |
| `TestNoGlobals_EventsOutbox` (R5 AST) | no globals |

## 5E.13 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/plugin.EventBus` + 12 `EventType`s (`internal/plugin/events.go:18-31`) | `events.Bus` + `books` topics: `books.book.imported.v1`, `books.book.deleted.v1`, `books.metadata.applied.v1`, `books.tags.written.v1`, `books.file.organized.v1`, `books.dedup.detected.v1`, `books.dedup.merged.v1`, `books.cover.changed.v1`, `books.read-status.changed.v1`, `books.scan.completed.v1`, `books.book.quarantined.v1`, `books.book.unquarantined.v1`, each with a typed payload struct replacing `BookID`/`Data map[string]any` |
| `plugin.EventPublisher` (11 publisher files) | `events.Publish(d.Events, ctx, books.TopicX, id, payload)` through the module's `Deps.Events` |
| `internal/dedup/lifecycle.go:94` subscriber | `events.Subscribe` inside `books` |
| webhook plugin (`internal/plugins/webhook/plugin.go:79`) | a [05-notify](05-notify.md) webhook adapter consuming via `SubscribeRaw` (best-effort) or an outbox consumer (if 05-notify chooses at-least-once) |
| `internal/operations/registry.EventHub` + `op.*` names | core topics `core.task.created.v1`, `core.task.progress.v1` (merges `op.updated` and `op.current_item`), `core.task.log.v1`, `core.task.finished.v1`; published by the executor after each commit |
| `realtime.EventHub` `Send*` helpers (3 call sites outside the package) and `BookChangeCoalescer` | publishes of typed topics; the SSE hub ([05-realtime](05-realtime.md)) is a subscriber; book-change coalescing becomes a `Coalesce` subscription inside `books` |
| `OperationDef.Triggers` (1 dead declaration) | `outbox.ConsumeIntoTask`: `books` stages `books.book.relocated.v1` in the same batch that moves a book, and a consumer starts `books.deluge.path-update` (or the 05-download successor) with the book ID — this makes the relocation follow-up durable for the first time |
| direct `EnqueueOp` from handlers after a state change | case by case: `outbox.ConsumeIntoTask` where the follow-up is correctness-critical (inventory in [09-abo-on-core](09-abo-on-core.md)) |

Order: (1) land `events`/`outbox` in core; (2) bridge — the plugin `EventBus` becomes a thin adapter
publishing onto `events.Bus` so publishers move file by file; (3) executor publishes `core.task.*`
and the realtime hub subscribes; (4) remove `internal/plugin` events and `registry.EventHub`.

## 5E.14 Open items

1. 05-notify decides whether user-configured webhooks are best-effort (`events`) or at-least-once (`outbox`).
2. Inventory of handler-side "change then enqueue" sequences that should become outbox consumers is a
   09 task; its size is UNKNOWN at HEAD (no grep can tell correctness-critical from cosmetic).
3. 05-storage must add `Batch.OnResolve`; without the resolution hook the gap-safe horizon needs a
   per-batch commit sequence from the engine instead. 06 must add `Registrar.OutboxTopic`.
4. Multi-app-in-one-binary: buses are per `App`; cross-app events are out of scope (lowest priority, not ruled out).
5. 06 §6.4.4 types `Deps.Events` as `events.Bus` (a value); this spec uses `*events.Bus` with generic
   free functions (`events.Publish`, `events.Subscribe`) because Go methods cannot take type
   parameters. Either 06 changes the field to `*events.Bus` or `events.Bus` becomes a small handle
   struct wrapping the pointer; behaviour is identical.
