// file: core/realtime/realtime.go
// version: 1.1.0
// guid: 213444a2-5f76-43a9-984f-548693fefa25
// last-edited: 2026-11-12

// Package realtime is core's one server-push channel: a permission-filtered,
// resumable SSE hub per App (05-realtime). It declares no topics and no
// permissions of its own; it streams the events-catalog topics whose entry
// carries Realtime metadata, and filters every event, for every subscriber, at
// SEND time by the topic's own Permission, Visible and Redact, with the
// subscriber's current rights. A replayed ring entry goes down the same path
// as a live one, so a resume can never show a principal an event it cannot see
// now (05-realtime §4.4).
//
// The hub is built in P5 and is dark until F6-03 mounts it: the kernel calls
// Subscriptions after every topic is declared and before the bus freezes,
// registers the result, and mounts Handler through internal/eventhttp.
//
// Bounds. The ring holds RingSize entries of at most MaxPayload bytes; a
// subscriber's live queue holds ClientBuffer entries and a slow one is told to
// reset, never waited for; at most MaxClients streams are open and at most
// MaxPerBucket per user and credential class; a query carries a bounded number
// of bounded patterns and a Last-Event-ID is length-checked before it is
// parsed. A stream is its HTTP handler's goroutine: the hub starts none.
//
// Layer L6 (03 R4). No package-level mutable state (R5).
package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/sse"
	"github.com/falkcorp/mediacore/metrics"
)

// Options configure the hub; a zero field takes its default (05-realtime §3).
type Options struct {
	RingSize     int           // resumable window, events (default 1024, at most 65536)
	ClientBuffer int           // per-subscriber queue (default 128, at most 65536)
	MaxClients   int           // global open streams (default 256, at most 65536)
	MaxPerBucket int           // streams per user per credential class (default 16, §4.5)
	Heartbeat    time.Duration // default 15s
	WriteTimeout time.Duration // per-write deadline via http.ResponseController (default 10s)
	RetryAfter   time.Duration // on 429/503 (default 5s)
	// MaxPayload bounds one event's JSON, in bytes (default 262144). The ring
	// keeps RingSize of them, so this is what bounds the hub's memory; an
	// event over it is dropped and counted, never truncated.
	MaxPayload int
	// AllowNoControlTopics lets Subscriptions succeed when the catalog does
	// not declare core.principal.invalidated.v1 or core.rbac.changed.v1. Off
	// (the default) it is an error: a hub that cannot hear them reacts to a
	// revocation or a role change only on the next heartbeat. It exists for a
	// catalog that is deliberately smaller (a test, an App with no auth).
	AllowNoControlTopics bool
}

// Option defaults and ceilings.
const (
	defaultRingSize     = 1024
	defaultClientBuffer = 128
	defaultMaxClients   = 256
	defaultMaxPerBucket = 16
	defaultHeartbeat    = 15 * time.Second
	defaultWriteTimeout = 10 * time.Second
	defaultRetryAfter   = 5 * time.Second
	defaultMaxPayload   = 256 << 10
	maxCount            = 1 << 16 // ceiling of every count option
	maxMaxPayload       = 4 << 20
	// maxHeartbeat bounds the revocation cut-off (a stream is revalidated and
	// its rights refreshed on every heartbeat) and maxWriteTimeout how long a
	// client that stopped reading can hold a stream's goroutine.
	maxHeartbeat    = time.Minute
	maxWriteTimeout = time.Minute

	// closeBudget bounds how long Run waits for streams to drain when its
	// context ends.
	closeBudget = 5 * time.Second
)

// resolve fills the defaults and refuses a negative or oversized value.
func (o Options) resolve() (Options, error) {
	for _, c := range []struct {
		name string
		v    *int
		def  int
		max  int
	}{
		{"RingSize", &o.RingSize, defaultRingSize, maxCount},
		{"ClientBuffer", &o.ClientBuffer, defaultClientBuffer, maxCount},
		{"MaxClients", &o.MaxClients, defaultMaxClients, maxCount},
		{"MaxPerBucket", &o.MaxPerBucket, defaultMaxPerBucket, maxCount},
		{"MaxPayload", &o.MaxPayload, defaultMaxPayload, maxMaxPayload},
	} {
		switch {
		case *c.v < 0 || *c.v > c.max:
			return o, fmt.Errorf("realtime: Options.%s %d outside [0, %d]", c.name, *c.v, c.max)
		case *c.v == 0:
			*c.v = c.def
		}
	}
	for _, c := range []struct {
		name string
		v    *time.Duration
		def  time.Duration
		max  time.Duration
	}{
		{"Heartbeat", &o.Heartbeat, defaultHeartbeat, maxHeartbeat},
		{"WriteTimeout", &o.WriteTimeout, defaultWriteTimeout, maxWriteTimeout},
		{"RetryAfter", &o.RetryAfter, defaultRetryAfter, time.Hour},
	} {
		switch {
		case *c.v < 0 || *c.v > c.max:
			return o, fmt.Errorf("realtime: Options.%s %v outside [0, %v]", c.name, *c.v, c.max)
		case *c.v == 0:
			*c.v = c.def
		}
	}
	return o, nil
}

