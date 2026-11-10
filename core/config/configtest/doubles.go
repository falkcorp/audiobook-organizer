// file: core/config/configtest/doubles.go
// version: 1.0.0
// guid: ed251aa4-8912-4853-b2a1-165c184ebd64
// last-edited: 2026-11-10

package configtest

import (
	"context"
	"iter"
	"maps"
	"slices"
	"sync"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
)

// Memory is an in-memory config.OverrideStore for tests that drive a
// config.Store without storage. It is a double, not an implementation of the
// contract: Stage ignores the batch and writes at once (OverrideStoreSuite
// runs over the native store, not over this). Safe for concurrent use.
type Memory struct {
	mu      sync.Mutex
	rows    map[config.Path]config.Override
	loads   int
	stages  int
	loadErr error
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{rows: map[config.Path]config.Override{}} }

// Put stores ov as if a past commit had written it, raw: an invalid value is
// kept as given.
func (m *Memory) Put(ov config.Override) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ov.Value = slices.Clone(ov.Value)
	m.rows[ov.Path] = ov
}

// FailLoad makes every later Load yield err.
func (m *Memory) FailLoad(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadErr = err
}

// Rows returns a copy of every stored row.
func (m *Memory) Rows() map[config.Path]config.Override {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[config.Path]config.Override, len(m.rows))
	for p, ov := range m.rows {
		ov.Value = slices.Clone(ov.Value)
		out[p] = ov
	}
	return out
}

// Loads and Stages count the calls.
func (m *Memory) Loads() int  { m.mu.Lock(); defer m.mu.Unlock(); return m.loads }
func (m *Memory) Stages() int { m.mu.Lock(); defer m.mu.Unlock(); return m.stages }

// Load yields every row, sorted by path.
func (m *Memory) Load(context.Context) iter.Seq2[config.Override, error] {
	m.mu.Lock()
	m.loads++
	err := m.loadErr
	m.mu.Unlock()
	rows := m.Rows()
	return func(yield func(config.Override, error) bool) {
		if err != nil {
			yield(config.Override{}, err)
			return
		}
		for _, p := range slices.Sorted(maps.Keys(rows)) {
			if !yield(rows[p], nil) {
				return
			}
		}
	}
}

// Stage writes set and removes unset at once; b is ignored.
func (m *Memory) Stage(_ context.Context, _ *storage.Batch, set []config.Override, unset []config.Path) error {
	m.mu.Lock()
	m.stages++
	m.mu.Unlock()
	m.apply(set, unset)
	return nil
}

func (m *Memory) apply(set []config.Override, unset []config.Path) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ov := range set {
		ov.Value = slices.Clone(ov.Value)
		m.rows[ov.Path] = ov
	}
	for _, p := range unset {
		delete(m.rows, p)
	}
}

// Committer is a config.Committer double: it records every Commit, fails
// with the error Fail set, and otherwise applies the commit's rows to its
// Memory. Safe for concurrent use.
type Committer struct {
	Store *Memory

	mu      sync.Mutex
	err     error
	commits []config.Commit
	calls   int
}

// NewCommitter returns a Committer over m.
func NewCommitter(m *Memory) *Committer { return &Committer{Store: m} }

// Fail makes every later Commit return err (nil: succeed again).
func (c *Committer) Fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// Calls counts every Commit call, failed ones included.
func (c *Committer) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Commits returns the successful commits, in order.
func (c *Committer) Commits() []config.Commit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.commits)
}

// Commit records cm and applies it to the Memory.
func (c *Committer) Commit(_ context.Context, cm config.Commit) error {
	c.mu.Lock()
	c.calls++
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.commits = append(c.commits, cm)
	c.mu.Unlock()
	if c.Store != nil {
		c.Store.apply(cm.Set, cm.Unset)
	}
	return nil
}
