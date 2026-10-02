// file: internal/server/asin_backfill_shutdown_test.go
// version: 1.0.0
// guid: 9a4c2e71-6d3b-4f58-b1e0-7c5d8a2f3e64
// last-edited: 2026-10-02

package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Shutdown stops the ASIN backfill queue (the step that runs before the op
// registry drains): an armed debounce timer must not enqueue afterwards, and
// the stop is nil-safe and idempotent.
func TestLifecycle_StopASINBackfillQueueBeforeRegistry(t *testing.T) {
	(&Server{}).stopASINBackfillQueue() // no metafetch service

	svc := metafetch.NewService(&database.MockStore{})
	s := &Server{metadataFetchService: svc}
	s.stopASINBackfillQueue() // no queue wired yet

	var enqueued atomic.Int32
	svc.SetASINBackfillQueue(metafetch.NewASINBackfillQueue(func(context.Context, []string) (string, error) {
		enqueued.Add(1)
		return "op", nil
	}, nil, nil, 20*time.Millisecond))
	svc.ASINBackfillQueue().Add("b1")

	s.stopASINBackfillQueue()
	s.stopASINBackfillQueue()
	time.Sleep(80 * time.Millisecond)
	if n := enqueued.Load(); n != 0 {
		t.Fatalf("queue enqueued %d run(s) after shutdown stopped it", n)
	}
}
