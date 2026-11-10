// file: core/events/bus.go
// version: 1.0.0
// guid: f793adf3-caa6-43f6-8e78-c1fdab681980
// last-edited: 2026-11-10

package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/falkcorp/mediacore/metrics"
)

// Envelope is one typed event as a typed subscriber receives it.
type Envelope[T any] struct {
	ID      string // ULID, unique per publish
	Topic   string
	Time    time.Time
	Key     string // entity key for coalescing/filtering ("" = none)
	Actor   string // user ID, "system", or "task:<run-id>"
	RunID   string // originating task run, if any
	TraceID string // from the publisher's span, for correlation only
	Payload T
}

// RawEnvelope is one event as a raw subscriber receives it.
type RawEnvelope struct {
	ID, Topic, Key, Actor, RunID, TraceID string
	Time                                  time.Time
	Payload                               json.RawMessage
}

// Bus is one per App (R5: never a package global). Constructed by the kernel
// and held only by core; modules never see a *Bus.
type Bus struct {
	log       *slog.Logger
	queueSize int
	origin    func(ctx context.Context) (actor, runID string)
	inst      *instruments

	mu       sync.Mutex // the registration state below
	topics   map[string]*topicState
	disabled map[string]bool
	subs     []*sub
	subNames map[string]bool

	// frozen is published by Freeze; Publish reads it without mu.
	frozen atomic.Pointer[frozenState]

	lifeMu  sync.Mutex // Open and Close
	opened  bool
	closed  atomic.Bool
	ctx     context.Context // the bus lifecycle context handed to every handler
	cancel  context.CancelFunc
	running atomic.Int32  // subscription goroutines still running
	allDone chan struct{} // closed when the last one exits

	rejected sync.Map // topic name -> struct{}: publish_rejected logged once per topic
}

// BusOptions is what the kernel builds a Bus from (coined: 05-events gives
// the Bus no constructor signature).
type BusOptions struct {
	// Logger receives the bus's warnings; nil discards.
	Logger *slog.Logger
	// Metrics is the core "events" scope (<namespace>.events) with the
	// attribute keys topic, subscriber and reason; nil records nothing.
	Metrics *metrics.Scope
	// QueueSize is core.events.queue_size, the queue length of a
	// subscription without QueueSize; 0 = 256, at most 65536.
	QueueSize int
	// Origin reads the actor and the originating task run of a publish from
	// its context; nil leaves both empty. The kernel supplies it, so events
	// imports neither auth nor tasks.
	Origin func(ctx context.Context) (actor, runID string)
}

// NewBus returns an empty, unfrozen bus.
func NewBus(o BusOptions) (*Bus, error) {
	qs := o.QueueSize
	switch {
	case qs == 0:
		qs = defaultQueueSize
	case qs < 1 || qs > maxQueueSize:
		return nil, fmt.Errorf("events: core.events.queue_size %d outside [1, %d]", qs, maxQueueSize)
	}
	l := o.Logger
	if l == nil {
		l = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := &Bus{
		log: l, queueSize: qs, origin: o.Origin,
		topics: map[string]*topicState{}, disabled: map[string]bool{}, subNames: map[string]bool{},
		allDone: make(chan struct{}),
	}
	// The lifecycle context belongs to the bus, never to a caller: handlers
	// must outlive the request that published (§5E.5.1 "Handler context").
	b.ctx, b.cancel = context.WithCancel(context.Background())
	inst, err := newInstruments(o.Metrics, b)
	if err != nil {
		return nil, err
	}
	b.inst = inst
	return b, nil
}

// Scope is a module's publish handle, module.Deps.Events (06 §6.4.4, COR-21):
// the App's Bus bound to one module ID by the kernel. Publishing through it
// is accepted only for catalog entries the bound module declared; that rule
// needs the module ID, which a shared *Bus cannot know. Safe for concurrent use.
type Scope struct {
	bus    *Bus
	module string
}

// Scope binds the bus to moduleID. Kernel-only: called once per enabled module
// when it builds that module's Deps.
func (b *Bus) Scope(moduleID string) Scope { return Scope{bus: b, module: moduleID} }

// Publisher is implemented by *Bus (core publishers: core.* topics only) and by
// Scope (one module's own topics). The interface is sealed (unexported method).
type Publisher interface{ publisher() }

func (b *Bus) publisher()  {}
func (s Scope) publisher() {}

// Publish never blocks on subscribers and never returns an error: it is
// notify-only. ctx is read for actor/run/trace values only; it is NOT handed
// to handlers. Publishing to an undeclared topic, a topic outside the
// publisher's namespace (another module's, or core.* through a Scope) or a
// Durable topic is counted (publish_rejected) and logged once per topic.
func Publish[T any](p Publisher, ctx context.Context, t Topic[T], key string, payload T) {
	var b *Bus
	owner := coreOwner
	switch v := p.(type) {
	case *Bus:
		b = v
	case Scope:
		b, owner = v.bus, v.module
	}
	if b == nil {
		return
	}
	b.publish(ctx, owner, t.name, reflect.TypeFor[T](), key, payload)
}

// event is one publish, shared by every subscriber it is queued to.
type event struct {
	topic                          *topicState
	id, key, actor, runID, traceID string
	time                           time.Time
	payload                        any

	rawOnce sync.Once
	raw     json.RawMessage
	rawErr  error
}

// json is the payload's JSON form, encoded and validated against the declared
// schema once per publish, on the first raw subscriber's goroutine.
func (ev *event) json() (json.RawMessage, error) {
	ev.rawOnce.Do(func() {
		b, err := json.Marshal(ev.payload)
		if err != nil {
			ev.rawErr = err
			return
		}
		if rs := ev.topic.resolved; rs != nil {
			var inst any
			if err := json.Unmarshal(b, &inst); err != nil {
				ev.rawErr = err
				return
			}
			if err := rs.Validate(inst); err != nil {
				ev.rawErr = fmt.Errorf("events: %s payload fails its schema: %w", ev.topic.spec.Name, err)
				return
			}
		}
		ev.raw = b
	})
	return ev.raw, ev.rawErr
}

func (b *Bus) publish(ctx context.Context, owner, name string, typ reflect.Type, key string, payload any) {
	st := b.frozen.Load()
	if st == nil {
		b.reject(name, "the bus is not frozen yet (publish after the kernel's P7)")
		return
	}
	ts, ok := st.topics[name]
	switch {
	case !ok:
		b.reject(name, "no module declares this topic")
		return
	case ts.owner != owner:
		b.reject(name, fmt.Sprintf("the topic is %s's, the publisher is %s", ts.owner, owner))
		return
	case ts.spec.Durable != nil:
		b.reject(name, "the topic is Durable: stage it through the outbox")
		return
	case ts.spec.payload != nil && ts.spec.payload != typ:
		b.reject(name, fmt.Sprintf("the payload is %v, the topic carries %v", typ, ts.spec.payload))
		return
	}
	b.inst.published(ts)
	if len(ts.subs) == 0 {
		return
	}
	ev := &event{topic: ts, key: key, time: time.Now(), payload: payload, id: newULID()}
	if b.origin != nil {
		ev.actor, ev.runID = b.origin(ctx)
	}
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		ev.traceID = sc.TraceID().String()
	}
	for _, s := range ts.subs {
		if s.cfg.keyFilter != nil && !s.cfg.keyFilter(key) {
			continue
		}
		s.enqueue(ev)
	}
}

