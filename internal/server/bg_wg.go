// file: internal/server/bg_wg.go
// version: 1.1.1
// guid: cf86ebb1-cc97-4d0d-8a9b-03d5c4faa7c1
// last-edited: 2026-09-02

// namedWaitGroup wraps sync.WaitGroup and keeps a concurrent set of
// goroutine names so that when the 30s shutdown grace period expires we
// can log *which* goroutines are still running rather than just reporting
// an opaque count.
//
// Contract mirrors sync.WaitGroup:
//   - Add(name) increments the counter and registers the name.
//   - Done(name) decrements the counter and removes the name.
//   - Go(name, fn) does both around a new goroutine (preferred).
//   - Wait() blocks until the counter reaches zero.
//   - Running() returns a snapshot of the currently-registered names.
//
// Thread-safe; Add/Done/Running may be called concurrently.

package server

import (
	"sync"
)

// namedWaitGroup is a sync.WaitGroup augmented with name tracking.
type namedWaitGroup struct {
	wg  sync.WaitGroup
	mu  sync.Mutex
	set map[string]int // name → count (>1 if same name used multiple times)
}

// Add registers name and increments the WaitGroup counter by 1.
// Must be called before the associated goroutine is started.
func (n *namedWaitGroup) Add(name string) {
	n.mu.Lock()
	if n.set == nil {
		n.set = make(map[string]int)
	}
	n.set[name]++
	n.mu.Unlock()
	n.wg.Add(1)
}

// Done removes one registration of name and decrements the WaitGroup
// counter. Callers should defer Done(name) inside the goroutine started
// after Add(name).
func (n *namedWaitGroup) Done(name string) {
	n.mu.Lock()
	if n.set[name] <= 1 {
		delete(n.set, name)
	} else {
		n.set[name]--
	}
	n.mu.Unlock()
	n.wg.Done()
}

// Go registers name, starts fn in a new goroutine, and deregisters name
// when fn returns. Mirrors sync.WaitGroup.Go while keeping the name registry
// that lets Shutdown log *which* goroutines outlived the grace period.
//
// The Add is performed synchronously in the calling goroutine, before fn is
// started, so a concurrent Wait() can never miss this registration.
func (n *namedWaitGroup) Go(name string, fn func()) {
	n.Add(name)
	go func() {
		defer n.Done(name)
		fn()
	}()
}

// Wait blocks until the counter reaches zero, identically to sync.WaitGroup.Wait.
func (n *namedWaitGroup) Wait() {
	n.wg.Wait()
}

// Running returns a snapshot slice of currently-registered goroutine names.
// The slice may contain duplicates if the same name was added more than once.
func (n *namedWaitGroup) Running() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.set))
	for name, count := range n.set {
		for range count {
			out = append(out, name)
		}
	}
	return out
}
