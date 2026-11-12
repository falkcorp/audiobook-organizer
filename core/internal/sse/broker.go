// file: core/internal/sse/broker.go
// version: 1.1.0
// guid: 9889a9af-4387-41da-9d7a-db513446cc95
// last-edited: 2026-11-12

// Package sse is the engine under core's realtime hub (05-realtime §3, §4.2,
// §4.3, §4.5, §7): a numbered ring for Last-Event-ID resume, one bounded queue
// and one wake channel per subscriber, overflow that drops what is queued and
// says reset, a per-write deadline, a heartbeat, and the frame writer. It is
// the algorithm of HEAD's v3 ops broker (internal/server/handlers/opsv3/
// events.go), generalised from runs to opaque entries.
//
// The package knows nothing about topics' permissions, redaction or the
// principal. The hub (core/realtime) hands each subscriber a Judge, and the
// stream loop asks it about every entry at SEND time, whether the entry is live
// or replayed from the ring, so there is exactly one filter path (05-realtime
// §4.4).
//
// Bounds, all fixed at NewBroker: the ring holds RingSize entries; a
// subscriber's queue holds ClientBuffer entries and its replay at most
// RingSize; the broker holds MaxClients subscribers and MaxPerBucket of one
// bucket; a Last-Event-ID longer than maxIDLen is malformed. Publish does
// O(subscribers) non-blocking work under one mutex and starts no goroutine; a
// stream runs on its caller's goroutine (the HTTP handler's) and on no other.
//
// Layer L6 (03 R4): it implements realtime's stream, so it takes realtime's
// layer. It holds no package-level mutable state (R5).
package sse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors Subscribe returns. The hub maps them to its own sentinels.
var (
	ErrTooManyClients = errors.New("sse: too many streams")
	ErrTooManyBucket  = errors.New("sse: too many streams in one bucket")
	ErrClosed         = errors.New("sse: the broker is closed")
)

// Reset reasons (05-realtime §4.2): the payload of core.stream.reset.v1.
const (
	ResetRestart  = "restart"  // the id carries another process's nonce
	ResetGap      = "gap"      // the id is malformed, or older than the ring
	ResetOverflow = "overflow" // the subscriber fell behind its queue
)

// Close reasons that go on the wire in core.stream.closed.v1, and the two that
// never do (the connection is already gone).
const (
	CloseShutdown          = "shutdown"
	CloseCredentialRevoked = "credential_revoked"
	ClosePermissionRevoked = "permission_revoked"
	CloseClient            = "client"        // the request's context ended or a write failed
	CloseWriteTimeout      = "write_timeout" // a write passed its deadline
)

// maxIDLen bounds a Last-Event-ID: a 16-hex-digit nonce, '-', and a uint64's
// 20 digits, with room for nothing else. A longer value is malformed without
// being parsed.
const maxIDLen = 16 + 1 + 20

// Entry is one published event. Seq is hub-wide and assigned by Publish; Item
// is the hub's own payload and is never modified after Publish, so every
// subscriber may read it at once.
type Entry struct {
	Seq   uint64
	Topic string
	Key   string
	At    time.Time
	Item  any
}

// Hooks are the broker's counters. Each is optional and must not block: they
// run on the publisher's or the stream's goroutine.
type Hooks struct {
	Reset   func(reason string) // a core.stream.reset.v1 frame was written
	Dropped func(n int)         // n queued entries were dropped on overflow
	Closed  func(reason string) // a stream ended
	// InterruptFailed is called when ending a stream could not move its write
	// deadline to now (so a write in flight runs to its own deadline); it runs
	// on the goroutine that ended the stream, under the broker's lock.
	InterruptFailed func(err error)
}

// Options configure NewBroker. Every size must be positive.
type Options struct {
	RingSize     int           // resumable window, entries
	ClientBuffer int           // per-subscriber live queue, entries
	MaxClients   int           // open streams
	MaxPerBucket int           // open streams of one non-empty bucket
	Heartbeat    time.Duration // comment-line interval and revalidation tick
	WriteTimeout time.Duration // per-write deadline
	Now          func() time.Time
	// Nonce is the boot nonce; empty draws 8 random bytes, hex encoded. Tests
	// set it to pin ids.
	Nonce string
	Hooks Hooks
}

