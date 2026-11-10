// file: core/internal/keyedmutex/keyedmutex.go
// version: 1.0.0
// guid: bbba076f-2a8e-4141-9e1f-0a117bdec155
// last-edited: 2026-11-10

// Package keyedmutex provides a set of mutexes, one per string key.
//
// It is core's own copy of ABO's internal/keyedmutex (04 §2.3, §8.1 F3;
// 05-config §2.6): core cannot import ABO, and ABO keeps its package. The
// secrets store serializes Put, Stage and Delete per secret name through it.
//
// Layer L0 (03 R4): it imports the standard library only.
package keyedmutex

import "sync"

// Mutex is a set of mutexes, one per key, created on first use and dropped
// when nobody holds or waits on it, so a long run over many keys does not
// leave one map entry per key behind. The zero value is ready to use. It is
// process-local: it serializes goroutines in this process only.
type Mutex struct {
	mu    sync.Mutex
	locks map[string]*refMutex
}

type refMutex struct {
	sync.Mutex
	refs int // holders + waiters; guarded by Mutex.mu
}

// Lock blocks until key is free and returns the matching unlock, which must
// be called exactly once. It is not reentrant: locking a key the calling
// goroutine already holds deadlocks.
func (k *Mutex) Lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*refMutex)
	}
	m := k.locks[key]
	if m == nil {
		m = &refMutex{}
		k.locks[key] = m
	}
	m.refs++
	k.mu.Unlock()

	m.Lock()
	return func() {
		m.Unlock()
		k.mu.Lock()
		m.refs--
		if m.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
