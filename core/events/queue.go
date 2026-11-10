// file: core/events/queue.go
// version: 1.0.0
// guid: 3fa2267c-9b55-463e-b3bd-292034ecb3d7
// last-edited: 2026-11-10

package events

import (
	"context"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// The drop reasons of mediacore_events_dropped_total (05-events §5E.10).
const (
	reasonQueueFull = "queue_full"
	reasonEncode    = "encode" // the JSON form failed to encode or its schema, or a typed payload of another type
	reasonClosed    = "closed"
)

// warnEvery bounds the drop and panic logs: at most one per subscriber per
// interval (§5E.5.1).
const warnEvery = time.Minute

// sub is one subscription at run time: one bounded FIFO and one goroutine
// (§5E.5.1). Publish does a non-blocking enqueue; the goroutine started by
// Open delivers in queue order.
type sub struct {
	b     *Bus
	name  string
	topic string
	raw   bool
	ts    *topicState
	cfg   subConfig
	call0 func(ctx context.Context, ev *event) string
	attrs subAttrs

	mu      sync.Mutex
	buf     []*event // ring buffer of len = queue size
	head, n int
	closing bool

	signal    chan struct{} // cap 1: something was queued
	stop      chan struct{} // closed by beginClose
	inHandler atomic.Bool
	lastWarn  atomic.Int64 // unix nanos of the last drop warning
	lastPanic atomic.Int64 // unix nanos of the last panic log
}

func newSub(b *Bus, s Subscription, ts *topicState) *sub {
	size := s.cfg.queueSize
	if size == 0 {
		size = b.queueSize
	}
	return &sub{
		b: b, name: s.name, topic: s.topic, raw: s.raw, ts: ts, cfg: s.cfg, call0: s.deliver,
		attrs: newSubAttrs(s.topic, s.name),
		buf:   make([]*event, size), signal: make(chan struct{}, 1), stop: make(chan struct{}),
	}
}

// enqueue is Publish's non-blocking send: a full queue drops the incoming
// event (DropNewest) or the oldest queued one (DropOldest), counted.
func (s *sub) enqueue(ev *event) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		s.b.inst.dropped(s, reasonClosed)
		return
	}
	evicted := false
	if s.n == len(s.buf) {
		if !s.cfg.dropOldest {
			s.mu.Unlock()
			s.drop(reasonQueueFull, "")
			return
		}
		s.buf[s.head] = nil
		s.head = (s.head + 1) % len(s.buf)
		s.n--
		evicted = true
	}
	s.buf[(s.head+s.n)%len(s.buf)] = ev
	s.n++
	s.mu.Unlock()
	if evicted {
		s.drop(reasonQueueFull, "")
	}
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// pop takes the oldest queued event, or nil; closing reports Close began.
func (s *sub) pop() (ev *event, closing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == 0 {
		return nil, s.closing
	}
	ev = s.buf[s.head]
	s.buf[s.head] = nil
	s.head = (s.head + 1) % len(s.buf)
	s.n--
	return ev, s.closing
}

// depth is the number of queued events (the queue_depth gauge).
func (s *sub) depth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// beginClose refuses further events and wakes the goroutine to drain.
func (s *sub) beginClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closing {
		s.closing = true
		close(s.stop)
	}
}

// dropQueued counts and discards every queued event (reason "closed").
func (s *sub) dropQueued() int {
	s.mu.Lock()
	n := s.n
	for i := range s.buf {
		s.buf[i] = nil
	}
	s.head, s.n, s.closing = 0, 0, true
	s.mu.Unlock()
	for range n {
		s.b.inst.dropped(s, reasonClosed)
	}
	return n
}

// drop counts one dropped event and warns at most once per minute. The
// warning carries the subscriber, the topic, the reason and, for an encode
// drop, a fixed cause word: never an error's text or a payload value.
func (s *sub) drop(reason, cause string) {
	s.b.inst.dropped(s, reason)
	now := time.Now().UnixNano()
	last := s.lastWarn.Load()
	if now-last < int64(warnEvery) && last != 0 {
		return
	}
	if !s.lastWarn.CompareAndSwap(last, now) {
		return
	}
	args := []any{"subscriber", s.name, "topic", s.topic, "reason", reason, "queue_size", len(s.buf)}
	if cause != "" {
		args = append(args, "cause", cause)
	}
	s.b.log.Warn("events: dropping events for a subscriber (counted; at most one warning per minute)", args...)
}

