// file: internal/activity/service_deferred_test.go
// version: 1.0.0
// guid: 1f6c9e3a-7d25-4b80-a4e1-3c8b5d0f2e69
// last-edited: 2026-09-14

package activity

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingStore is an ActivityStorer whose Record blocks until gate is closed,
// standing in for a SQLite write stuck behind a multi-GB checkpoint. Only
// Record is implemented; any other method would panic on the nil embed, which
// is the point: RecordDeferred must touch nothing else.
type blockingStore struct {
	database.ActivityStorer
	gate  chan struct{}
	calls atomic.Int32

	mu  sync.Mutex
	got []string
}

func (b *blockingStore) Record(e database.ActivityEntry) (int64, error) {
	b.calls.Add(1)
	<-b.gate
	b.mu.Lock()
	b.got = append(b.got, e.Summary)
	b.mu.Unlock()
	return 1, nil
}

func (b *blockingStore) summaries() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.got...)
}

func TestRecordDeferred_NeverCallsTheStoreOnTheCallersGoroutine(t *testing.T) {
	bs := &blockingStore{gate: make(chan struct{})}
	defer func() {
		select {
		case <-bs.gate:
		default:
			close(bs.gate)
		}
	}()
	svc := NewService(bs)

	done := make(chan struct{})
	go func() {
		svc.RecordDeferred(database.ActivityEntry{Summary: "first"})
		svc.RecordDeferred(database.ActivityEntry{Summary: "second"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RecordDeferred blocked on the store")
	}
	assert.Zero(t, bs.calls.Load(), "nothing may reach the store before StartDeferredFlush")

	// Release: the flush runs in the background, so this returns even though
	// the store is still blocked.
	start := time.Now()
	svc.StartDeferredFlush()
	assert.Less(t, time.Since(start), time.Second, "StartDeferredFlush must not wait for the writes")

	// Queued after release, while the flush is stuck: must still not block.
	svc.RecordDeferred(database.ActivityEntry{Summary: "third"})

	// Shutdown's bounded wait gives up while the store is stuck...
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, svc.FlushDeferred(ctx))

	// ...and every entry lands, in order, once the store moves.
	close(bs.gate)
	require.NoError(t, svc.FlushDeferred(context.Background()))
	assert.Equal(t, []string{"first", "second", "third"}, bs.summaries())
}

func TestFlushDeferred_WritesEntriesQueuedBeforeRelease(t *testing.T) {
	bs := &blockingStore{gate: make(chan struct{})}
	close(bs.gate)
	svc := NewService(bs)
	svc.RecordDeferred(database.ActivityEntry{Summary: "startup"})

	// A shutdown that arrives before the listener ever started still writes it.
	require.NoError(t, svc.FlushDeferred(context.Background()))
	assert.Equal(t, []string{"startup"}, bs.summaries())
	// Nothing queued: returns at once.
	require.NoError(t, svc.FlushDeferred(context.Background()))
}
