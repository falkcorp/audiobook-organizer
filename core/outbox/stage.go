// file: core/outbox/stage.go
// version: 1.0.0
// guid: 0f0d8f9d-4cb3-4a52-a00d-7f7fb8f0c1c6
// last-edited: 2026-11-12

package outbox

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	randv2 "math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
)

// Topic is a durable stream: an events catalog entry with Durable set. Same
// name grammar; one catalog (SIMP-5), so a name is either bus-only or durable.
type Topic[T any] struct{ name string }

// NewTopic returns the topic named name. Like events.NewTopic it is inert
// until its owner declares it.
func NewTopic[T any](name string) Topic[T] { return Topic[T]{name: name} }

// Name is the topic's name.
func (t Topic[T]) Name() string { return t.name }

// Spec returns s with Name, Schema and Durable{Partitions: partitions} set
// (partitions 0 = 16), for Registrar.Topics: there is no separate outbox
// declaration (COR-11). It is events.Topic[T].Spec plus the Durable mark.
func (t Topic[T]) Spec(s events.TopicSpec, partitions int) events.TopicSpec {
	s = events.NewTopic[T](t.name).Spec(s)
	s.Durable = &events.Durable{Partitions: partitions}
	return s
}

// Writer is the module-scoped staging handle (module.Deps.Outbox, 06 §6.4.4):
// the Outbox plus the calling module's ID. The zero Writer stages nothing.
type Writer struct {
	o      *Outbox
	module string
}

// Writer returns the staging handle of module (the ID of the calling module;
// "core" for core's own services).
func (o *Outbox) Writer(module string) Writer { return Writer{o: o, module: module} }

// StageOption tunes one Stage.
type StageOption func(*stageConfig)

type stageConfig struct{ notBefore time.Time }

// NotBefore delays delivery: the message is sequenced only once t has passed.
// Nothing orders a delayed message against later ones of its key; it takes
// its place in the stream when it is sequenced.
func NotBefore(t time.Time) StageOption { return func(c *stageConfig) { c.notBefore = t } }

// topicOwner is the module a topic name belongs to: its first segment.
func topicOwner(name string) string {
	owner, _, _ := strings.Cut(name, ".")
	return owner
}

// Stage writes msg into b's intake. It is committed if and only if b commits;
// a discarded batch leaves nothing. Stage binds the outbox keyspace to b and
// writes only in/ inside it, so R8 holds although b was opened by a module.
// Only the topic's owner may stage it. The intake ULID comes from ONE
// monotonic generator per Outbox (strictly increasing within the process,
// also within one millisecond and across a backward clock step), so intake
// keys keep Stage call order within a process: two messages staged in one
// batch commit, and are sequenced, in Stage order (COR4-14).
//
// A refusal stages nothing and the caller should discard b: a correctness
// message must not vanish (05-events §5E.7).
func Stage[T any](w Writer, b *storage.Batch, t Topic[T], key string, payload T, opts ...StageOption) error {
	o := w.o
	if o == nil || w.module == "" {
		return errors.New("outbox: Stage through a zero Writer")
	}
	if b == nil {
		return errors.New("outbox: Stage needs the producer's batch")
	}
	spec, err := o.stageable(w.module, t.name)
	if err != nil {
		return err
	}
	if pt := spec.PayloadType(); pt != nil && pt != reflect.TypeFor[T]() {
		return fmt.Errorf("%w: %s carries %v, staged %v", ErrPayloadType, t.name, pt, reflect.TypeFor[T]())
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: payload of %s does not encode: %w", t.name, err)
	}
	if len(raw) > MaxPayloadBytes {
		return fmt.Errorf("%w: %d bytes of %s, at most %d", ErrPayloadTooLarge, len(raw), t.name, MaxPayloadBytes)
	}
	var c stageConfig
	for _, opt := range opts {
		opt(&c)
	}
	id, err := o.ids.next()
	if err != nil {
		return err
	}
	rec := outboxstore.Intake{
		ID: id, Key: key, Payload: raw, StagedAt: o.now().UTC(), Producer: w.module,
		NotBefore: c.notBefore.UTC(),
	}
	if err := o.store.StageIntake(b, t.name, id, rec); err != nil {
		return err
	}
	o.inst.staged(t.name)
	o.Wake()
	return nil
}

// stageable looks topic up and checks module may stage it.
func (o *Outbox) stageable(module, topic string) (events.TopicSpec, error) {
	spec, ok := o.cat.Lookup(topic)
	if !ok {
		return events.TopicSpec{}, fmt.Errorf("%w: %s", ErrUnknownTopic, topic)
	}
	if spec.Durable == nil {
		return events.TopicSpec{}, fmt.Errorf("%w: %s is bus-only: %w", ErrUnknownTopic, topic, ErrNotDurable)
	}
	if topicOwner(topic) != module {
		return events.TopicSpec{}, fmt.Errorf("%w: %s belongs to %s, not %s", ErrNotOwner, topic, topicOwner(topic), module)
	}
	if !outboxstore.ValidPartitions(outboxstore.Partitions(spec.Durable.Partitions)) {
		return events.TopicSpec{}, fmt.Errorf("%w: %s declares %d", ErrBadPartitions, topic, spec.Durable.Partitions)
	}
	return spec, nil
}

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ulidGen is the one monotonic ULID generator of an Outbox. IDs are strictly
// increasing in call order: the millisecond never goes back (a clock that
// steps backward keeps the last one), and calls inside one millisecond
// increment the 80-bit entropy field instead of drawing a new one. A random
// field that would overflow carries into the next millisecond.
type ulidGen struct {
	now func() time.Time

	mu      sync.Mutex
	lastMs  uint64
	entropy [10]byte
}

// next returns the next ID.
func (g *ulidGen) next() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := uint64(max(g.now().UnixMilli(), 0))
	switch {
	case ms > g.lastMs:
		g.lastMs = ms
		if _, err := rand.Read(g.entropy[:]); err != nil {
			return "", fmt.Errorf("outbox: ULID entropy: %w", err)
		}
		// Clear the top bit: a fresh field then has room to count up through
		// every call a millisecond can make before it would overflow.
		g.entropy[0] &= 0x7f
	default:
		// The same millisecond, or the clock went back: stay on lastMs.
		if !g.incrementEntropy() {
			g.lastMs++
			g.entropy = [10]byte{}
		}
	}
	return encodeULID(g.lastMs, g.entropy), nil
}

// incrementEntropy adds one to the 80-bit field; false when it overflowed.
func (g *ulidGen) incrementEntropy() bool {
	for i := len(g.entropy) - 1; i >= 0; i-- {
		g.entropy[i]++
		if g.entropy[i] != 0 {
			return true
		}
	}
	return false
}

func encodeULID(ms uint64, entropy [10]byte) string {
	var b [16]byte
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(b[:6], ts[2:])
	copy(b[6:], entropy[:])
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// randFloat is the default backoff jitter source: a float in [0, 1).
func randFloat() float64 { return randv2.Float64() }

// maxDuration is the largest time.Duration, the cap of the backoff doubling.
const maxDuration = time.Duration(math.MaxInt64)
