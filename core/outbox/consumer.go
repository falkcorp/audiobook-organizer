// file: core/outbox/consumer.go
// version: 1.0.1
// guid: 3ec1a6c8-90d6-4e8d-9a3e-7b34e8e5d5a2
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Message is one delivery of a typed topic's message.
type Message[T any] struct {
	Seq            uint64 // per topic, assigned at intake (05-events §5E.5.2)
	Topic          string
	Key            string
	IdempotencyKey string // the ULID Stage assigned; the same on every redelivery (handler-side dedupe)
	Attempt        int    // 1 on first delivery
	EnqueuedAt     time.Time
	Payload        T
}

// RawMessage is one delivery to a consumer that names the topic by string.
type RawMessage struct {
	Seq            uint64
	Topic, Key     string
	IdempotencyKey string
	Attempt        int
	EnqueuedAt     time.Time
	Payload        json.RawMessage // validated against the catalog schema before delivery
}

// HandlerFunc is at-least-once and bounded: it runs under
// core.outbox.handler_timeout, must honour its context, must be idempotent
// (keyed on IdempotencyKey) and must not write module entities.
type HandlerFunc[T any] func(ctx context.Context, m Message[T]) error

// TxHandlerFunc is for core consumers whose effect is a write to their own
// core keyspace and/or a run to stage (COR2-10): the handler stages into the
// relay batch and the relay commits it with the cursor advance.
type TxHandlerFunc[T any] func(ctx context.Context, tx *Tx, m Message[T]) error

// Tx is a core TxHandlerFunc's view of the relay batch (COR2-10). The relay
// opens the batch, the handler stages its writes and runs, and the relay
// commits them together with the cursor advance: effectively once. The commit
// is Executor.WriteRuns', never the handler's.
type Tx struct {
	ctx  context.Context
	b    *storage.Batch
	runs Runs
	// folded is the run a StartRun of this delivery folded into (or replayed)
	// instead of inserting one; empty when every start inserted its own.
	folded tasks.RunID
}

// Batch is the relay batch. It was opened on the outbox's own keyspace, so a
// handler stages into its own core keyspace through b.Bind(h).In(h) and never
// through b.Set, which would write c/outbox/.
func (tx *Tx) Batch() *storage.Batch { return tx.b }

// StartRun stages a run of any registered kind in the relay batch through
// Executor.StartIn, with Trigger Outbox: the run's queue row and the cursor
// advance commit together, so a crash between them cannot happen and a
// redelivery of an acked message starts nothing (SIMP3-8). req.Mode must be
// ModeRequestLive or ModeRequestPreview (unset: tasks.ErrModeUnset, a
// programming error that dead-letters). A module kind's run is stamped with
// that module's system actor.
//
// A start may fold into a run that already exists (the kind's DedupePolicy,
// or an idempotency key): the returned ref says so (Existing) and no new run
// is queued, so the message's own work is done only if that run does it. The
// relay counts and logs every such delivery (mediacore_outbox_folded_total),
// but a kind started from an outbox consumer must still be idempotent and
// carry a DedupePolicy.Key (queued-only) rather than the default params rule,
// which also folds into a RUNNING run and drops what arrived after it began.
func (tx *Tx) StartRun(kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	if tx == nil || tx.b == nil || tx.runs == nil {
		return tasks.RunRef{}, ErrNoRuns
	}
	req.Trigger = tasks.TriggerOutbox
	ref, err := tx.runs.StartIn(tx.ctx, tx.b, kind, req)
	if err == nil && ref.Existing {
		tx.folded = ref.ID
	}
	return ref, err
}

// consumerConfig is what ConsumerOptions set.
type consumerConfig struct {
	unordered     bool
	maxAttempts   int
	backoffMin    time.Duration
	backoffMax    time.Duration
	fromBeginning bool
	live, preview int
	whereT        reflect.Type // T of a Where[T]
	where         any          // the func(Message[T]) bool
	whereRaw      func(RawMessage) bool
	errs          []error // option misuse, reported by Register
}

// ConsumerOption tunes one consumer.
type ConsumerOption func(*consumerConfig)

