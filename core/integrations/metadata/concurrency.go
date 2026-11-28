// file: core/integrations/metadata/concurrency.go
// version: 0.1.0
// guid: e4a1c7d2-3b58-4096-8f7a-1d92b5c60e43
// last-edited: 2026-11-22

package metadata

import (
	"context"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// maxConcurrent is the bound on searches in flight to ref (0: none): the
// instance's Limits.MaxConcurrent, else its adapter's Meta.Limits, read on every
// ask so an edit takes effect on the next search. A negative value is no bound.
func (c *Chain[Q, R]) maxConcurrent(ref integrations.Ref) int {
	ic, ok := c.reg.Config(ref)
	if !ok {
		return 0
	}
	var base integrations.Limits
	for meta := range c.reg.Adapters(ref.Kind) {
		if meta.ID == ic.Adapter {
			base = meta.Limits
			break
		}
	}
	return max(providerhttp.MergeLimits(base, ic.Limits).MaxConcurrent, 0)
}

// gateOf is ref's in-flight gate, made on first use. A gate is a few words and
// there is one per instance ever asked, so none is dropped.
func (c *Chain[Q, R]) gateOf(ref integrations.Ref) *gate {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.gates[ref]
	if g == nil {
		g = &gate{}
		if c.gates == nil {
			c.gates = map[integrations.Ref]*gate{}
		}
		c.gates[ref] = g
	}
	return g
}

// bounded is a Provider whose Search first takes one of the instance's
// in-flight slots, then runs under timeout. The deadline starts once the slot
// is held: a search that queued behind others has spent none of its provider's
// time, and a bulk fetch with more workers than slots would otherwise turn the
// bound into failures instead of waiting. The wait is bounded by the caller's
// context alone. A cached answer never reaches Search, so it never takes a slot.
type bounded[Q, R any] struct {
	Provider[Q, R]
	g       *gate
	limit   int
	timeout time.Duration
}

func (b bounded[Q, R]) Search(ctx context.Context, q Q) ([]R, error) {
	release, err := b.g.acquire(ctx, b.limit)
	if err != nil {
		return nil, err
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.Provider.Search(cctx, q)
}

// gate bounds the holders of one instance's slots. Its limit is read from the
// instance's current Limits on every acquire, so lowering it lets the holders
// finish and admits no one until they are under the new bound, and raising it
// admits waiters at once. Waiters are admitted in arrival order.
type gate struct {
	mu      sync.Mutex
	held    int
	limit   int // the last limit an acquire passed; <= 0 admits everyone
	waiters []*waiter
}

// waiter is one caller queued for a slot; ch is closed when it holds one.
type waiter struct {
	ch      chan struct{}
	granted bool
}

// acquire takes a slot, waiting while limit others hold them, and returns the
// function that gives it back (safe to call more than once). limit <= 0 takes
// none. It returns ctx.Err() if ctx ends first, holding nothing.
func (g *gate) acquire(ctx context.Context, limit int) (func(), error) {
	g.mu.Lock()
	g.limit = limit
	g.admitLocked() // a raised or lifted bound admits the waiters it now has room for
	if limit <= 0 {
		g.mu.Unlock()
		return func() {}, nil
	}
	// admitLocked has just seated every waiter the bound has room for, so a free
	// slot here means nobody is queued ahead: this caller cannot barge past one.
	if g.held < limit {
		g.held++
		g.mu.Unlock()
		return g.releaser(), nil
	}
	w := &waiter{ch: make(chan struct{})}
	g.waiters = append(g.waiters, w)
	g.mu.Unlock()

	select {
	case <-w.ch:
		return g.releaser(), nil
	case <-ctx.Done():
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.granted { // admitted in the same instant: give the slot straight back
		g.held--
		g.admitLocked()
		return nil, ctx.Err()
	}
	for i, x := range g.waiters {
		if x == w {
			g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
			break
		}
	}
	return nil, ctx.Err()
}

// releaser returns the one-shot function that gives a held slot back.
func (g *gate) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.held--
			g.admitLocked()
			g.mu.Unlock()
		})
	}
}

// admitLocked wakes waiters, oldest first, while the bound has room for them.
func (g *gate) admitLocked() {
	for len(g.waiters) > 0 && (g.limit <= 0 || g.held < g.limit) {
		w := g.waiters[0]
		g.waiters = g.waiters[1:]
		g.held++
		w.granted = true
		close(w.ch)
	}
}