// reject counts a refused publish and logs it once per topic.
func (b *Bus) reject(name, why string) {
	b.inst.rejected(name)
	if _, seen := b.rejected.LoadOrStore(name, struct{}{}); !seen {
		b.log.Warn("events: publish rejected (counted; logged once per topic)", "topic", name, "reason", why)
	}
}

// Open starts one goroutine per subscription (06 §6.5.2: after module hooks
// and workers start). No handler runs before it; events queued since Freeze
// are delivered from here. Kernel-only.
func (b *Bus) Open() error {
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	st := b.frozen.Load()
	switch {
	case b.closed.Load():
		return ErrClosed
	case b.opened:
		return ErrOpen
	case st == nil:
		return ErrNotFrozen
	}
	b.opened = true
	b.running.Store(int32(len(st.subs)))
	if len(st.subs) == 0 {
		close(b.allDone)
		return nil
	}
	for _, s := range st.subs {
		go s.run(b.ctx)
	}
	return nil
}

// Close stops accepting, drains the queues until ctx is done, then cancels
// the handlers' context and drops and counts what is left (reason "closed").
// 06 §6.5.2 calls it after the executor drain, with min(core.events.close_drain,
// the remaining shutdown budget). It returns an error naming the
// subscriptions still inside a handler when the budget ran out; those
// goroutines exit when their handler returns. Idempotent. Kernel-only.
func (b *Bus) Close(ctx context.Context) error {
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	if b.closed.Swap(true) {
		return nil
	}
	var subs []*sub
	if st := b.frozen.Load(); st != nil {
		subs = st.subs
	}
	for _, s := range subs {
		s.beginClose()
	}
	if !b.opened {
		b.cancel()
		b.dropAll(subs)
		return nil
	}
	select {
	case <-b.allDone:
		b.cancel()
		return nil
	case <-ctx.Done():
	}
	b.cancel()
	b.dropAll(subs)
	select {
	case <-b.allDone:
		return nil
	default:
	}
	var busy []string
	for _, s := range subs {
		if s.inHandler.Load() {
			busy = append(busy, s.name)
		}
	}
	if len(busy) == 0 {
		return nil
	}
	return fmt.Errorf("events: close: %s still in a handler after the drain budget: %w",
		strings.Join(busy, ", "), errors.Join(ErrClosed, context.Cause(ctx)))
}

// dropAll counts every event still queued as dropped (reason "closed").
func (b *Bus) dropAll(subs []*sub) {
	n := 0
	for _, s := range subs {
		n += s.dropQueued()
	}
	if n > 0 {
		b.log.Warn("events: close dropped undelivered events (counted)", "events", n)
	}
}