// Revalidator is what the hub asks about a stream's credential on every
// heartbeat. *auth.Service is the one implementation (05-authn §4.12).
type Revalidator interface {
	Revalidate(ctx context.Context, p *auth.Principal) error
}

// Deps are the hub's collaborators. Auth and Catalog are required: a hub that
// cannot revalidate would keep a revoked credential streaming, so NewHub
// refuses to be built without them.
type Deps struct {
	Auth    Revalidator    // Revalidate
	Catalog events.Catalog // the one topic catalog (05-events)
	Log     *slog.Logger
	Metrics *metrics.Scope // the core "realtime" scope, labels topic and reason (05-metrics); nil records nothing
	Clock   func() time.Time
	// Refresh re-resolves a stream's principal: it returns the principal as it
	// would be built now, with a fresh checker. It is REQUIRED (NewHub refuses
	// nil): the hub calls it after core.rbac.changed.v1 and on every
	// heartbeat, because a role assignment change (auth.SetRoles) moves
	// neither the credential epoch nor the rbac generation, so only a fresh
	// build notices it. auth exports no such call (a key principal's scopes and
	// a method's interactivity are the kernel's to rebuild), so the kernel
	// supplies it (F6-03). A definite error ends the stream credential_revoked;
	// a store failure is retried on each heartbeat, withholds every event
	// while it persists, and ends the stream permission_revoked on the third
	// in a row.
	Refresh func(ctx context.Context, p *auth.Principal) (*auth.Principal, error)
}

// Envelope is the wire frame (the data: line) of the native stream.
type Envelope struct {
	Topic string          `json:"topic"`
	Time  time.Time       `json:"ts"`
	Key   string          `json:"key,omitempty"`
	Data  json.RawMessage `json:"data"`
}

// Errors the hub returns before a stream starts; the handler maps each to a
// status (05-realtime §6).
var (
	ErrTooManyClients = errors.New("realtime: too many event streams")           // 503
	ErrTooManyForUser = errors.New("realtime: too many event streams for user")  // 429
	ErrUnknownTopic   = errors.New("realtime: unknown topic")                    // 400
	ErrTopicDenied    = errors.New("realtime: topic not permitted")              // 403 (explicit topic only)
	ErrNotPermitted   = errors.New("realtime: the caller may not open a stream") // 403
	ErrNotReady       = errors.New("realtime: the hub has no topics yet")        // 503 (Subscriptions not called)
	ErrClosed         = errors.New("realtime: the hub is closed")                // 503
)

// topicTable is the realtime topics of the frozen catalog, read once.
type topicTable struct {
	specs map[string]events.TopicSpec
	names []string // sorted
}

// Hub is created by the kernel in P5, one per App (R5).
type Hub struct {
	opt Options
	d   Deps
	log *slog.Logger
	br  *sse.Broker
	m   *instruments

	table  atomic.Pointer[topicTable]
	gen    atomic.Uint64 // core.rbac.changed.v1 events seen
	closed atomic.Bool

	warnMu   sync.Mutex
	warnedAt map[string]time.Time // topic -> last decode warning
}

