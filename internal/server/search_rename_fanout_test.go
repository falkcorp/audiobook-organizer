// file: internal/server/search_rename_fanout_test.go
// version: 1.0.0
// guid: 8603d299-d62c-4c75-ac8c-71c03c01de5c
// last-edited: 2026-09-25

package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

// Review 2, finding 5: a bulk rename pass must not start one concurrent book
// read per rename. When memdb is short rows each read is a full Pebble book
// scan, and UpdateAuthor has no synchronous scan to throttle the caller.
// Before the fix every rename started its own goroutine, so N renames ran N
// loads at once. Repeated renames of the same queued author also coalesce.
func TestSearchChangeObserver_RenameFanOutIsBounded(t *testing.T) {
	srv := &Server{searchChanges: searchcache.NewChangeLog(64)}
	o := &searchChangeObserver{s: srv}

	const renames = 20
	var running, peak, loads atomic.Int64
	gate := make(chan struct{})
	load := func() ([]database.BookCore, error) {
		loads.Add(1)
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-gate
		running.Add(-1)
		return nil, nil
	}

	returned := make(chan struct{})
	go func() {
		for i := 0; i < renames; i++ {
			o.fanOut(load, "author", i)
		}
		// A second rename of an author whose fan-out is still queued adds no
		// work: that fan-out has not read its books yet.
		o.fanOut(load, "author", renames-1)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("fanOut blocked its caller (the store calls it under a lock)")
	}

	time.Sleep(50 * time.Millisecond)
	if p := peak.Load(); p > renameFanOutWorkers {
		t.Fatalf("peak concurrent rename loads = %d, want <= %d", p, renameFanOutWorkers)
	}
	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&srv.indexWorkerBusy) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("indexWorkerBusy = %d after every fan-out ended", atomic.LoadInt32(&srv.indexWorkerBusy))
		}
		time.Sleep(time.Millisecond)
	}
	if n := loads.Load(); n != renames {
		t.Fatalf("loads = %d, want %d (one per distinct author)", n, renames)
	}
}
