<!-- file: docs/specs/platform-core/05-events.md -->
<!-- version: 1.2.0 -->
<!-- guid: 6dbdef8e-e3f3-4088-bdb9-ba37a706165d -->
<!-- last-edited: 2026-11-04 -->

# 05 — Events (`core/events`) and Outbox (`core/outbox`)

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §5,
[`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md) §2 and §7.3,
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md) §2 (adversarial attack 2), [`raw/06-tasks-ops.md`](../../plans/platform-core/raw/06-tasks-ops.md) §5.
Decision basis: [02](02-decision-record.md) decision 11 ("bus is notification-only; correctness uses
durable ops or an outbox") and gate **G4**. Layer **L4** (03 R4 v1.2.0). Imports among L4 peers:
`outbox` imports `events` (one catalog) and `tasks` (its relay and its module consumers are tasks);
`tasks` imports `events` (it publishes run events); `events` imports `rbac` (topic permissions) and
nothing that imports `events` back (see open item 4 on `rbac`).

## 5E.1 Purpose & scope

Two packages with deliberately different guarantees:

| | `events` | `outbox` |
|---|---|---|
| Guarantee | **notify-only**: best effort, in-process, may drop | **durable, at-least-once**, survives crash, committed atomically with the state change that caused it |
| Use for | UI refresh, cache invalidation, metrics, realtime frames, "something changed" hints | any cross-module or external effect whose loss would leave state wrong (G4) |
| Never use for | anything correctness-critical | high-frequency UI chatter |

Out of scope: SSE transport, resumable streams and per-viewer delivery ([05-realtime](05-realtime.md)
subscribes to `events`); notification adapters ([05-notify](05-notify.md)). Both **read** the one topic
catalog this file owns (5E.3.1) and never declare a topic or a topic permission of their own (SIMP-5).

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

### 5E.3.1 The topic catalog (one per App, SIMP-5)

Every topic — bus or durable — is declared **once**, by its owning module (or core) through
`Registrar.Topics` ([06](06-app-sdk.md) §6.4.3), and published in the module manifest with its JSON
Schema. That declaration carries the topic's permission and its optional notification and realtime
metadata; outbox, notify and realtime read this catalog and add nothing to it. There is no
`Registrar.OutboxTopics` and no `Registrar.NotificationTopic` (06 and 05-notify drop them), and
`realtime` takes no permission of its own.

```go
package events // github.com/falkcorp/mediacore/events

// Topic names a typed event stream. Name grammar (03 §3.6):
//   "<moduleID>.<noun>.<verb-past>.v<N>", segments [a-z][a-z0-9-]*, N >= 1;
//   <noun> may carry one qualifier segment ("download.grab").
// e.g. "books.book.imported.v1", "core.task.run-changed.v1", "core.download.grab.added.v1".
// A Topic value is inert until its owner declares it.
type Topic[T any] struct{ name string }

func NewTopic[T any](name string) Topic[T]
func (t Topic[T]) Name() string

// Spec returns s with Name and Schema filled from t and T (the JSON Schema
// reflector owned by 05-config, SIMP-14). The caller supplies the rest.
func (t Topic[T]) Spec(s TopicSpec) TopicSpec

// TopicSpec is the single catalog entry for a topic.
type TopicSpec struct {
	Name       string
	Schema     json.RawMessage  // payload JSON Schema; the versioned contract raw subscribers and raw consumers validate against
	Permission rbac.Requirement // who may observe it on any surface (realtime, notify routing, HTTP); zero value rejected
	Visible    VisibleFunc      // optional per-envelope filter on top of Permission (e.g. a task definition's View)
	Durable    *Durable         // non-nil: an outbox topic (5E.4); nil: bus only
	Notify     *NotifyMeta      // non-nil: offered to notification routing (meaning owned by 05-notify)
	Realtime   *RealtimeMeta    // non-nil: exposed as a realtime stream (meaning owned by 05-realtime)
}

type VisibleFunc func(ctx context.Context, who rbac.Checker, e RawEnvelope) bool

type Durable struct {
	Partitions int // ordering domains; default 16; fixed for the topic's lifetime
}

// Plain data, so events imports neither notify nor realtime.
type NotifyMeta struct {
	Title           string
	DefaultSeverity string // "info" | "warning" | "error"
}

type RealtimeMeta struct {
	Coalesce time.Duration // latest per Key within the window; 0 = every envelope
}

// Catalog is the kernel-built, read-only view of every declared topic of every
// enabled module, plus the declared-but-disabled set (for G1 warnings).
type Catalog interface {
	Lookup(name string) (TopicSpec, bool)
	All() iter.Seq[TopicSpec]
}
```

Kernel validation at registration (boot fails): bad name grammar; a name declared twice; a topic
outside the declaring module's namespace; a zero `Permission`; a `Permission` naming another
module's scope (05-rbac R3 rule); `Realtime` or `Notify` on a topic whose `Schema` is empty.

**Core topics** (declared by core; payload types live in core, so any module may consume them typed):

| Topic | Owner | Channel | Permission / visibility |
|---|---|---|---|
| `core.task.run-changed.v1` | 05-tasks | bus, `Realtime{Coalesce}` | `AnyAuthenticated` + `Visible` = the definition's `Perms.View` (COR-20: a books-only user sees live progress for the runs REST shows) |
| `core.task.log-appended.v1` | 05-tasks | bus, `Realtime` | as above |
| `core.task.finished.v1` | 05-tasks | bus, `Notify` (outcome in payload) | as above |
| `core.update.found.v1` | 05-updater | bus, `Notify` | `core:system.manage` |
| `core.download.grab.added.v1`, `core.download.grab.failed.v1`, `core.download.item.changed.v1` | [05-download](05-download.md) (payloads and producer there) | as 05-download declares (`Durable` where a consumer's correctness depends on it) | as 05-download declares |

Other core topics (`core.rbac.changed.v1`, backup, integration health, auth) are declared by their
owning files in the same catalog with the same rules.

### 5E.3.2 Bus

```go
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

// Bus is one per App (R5: never a package global). Constructed by the kernel
// and held only by core; modules never see a *Bus.
type Bus struct{ /* unexported */ }

// Scope is a module's publish handle, module.Deps.Events (06 §6.4.4, COR-21):
// the App's Bus bound to one module ID by the kernel. Publishing through it
// is accepted only for catalog entries the bound module declared; that rule
// needs the module ID, which a shared *Bus cannot know. Safe for concurrent use.
type Scope struct{ /* bus + module ID */ }

// Scope binds the bus to moduleID. Kernel-only: called once per enabled module
// when it builds that module's Deps.
func (b *Bus) Scope(moduleID string) Scope

// Publisher is implemented by *Bus (core publishers: core.* topics only) and by
// Scope (one module's own topics). The interface is sealed (unexported method).
type Publisher interface{ publisher() }

// Publish never blocks on subscribers and never returns an error: it is
// notify-only. ctx is read for actor/run/trace values only; it is NOT handed
// to handlers. Publishing to an undeclared topic, a topic outside the
// publisher's namespace (another module's, or core.* through a Scope) or a
// Durable topic is counted (publish_rejected) and logged once per topic.
func Publish[T any](p Publisher, ctx context.Context, t Topic[T], key string, payload T)

type Handler[T any] func(ctx context.Context, e Envelope[T])
type RawHandler func(ctx context.Context, e RawEnvelope)

// Subscriptions are declarations, made in Register through
// Registrar.Subscribe(...events.Subscription) (COR-8) and frozen when
// Register returns. Delivery begins at Bus.Open (06 §6.5.2).
type Subscription struct{ /* unexported */ }

// On subscribes typed: allowed for the declaring module's own topics and for
// core.* topics (whose payload types live in core).
func On[T any](t Topic[T], name string, h Handler[T], opts ...SubOption) Subscription

// OnRaw is how a module subscribes to ANOTHER module's topic without importing
// it (R3): by name, with the JSON payload validated against the declared
// schema. The JSON form is encoded once per publish, lazily, only when a raw
// subscriber exists.
func OnRaw(topic, name string, h RawHandler, opts ...SubOption) Subscription

type SubOption func(*subConfig)

func QueueSize(n int) SubOption               // default core.events.queue_size (256); max 65536
func DropOldest() SubOption                   // default policy is DropNewest (keep queued, reject incoming)
func KeyFilter(f func(key string) bool) SubOption
func Coalesce(window time.Duration) SubOption // deliver at most one event per Key per window (latest wins)

// Kernel-only lifecycle (06 §6.5.2).
func (b *Bus) Open() error                     // start one goroutine per subscription; no delivery before
func (b *Bus) Close(ctx context.Context) error // stop accepting, drain queues until ctx, then drop and count
```

Subscription names are `<moduleID>.<name>` and unique per bus (kernel-checked); they are the
`subscriber` metric label. Core subscribers (the realtime hub, notify routing) are declared by the
kernel before `Open` the same way.

## 5E.4 Go API — `outbox`

```go
package outbox // github.com/falkcorp/mediacore/outbox

// Topic is a durable stream: an events catalog entry with Durable set. Same
// name grammar; one catalog (SIMP-5), so a name is either bus-only or durable.
type Topic[T any] struct{ name string }

func NewTopic[T any](name string) Topic[T]

// Spec returns s with Name, Schema and Durable{Partitions: partitions} set
// (partitions 0 = 16), for Registrar.Topics (COR-11: no separate OutboxTopics).
func (t Topic[T]) Spec(s events.TopicSpec, partitions int) events.TopicSpec

type Outbox struct{ /* unexported; one per App, kernel-held */ }

// Writer is the module-scoped staging handle (module.Deps.Outbox, 06 §6.4.4).
type Writer struct{ /* unexported: the Outbox plus the calling module's ID */ }

// Stage writes msg into b's intake. It is committed if and only if b commits;
// a discarded batch leaves nothing. Stage binds the outbox keyspace to b
// (storage.Batch.Bind / In, 05-storage §5S.3.2) and writes only
// c/outbox/in/ inside it, so R8 holds even though b was opened by a module.
// Only the topic's owner may stage it.
func Stage[T any](w Writer, b *storage.Batch, t Topic[T], key string, payload T, opts ...StageOption) error

type StageOption func(*stageConfig)

func IdempotencyKey(k string) StageOption // default: the message's ULID (no enqueue-side dedupe)
func NotBefore(t time.Time) StageOption   // delayed delivery

type Message[T any] struct {
	Seq            uint64 // per topic, assigned at intake (5E.5.2)
	Topic          string
	Key            string
	IdempotencyKey string
	Attempt        int // 1 on first delivery
	EnqueuedAt     time.Time
	Payload        T
}

type RawMessage struct {
	Seq            uint64
	Topic, Key     string
	IdempotencyKey string
	Attempt        int
	EnqueuedAt     time.Time
	Payload        json.RawMessage // validated against the catalog schema before delivery
}

// Handler is a consumer declaration, registered in Register through
// Registrar.OutboxHandler (06 §6.4.3), never at runtime. Consumer names are
// "<moduleID>.<name>" and are also listed in the module manifest
// (Manifest.OutboxConsumers) so they are known while the module is disabled.
type Handler struct{ /* unexported */ }

// --- Module consumers: always a task run (COR-9, 05-tasks 5T.3.12) ---

// ConsumeIntoTask turns each message into a run of kind (which must belong to
// the consumer's module), staged in the same batch as the cursor advance with
// IdempotencyKey "<consumer>:<topic>:<seq>". This is how event-triggered tasks
// are declared (it replaces OperationDef.Triggers). Typed form: own and core.* topics.
func ConsumeIntoTask[T any](t Topic[T], consumer string, kind tasks.Kind,
	params func(Message[T]) (any, tasks.ModeRequest, error), opts ...ConsumerOption) Handler

// ConsumeIntoTaskRaw consumes ANOTHER module's durable topic by name without
// importing it (R3, COR-11); the payload is validated against the declared schema.
func ConsumeIntoTaskRaw(topic, consumer string, kind tasks.Kind,
	params func(RawMessage) (any, tasks.ModeRequest, error), opts ...ConsumerOption) Handler

// --- Core consumers only (archtest.OutboxModuleConsumersAreTasks flags these in module packages) ---

// HandlerFunc is at-least-once and bounded: it runs under core.outbox.handler_timeout,
// must be idempotent (keyed on IdempotencyKey), and must not write module entities.
type HandlerFunc[T any] func(ctx context.Context, m Message[T]) error

// TxHandlerFunc is for core consumers whose effect is a write to their own
// core keyspace: the relay opens b, the handler stages its writes, and the
// relay commits them together with the cursor advance — effectively once.
type TxHandlerFunc[T any] func(ctx context.Context, b *storage.Batch, m Message[T]) error

func Consume[T any](t Topic[T], consumer string, h HandlerFunc[T], opts ...ConsumerOption) Handler
func ConsumeTx[T any](t Topic[T], consumer string, h TxHandlerFunc[T], opts ...ConsumerOption) Handler
func ConsumeRaw(topic, consumer string, h func(ctx context.Context, m RawMessage) error, opts ...ConsumerOption) Handler

type ConsumerOption func(*consumerConfig)

func Unordered() ConsumerOption                     // no per-key head-of-line blocking
func MaxAttempts(n int) ConsumerOption              // default core.outbox.max_attempts (20)
func Backoff(min, max time.Duration) ConsumerOption // default 1s .. 5m, full jitter
func FromBeginning() ConsumerOption                 // a NEW consumer starts at the oldest retained message instead of "now"

var (
	ErrUnknownTopic = errors.New("outbox: topic not declared")
	ErrNotOwner     = errors.New("outbox: topic belongs to another module")
	ErrPermanent    = errors.New("outbox: permanent failure") // wrap to dead-letter immediately
	ErrSchema       = errors.New("outbox: payload does not match the declared schema")
)
```

**Why module consumers are tasks (COR-9).** A handler run inside the relay would write module
entities while the relay declares only `Writes(Res("core","outbox"))`: it would bypass `Effects`
(G8), the write-set conflict rule against the module's own tasks, the fence, and progress and cancel,
and a multi-GB import would occupy one of the relay workers under head-of-line blocking until the
relay rotated. With `ConsumeIntoTask` the relay only enqueues; the run carries the module's declared
effects and ref locks (05-tasks 5T.3.4, 5T.3.12). Core consumers (e.g. 05-notify's webhook and mail
adapters, which write nothing but their own delivery records) may use `Consume`/`ConsumeTx`/
`ConsumeRaw`, bounded by `core.outbox.handler_timeout`.

**No storage hook is required (COR-10, DATA-29).** The earlier design needed a
`(*storage.Batch).OnResolve` hook to keep a gap-safe horizon; commit-ordered intake (5E.5.2) removes
both the horizon and the hook, so 05-storage need not add `OnResolve` for this file.

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
| Versioning | An incompatible payload change is a **new topic** (`.v2`); the publisher dual-publishes `.v1` and `.v2` for the compatibility window defined in [09](09-abo-on-core.md) §9.2.1 (SIMP-7) | — |
| Delivery start | No handler runs before `Bus.Open`, which 06 §6.5.2 calls after module hooks and workers have started; `Bus.Close` runs after the executor drain and before workers and hooks stop (COR-8) | handlers ran outside their module's hook window |

### 5E.5.2 Outbox delivery

Storage (`c/outbox/` core keyspace):

| Key | Value |
|---|---|
| `c/outbox/in/<topic>/<ulid>` | staged message awaiting intake; written by `Stage` inside the producer's batch |
| `c/outbox/m/<topic>/<pp>/<seq20>` | sequenced message (`pp` = partition, 2 digits; `seq20` zero-padded, per topic) |
| `c/outbox/seq/<topic>` | the topic's high-water mark; written only by the relay, in the intake batch |
| `c/outbox/cur/<consumer>/<topic>/<pp>` | highest acked seq |
| `c/outbox/try/<consumer>/<topic>/<pp>` | `{seq, attempt, next_at, last_error_token}` for the head message |
| `c/outbox/dead/<consumer>/<topic>/<seq20>` | dead-lettered message + error token + attempts |
| `c/outbox/idem/<topic>/<key>` | seq; dedupe of explicit `IdempotencyKey`s within `core.outbox.idempotency_ttl` |

| Property | Contract |
|---|---|
| Atomicity | `Stage` writes an intake row into the caller's batch; commit = durable, discard = nothing. Same Pebble, same batch (no two-phase anything). |
| Commit-ordered intake (COR-10) | The relay is the single writer of `m/` and `seq/`. It scans `in/<topic>/` and, in one batch per topic chunk, deletes the intake rows, writes them to `m/<topic>/<pp>/<seq>` with consecutive seqs, and advances `seq/<topic>`. The batch goes through the relay run's fenced write path, and `seq/<topic>` lies in a `Serialized` sub-prefix of `c/outbox/` and is read with `Batch.GetForUpdate` (an `Expect` precondition checked at commit, 05-storage §5S.3.2, DATA-13), so a zombie relay whose read went stale fails its commit and can never number a message twice. |
| No reissue (DATA-19, consumer side) | A seq and the high-water mark that covers it commit in the same batch; a crash before that commit leaves the intake rows in place to be sequenced again, and nothing after it needs redoing. There is no lease and no shared `storage.Sequence`. |
| No horizon, no global stall (COR-10) | Only committed messages receive a seq, so a cursor can never skip one and no relay waits for an unresolved batch. A long-open or leaked producer batch (error path, panic, a large migration step) delays only its own messages. Topics are sequenced independently, so one topic's backlog never holds another's. |
| Intake latency | `Stage` nudges the relay (in memory); the relay scans intake after the nudge and at least every `core.outbox.intake_poll` (1 s; an in-process tick, 05-tasks 5T.3.8). An empty prefix costs one seek. |
| Partitioning / order | `partition = fnv1a32(key) % partitions`. Messages of one key are sequenced in intake order; rows taken in one scan are ordered by their stage-time ULID. Two batches that stage the same key **concurrently** have no defined order between their state changes either, so a producer needing strict per-key order serialises those writes (for example under the entity's ref lock, 05-tasks 5T.3.4). Per (consumer, partition), messages are delivered one at a time in seq order. A failing head message blocks later messages of that partition for that consumer (head-of-line) — the cost of per-key order. `Unordered()` consumers skip HOL blocking and process a partition with up to `core.outbox.unordered_parallelism` in flight. |
| At-least-once | A message is redelivered until its consumer acks (handler returned nil and the cursor write committed). Crash after a core handler's external effect but before the ack → redelivery; handlers dedupe on `IdempotencyKey`. `ConsumeTx` and `ConsumeIntoTask[Raw]` commit effect (or the run's queue row) + cursor in one batch: effectively once. |
| Retry | Exponential backoff with full jitter between `Backoff(min,max)`; `ErrPermanent`, `ErrSchema` or `MaxAttempts` reached → dead letter, cursor advances, `core.outbox` health component Degraded while any dead letter exists. |
| Idempotency at enqueue | Only for explicit `IdempotencyKey`: at intake, a second message with the same key within the TTL is dropped (its intake row deleted, the first message stands), counted. |
| Retention (COR-12) | A message is deleted when every consumer **declared** for its topic — in the manifests of enabled **and disabled** modules (`Manifest.OutboxConsumers`, 06) — has a persisted cursor past it. A disabled module's consumers therefore keep their messages: not delivered, not dropped, until the module is re-enabled (delivery resumes from the persisted cursor) or an operator purges that consumer's backlog (5E.11, audited). A declared consumer that has never run holds no messages; its cursor is created at first registration ("now", or the oldest retained message with `FromBeginning()`). `core.outbox.orphan_retention` (7 d) applies only to cursors of consumer names that no manifest in the build declares any more, and to topics with no declared consumer; those messages are then dropped with a counted metric (fail-open: a removed consumer must not grow the store forever). Dead letters stay until an operator retries or discards them. |
| New consumer | Starts at "now" (cursor = current topic high-water mark) unless `FromBeginning()`. |

### 5E.5.3 The relay is a core task

The relay is the task kind `core.outbox.relay` ([05-tasks](05-tasks.md)): Shape Task,
`Resume: ResumeFromZero` (all progress lives in intake rows and cursors, so restarting from zero is
exact), `ConcurrencyKey: core.outbox.relay`, `Priority: tasks.PrioritySystem` (reserved system
workers, so a saturated executor cannot starve it), `Schedule: tasks.Every(time.Minute).OnStart()` —
core plumbing, deliberately not an operator schedule key (05-tasks 5T.3.8) — which folds into the
active run, so it only *ensures* the relay is running. Effects: `ReadOnly` plus
`Writes(tasks.Res("core","outbox"))`; outbox-triggered runs are staged through the kernel-internal
`Executor.StartIn` (05-tasks 5T.3.12), so the relay never writes module entities. One run drains for
at most `core.outbox.relay_rotate` (1 h) and then succeeds, so history shows bounded runs and a wedged
consumer cannot keep one run alive forever. A `Stage` nudge wakes the running relay; with no relay
running it starts one (rate-limited to 1/s). Per-consumer delivery inside the run uses
`tasks.RunItems` over (consumer, partition) pairs with
`tasks.Fixed(min(partitions, core.outbox.relay_workers), "outbox relay workers")`; each core handler
call runs under `core.outbox.handler_timeout`. An admin can cancel the run; the next schedule tick
restarts it. The operator dial `core.outbox.relay_enabled=false` (OPS-17) makes each relay run exit
at once: staging still commits, intake grows, and `core.outbox` reports Degraded `relay_disabled`
until it is turned back on.

### 5E.5.4 What goes on which channel (G4 review rule)

A change is **correctness-critical** if, when its follow-up is lost, persisted state is wrong or
an external system diverges (download client path, a media-server library, a module's derived
index that nothing rebuilds). Those use `outbox`, and a module consumer of such a topic is a task
(`ConsumeIntoTask[Raw]`). Everything else uses `events`. The PR review checklist for modules
([06-app-sdk](06-app-sdk.md)) carries this rule; `archtest` cannot decide which channel a change
needs, so that part is a review gate. `archtest.OutboxModuleConsumersAreTasks` does enforce that
module packages call no `outbox.Consume`/`ConsumeTx`/`ConsumeRaw`.

## 5E.6 Lifecycle

The start and stop order is defined once, in [06-app-sdk](06-app-sdk.md) §6.5.2 (COR-8). What
`events` and `outbox` do at each step:

- `app.New` (`Register`): the kernel builds the `Catalog`, `Bus` and `Outbox`; modules declare topics
  (`Registrar.Topics`), bus subscriptions (`Registrar.Subscribe`) and outbox consumers
  (`Registrar.OutboxHandler`). The kernel validates names, uniqueness, ownership, raw targets, that
  every consumer name is listed in the module's `Manifest.OutboxConsumers`, and that every
  `ConsumeIntoTask` kind belongs to the consumer's module. Errors fail boot. The sets are frozen when
  `Register` returns.
- `App.Run`: core services → module hooks → module workers → **`Bus.Open`** (subscription goroutines
  start; no handler runs earlier) → executor (resume-sweep events are delivered) → scheduler (the relay
  starts from its `OnStart`) → listeners.
- Shutdown (budget from [05-health](05-health.md) 5H.4): readiness → listeners → scheduler →
  executor drain (the relay run is canceled with `ErrShutdown` and acks nothing further) →
  **`Bus.Close`** with `min(core.events.close_drain, remaining budget)` → workers → hooks → realtime →
  storage. Bus handlers therefore never run before their module's hooks start or after they stop.
  Undelivered bus events are dropped and counted; intake rows and outbox messages are durable and
  resume next boot.

## 5E.7 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Undeclared, foreign or durable topic at `Publish` | open: dropped, counted, logged once | notify-only; never break the publisher |
| Invalid catalog entry, duplicate topic or subscriber, undeclared consumer name, foreign `ConsumeIntoTask` kind at registration | closed: boot fails | programmer error, caught by `coretest` |
| Raw subscription or raw consumer of a topic no module declares | closed: boot fails; of a **disabled** module's topic: open (WARN, receives nothing) | G1 |
| Subscriber queue full | open: drop + metric | notify-only |
| Handler panic | open: recovered, counted, subscription continues | isolation |
| Raw JSON encode failure | open: raw subscribers get a drop with `reason="encode"` | typed subscribers unaffected |
| `outbox.Stage` on undeclared or foreign topic | closed: `ErrUnknownTopic` / `ErrNotOwner`; the caller's batch should be discarded | a correctness message must not vanish |
| Batch discarded after `Stage` | closed (nothing persisted) | atomicity |
| Raw payload fails the declared schema | closed for that message: dead letter with `ErrSchema` | a consumer must not act on a payload it cannot read |
| Handler error / run enqueue error | retried with backoff; after `MaxAttempts` or `ErrPermanent` → dead letter + health Degraded | at-least-once without poison-pill stalls beyond the limit |
| Core handler exceeds `handler_timeout` | handler error (retried) | bounded relay work (COR-9) |
| Cursor write fails after handler success | open: message redelivered | at-least-once |
| Intake batch fails or loses its conditional write | open: intake rows stay; next scan retries | single-writer sequencing |
| Relay run canceled/crashed, or relay disabled | open: next schedule tick restarts it (or the dial is turned back on); no data lost | durability is in storage |
| Disabled module's consumer | open: messages retained, shown as backlog; never dropped by `orphan_retention` | COR-12 |
| Message older than `orphan_retention` with no declared consumer | open: dropped, counted | bounded storage |

## 5E.8 Concurrency contract

- `Bus`, `Catalog`, `Outbox` are goroutine-safe. `Publish` is O(subscribers) non-blocking sends.
- Goroutines: one per bus subscription (bounded by the static subscription set, started by
  `Bus.Open`), plus the relay's bounded pool inside one task run. No per-event goroutine anywhere.
- Handlers run on their subscription goroutine; a slow handler delays only its own queue.
- Sequencing has one writer (the relay run, fenced, serialised by its `ConcurrencyKey`, and guarded by
  the conditional write on `seq/<topic>`); producers never contend on a shared counter. The intake
  nudge is a per-`Outbox` field (R5).

## 5E.9 Config keys

| Key | Type | Default | Hot reload |
|---|---|---|---|
| `core.events.queue_size` | int | 256 | no (new subscriptions only) |
| `core.events.close_drain` | duration | 2s (a slice of `core.shutdown.budget`) | yes |
| `core.outbox.max_attempts` | int | 20 | yes |
| `core.outbox.backoff_min` / `backoff_max` | duration | 1s / 5m | yes |
| `core.outbox.relay_workers` | int | 4 | yes (next run) |
| `core.outbox.relay_rotate` | duration | 1h | yes (next run) |
| `core.outbox.relay_enabled` | bool | true (OPS-17 operational dial) | yes |
| `core.outbox.handler_timeout` | duration | 30s | yes |
| `core.outbox.intake_poll` | duration | 1s | yes |
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
| `mediacore_outbox_intake_pending` | gauge | `topic` (intake rows not yet sequenced) |
| `mediacore_outbox_intake_age_seconds` | gauge | `topic` (age of the oldest intake row) |
| `mediacore_outbox_delivered_total` | counter | `topic`, `consumer` |
| `mediacore_outbox_retries_total` | counter | `topic`, `consumer` |
| `mediacore_outbox_dead_letters` | gauge | `consumer` |
| `mediacore_outbox_pending` | gauge | `consumer`, `state` (`enabled`, `disabled`) |
| `mediacore_outbox_lag_seconds` | gauge | `consumer` (age of the oldest unacked message) |
| `mediacore_outbox_idempotent_drops_total` | counter | `topic` |
| `mediacore_outbox_orphans_dropped_total` | counter | `topic` |

`topic`, `subscriber` and `consumer` are closed sets (declared at boot, disabled modules' consumer
names included), so cardinality is bounded.

## 5E.11 HTTP

| Route | Permission |
|---|---|
| `GET /api/core/v1/events/topics` (the catalog: topics, schemas, permissions, channels) | `core:system.view` |
| `GET /api/core/v1/outbox/consumers` (lag, pending, dead count, intake backlog; disabled modules' consumers listed with their retained backlog) | `core:system.view` |
| `GET /api/core/v1/outbox/dead`, `POST /dead/{consumer}/{seq}/retry`, `DELETE /dead/{consumer}/{seq}` | `core:system.manage` (audited) |
| `DELETE /api/core/v1/outbox/consumers/{consumer}/backlog` (explicit purge: advance that consumer's cursors to the head; the only way a disabled module's messages are discarded) | `core:system.manage` + interactive method (audited) |

## 5E.12 Tests

| Test | Proves |
|---|---|
| `TestTopicName_Grammar`, `TestCatalog_RejectsDuplicateZeroPermissionForeignScope` | naming, one catalog |
| `TestCatalog_NotifyAndRealtimeReadNoSecondDeclaration` | SIMP-5 |
| `TestBus_SlowSubscriberDropsAndCounts`, `TestBus_DropOldestKeepsNewest` | bounded queue, drop metric |
| `TestBus_PanicIsolated_SubscriptionSurvives` | panic isolation |
| `TestBus_HandlerContextIsBusNotPublisher` (publisher ctx canceled before delivery) | Q1 decision honoured |
| `TestBus_NoDeliveryBeforeOpenNoneAfterClose` | COR-8 |
| `TestBus_PerPublisherFIFO` | ordering |
| `TestBus_CoalesceLatestPerKey` (synctest) | coalescing |
| `TestBus_CloseDrainsWithinBudget` (synctest) | shutdown |
| `TestBus_NoGoroutinePerEvent` (goroutine count stable under 1e5 publishes) | fix of `events.go:90-101` |
| `TestOnRaw_CrossModuleWithoutImport`, `TestOnRaw_DisabledModuleWarns` | R3, G1 |
| `TestOutbox_StageCommitsWithBatch`, `TestOutbox_DiscardLeavesNothing` | atomicity |
| `TestOutbox_IntakeOutOfOrderCommitsNoSkip` (two batches commit out of stage order; both delivered) | COR-10 |
| `TestOutbox_LeakedBatchDoesNotStall` (one batch never closed; other messages and topics flow) | COR-10 |
| `TestOutbox_IntakeCrashNoReissue` (`CrashClone` at every point of the intake batch) | DATA-19 |
| `TestOutbox_ZombieRelayCannotDoubleSequence` | conditional write |
| `TestOutbox_RedeliveryAfterCrashBeforeAck` (crash injection) | at-least-once |
| `TestOutbox_TxHandlerEffectivelyOnce` | ConsumeTx |
| `TestOutbox_PerKeyOrderAndHeadOfLine`, `TestOutbox_UnorderedNoHOL` | ordering |
| `TestOutbox_DeadLetterAfterMaxAttempts` (synctest backoff), `TestOutbox_RetryDeadLetter`, `TestOutbox_RawSchemaMismatchDeadLetters` | retry, schema |
| `TestOutbox_IdempotencyKeyDedupeAtIntake` | enqueue dedupe |
| `TestOutbox_ConsumeIntoTaskStartsOnce` (redeliver same seq → one run; crash between enqueue and cursor) | task triggers |
| `TestOutbox_ConsumeIntoTaskRawWithoutImport` | COR-11 |
| `TestArchtest_ModuleConsumeFlagged` | COR-9 |
| `TestOutbox_DisabledConsumerRetainsUntilReenableOrPurge` (another consumer acks; orphan window passes; nothing deleted) | COR-12 |
| `TestOutbox_RelayRestartsAfterCancel`, `TestOutbox_RelayDisabledDegradedNoLoss` | relay as task, dial |
| `TestNoGlobals_EventsOutbox` (R5 AST) | no globals |

## 5E.13 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/plugin.EventBus` + 12 `EventType`s (`internal/plugin/events.go:18-31`) | `events.Bus` + `books` topics: `books.book.imported.v1`, `books.book.deleted.v1`, `books.metadata.applied.v1`, `books.tags.written.v1`, `books.file.organized.v1`, `books.dedup.detected.v1`, `books.dedup.merged.v1`, `books.cover.changed.v1`, `books.read-status.changed.v1`, `books.scan.completed.v1`, `books.book.quarantined.v1`, `books.book.unquarantined.v1`, each with a typed payload struct replacing `BookID`/`Data map[string]any` and one catalog entry carrying its permission |
| `plugin.EventPublisher` (11 publisher files) | `events.Publish(d.Events, ctx, books.TopicX, id, payload)` through the module's `Deps.Events` |
| `internal/dedup/lifecycle.go:94` subscriber | `events.On` declared in `books`' `Register` |
| webhook plugin (`internal/plugins/webhook/plugin.go:79`) | a [05-notify](05-notify.md) webhook adapter (core consumer) reading the catalog's `Notify` topics |
| `internal/operations/registry.EventHub` + `op.*` names | core topics `core.task.run-changed.v1` (merges `op.created`, `op.updated`, `op.current_item`, `op.terminal`), `core.task.log-appended.v1` (`op.log`) and `core.task.finished.v1`; published by the executor after each commit; realtime bridges the first two one-to-one, and its legacy projections map them back to the old frame names ([05-realtime](05-realtime.md)) |
| `realtime.EventHub` `Send*` helpers (3 call sites outside the package) and `BookChangeCoalescer` | publishes of typed topics; the SSE hub ([05-realtime](05-realtime.md)) is a core subscriber; book-change coalescing becomes a `Coalesce` subscription or `RealtimeMeta.Coalesce` inside `books` |
| `OperationDef.Triggers` (1 dead declaration) | `outbox.ConsumeIntoTask`: `books` stages the durable topic `books.book.relocated.v1` in the same batch that moves a book, and a books consumer starts `books.deluge.path-update` (or the 05-download successor) with the book ID — this makes the relocation follow-up durable for the first time |
| direct `EnqueueOp` from handlers after a state change | case by case: `outbox.ConsumeIntoTask` where the follow-up is correctness-critical (inventory in [09-abo-on-core](09-abo-on-core.md)) |

Order: (1) land `events`/`outbox` in core; (2) bridge — the plugin `EventBus` becomes a thin adapter
publishing onto `events.Bus` so publishers move file by file; (3) executor publishes `core.task.*`
and the realtime hub subscribes; (4) remove `internal/plugin` events and `registry.EventHub`.

## 5E.14 Open items

1. Inventory of handler-side "change then enqueue" sequences that should become outbox consumers is a
   09 task; its size is UNKNOWN at HEAD (no grep can tell correctness-critical from cosmetic).
2. (Closed: 06 v1.1.0 has `Registrar.Topics`, `Registrar.Subscribe`, `Registrar.OutboxHandler`,
   `Manifest.OutboxConsumers` and the start/stop order with `Bus.Open`/`Bus.Close` in §6.5.2, and no
   `OutboxTopics`/`NotificationTopic`.)
3. Multi-app-in-one-binary: buses are per `App`; cross-app events are out of scope (lowest priority,
   not ruled out).
4. (Closed: 05-rbac publishes `core.rbac.changed.v1` through the kernel-bound `OnChange` callback and
   does not import `events`, so `events` → `rbac` for `TopicSpec.Permission` is acyclic.)
5. (Closed: 05-storage provides `Serialized` ranges with `Batch.Expect`/`GetForUpdate` on core
   keyspaces, DATA-13; no `Batch.OnResolve` is needed.)

## Objections to 03

None open. (The topic noun qualifier is in 03 v1.4.0 §3.6.)

## Review history

Round 1 findings applied to this file (v1.1.0):

- COR-1 — consumer registration surface (`Registrar.OutboxHandler`, `Registrar.Subscribe`) and manifest consumer list requested from 06.
- COR-8 — subscriptions declared in `Register`; `Bus.Open`/`Bus.Close` placed in 06's single start/stop order.
- COR-9 — module consumers are `ConsumeIntoTask[Raw]` runs; `Consume`/`ConsumeTx`/`ConsumeRaw` core-only with `handler_timeout`; archtest guard.
- COR-10 — commit-ordered intake replaces the global gap-safe horizon; per-topic sequencing; no `OnResolve`.
- COR-11 — `ConsumeIntoTaskRaw`/`ConsumeRaw` with schema validation; `outbox.Topic.Spec` feeds the one catalog.
- COR-12 — retention over declared consumers of enabled and disabled modules; explicit purge route; disabled backlog shown.
- COR-19 — layer stated as L4.
- COR-20 — task topics renamed to the grammar and mapped one-to-one to realtime; permission `AnyAuthenticated` + definition `View`.
- DATA-19 — outbox seq and high-water mark committed together by the single relay writer; no lease.
- DATA-29 — `OnResolve` requirement withdrawn; cross-reference fixed.
- OPS-17 — `core.outbox.relay_enabled` operational dial.
- SIMP-5 — events owns the single topic catalog (`TopicSpec` with permission, visibility, durable, notify and realtime metadata); core topic list incl. 05-download's topics.
- SIMP-6 — relay keeps a fixed core `Every` schedule; intake poll listed as an in-process tick.
- SIMP-7 — dual-publish window references 09 §9.2.1.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-G request (COR-21) — `events.Scope` (module-bound publisher, `Bus.Scope(moduleID)`, kernel-only) is `module.Deps.Events`; `Publish` takes the sealed `Publisher` interface so core keeps `*Bus`.
- integration pass: R-B request (COR-10, DATA-13) — commit-ordered intake was already specified; the high-water-mark write now names 05-storage's real primitive (`Serialized` + `GetForUpdate`/`Expect`) instead of the non-existent `SetIfUnchanged`/`Keyspace.Lock`.
- integration pass: Open items 2, 4 and 5 closed (06 v1.1.0, 05-rbac `OnChange`, 05-storage DATA-13); objection on the topic qualifier removed (03 v1.4.0).
