// file: internal/activity/service_deferred_test.go
// version: 1.1.0
// guid: 1f6c9e3a-7d25-4b80-a4e1-3c8b5d0f2e69
// last-edited: 2026-09-14

package activity

import (
	"context"
	"errors"
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

// slowClosableStore stands in for a SQLite store whose deferred write is stuck
// behind a checkpoint when shutdown arrives. Record blocks on gate; a Record
// that runs after Close counts as a write against a closed handle.
type slowClosableStore struct {
	database.ActivityStorer
	gate       chan struct{}
	entered    chan struct{}
	enterOnce  sync.Once
	closed     atomic.Bool
	closeCalls atomic.Int32
	afterClose atomic.Int32

	mu  sync.Mutex
	got []string
}

func (s *slowClosableStore) Record(e database.ActivityEntry) (int64, error) {
	s.enterOnce.Do(func() { close(s.entered) })
	<-s.gate
	if s.closed.Load() {
		s.afterClose.Add(1)
		return 0, errors.New("sql: database is closed")
	}
	s.mu.Lock()
	s.got = append(s.got, e.Summary)
	s.mu.Unlock()
	return 1, nil
}

func (s *slowClosableStore) Close() error {
	s.closeCalls.Add(1)
	s.closed.Store(true)
	return nil
}

func (s *slowClosableStore) summaries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

// Shutdown against a slow store: Close's budget runs out while a deferred write
// is in flight. The store must stay open (no write may race a closed handle),
// the error must count what is unconfirmed, and nothing may be dropped.
func TestClose_NeverClosesTheStoreUnderAnInFlightDeferredWrite(t *testing.T) {
	st := &slowClosableStore{gate: make(chan struct{}), entered: make(chan struct{})}
	gateOpen := false
	defer func() {
		if !gateOpen {
			close(st.gate)
		}
	}()
	svc := NewService(st)

	svc.RecordDeferred(database.ActivityEntry{Summary: "startup"})
	svc.StartDeferredFlush()
	select {
	case <-st.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("deferred flush never reached the store")
	}
	svc.RecordDeferred(database.ActivityEntry{Summary: "queued-behind"})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := svc.Close(ctx)
	require.Error(t, err, "Close must report that its budget ran out")
	assert.Contains(t, err.Error(), "2 deferred entries not yet confirmed written")
	assert.Zero(t, st.closeCalls.Load(), "the store must stay open while a deferred write is in flight")

	// The store moves: the in-flight write and the one queued behind it land,
	// and a retried Close now closes the store.
	close(st.gate)
	gateOpen = true
	require.NoError(t, svc.Close(context.Background()))
	assert.Zero(t, st.afterClose.Load(), "a deferred write ran against a closed store")
	assert.Equal(t, []string{"startup", "queued-behind"}, st.summaries())
	assert.EqualValues(t, 1, st.closeCalls.Load())

	// After Close: refused (and logged), never written to the closed store.
	svc.RecordDeferred(database.ActivityEntry{Summary: "late"})
	time.Sleep(20 * time.Millisecond)
	assert.Zero(t, st.afterClose.Load())
	require.NoError(t, svc.Close(context.Background()), "a second Close is a no-op")
	assert.EqualValues(t, 1, st.closeCalls.Load())
}