// Unordered lifts per-key head-of-line blocking: a failing message waits for
// its retry while later messages of its partition are delivered, with up to
// core.outbox.unordered_parallelism in flight. Only Consume and ConsumeRaw
// take it (their cursor commits apart from any effect). A partition is
// settled a round at a time, so after a crash the unsettled round is
// delivered again.
func Unordered() ConsumerOption { return func(c *consumerConfig) { c.unordered = true } }

// MaxAttempts caps the deliveries of one message; the default is
// core.outbox.max_attempts (20).
func MaxAttempts(n int) ConsumerOption {
	return func(c *consumerConfig) {
		if n < 1 {
			c.errs = append(c.errs, fmt.Errorf("%w: MaxAttempts(%d) must be at least 1", ErrInvalidHandler, n))
		}
		c.maxAttempts = n
	}
}

// Backoff sets the retry delay bounds; the defaults are core.outbox.backoff_min
// and backoff_max (1s, 5m). The delay grows exponentially from min, with
// jitter, and never passes max.
func Backoff(minDelay, maxDelay time.Duration) ConsumerOption {
	return func(c *consumerConfig) {
		if minDelay <= 0 || maxDelay < minDelay {
			c.errs = append(c.errs, fmt.Errorf("%w: Backoff(%s, %s) needs 0 < min <= max", ErrInvalidHandler, minDelay, maxDelay))
		}
		c.backoffMin, c.backoffMax = minDelay, maxDelay
	}
}

// FromBeginning starts a NEW consumer at the oldest retained message instead
// of "now".
func FromBeginning() ConsumerOption { return func(c *consumerConfig) { c.fromBeginning = true } }

// Live sets the mode of the runs a ConsumeIntoTask[Raw] starts; exactly one of
// Live and Preview is required there and neither is accepted on other forms.
func Live() ConsumerOption { return func(c *consumerConfig) { c.live++ } }

// Preview is Live's counterpart: the runs start in Preview.
func Preview() ConsumerOption { return func(c *consumerConfig) { c.preview++ } }

// Where filters any typed consumer form: a message for which f returns false
// is acked (cursor advanced) without running the handler or starting a run,
// and counted as filtered. A Where[T] on a consumer of another payload type
// fails registration; raw consumers use WhereRaw.
func Where[T any](f func(Message[T]) bool) ConsumerOption {
	return func(c *consumerConfig) { c.whereT, c.where = reflect.TypeFor[T](), f }
}

// WhereRaw is Where for the raw forms.
func WhereRaw(f func(RawMessage) bool) ConsumerOption {
	return func(c *consumerConfig) { c.whereRaw = f }
}

// handlerForm is how a consumer's effect is committed.
type handlerForm uint8

const (
	formPlain handlerForm = iota + 1 // Consume, ConsumeRaw: runs outside the commit, acks after
	formTx                           // ConsumeTx: effect and cursor in one batch
	formTask                         // ConsumeIntoTask[Raw]: the run's queue row and cursor in one batch
)

// delivery is one message handed to a handler: the sequenced row plus the
// attempt number.
type delivery struct {
	topic, key, id, producer string
	seq                      uint64
	attempt                  int
	enqueuedAt               time.Time
	payload                  json.RawMessage
}

func (d delivery) raw() RawMessage {
	return RawMessage{Seq: d.seq, Topic: d.topic, Key: d.key, IdempotencyKey: d.id, Attempt: d.attempt, EnqueuedAt: d.enqueuedAt, Payload: d.payload}
}

// consumerReg is one registered consumer, built by a Consume constructor and
// validated by Outbox.Register.
type consumerReg struct {
	name, topic string
	owner       string // the registering module; set by Register
	form        handlerForm
	cfg         consumerConfig
	kind        tasks.Kind // formTask
	// payload is T of a typed consumer, nil for a raw one.
	payload reflect.Type
	// invoke runs the handler on d. matched is false when a Where rejected the
	// message (nothing ran). A payload that does not decode as T is ErrSchema.
	invoke func(ctx context.Context, tx *Tx, d delivery) (matched bool, err error)
}

