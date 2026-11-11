// file: core/internal/executor/budget.go
// version: 1.0.0
// guid: fb03fd2e-e7ba-44c0-bdf6-eb8ecbecbff7
// last-edited: 2026-11-10

package executor

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// cpuBudget is the executor's core.tasks.cpu_slots semaphore (5T.3.2): one
// slot per item of a CPU() loop, shared by every run of this executor.
// Resizing applies to new acquisitions; a shrink never revokes a held slot.
type cpuBudget struct {
	mu      sync.Mutex
	limit   int
	inUse   int
	changed chan struct{}
}

func newCPUBudget(n int) *cpuBudget {
	return &cpuBudget{limit: max(n, 1), changed: make(chan struct{})}
}

func (c *cpuBudget) broadcastLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// acquire takes one slot, waiting while none is free; ctx ends the wait.
func (c *cpuBudget) acquire(ctx context.Context) (func(), error) {
	for {
		c.mu.Lock()
		if c.inUse < c.limit {
			c.inUse++
			c.mu.Unlock()
			return sync.OnceFunc(func() {
				c.mu.Lock()
				c.inUse--
				c.broadcastLocked()
				c.mu.Unlock()
			}), nil
		}
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

func (c *cpuBudget) resize(n int) {
	c.mu.Lock()
	c.limit = max(n, 1)
	c.broadcastLocked()
	c.mu.Unlock()
}

func (c *cpuBudget) used() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inUse
}

// refTable is the executor's one ref-lock table (5T.3.4, COR-7): rc.LockRefs,
// ic.LockRefs, tasks.LockRefs with an RC context, Batch ExclusiveRefs and
// Fixer Row.Subjects all take it. Acquisition is all-or-wait over the refs
// sorted by FormatRef: a caller holds every ref it asked for or none, so no
// two callers can each hold part of what the other waits for (no
// lock-order deadlock). Overlap is tasks.Overlaps: a kind-wide ref conflicts
// with every ID of its kind.
type refTable struct {
	mu      sync.Mutex
	held    map[uint64][]entity.Ref
	next    uint64
	changed chan struct{}
}

func newRefTable() *refTable {
	return &refTable{held: map[uint64][]entity.Ref{}, changed: make(chan struct{})}
}

// sortRefs returns refs deduplicated and sorted by FormatRef.
func sortRefs(refs []entity.Ref) []entity.Ref {
	out := slices.Clone(refs)
	slices.SortFunc(out, func(a, b entity.Ref) int { return cmp.Compare(tasks.FormatRef(a), tasks.FormatRef(b)) })
	return slices.Compact(out)
}

func (t *refTable) conflictLocked(refs []entity.Ref) bool {
	for _, set := range t.held {
		for _, h := range set {
			for _, r := range refs {
				if tasks.Overlaps(h, r) {
					return true
				}
			}
		}
	}
	return false
}

// lock takes every ref or waits; ctx ends the wait with its cause. The
// release is idempotent.
func (t *refTable) lock(ctx context.Context, refs []entity.Ref) (func(), error) {
	refs = sortRefs(refs)
	if len(refs) == 0 {
		return func() {}, nil
	}
	for {
		t.mu.Lock()
		if !t.conflictLocked(refs) {
			t.next++
			tok := t.next
			t.held[tok] = refs
			t.mu.Unlock()
			return sync.OnceFunc(func() {
				t.mu.Lock()
				delete(t.held, tok)
				close(t.changed)
				t.changed = make(chan struct{})
				t.mu.Unlock()
			}), nil
		}
		ch := t.changed
		t.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// lane is one registered lane's lease counter (5T.3.9).
type lane struct {
	l       tasks.Lane
	mu      sync.Mutex
	inUse   int
	changed chan struct{}
}

func newLane(l tasks.Lane) *lane { return &lane{l: l, changed: make(chan struct{})} }

// lanePoll re-reads a lane's capacity while a lease waits (capacity is a
// live value, e.g. connected remote workers, with no change signal).
const lanePoll = time.Second

func (ln *lane) capacity() int {
	if ln.l.Capacity == nil {
		return 0
	}
	return max(ln.l.Capacity(), 0)
}

// acquire leases one unit of the lane. With no capacity at all it returns
// ErrNoLaneCapacity unless NoLocalFallback, in which case it waits; a full
// lane waits. ctx ends the wait.
func (ln *lane) acquire(ctx context.Context) (tasks.LaneLease, error) {
	for {
		limit := ln.capacity()
		ln.mu.Lock()
		if ln.inUse < limit {
			ln.inUse++
			ln.mu.Unlock()
			lctx, cancel := context.WithCancel(ctx)
			return &laneLease{ctx: lctx, release: sync.OnceFunc(func() {
				cancel()
				ln.mu.Lock()
				ln.inUse--
				close(ln.changed)
				ln.changed = make(chan struct{})
				ln.mu.Unlock()
			})}, nil
		}
		if limit == 0 && !ln.l.NoLocalFallback {
			ln.mu.Unlock()
			return nil, ErrNoLaneCapacity
		}
		ch := ln.changed
		ln.mu.Unlock()
		t := time.NewTimer(lanePoll)
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, context.Cause(ctx)
		}
		t.Stop()
	}
}

func (ln *lane) used() int {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	return ln.inUse
}

type laneLease struct {
	ctx     context.Context
	release func()
}

func (l *laneLease) Release()                 { l.release() }
func (l *laneLease) Context() context.Context { return l.ctx }