// pending is one coalesced key awaiting its window's end.
type pending struct {
	ev  *event
	due time.Time
}

// coalescer holds the latest event per key in arrival order; every key's
// window has the same length, so arrival order is due order.
type coalescer struct {
	order []string
	m     map[string]*pending
}

func (c *coalescer) add(ev *event, window time.Duration) {
	if p, ok := c.m[ev.key]; ok {
		p.ev = ev // latest wins
		return
	}
	if c.m == nil {
		c.m = map[string]*pending{}
	}
	c.m[ev.key] = &pending{ev: ev, due: time.Now().Add(window)}
	c.order = append(c.order, ev.key)
}

// due removes and returns the events whose window ended (all when all).
func (c *coalescer) due(all bool) []*event {
	now := time.Now()
	var out []*event
	i := 0
	for ; i < len(c.order); i++ {
		p := c.m[c.order[i]]
		if !all && now.Before(p.due) {
			break
		}
		out = append(out, p.ev)
		delete(c.m, c.order[i])
	}
	c.order = c.order[i:]
	return out
}

// next is the earliest pending deadline.
func (c *coalescer) next() (time.Time, bool) {
	if len(c.order) == 0 {
		return time.Time{}, false
	}
	return c.m[c.order[0]].due, true
}

// run is the subscription's one goroutine: it delivers queued events in
// order, holds coalesced keys until their window ends, and on Close drains
// the queue (and every held key) until the bus context is canceled.
func (s *sub) run(ctx context.Context) {
	var co coalescer
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		for range co.due(true) {
			s.b.inst.dropped(s, reasonClosed)
		}
		if s.b.running.Add(-1) == 0 {
			close(s.b.allDone)
		}
	}()
	for {
		closing := false
		for {
			ev, c := s.pop()
			closing = c
			if ev == nil {
				break
			}
			if ctx.Err() != nil {
				s.b.inst.dropped(s, reasonClosed)
				continue
			}
			if s.cfg.coalesce > 0 && ev.key != "" && !closing {
				co.add(ev, s.cfg.coalesce)
				continue
			}
			s.call(ctx, ev)
		}
		if ctx.Err() != nil {
			return
		}
		for _, ev := range co.due(closing) {
			s.call(ctx, ev)
		}
		if closing {
			if s.depth() == 0 && len(co.order) == 0 {
				return
			}
			continue
		}
		var timerC <-chan time.Time
		if at, ok := co.next(); ok {
			if timer == nil {
				timer = time.NewTimer(time.Until(at))
			} else {
				timer.Reset(time.Until(at))
			}
			timerC = timer.C
		}
		select {
		case <-s.signal:
		case <-timerC:
		case <-s.stop:
		case <-ctx.Done():
		}
	}
}

// call runs the handler once, isolated: a panic is recovered, counted and
// logged with its stack at most once per minute, and the subscription keeps
// running (§5E.5.1 "Panic isolation").
func (s *sub) call(ctx context.Context, ev *event) {
	s.inHandler.Store(true)
	start := time.Now()
	reason := s.invoke(ctx, ev)
	s.inHandler.Store(false)
	if reason != "" {
		cause := ""
		if reason == reasonEncode {
			cause = causePayloadType
			if s.raw {
				_, cause = ev.json()
			}
		}
		s.drop(reason, cause)
		return
	}
	s.b.inst.delivered(s, time.Since(start))
}

func (s *sub) invoke(ctx context.Context, ev *event) (reason string) {
	defer func() {
		if r := recover(); r != nil {
			s.b.inst.panicked(s)
			now := time.Now().UnixNano()
			last := s.lastPanic.Load()
			if (last == 0 || now-last >= int64(warnEvery)) && s.lastPanic.CompareAndSwap(last, now) {
				s.b.log.Error("events: subscriber panicked (recovered; the subscription keeps running)",
					"subscriber", s.name, "topic", s.topic, "panic", r, "stack", string(debug.Stack()))
			}
		}
	}()
	return s.call0(ctx, ev)
}
