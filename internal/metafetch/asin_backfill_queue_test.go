// file: internal/metafetch/asin_backfill_queue_test.go
// version: 1.2.0
// guid: 8f2b6d14-0a93-4c57-9e18-b4c7d2e05a61
// last-edited: 2026-10-02

package metafetch

import (
	"context"
	"errors"
	"fmt"
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
	cleared  []string
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
			return fmt.Sprintf("op-%d", len(m.enqueued)), nil
		},
		func(opID string) (string, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.status[opID], nil
		},
		func(bookID string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.cleared = append(m.cleared, bookID)
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
	require.Equal(t, [][]string{{"b3", "b1", "b2"}}, m.enqueued, "arrival order, duplicate dropped")
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
	assert.Equal(t, []string{"b1"}, m.cleared,
		"the apply must clear the book's no-match markers so the scheduled walk re-checks it if the queue is lost")
	m.fire()
	assert.Equal(t, [][]string{{"b1"}}, m.enqueued)

	// A book that already has both identifiers is not queued.
	asin, isbn := "B00X", "9780000000002"
	assert.False(t, needsIdentifierBackfill(&database.Book{ASIN: &asin, ISBN13: &isbn}))
}

// Add clears the book's markers before anything else, even after Stop: the
// clear is what survives a restart, the pending set is not.
func TestASINBackfillQueue_AddClearsMarkersEvenWhenStopped(t *testing.T) {
	m := newManualQueue()
	m.q.Add("b1")
	m.q.Stop()
	m.q.Add("b2")
	assert.Equal(t, []string{"b1", "b2"}, m.cleared)
	assert.Equal(t, []string{"b1"}, m.q.Pending(), "only the pre-Stop id is pending")
	assert.Equal(t, 1, m.fire(), "the flush armed before Stop is the only one")
	assert.Empty(t, m.enqueued, "a flush after Stop enqueues nothing")
}

// Stop cancels the real debounce timer: nothing is enqueued after shutdown.
func TestASINBackfillQueue_StopCancelsTimer(t *testing.T) {
	var mu sync.Mutex
	var enqueued int
	q := NewASINBackfillQueue(func(context.Context, []string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		enqueued++
		return "op", nil
	}, nil, nil, 20*time.Millisecond)
	q.Add("b1")
	q.Stop()
	q.Stop() // idempotent
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, enqueued, "the timer fired after Stop")

	var nilQ *ASINBackfillQueue
	nilQ.Stop()
	nilQ.Add("x")
}

// A burst larger than maxASINBackfillRunIDs goes out as bounded runs, one at
// a time, oldest ids first: the rest wait while the previous run is still
// queued. Ids are added in DESCENDING sort order so a sorted take would fail.
func TestASINBackfillQueue_SplitsLargeBurst(t *testing.T) {
	m := newManualQueue()
	const n = 2*maxASINBackfillRunIDs + 201
	want := make([]string, 0, n)
	for i := range n {
		id := fmt.Sprintf("b%05d", n-i)
		want = append(want, id)
		m.q.Add(id)
	}
	m.q.Add(want[0]) // a re-Add keeps its original place
	require.Equal(t, 1, m.fire())
	require.Len(t, m.enqueued, 1)
	assert.Len(t, m.enqueued[0], maxASINBackfillRunIDs)
	assert.Len(t, m.q.Pending(), n-maxASINBackfillRunIDs)

	// Still queued: the re-armed flush holds.
	m.status["op-1"] = "queued"
	require.Equal(t, 1, m.fire())
	require.Len(t, m.enqueued, 1)

	m.status["op-1"] = "running"
	require.Equal(t, 1, m.fire())
	require.Len(t, m.enqueued, 2)
	assert.Len(t, m.enqueued[1], maxASINBackfillRunIDs)

	m.status["op-2"] = "succeeded"
	require.Equal(t, 1, m.fire())
	require.Len(t, m.enqueued, 3)
	assert.Len(t, m.enqueued[2], 201)
	assert.Empty(t, m.q.Pending())
	assert.Equal(t, 0, m.fire(), "nothing left to arm")

	var sent []string
	for _, run := range m.enqueued {
		sent = append(sent, run...)
	}
	assert.Equal(t, want, sent, "every id exactly once, in arrival (FIFO) order")
}

// SetASINBackfillQueue races an apply's read without a data race.
func TestService_SetASINBackfillQueueConcurrent(t *testing.T) {
	svc := NewService(&database.MockStore{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				svc.SetASINBackfillQueue(newManualQueue().q)
				svc.queueIdentifierBackfill("b1", &database.Book{ID: "b1"})
			}
		})
	}
	wg.Wait()
	assert.NotNil(t, svc.ASINBackfillQueue())
}
