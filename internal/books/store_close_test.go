// file: internal/books/store_close_test.go
// version: 1.0.1
// guid: 57bdf0e4-66f2-42ee-bb57-600cb04025ce
// last-edited: 2026-11-09

package books

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// TestStoreCloseUnderAbandonedStop (R1-15 review m2): serve closes the store
// as soon as Run returns, even when the kernel abandoned books' StopHook at
// the end of the shutdown budget (cmd booksStopped). This is the claim that
// makes it safe, driven through books' own wiring rather than the raw gate:
// a writer still recording activity when the store closes (what a stop step
// left running does) gets an error wrapping pebble.ErrClosed, not a panic,
// and Close returns within its drain bound. That the write is refused at all
// shows the activity store has no engine of its own: it borrows the main
// store's GatedDB. Rows committed before the close surviving a reopen is the
// database package's TestPebbleStoreClose_DrainTimeoutStillSyncsCommittedRows;
// this store is in memory.
func TestStoreCloseUnderAbandonedStop(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	svc := c.srv.ActivityService()
	require.NotNil(t, svc, "the composed server built no activity service")

	var recorded atomic.Int64
	writing := make(chan struct{})
	refused := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				refused <- fmt.Errorf("the late writer panicked: %v", r)
			}
		}()
		for i := 0; ; i++ {
			err := svc.Record(database.ActivityEntry{
				Timestamp: time.Now(), Tier: "change", Type: "test", Level: "info",
				Source: "gotham", Summary: fmt.Sprintf("Batarang %d thrown after the stop budget", i),
			})
			if err != nil {
				refused <- err
				return
			}
			if recorded.Add(1) == 20 {
				close(writing)
			}
		}
	}()
	select {
	case <-writing:
	case err := <-refused:
		t.Fatalf("the writer failed before the store closed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the writer never got going")
	}

	start := time.Now()
	closeErr := c.store.Close()
	took := time.Since(start)

	select {
	case err := <-refused:
		require.ErrorIs(t, err, pebble.ErrClosed, "a write after the close must be refused as closed")
	case <-time.After(10 * time.Second):
		t.Fatal("the writer was never refused: the activity store does not share the main store's engine")
	}
	assert.NoError(t, closeErr, "each write is short, so the drain finishes and the engine closes")
	assert.Less(t, took, database.DefaultCloseDrainTimeout+time.Second, "Close outlived its own drain bound")
	assert.Positive(t, recorded.Load())
}