func (c *consumerReg) raw() bool { return c.payload == nil }

// needsBatch reports whether the consumer commits through Executor.WriteRuns.
func (c *consumerReg) needsBatch() bool { return c.form != formPlain }

// Handler is a consumer declaration, registered through Register (06 §6.4.3)
// and never at runtime. One consumer name is one Handler over ONE topic
// (COR3-4): a consumer never reads several topics, because cursors are per
// (consumer, topic, partition) and nothing orders one topic against another.
type Handler struct{ reg *consumerReg }

// decodeTyped decodes d into a Message[T]; failure is ErrSchema.
func decodeTyped[T any](d delivery) (Message[T], error) {
	m := Message[T]{Seq: d.seq, Topic: d.topic, Key: d.key, IdempotencyKey: d.id, Attempt: d.attempt, EnqueuedAt: d.enqueuedAt}
	if err := json.Unmarshal(d.payload, &m.Payload); err != nil {
		// The decoder's text is dropped: a type error can quote the value it
		// rejected, and the payload must not reach an error chain or a log.
		return m, fmt.Errorf("%w: %s does not decode as %v", ErrSchema, d.topic, reflect.TypeFor[T]())
	}
	return m, nil
}

// typedWhere resolves cfg's Where for payload type T, recording a mismatch.
func typedWhere[T any](cfg *consumerConfig) func(Message[T]) bool {
	if cfg.whereRaw != nil {
		cfg.errs = append(cfg.errs, fmt.Errorf("%w: WhereRaw on a typed consumer; use Where", ErrWhereType))
	}
	if cfg.where == nil {
		return nil
	}
	f, ok := cfg.where.(func(Message[T]) bool)
	if !ok {
		cfg.errs = append(cfg.errs, fmt.Errorf("%w: Where over %v on a consumer of %v", ErrWhereType, cfg.whereT, reflect.TypeFor[T]()))
		return nil
	}
	return f
}

// rawWhere resolves cfg's WhereRaw, recording a typed Where.
func rawWhere(cfg *consumerConfig) func(RawMessage) bool {
	if cfg.where != nil {
		cfg.errs = append(cfg.errs, fmt.Errorf("%w: Where[%v] on a raw consumer; use WhereRaw", ErrWhereType, cfg.whereT))
	}
	return cfg.whereRaw
}

func applyOptions(opts []ConsumerOption) consumerConfig {
	var cfg consumerConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return cfg
}

// Consume is the typed core consumer: h runs outside the commit, and the
// cursor advances once it returned nil. At-least-once: a crash after h's
// external effect and before the ack redelivers, so h dedupes on
// IdempotencyKey. Module packages must not call it
// (archtest.OutboxModuleConsumersAreTasks); Register refuses a module owner.
func Consume[T any](t Topic[T], consumer string, h HandlerFunc[T], opts ...ConsumerOption) Handler {
	cfg := applyOptions(opts)
	where := typedWhere[T](&cfg)
	return Handler{reg: &consumerReg{
		name: consumer, topic: t.name, form: formPlain, cfg: cfg, payload: reflect.TypeFor[T](),
		invoke: func(ctx context.Context, _ *Tx, d delivery) (bool, error) {
			m, err := decodeTyped[T](d)
			if err != nil {
				return false, err
			}
			if where != nil && !where(m) {
				return false, nil
			}
			return true, h(ctx, m)
		},
	}}
}

// ConsumeTx is the typed core consumer whose effect is a write to its own core
// keyspace and/or a run to stage: the effect and the cursor advance commit in
// one batch (COR2-10).
func ConsumeTx[T any](t Topic[T], consumer string, h TxHandlerFunc[T], opts ...ConsumerOption) Handler {
	cfg := applyOptions(opts)
	where := typedWhere[T](&cfg)
	return Handler{reg: &consumerReg{
		name: consumer, topic: t.name, form: formTx, cfg: cfg, payload: reflect.TypeFor[T](),
		invoke: func(ctx context.Context, tx *Tx, d delivery) (bool, error) {
			m, err := decodeTyped[T](d)
			if err != nil {
				return false, err
			}
			if where != nil && !where(m) {
				return false, nil
			}
			return true, h(ctx, tx, m)
		},
	}}
}

