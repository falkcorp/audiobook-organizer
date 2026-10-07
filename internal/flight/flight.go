// file: internal/flight/flight.go
// version: 1.0.0
// guid: 8e4b1d7a-2c6f-4a93-b0d5-7f3e9a1c6b28
// last-edited: 2026-10-06

// Package flight collapses concurrent identical requests into one build and
// cancels that build when the LAST caller waiting on it leaves.
//
// Extracted from internal/server/handlers/audiobooks/list_flight.go (#3817)
// so the library list and the scoped tag facets share one implementation.
// It replaces a plain singleflight.Group whose build ran under
// context.WithoutCancel: a client could send many distinct queries, close each
// connection, and leave every build running detached. Here each caller waits
// on its own request context: a caller that leaves stops waiting and drops its
// reference, and when no caller is left the build's context is cancelled. A
// build that outlives its timeout is cancelled too.
//
// Results, including errors, reach only the callers that were waiting for
// that build; nothing here is cached (callers cache on success themselves).
package flight

import (
	"context"
	"sync"
	"time"
)

// Group shares builds by key. The zero value is ready to use.
type Group[T any] struct {
	mu    sync.Mutex
	calls map[string]*call[T]
}

type call[T any] struct {
	done    chan struct{} // closed when val/err are set
	val     T
	err     error
	waiters int
	cancel  context.CancelFunc
}

// Do returns build's result for key, sharing one build among concurrent
// callers. reqCtx is this caller's request context: when it is done before the
// build finishes, Do returns reqCtx.Err() at once, and the build is cancelled
// if this was the last caller waiting on it. build receives the shared build's
// context: reqCtx's values, the group's own cancellation, and timeout (<= 0
// means no deadline).
func (g *Group[T]) Do(reqCtx context.Context, key string, timeout time.Duration, build func(ctx context.Context) (T, error)) (T, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*call[T])
	}
	c, ok := g.calls[key]
	if !ok {
		var ctx context.Context
		var cancel context.CancelFunc
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(context.WithoutCancel(reqCtx), timeout)
		} else {
			ctx, cancel = context.WithCancel(context.WithoutCancel(reqCtx))
		}
		c = &call[T]{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = c
		go func() {
			val, err := build(ctx)
			g.mu.Lock()
			c.val, c.err = val, err
			if g.calls[key] == c {
				delete(g.calls, key)
			}
			g.mu.Unlock()
			cancel()
			close(c.done)
		}()
	}
	c.waiters++
	g.mu.Unlock()

	select {
	case <-c.done:
		return c.val, c.err
	case <-reqCtx.Done():
		g.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			// Nobody is waiting for this build any more. Cancel it, and take it
			// out of the map so a caller arriving now starts a fresh build
			// instead of joining one that is being torn down.
			c.cancel()
			if g.calls[key] == c {
				delete(g.calls, key)
			}
		}
		g.mu.Unlock()
		var zero T
		return zero, reqCtx.Err()
	}
}

// Waiters reports how many callers wait on key's in-flight build (0 when
// none is in flight). For tests and diagnostics.
func (g *Group[T]) Waiters(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.calls[key]; ok {
		return c.waiters
	}
	return 0
}

// InFlight reports whether a build for key is registered.
func (g *Group[T]) InFlight(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.calls[key]
	return ok
}
