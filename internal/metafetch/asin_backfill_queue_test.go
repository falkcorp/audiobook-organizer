// file: internal/metafetch/asin_backfill_queue_test.go
// version: 1.0.0
// guid: 8f2b6d14-0a93-4c57-9e18-b4c7d2e05a61
// last-edited: 2026-10-02

package metafetch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// manualQueue builds a queue whose timer never fires on its own: fire() runs
// the armed flush, as the timer would.
type manualQueue struct {
	q        *ASINBackfillQueue
	mu       sync.Mutex
	armed    []func()
	enqueued [][]string
	status   map[string]string
	err      error
}

func newManualQueue() *manualQueue {
	m := &manualQueue{status: map[string]string{}}
	m.q = NewASINBackfillQueue(
		func(_ context.Context, ids []string) (string, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.err != nil {
				return "", m.err
			}
			m.enqueued = append(m.enqueued, append([]string(nil), ids...))
			return "op-" + string(rune('0'+len(m.enqueued))), nil
		},
		func(opID string) (string, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.status[opID], nil
		}, time.Hour)
	m.q.afterFunc = func(_ time.Duration, f func()) *time.Timer {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.armed = append(m.armed, f)
		return nil
	}
	return m
}

// fire runs every flush armed so far and reports how many there were.
func (m *manualQueue) fire() int {
	m.mu.Lock()
	fs := m.armed
	m.armed = nil
	m.mu.Unlock()
	for _, f := range fs {
		f()
	}
	return len(fs)
}

// A burst of adds arms ONE flush and enqueues ONE run with every id.
func TestASINBackfillQueue_CoalescesBurst(t *testing.T) {
	m := newManualQueue()
	for _, id := range []string{"b3", "b1", "b2", "b1"} {
		m.q.Add(id)
	}
	require.Equal(t, 1, m.fire(), "a burst must arm exactly one flush")
	require.Equal(t, [][]string{{"b1", "b2", "b3"}}, m.enqueued)
	assert.Empty(t, m.q.Pending())
	assert.Equal(t, 0, m.fire(), "nothing re-armed after a flush with no new ids")
}

// While the last run is still queued (e.g. behind a full-library walk on the
// shared ConcurrencyKey), new ids wait and merge into ONE next run.
func TestASINBackfillQueue_HoldsWhileLastRunQueued(t *testing.T) {
	m := newManualQueue()
	m.q.Add("b1")
	m.fire()
	m.status["op-1"] = "queued"

	m.q.Add("b2")
	m.q.Add("b3")
	require.Equal(t, 1, m.fire())
	assert.Len(t, m.enqueued, 1, "no second run while the first has not started")
	assert.Equal(t, []string{"b2", "b3"}, m.q.Pending())

	m.q.Add("b4")
	m.status["op-1"] = "running"
	require.Equal(t, 1, m.fire(), "the held flush re-armed itself; Add must not arm a second")
	require.Len(t, m.enqueued, 2)
	assert.Equal(t, []string{"b2", "b3", "b4"}, m.enqueued[1])
}

// A failed enqueue is dropped (logged), not retried forever.
func TestASINBackfillQueue_EnqueueErrorDrops(t *testing.T) {
	m := newManualQueue()
	m.err = errors.New("registry: unknown defID")
	m.q.Add("b1")
	m.fire()
	assert.Empty(t, m.q.Pending())
	assert.Equal(t, 0, m.fire())
}

// A metadata apply that leaves the book without an ASIN queues it for
// metafetch.asin-backfill and writes no ASIN itself: no background search of
// Google Books / Open Library runs any more.
func TestApplyMetadataCandidate_QueuesASINBackfillAndWritesNoASIN(t *testing.T) {
	book := &database.Book{ID: "b1", Title: "Old"}
	var mu sync.Mutex
	var writes []*database.Book
	mock := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			mu.Lock()
			defer mu.Unlock()
			b := *book
			if n := len(writes); n > 0 {
				b = *writes[n-1]
			}
			return &b, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			mu.Lock()
			defer mu.Unlock()
			c := *b
			writes = append(writes, &c)
			return b, nil
		},
		CreateAuthorFunc: func(name string) (*database.Author, error) {
			return &database.Author{ID: 5, Name: name}, nil
		},
	}
	svc := NewService(mock)
	m := newManualQueue()
	svc.SetASINBackfillQueue(m.q)

	_, err := svc.ApplyMetadataCandidate("b1", MetadataCandidate{Title: "New Title", Author: "New Author", Source: "Google Books"}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"b1"}, m.q.Pending(), "the applied book must be queued for the backfill")
	mu.Lock()
	for i, w := range writes {
		assert.True(t, w.ASIN == nil || *w.ASIN == "", "write %d set ASIN %v", i, w.ASIN)
	}
	mu.Unlock()
	m.fire()
	assert.Equal(t, [][]string{{"b1"}}, m.enqueued)

	// A book that already has both identifiers is not queued.
	asin, isbn := "B00X", "9780000000002"
	assert.False(t, needsIdentifierBackfill(&database.Book{ASIN: &asin, ISBN13: &isbn}))
}
