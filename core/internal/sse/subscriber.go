// file: core/internal/sse/subscriber.go
// version: 1.2.0
// guid: 4e12e66a-6a25-4459-a95e-5a5525a86b94
// last-edited: 2026-11-12

package sse

import (
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"
)

// interruptGrace is how long a write in flight is given once its stream is
// ended: long enough for a healthy write to finish, short enough that a stuck
// one does not hold the shutdown.
const interruptGrace = time.Second

// Subscriber is one stream's registration: its interest, its bounded queue and
// its wake channel. All queue state is guarded by mu; the broker takes mu only
// inside its own lock (broker.mu, then s.mu, never the reverse).
type Subscriber struct {
	b      *Broker
	bucket string
	key    string
	judge  Judge
	meta   any

	wake chan struct{} // capacity 1: something is queued, ended or reset

	mu sync.Mutex
	// interest is the topic set the broker queues for. It starts as the
	// Subscribe set and only SetInterest replaces it (always with a new map,
	// under mu). It is a superset of what the judge delivers: the judge still
	// decides each entry at send time.
	interest map[string]struct{}
	replay   []*Entry // the ring's wanted entries after the resume id; drained first
	rpos     int
	q        []*Entry // circular, len ClientBuffer
	qh, qn   int
	reset    string // pending core.stream.reset.v1 reason, "" none
	ended    string // pending close reason, "" none; the first reason wins

	// wmu serialises the stream's write deadline between its writer (write) and
	// interrupt. It is never held across a write and never taken by offer or
	// Publish, so a ResponseController that is slow to take a deadline cannot
	// hold up a publisher.
	wmu     sync.Mutex
	rc      *http.ResponseController // set by Serve before its first write
	writing bool                     // a non-final write holds a deadline right now

	released sync.Once
}

// wants is the subscriber's interest: its topic set and its key. The caller
// holds s.mu, or s is not yet shared (Subscribe's replay snapshot).
func (s *Subscriber) wants(e *Entry) bool {
	if _, ok := s.interest[e.Topic]; !ok {
		return false
	}
	return s.key == "" || e.Key == s.key
}

// offer queues e if the subscriber wants it and returns how many queued
// entries it dropped to do so. It never blocks. A full queue is dropped whole
// (the replay too), a reset(overflow) is owed, and e starts the fresh queue:
// the subscriber continues live and the client refetches over REST.
func (s *Subscriber) offer(e *Entry) (dropped int) {
	s.mu.Lock()
	if s.ended != "" || !s.wants(e) {
		s.mu.Unlock()
		return 0
	}
	if s.qn == len(s.q) {
		dropped = s.qn + (len(s.replay) - s.rpos)
		clear(s.q)
		s.qh, s.qn = 0, 0
		s.replay, s.rpos = nil, 0
		if s.reset == "" {
			s.reset = ResetOverflow
		}
	}
	s.q[(s.qh+s.qn)%len(s.q)] = e
	s.qn++
	s.mu.Unlock()
	s.signal()
	return dropped
}

// signal wakes the stream without blocking.
func (s *Subscriber) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// end records the reason the stream must stop and wakes it; the first reason
// wins. It reports whether this call set it. It touches only the queue state,
// so it is safe under the broker's lock; the caller then calls interrupt, after
// it has released that lock.
func (s *Subscriber) end(reason string) bool {
	s.mu.Lock()
	first := s.ended == ""
	if first {
		s.ended = reason
	}
	s.mu.Unlock()
	if first {
		s.signal()
	}
	return first
}

// interrupt cuts short a write that is in flight when the stream has been
// ended, so a client that stopped reading cannot hold a shutdown or a
// revocation for a whole WriteTimeout. It moves that write's deadline to a
// short grace in the FUTURE (never to the past: on HTTP/2 a deadline in the
// past resets the stream at once, and the closed frame could no longer be
// sent). A stream with no write in flight is left alone: it is idle and its
// final frame goes out normally. A ResponseController that cannot set a
// deadline leaves the write bounded by WriteTimeout (ErrNotSupported is
// tolerated everywhere, as a test recorder has no deadlines); any other failure
// is reported through Hooks.InterruptFailed.
func (s *Subscriber) interrupt() {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if !s.writing || s.rc == nil {
		return
	}
	grace := min(s.b.opt.WriteTimeout, interruptGrace)
	if err := s.rc.SetWriteDeadline(time.Now().Add(grace)); err != nil && !errors.Is(err, http.ErrNotSupported) && s.b.opt.Hooks.InterruptFailed != nil {
		s.b.opt.Hooks.InterruptFailed(err)
	}
}

// endedReason is the reason the stream was ended with, "" if it was not.
func (s *Subscriber) endedReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// SetInterest replaces the topic set the broker queues for. The judge calls it
// when the stream's rights narrow, so entries of a topic the stream may no
// longer see stop filling its queue (and counting toward its overflow). A key
// narrowing is unchanged. Entries already queued are still judged at send.
func (s *Subscriber) SetInterest(topics []string) {
	m := make(map[string]struct{}, len(topics))
	for _, t := range topics {
		m[t] = struct{}{}
	}
	s.mu.Lock()
	s.interest = m
	s.mu.Unlock()
}

// Topics is the sorted current interest set.
func (s *Subscriber) Topics() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.interest))
}

// RequestReset owes the stream a core.stream.reset.v1 frame with reason, sent
// before its next entry. The judge uses it after it has withheld events it
// cannot replay (the client must refetch). A reset already owed keeps its reason.
func (s *Subscriber) RequestReset(reason string) {
	s.mu.Lock()
	if s.reset == "" {
		s.reset = reason
	}
	s.mu.Unlock()
	s.signal()
}

// take removes up to limit pending entries, the replay first and then the live
// queue, and returns them with the pending reset and end reasons. more reports
// that entries remain.
func (s *Subscriber) take(limit int) (reset, end string, ents []*Entry, more bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended != "" {
		return "", s.ended, nil, false
	}
	reset, s.reset = s.reset, ""
	for len(ents) < limit && s.rpos < len(s.replay) {
		ents = append(ents, s.replay[s.rpos])
		s.replay[s.rpos] = nil
		s.rpos++
	}
	if s.rpos == len(s.replay) {
		s.replay, s.rpos = nil, 0
	}
	for len(ents) < limit && s.qn > 0 {
		ents = append(ents, s.q[s.qh])
		s.q[s.qh] = nil
		s.qh = (s.qh + 1) % len(s.q)
		s.qn--
	}
	return reset, "", ents, s.qn > 0 || s.rpos < len(s.replay)
}

// Release unregisters the subscriber. It is idempotent, and Serve calls it, so
// only a caller that never serves needs it.
func (s *Subscriber) Release() {
	s.released.Do(func() {
		b := s.b
		b.mu.Lock()
		delete(b.subs, s)
		if s.bucket != "" {
			if b.buckets[s.bucket]--; b.buckets[s.bucket] <= 0 {
				delete(b.buckets, s.bucket)
			}
		}
		b.signalIdleLocked()
		b.mu.Unlock()
	})
}