func (o Options) validate() error {
	for _, c := range []struct {
		name string
		v    int64
	}{
		{"RingSize", int64(o.RingSize)}, {"ClientBuffer", int64(o.ClientBuffer)},
		{"MaxClients", int64(o.MaxClients)}, {"MaxPerBucket", int64(o.MaxPerBucket)},
		{"Heartbeat", int64(o.Heartbeat)}, {"WriteTimeout", int64(o.WriteTimeout)},
	} {
		if c.v <= 0 {
			return fmt.Errorf("sse: Options.%s must be positive, got %d", c.name, c.v)
		}
	}
	return nil
}

// Broker numbers published entries, keeps the last RingSize of them and fans
// them out to subscribers without ever blocking the publisher.
type Broker struct {
	opt   Options
	nonce string
	now   func() time.Time

	mu      sync.Mutex
	seq     uint64
	ring    []*Entry // circular, len RingSize
	head, n int      // index of the oldest entry, entries held
	subs    map[*Subscriber]struct{}
	buckets map[string]int
	closed  bool
	idle    chan struct{} // closed when the broker is closed and has no subscriber left
	idleSet bool
}

// NewBroker validates o and builds a Broker.
func NewBroker(o Options) (*Broker, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	nonce := o.Nonce
	if nonce == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, fmt.Errorf("sse: boot nonce: %w", err)
		}
		nonce = hex.EncodeToString(b[:])
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Broker{
		opt: o, nonce: nonce, now: now,
		ring: make([]*Entry, o.RingSize), subs: map[*Subscriber]struct{}{}, buckets: map[string]int{},
		idle: make(chan struct{}),
	}, nil
}

// Nonce is this process's boot nonce, the first half of every event id.
func (b *Broker) Nonce() string { return b.nonce }

// eventID is the SSE id of seq: "<bootNonce>-<seq>".
func (b *Broker) eventID(seq uint64) string { return b.nonce + "-" + strconv.FormatUint(seq, 10) }

// Publish numbers e, appends it to the ring and offers it to every subscriber
// that wants it, then returns its seq. It never blocks and starts no
// goroutine. On a closed broker it does nothing and returns 0.
func (b *Broker) Publish(e Entry) uint64 {
	e.At = b.now()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0
	}
	b.seq++
	e.Seq = b.seq
	ent := &e
	if b.n == len(b.ring) {
		b.ring[b.head] = ent
		b.head = (b.head + 1) % len(b.ring)
	} else {
		b.ring[(b.head+b.n)%len(b.ring)] = ent
		b.n++
	}
	dropped := 0
	for s := range b.subs {
		dropped += s.offer(ent)
	}
	b.mu.Unlock()
	if dropped > 0 && b.opt.Hooks.Dropped != nil {
		b.opt.Hooks.Dropped(dropped)
	}
	return ent.Seq
}

// Subscription is what a stream asks for.
type Subscription struct {
	// Bucket is the cap key (the hub's user and credential class). Empty
	// opts out of MaxPerBucket; MaxClients still bounds it.
	Bucket string
	// Topics is the interest set: only entries of these topics are ever
	// queued for the subscriber. It is also the list core.stream.opened.v1
	// reports. It is copied.
	Topics []string
	// Key narrows to one entity ("" = all).
	Key string
	// LastEventID is the raw resume id ("" = none).
	LastEventID string
	// Judge decides each entry at send time and watches the credential.
	Judge Judge
	// Meta is the caller's handle, handed back by End's matcher.
	Meta any
}

// Judge is the per-subscriber policy the stream loop consults. Both methods
// run on the stream's own goroutine and must be quick.
type Judge interface {
	// Deliver decides one entry at send time, live or replayed alike: the
	// JSON for the frame's data line, or ok=false to withhold it.
	Deliver(ctx context.Context, e *Entry) (data []byte, ok bool)
	// Check runs on every wake and, with beat set, on every heartbeat (the
	// revalidation point). A non-empty return ends the stream with that reason.
	Check(ctx context.Context, beat bool) (closeReason string)
}

