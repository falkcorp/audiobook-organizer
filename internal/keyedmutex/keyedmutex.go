// file: internal/keyedmutex/keyedmutex.go
// version: 1.0.0
// guid: 4a9c27e1-58b3-4d06-9f12-c07e6b3d85a9
// last-edited: 2026-10-10

// Package keyedmutex provides a set of mutexes, one per string key.
//
// It was internal/deluge's private keyedMutex (the per-destination import
// lock) until 2026-10-10, when internal/versions needed the same thing for
// its per-torrent link lock; one implementation now serves both.
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
