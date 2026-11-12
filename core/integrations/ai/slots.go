// file: core/integrations/ai/slots.go
// version: 1.0.0
// guid: a4e07c19-2b68-4d35-8f91-c6d3e5b7a024
// last-edited: 2026-11-12

package ai

import (
	"context"
	"sync"

	"github.com/falkcorp/mediacore/integrations"
)

// slots is the router's in-flight accounting: a hard cap per endpoint and an
// optional pool-wide cap per kind (core.ai.kind_limits.<kind>, the production
// setter HEAD lacks for whisper_max_in_flight).
//
// The cap has to live at the last shared choke point, the request itself, in
// state that outlives any one call: callers dispatch independently, so N
// callers would otherwise produce N concurrent requests at one server no
// matter what the operator configured. The router owns it (no package global),
// and every call through the router contends for the same slots.
//
// The limits are read at acquisition, so a changed concurrency or kind limit
// takes effect on the next acquire; HEAD kept the established cap until
// restart because its pools were fixed-size channels. A kind limit below 1
// means unlimited; an endpoint limit below 1 is refused before selection
// (EndpointSettings.Concurrency is at least 1).
//
// Waiters sleep on a broadcast channel that every release replaces, so an
// acquire honours its context and a release never strands a waiter.
type slots struct {
	mu      sync.Mutex
	used    map[integrations.Ref]int
	byKind  map[Kind]int
	changed chan struct{}
}

func newSlots() *slots {
	return &slots{used: map[integrations.Ref]int{}, byKind: map[Kind]int{}, changed: make(chan struct{})}
}

// tryLocked takes a slot of ref and kind if both caps have room. s.mu is held.
func (s *slots) tryLocked(ref integrations.Ref, limit int, kind Kind, kindLimit int) bool {
	if s.used[ref] >= limit {
		return false
	}
	if kindLimit >= 1 && s.byKind[kind] >= kindLimit {
		return false
	}
	s.used[ref]++
	s.byKind[kind]++
	return true
}

// releaser returns the release of one taken slot, safe to call more than once.
func (s *slots) releaser(ref integrations.Ref, kind Kind) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.used[ref]--
			s.byKind[kind]--
			if s.used[ref] <= 0 {
				delete(s.used, ref)
			}
			if s.byKind[kind] <= 0 {
				delete(s.byKind, kind)
			}
			close(s.changed)
			s.changed = make(chan struct{})
			s.mu.Unlock()
		})
	}
}

// tryAcquire takes a slot only if ref and the kind both have one free right
// now. It is what lets a call fill FREE slots across endpoints instead of
// queueing on the preferred endpoint while a peer sits idle.
func (s *slots) tryAcquire(ref integrations.Ref, limit int, kind Kind, kindLimit int) (func(), bool) {
	s.mu.Lock()
	ok := s.tryLocked(ref, limit, kind, kindLimit)
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	return s.releaser(ref, kind), true
}

// acquire blocks until ref has a free slot (and the kind has room) or ctx is
// done; the error is ctx's.
func (s *slots) acquire(ctx context.Context, ref integrations.Ref, limit int, kind Kind, kindLimit int) (func(), error) {
	for {
		s.mu.Lock()
		if s.tryLocked(ref, limit, kind, kindLimit) {
			s.mu.Unlock()
			return s.releaser(ref, kind), nil
		}
		wait := s.changed
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// depth is how many slots of ref are held now.
func (s *slots) depth(ref integrations.Ref) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used[ref]
}