// NewHub validates o and d and builds the hub. It creates no goroutine and
// subscribes to nothing: Subscriptions does that, once the catalog is complete.
func NewHub(o Options, d Deps) (*Hub, error) {
	opt, err := o.resolve()
	if err != nil {
		return nil, err
	}
	switch {
	case d.Auth == nil:
		return nil, errors.New("realtime: Deps.Auth is required")
	case d.Catalog == nil:
		return nil, errors.New("realtime: Deps.Catalog is required")
	case d.Refresh == nil:
		return nil, errors.New("realtime: Deps.Refresh is required: without it a role change would reach an open stream only when it reconnects")
	}
	h := &Hub{opt: opt, d: d, log: d.Log, warnedAt: map[string]time.Time{}}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	if h.m, err = newInstruments(d.Metrics, h); err != nil {
		return nil, err
	}
	if h.br, err = sse.NewBroker(sse.Options{
		RingSize: opt.RingSize, ClientBuffer: opt.ClientBuffer, MaxClients: opt.MaxClients,
		MaxPerBucket: opt.MaxPerBucket, Heartbeat: opt.Heartbeat, WriteTimeout: opt.WriteTimeout,
		Now: d.Clock, Hooks: sse.Hooks{
			Reset: h.m.reset, Dropped: h.m.overflowed, Closed: h.m.closed,
			InterruptFailed: func(err error) {
				h.log.Warn("realtime: could not interrupt a stream's write on shutdown or revocation; it ends at its write deadline", "err", err)
			},
		},
	}); err != nil {
		return nil, err
	}
	return h, nil
}

// Control topics the hub reads from the catalog when its owner declares them
// (F4-08). Neither carries Realtime metadata, so neither is ever streamed.
const (
	topicInvalidated = "core.principal.invalidated.v1" // {user_id, credential_id?, reason} (05-authn §4.12)
	topicRBACChanged = "core.rbac.changed.v1"          // {generation} (05-rbac §5)
)

// invalidation is the payload of core.principal.invalidated.v1.
type invalidation struct {
	UserID       string `json:"user_id"`
	CredentialID string `json:"credential_id"`
}

// Subscriptions builds the raw bus subscriptions the hub needs: one per
// catalog topic with Realtime metadata, coalesced by the topic's own window,
// and one each for the two control topics (an error if the catalog does not
// declare them, unless Options.AllowNoControlTopics). The
// kernel registers them with Bus.Subscribe("core", ...) after every Declare
// and before Freeze (events.Bus refuses a subscription once frozen, so the hub
// cannot subscribe itself at P7). It reads the catalog once, fixing the topic
// set for the life of the process, and may be called once.
func (h *Hub) Subscriptions() ([]events.Subscription, error) {
	tab := &topicTable{specs: map[string]events.TopicSpec{}}
	var subs []events.Subscription
	taken := map[string]bool{}
	for spec := range h.d.Catalog.All() {
		if spec.Realtime == nil {
			continue
		}
		var opts []events.SubOption
		if spec.Realtime.Coalesce > 0 {
			opts = append(opts, events.Coalesce(spec.Realtime.Coalesce))
		}
		name := subscriptionName(spec.Name, taken)
		subs = append(subs, events.OnRaw(spec.Name, name, h.onEvent, opts...))
		tab.specs[spec.Name] = spec
		tab.names = append(tab.names, spec.Name)
	}
	slices.Sort(tab.names)
	for _, c := range []struct {
		topic, name string
		fn          func(context.Context, events.RawEnvelope)
		effect      string
	}{
		{topicInvalidated, "core.realtime.principal-invalidated", h.onInvalidated, "a revoked credential would end a stream only on its next heartbeat"},
		{topicRBACChanged, "core.realtime.rbac-changed", h.onRBACChanged, "a rights change would reach a stream only on its next heartbeat"},
	} {
		if _, ok := h.d.Catalog.Lookup(c.topic); ok {
			subs = append(subs, events.OnRaw(c.topic, c.name, c.fn))
			continue
		}
		if !h.opt.AllowNoControlTopics {
			return nil, fmt.Errorf("realtime: the catalog does not declare %s, so %s (declare it, or set Options.AllowNoControlTopics)", c.topic, c.effect)
		}
		h.log.Warn("realtime: the catalog does not declare a control topic: "+c.effect, "topic", c.topic)
	}
	if !h.table.CompareAndSwap(nil, tab) {
		return nil, errors.New("realtime: Subscriptions may be called once")
	}
	return subs, nil
}

// subscriptionName is "core.realtime.<topic with dots as dashes>", made unique.
func subscriptionName(topic string, taken map[string]bool) string {
	base := "core.realtime." + strings.ReplaceAll(topic, ".", "-")
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	taken[name] = true
	return name
}

