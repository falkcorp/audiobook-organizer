// file: internal/server/server_startup_activity_test.go
// version: 1.0.0
// guid: 6b2e8f4d-0c19-4a73-b5d8-9e1a7c3f6d42
// last-edited: 2026-09-14

package server

import (
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// blockingActivityStore wraps a real activity store but blocks every Record
// until gate is closed: the stand-in for a SQLite write stuck behind a
// multi-GB checkpoint, which is what held production's listener closed for
// ~6 minutes on 2026-09-14.
type blockingActivityStore struct {
	database.ActivityStorer
	gate  chan struct{}
	calls atomic.Int32

	mu  sync.Mutex
	got []string
}

func (b *blockingActivityStore) Record(e database.ActivityEntry) (int64, error) {
	b.calls.Add(1)
	<-b.gate
	b.mu.Lock()
	b.got = append(b.got, e.Summary)
	b.mu.Unlock()
	return b.ActivityStorer.Record(e)
}

func (b *blockingActivityStore) summaries() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.got...)
}

// TestNewServer_DoesNotWriteActivitySynchronously builds a server whose
// activity store never returns from Record. NewServer must still return, with
// no Record having been attempted; the startup entry is written only after
// StartDeferredFlush (which Start calls once the listener is started).
func TestNewServer_DoesNotWriteActivitySynchronously(t *testing.T) {
	actDB, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "activity.pebble"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = actDB.Close() })
	inner := database.NewPebbleActivityStoreFromStore(actDB)
	require.NotNil(t, inner)

	bs := &blockingActivityStore{ActivityStorer: inner, gate: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(bs.gate) }) }

	activityStoreOverrideForTest = bs
	t.Cleanup(func() { activityStoreOverrideForTest = nil })

	type built struct {
		srv     *Server
		cleanup func()
	}
	ch := make(chan built, 1)
	go func() {
		srv, cleanup := setupTestServer(t)
		ch <- built{srv, cleanup}
	}()

	var b built
	select {
	case b = <-ch:
	case <-time.After(45 * time.Second):
		calls := bs.calls.Load()
		release()
		b = <-ch
		b.cleanup()
		t.Fatalf("NewServer did not return while activity writes were blocked "+
			"(Record attempts: %d): something before the HTTP listener writes activity synchronously", calls)
	}
	t.Cleanup(release)
	t.Cleanup(b.cleanup)

	require.Zero(t, bs.calls.Load(), "NewServer must not call the activity store's Record")

	b.srv.activityService.StartDeferredFlush()
	release()
	require.Eventually(t, func() bool {
		return slices.Contains(bs.summaries(), "Server started, activity log initialized")
	}, 10*time.Second, 10*time.Millisecond, "the deferred startup entry was never written")
}
