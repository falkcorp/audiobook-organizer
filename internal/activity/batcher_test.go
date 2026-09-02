// file: internal/activity/batcher_test.go
// version: 1.0.1
// guid: 4a8b2c1d-5e9f-4a3b-b7c8-1d2e4f6a8b0c
// last-edited: 2026-09-02

package activity

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// drainAll collects up to n entries from ch, waiting at most timeout each time.
func drainAll(ch <-chan database.ActivityEntry, n int, timeout time.Duration) []database.ActivityEntry {
	var results []database.ActivityEntry
	for range n {
		select {
		case e := <-ch:
			results = append(results, e)
		case <-time.After(timeout):
			return results
		}
	}
	return results
}

func TestActivityBatcher_BasicFlush(t *testing.T) {
	out := make(chan database.ActivityEntry, 10)
	b := NewActivityBatcher(out)

	key := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-1"}
	b.Submit(key, BatchItem{Name: "book1.m4b", Count: 1})
	b.Submit(key, BatchItem{Name: "book2.m4b", Count: 1})
	b.Submit(key, BatchItem{Name: "book3.m4b", Count: 1})

	b.FlushAll()

	entries := drainAll(out, 5, 100*time.Millisecond)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Details == nil {
		t.Fatal("expected non-nil Details")
	}

	batched, ok := e.Details["batched"]
	if !ok {
		t.Fatal("expected 'batched' key in Details")
	}
	if batched != true {
		t.Errorf("expected batched=true, got %v", batched)
	}

	origCount, ok := e.Details["original_count"]
	if !ok {
		t.Fatal("expected 'original_count' key in Details")
	}
	// JSON numbers unmarshal as float64
	if origCount.(float64) != 3 {
		t.Errorf("expected original_count=3, got %v", origCount)
	}
}

func TestActivityBatcher_EarlyFlush_ItemCount(t *testing.T) {
	// Use a long window so early flush must be triggered by item count, not timer.
	out := make(chan database.ActivityEntry, 10)
	b := newActivityBatcherWithWindow(out, 60*time.Second)

	key := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-early"}

	// Submit 501 items — should trigger early flush at >= 500 total.
	for range 501 {
		b.Submit(key, BatchItem{Name: "file", Count: 1})
	}

	// The early flush runs in a goroutine; give it time to complete.
	select {
	case e := <-out:
		if e.Details["batched"] != true {
			t.Errorf("expected batched entry, got %+v", e.Details)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected early flush entry within 500ms, got none")
	}
}

func TestActivityBatcher_MultipleKeys(t *testing.T) {
	out := make(chan database.ActivityEntry, 10)
	b := NewActivityBatcher(out)

	key1 := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-1"}
	key2 := BatchKey{Type: "path-repair", Source: "repairer", OperationID: "op-2"}

	b.Submit(key1, BatchItem{Name: "a", Count: 1})
	b.Submit(key2, BatchItem{Name: "b", Count: 1})

	b.FlushAll()

	entries := drainAll(out, 5, 100*time.Millisecond)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (one per key), got %d", len(entries))
	}
}

func TestActivityBatcher_EmptyFlush(t *testing.T) {
	out := make(chan database.ActivityEntry, 10)
	b := NewActivityBatcher(out)

	b.FlushAll()

	select {
	case e := <-out:
		t.Errorf("expected no entry, got %+v", e)
	case <-time.After(10 * time.Millisecond):
		// correct: nothing sent
	}
}

func TestActivityBatcher_TimerFires(t *testing.T) {
	// Use a 50ms window to avoid a real 15s sleep in CI.
	out := make(chan database.ActivityEntry, 10)
	b := newActivityBatcherWithWindow(out, 50*time.Millisecond)

	key := BatchKey{Type: "isbn-enrich", Source: "enricher", OperationID: "op-timer"}
	b.Submit(key, BatchItem{Name: "book", Count: 1})

	// Wait long enough for the timer to fire (50ms window + buffer).
	select {
	case e := <-out:
		if e.Details["batched"] != true {
			t.Errorf("expected batched=true, got %+v", e.Details)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timer did not fire within 300ms")
	}
}

func TestActivityBatcher_CloseFlushes(t *testing.T) {
	out := make(chan database.ActivityEntry, 10)
	b := NewActivityBatcher(out)

	key := BatchKey{Type: "metadata-apply", Source: "applier", OperationID: "op-close"}
	b.Submit(key, BatchItem{Name: "x", Count: 1})
	b.Submit(key, BatchItem{Name: "y", Count: 1})

	b.Close()

	entries := drainAll(out, 5, 100*time.Millisecond)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after Close, got %d", len(entries))
	}

	if entries[0].Details["batched"] != true {
		t.Errorf("expected batched=true")
	}
}

func TestActivityBatcher_Race(t *testing.T) {
	out := make(chan database.ActivityEntry, 20)
	b := NewActivityBatcher(out)

	key := BatchKey{Type: "embedded-tag-load", Source: "loader", OperationID: "op-race"}

	const goroutines = 10
	const itemsEach = 5

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range itemsEach {
				b.Submit(key, BatchItem{Name: "file", Count: 1})
			}
		}()
	}
	wg.Wait()

	b.FlushAll()

	entries := drainAll(out, 20, 100*time.Millisecond)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 merged entry, got %d", len(entries))
	}

	origCount := entries[0].Details["original_count"].(float64)
	if int(origCount) != goroutines*itemsEach {
		t.Errorf("expected original_count=%d, got %v", goroutines*itemsEach, origCount)
	}
}

func TestActivityBatcher_Overflow(t *testing.T) {
	out := make(chan database.ActivityEntry, 10)
	b := NewActivityBatcher(out)

	key := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-overflow"}

	// Submit maxBatchItems+1 = 201 items.
	total := maxBatchItems + 1
	for range total {
		b.Submit(key, BatchItem{Name: "file", Count: 1})
	}

	b.FlushAll()

	entries := drainAll(out, 5, 100*time.Millisecond)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]

	// Re-marshal Details into BatchDetails for structured access.
	raw, err := json.Marshal(e.Details)
	if err != nil {
		t.Fatalf("failed to marshal details: %v", err)
	}
	var bd BatchDetails
	if err := json.Unmarshal(raw, &bd); err != nil {
		t.Fatalf("failed to unmarshal BatchDetails: %v", err)
	}

	if !bd.Truncated {
		t.Errorf("expected Truncated=true for %d items (max=%d)", total, maxBatchItems)
	}
	if bd.OriginalCount != total {
		t.Errorf("expected OriginalCount=%d, got %d", total, bd.OriginalCount)
	}
	if len(bd.Items) != maxBatchItems {
		t.Errorf("expected Items len=%d, got %d", maxBatchItems, len(bd.Items))
	}
}