// onEvent is a Realtime topic's bus handler: it appends the event to the ring
// and offers it to the subscribers. It never blocks and never fails the
// publisher; an event that is too large or not JSON is dropped and counted.
func (h *Hub) onEvent(_ context.Context, e events.RawEnvelope) {
	switch {
	case len(e.Payload) > h.opt.MaxPayload:
		h.m.dropped("oversize")
		h.warnOnce(e.Topic, "realtime: an event over MaxPayload was dropped (counted; logged once per topic per minute)")
		return
	}
	// The ring outlives the call and the bus's bytes are the bus's, shared
	// with every other subscriber of the topic: keep our own.
	e.Payload = bytes.Clone(e.Payload)
	switch {
	case !json.Valid(e.Payload):
		h.m.dropped("decode")
		h.warnOnce(e.Topic, "realtime: an event whose payload is not JSON was dropped (counted; logged once per topic per minute)")
		return
	}
	head, err := envelopeHead(e)
	if err != nil {
		h.m.dropped("decode")
		return
	}
	if h.br.Publish(sse.Entry{Topic: e.Topic, Key: e.Key, Item: &item{raw: e, head: head}}) != 0 {
		h.m.published(e.Topic)
	}
}

// item is what a ring entry carries: the bus envelope as published (Visible
// and Redact are handed their own copy of its payload) and the encoded
// envelope up to the data member, built once per publish.
type item struct {
	raw  events.RawEnvelope
	head []byte
}

// envelopeHead is Envelope's JSON up to and including `"data":`, so a
// subscriber's frame is head + data + "}" and nothing is marshalled per
// subscriber. The wire shape is Envelope's, defined once.
func envelopeHead(e events.RawEnvelope) ([]byte, error) {
	b, err := json.Marshal(Envelope{Topic: e.Topic, Time: e.Time, Key: e.Key, Data: json.RawMessage("null")})
	if err != nil {
		return nil, err
	}
	head, ok := strings.CutSuffix(string(b), "null}")
	if !ok {
		return nil, errors.New("realtime: unexpected envelope encoding")
	}
	return []byte(head), nil
}

// warnOnce logs msg for topic at most once a minute.
func (h *Hub) warnOnce(topic, msg string) {
	now := h.now()
	h.warnMu.Lock()
	last, seen := h.warnedAt[topic]
	due := !seen || now.Sub(last) >= time.Minute
	if due {
		h.warnedAt[topic] = now
	}
	h.warnMu.Unlock()
	if due {
		h.log.Error(msg, "topic", topic)
	}
}

func (h *Hub) now() time.Time {
	if h.d.Clock != nil {
		return h.d.Clock()
	}
	return time.Now()
}

// onInvalidated ends every stream of the user, or of the one credential, that
// core.principal.invalidated.v1 names (05-realtime §4.6). A payload it cannot
// read ends nothing: the heartbeat revalidation is the backstop.
func (h *Hub) onInvalidated(_ context.Context, e events.RawEnvelope) {
	var inv invalidation
	if err := json.Unmarshal(e.Payload, &inv); err != nil || inv.UserID == "" {
		h.warnOnce(e.Topic, "realtime: an invalidation event could not be read; streams end on the heartbeat revalidation")
		return
	}
	h.br.End(func(meta any) bool {
		st, ok := meta.(*stream)
		return ok && st.user != "" && string(st.user) == inv.UserID && (inv.CredentialID == "" || st.cred == inv.CredentialID)
	}, sse.CloseCredentialRevoked)
}

// onRBACChanged records a new rbac generation and wakes every stream, so each
// re-resolves its rights on its own goroutine at once.
func (h *Hub) onRBACChanged(context.Context, events.RawEnvelope) {
	h.gen.Add(1)
	h.br.WakeAll()
}

// Subscribers is the number of open streams: the value of the
// <ns>_realtime_subscribers gauge.
func (h *Hub) Subscribers() int { return h.br.Len() }

// Run holds the hub's lifetime: it returns when ctx ends, after closing every
// stream within closeBudget. The raw subscriptions run on the bus's own
// goroutines and the coalescing timers on theirs, so Run owns none.
func (h *Hub) Run(ctx context.Context) error {
	<-ctx.Done()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeBudget)
	defer cancel()
	return h.Close(cctx)
}

// Close refuses new streams, sends core.stream.closed.v1 {reason: "shutdown"}
// to every open one whatever its permissions, and waits until they have
// returned or ctx ends. It is idempotent and is called from
// http.Server.RegisterOnShutdown, so Shutdown is not held open by streams.
func (h *Hub) Close(ctx context.Context) error {
	h.closed.Store(true)
	h.br.Close()
	return h.br.Wait(ctx)
}
