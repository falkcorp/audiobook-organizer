// file: core/events/subscription.go
// version: 1.0.0
// guid: b863b724-1fb2-4fc4-9f70-15990bd2abf2
// last-edited: 2026-11-10

package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// Handler is a typed subscriber's function. ctx is the bus lifecycle context,
// never the publisher's (05-events §5E.5.1).
type Handler[T any] func(ctx context.Context, e Envelope[T])

// RawHandler is a raw subscriber's function: the payload as JSON, validated
// against the topic's declared schema.
type RawHandler func(ctx context.Context, e RawEnvelope)

// Subscriptions are declarations, made in Register through
// Registrar.Subscribe(...events.Subscription) (COR-8) and frozen when
// Register returns. Delivery begins at Bus.Open (06 §6.5.2).
type Subscription struct {
	topic   string
	name    string
	raw     bool
	payload reflect.Type // a typed subscription's T
	deliver func(ctx context.Context, ev *event) (dropReason string)
	cfg     subConfig
	err     error // an option refused at construction, reported by Subscribe
}

// On subscribes typed: allowed for the declaring module's own topics and for
// core.* topics (whose payload types live in core).
func On[T any](t Topic[T], name string, h Handler[T], opts ...SubOption) Subscription {
	s := Subscription{topic: t.name, name: name, payload: reflect.TypeFor[T]()}
	if h == nil {
		s.err = errors.New("nil handler")
		return s
	}
	s.deliver = func(ctx context.Context, ev *event) string {
		p, ok := ev.payload.(T)
		if !ok {
			return reasonEncode
		}
		h(ctx, Envelope[T]{ID: ev.id, Topic: ev.topic.spec.Name, Time: ev.time, Key: ev.key,
			Actor: ev.actor, RunID: ev.runID, TraceID: ev.traceID, Payload: p})
		return ""
	}
	s.cfg, s.err = buildConfig(opts)
	return s
}

// OnRaw is how a module subscribes to ANOTHER module's topic without importing
// it (R3): by name, with the JSON payload validated against the declared
// schema. The JSON form is encoded once per publish, only when the topic has
// a raw subscriber, on the publisher's goroutine inside Publish (so a payload
// the publisher changes afterwards is not raced).
func OnRaw(topic, name string, h RawHandler, opts ...SubOption) Subscription {
	s := Subscription{topic: topic, name: name, raw: true}
	if h == nil {
		s.err = errors.New("nil handler")
		return s
	}
	s.deliver = func(ctx context.Context, ev *event) string {
		payload, cause := ev.json()
		if cause != "" {
			return reasonEncode
		}
		h(ctx, RawEnvelope{ID: ev.id, Topic: ev.topic.spec.Name, Key: ev.key, Actor: ev.actor,
			RunID: ev.runID, TraceID: ev.traceID, Time: ev.time, Payload: payload})
		return ""
	}
	s.cfg, s.err = buildConfig(opts)
	return s
}

// SubOption configures one subscription.
type SubOption func(*subConfig)

// subConfig is a subscription's options; zero fields take the bus defaults.
type subConfig struct {
	queueSize  int // 0 = the bus's core.events.queue_size
	dropOldest bool
	keyFilter  func(key string) bool
	coalesce   time.Duration
	bad        []error
}

// maxQueueSize is QueueSize's upper bound (05-events §5E.3.2).
const maxQueueSize = 65536

// defaultQueueSize is core.events.queue_size's default (§5E.9).
const defaultQueueSize = 256

// QueueSize sets the subscription's queue length: default
// core.events.queue_size (256); max 65536. A value outside [1, 65536] makes
// the subscription invalid.
func QueueSize(n int) SubOption {
	return func(c *subConfig) {
		if n < 1 || n > maxQueueSize {
			c.bad = append(c.bad, fmt.Errorf("QueueSize(%d) outside [1, %d]", n, maxQueueSize))
			return
		}
		c.queueSize = n
	}
}

// DropOldest makes a full queue drop its oldest event for the incoming one.
// The default policy is DropNewest (keep queued, reject incoming).
func DropOldest() SubOption { return func(c *subConfig) { c.dropOldest = true } }

// KeyFilter delivers only events whose Key f accepts. The filter runs on the
// subscription's own goroutine, under the same panic recover as its handler,
// never on the publisher's: a slow filter delays only this subscription, and
// a panicking one is counted in handler_panics and skips that event. An
// event the filter refuses still takes a queue slot until it is reached.
func KeyFilter(f func(key string) bool) SubOption {
	return func(c *subConfig) { c.keyFilter = f }
}

// Coalesce delivers at most one event per Key per window (latest wins): the
// first event of a key opens its window, and the latest event of that key is
// delivered when the window ends. Events with the empty Key are delivered
// uncoalesced. A window that is not positive makes the subscription invalid.
func Coalesce(window time.Duration) SubOption {
	return func(c *subConfig) {
		if window <= 0 {
			c.bad = append(c.bad, fmt.Errorf("Coalesce(%v): the window must be positive", window))
			return
		}
		c.coalesce = window
	}
}

func buildConfig(opts []SubOption) (subConfig, error) {
	var c subConfig
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	return c, errors.Join(c.bad...)
}