// ConsumeRaw consumes any durable topic by name, validated against its
// declared schema before delivery. Core only, like Consume.
func ConsumeRaw(topic, consumer string, h func(ctx context.Context, m RawMessage) error, opts ...ConsumerOption) Handler {
	cfg := applyOptions(opts)
	where := rawWhere(&cfg)
	return Handler{reg: &consumerReg{
		name: consumer, topic: topic, form: formPlain, cfg: cfg,
		invoke: func(ctx context.Context, _ *Tx, d delivery) (bool, error) {
			m := d.raw()
			if where != nil && !where(m) {
				return false, nil
			}
			return true, h(ctx, m)
		},
	}}
}

// ConsumeIntoTask turns each message into a run of kind (which must belong to
// the consumer's module), staged in the same batch as the cursor advance, so
// the hand-off is effectively once without a start-idempotency record. This is
// how event-triggered tasks are declared. Typed form: own and core.* topics.
// opts must contain exactly one of Live() or Preview(): the mode of every run
// it starts, so an outbox-triggered writer can never default to Preview.
func ConsumeIntoTask[T any](t Topic[T], consumer string, kind tasks.Kind,
	params func(Message[T]) (any, error), opts ...ConsumerOption) Handler {
	cfg := applyOptions(opts)
	where := typedWhere[T](&cfg)
	mode := taskMode(&cfg)
	return Handler{reg: &consumerReg{
		name: consumer, topic: t.name, form: formTask, cfg: cfg, kind: kind, payload: reflect.TypeFor[T](),
		invoke: func(_ context.Context, tx *Tx, d delivery) (bool, error) {
			m, err := decodeTyped[T](d)
			if err != nil {
				return false, err
			}
			if where != nil && !where(m) {
				return false, nil
			}
			p, err := params(m)
			if err != nil {
				return true, err
			}
			return true, startRun(tx, kind, mode, p)
		},
	}}
}

// ConsumeIntoTaskRaw consumes ANOTHER module's durable topic by name without
// importing it (R3, COR-11); the payload is validated against the declared
// schema. Same mode rule as ConsumeIntoTask.
func ConsumeIntoTaskRaw(topic, consumer string, kind tasks.Kind,
	params func(RawMessage) (any, error), opts ...ConsumerOption) Handler {
	cfg := applyOptions(opts)
	where := rawWhere(&cfg)
	mode := taskMode(&cfg)
	return Handler{reg: &consumerReg{
		name: consumer, topic: topic, form: formTask, cfg: cfg, kind: kind,
		invoke: func(_ context.Context, tx *Tx, d delivery) (bool, error) {
			m := d.raw()
			if where != nil && !where(m) {
				return false, nil
			}
			p, err := params(m)
			if err != nil {
				return true, err
			}
			return true, startRun(tx, kind, mode, p)
		},
	}}
}

// taskMode is the run mode the consumer's options name; exactly one of Live
// and Preview, else a registration error.
func taskMode(cfg *consumerConfig) tasks.ModeRequest {
	switch {
	case cfg.live == 1 && cfg.preview == 0:
		return tasks.ModeRequestLive
	case cfg.preview == 1 && cfg.live == 0:
		return tasks.ModeRequestPreview
	}
	return tasks.ModeRequestUnset
}

// startRun encodes p and stages the run in tx's batch. A params value that
// does not encode is permanent: no redelivery will change it.
func startRun(tx *Tx, kind tasks.Kind, mode tasks.ModeRequest, p any) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("%w: run params for %s do not encode: %v", ErrPermanent, kind, err)
	}
	_, err = tx.StartRun(kind, tasks.StartRequest{Mode: mode, Params: raw})
	return err
}