// parseID reads a resume id this broker could have issued. status is ""
// (resume from seq), ResetRestart (another process's nonce) or ResetGap
// (malformed).
func (b *Broker) parseID(raw string) (seq uint64, status string) {
	if len(raw) > maxIDLen {
		return 0, ResetGap
	}
	nonce, rest, found := strings.Cut(raw, "-")
	if !found || rest == "" {
		return 0, ResetGap
	}
	if nonce != b.nonce {
		return 0, ResetRestart
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return 0, ResetGap
		}
	}
	n, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return 0, ResetGap
	}
	return n, ""
}

// Subscribe registers a subscriber, refusing past MaxClients or the bucket's
// MaxPerBucket. A resume id from this process whose seq is still in the ring
// queues the ring's wanted entries after it (they are judged when sent, like
// live ones); any other id is told to reset. The snapshot and the registration
// happen under one lock, so no entry is replayed twice or missed.
func (b *Broker) Subscribe(req Subscription) (*Subscriber, error) {
	s := &Subscriber{
		b: b, bucket: req.Bucket, key: req.Key, judge: req.Judge, meta: req.Meta,
		interest: make(map[string]struct{}, len(req.Topics)),
		q:        make([]*Entry, b.opt.ClientBuffer),
		wake:     make(chan struct{}, 1),
	}
	for _, t := range req.Topics {
		s.interest[t] = struct{}{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.closed:
		return nil, ErrClosed
	case len(b.subs) >= b.opt.MaxClients:
		return nil, ErrTooManyClients
	case req.Bucket != "" && b.buckets[req.Bucket] >= b.opt.MaxPerBucket:
		return nil, ErrTooManyBucket
	}
	if req.LastEventID != "" {
		last, status := b.parseID(req.LastEventID)
		oldest := b.seq + 1
		if b.n > 0 {
			oldest = b.ring[b.head].Seq
		}
		switch {
		case status != "":
			s.reset = status
		case last > b.seq, last+1 < oldest:
			s.reset = ResetGap
		default:
			for i := range b.n {
				if e := b.ring[(b.head+i)%len(b.ring)]; e.Seq > last && s.wants(e) {
					s.replay = append(s.replay, e)
				}
			}
		}
	}
	b.subs[s] = struct{}{}
	if req.Bucket != "" {
		b.buckets[req.Bucket]++
	}
	return s, nil
}

// End ends every subscriber whose Meta match accepts, with reason, and returns
// how many it signalled. The streams write core.stream.closed.v1 and return on
// their own goroutines.
func (b *Broker) End(match func(meta any) bool, reason string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for s := range b.subs {
		if match(s.meta) && s.end(reason) {
			n++
		}
	}
	return n
}

// WakeAll wakes every subscriber so each runs its judge's Check on its own
// goroutine now (the hub does it after an rbac generation change).
func (b *Broker) WakeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		s.signal()
	}
}

// Len is the number of open subscribers.
func (b *Broker) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// OldestAge is how long the oldest ring entry has been held, 0 when empty.
func (b *Broker) OldestAge() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.n == 0 {
		return 0
	}
	return b.now().Sub(b.ring[b.head].At)
}

// Close refuses new subscribers and ends every open one with CloseShutdown.
// It is idempotent. Wait reports when the last stream has returned.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for s := range b.subs {
		s.end(CloseShutdown)
	}
	b.signalIdleLocked()
}

// Wait returns when the broker is closed and its last subscriber has left, or
// with ctx's error.
func (b *Broker) Wait(ctx context.Context) error {
	select {
	case <-b.idle:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sse: %d stream(s) still open: %w", b.Len(), ctx.Err())
	}
}

// signalIdleLocked closes idle once the broker is closed and empty.
func (b *Broker) signalIdleLocked() {
	if b.closed && len(b.subs) == 0 && !b.idleSet {
		b.idleSet = true
		close(b.idle)
	}
}