// Register validates and records hs as owner's consumers, owner being the
// registering module's ID ("core" for the kernel's core consumers). Every
// failure wraps one sentinel of this package; errors are joined, so one call
// reports every bad consumer. Registration closes at Freeze (05-events §5E.6,
// §5E.7).
func (o *Outbox) Register(owner string, hs ...Handler) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.frozen.Load() {
		return ErrFrozen
	}
	var errs []error
	for _, h := range hs {
		reg := h.reg
		if reg == nil {
			errs = append(errs, fmt.Errorf("%w: a zero Handler", ErrInvalidHandler))
			continue
		}
		reg.owner = owner
		if err := o.admit(reg); err != nil {
			errs = append(errs, fmt.Errorf("outbox: consumer %q: %w", reg.name, err))
			continue
		}
		o.consumer[reg.name] = reg
	}
	return errors.Join(errs...)
}

// admit checks one consumer against the catalog and the registration rules.
// The caller holds o.mu.
func (o *Outbox) admit(reg *consumerReg) error {
	var errs []error
	errs = append(errs, reg.cfg.errs...)
	if !validConsumerName(reg.owner, reg.name) {
		errs = append(errs, fmt.Errorf("%w: want %s.<name> with segments [a-z][a-z0-9-]*", ErrConsumerName, reg.owner))
	}
	if !o.listed(reg.owner, reg.name) {
		errs = append(errs, fmt.Errorf("%w: %s is neither in %s's manifest nor on the core consumer list", ErrConsumerUndeclared, reg.name, reg.owner))
	}
	if _, dup := o.consumer[reg.name]; dup {
		errs = append(errs, fmt.Errorf("%w: %s", ErrDuplicateConsumer, reg.name))
	}
	if reg.owner != "core" && reg.form != formTask {
		errs = append(errs, fmt.Errorf("%w: a module consumes through ConsumeIntoTask", ErrModuleHandler))
	}
	if reg.form == formTask {
		if reg.kind.Module() != reg.owner {
			errs = append(errs, fmt.Errorf("%w: kind %s is not %s's", ErrForeignKind, reg.kind, reg.owner))
		}
		if reg.cfg.live+reg.cfg.preview != 1 {
			errs = append(errs, fmt.Errorf("%w: got %d Live and %d Preview", ErrModeOption, reg.cfg.live, reg.cfg.preview))
		}
	} else if reg.cfg.live+reg.cfg.preview != 0 {
		errs = append(errs, ErrModeNotAllowed)
	}
	if reg.cfg.unordered && reg.form != formPlain {
		errs = append(errs, ErrUnorderedTx)
	}
	errs = append(errs, o.checkTopic(reg)...)
	return errors.Join(errs...)
}

// listed reports whether name is a declared consumer of owner.
func (o *Outbox) listed(owner, name string) bool {
	if owner == "core" {
		return o.core[name]
	}
	return o.byModule[owner][name]
}

// checkTopic validates the consumer's topic against the catalog.
func (o *Outbox) checkTopic(reg *consumerReg) []error {
	spec, ok := o.cat.Lookup(reg.topic)
	if !ok {
		return []error{fmt.Errorf("%w: %s", ErrUnknownTopic, reg.topic)}
	}
	if spec.Durable == nil {
		return []error{fmt.Errorf("%w: %s", ErrNotDurable, reg.topic)}
	}
	var errs []error
	if !validPartitionsOf(spec) {
		errs = append(errs, fmt.Errorf("%w: %s declares %d", ErrBadPartitions, reg.topic, spec.Durable.Partitions))
	}
	if !reg.raw() {
		if pt := spec.PayloadType(); pt != nil && pt != reg.payload {
			errs = append(errs, fmt.Errorf("%w: %s carries %v, the consumer takes %v", ErrPayloadType, reg.topic, pt, reg.payload))
		}
		if owner := topicOwner(reg.topic); owner != reg.owner && owner != "core" {
			errs = append(errs, fmt.Errorf("%w: %s belongs to %s; consume it raw", ErrForeignTopic, reg.topic, owner))
		}
	}
	return errs
}

func validPartitionsOf(spec events.TopicSpec) bool {
	return spec.Durable != nil && outboxstore.ValidPartitions(outboxstore.Partitions(spec.Durable.Partitions))
}
