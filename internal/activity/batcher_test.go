// file: internal/activity/batcher_test.go
// version: 1.0.2
// guid: 4a8b2c1d-5e9f-4a3b-b7c8-1d2e4f6a8b0c
// last-edited: 2026-11-09

package activity

import (
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// drainAll collects up to n entries from ch, waiting at most timeout each time.
// It is still used by api_test.go and user_data_test.go, which are not
// bubble-wrapped; the batcher tests use drainBuffered instead.
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

// drainBuffered returns every entry currently buffered in ch without blocking. Call
// it after synctest.Wait() so any flush goroutine has already finished sending.
func drainBuffered(ch <-chan database.ActivityEntry) []database.ActivityEntry {
	var results []database.ActivityEntry
	for {
		select {
		case e := <-ch:
			results = append(results, e)
		default:
			return results
		}
	}
}

func TestActivityBatcher_BasicFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan database.ActivityEntry, 10)
		b := NewActivityBatcher(out)
		defer b.Close()

		key := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-1"}
		b.Submit(key, BatchItem{Name: "book1.m4b", Count: 1})
		b.Submit(key, BatchItem{Name: "book2.m4b", Count: 1})
		b.Submit(key, BatchItem{Name: "book3.m4b", Count: 1})

		b.FlushAll()
		synctest.Wait()

		entries := drainBuffered(out)
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
	})
}

func TestActivityBatcher_EarlyFlush_ItemCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Use a long window so early flush must be triggered by item count, not timer.
		out := make(chan database.ActivityEntry, 10)
		b := newActivityBatcherWithWindow(out, 60*time.Second)
		defer b.Close()

		key := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-early"}

		// Exactly 500 items: the threshold is total >= 500. (A 501st Submit
		// could race the flush goroutine and either join this batch or open a
		// new one, which would make the assertion below nondeterministic.)
		start := time.Now()
		for range 500 {
			b.Submit(key, BatchItem{Name: "file", Count: 1})
		}

		// The early flush runs in a goroutine; let it settle. The fake clock has
		// not advanced, so only the count threshold can have flushed it.
		synctest.Wait()
		if !time.Now().Equal(start) {
			t.Fatalf("clock advanced from %v to %v; test must not rely on the timer", start, time.Now())
		}

		entries := drainBuffered(out)
		if len(entries) != 1 {
			t.Fatalf("expected 1 early-flush entry with no clock advance, got %d", len(entries))
		}
		if entries[0].Details["batched"] != true {
			t.Errorf("expected batched entry, got %+v", entries[0].Details)
		}
		if got := entries[0].Details["original_count"].(float64); int(got) != 500 {
			t.Errorf("expected original_count=500, got %v", got)
		}
	})
}

func TestActivityBatcher_MultipleKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan database.ActivityEntry, 10)
		b := NewActivityBatcher(out)
		defer b.Close()

		key1 := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-1"}
		key2 := BatchKey{Type: "path-repair", Source: "repairer", OperationID: "op-2"}

		b.Submit(key1, BatchItem{Name: "a", Count: 1})
		b.Submit(key2, BatchItem{Name: "b", Count: 1})

		b.FlushAll()
		synctest.Wait()

		entries := drainBuffered(out)
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries (one per key), got %d", len(entries))
		}
	})
}

func TestActivityBatcher_EmptyFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan database.ActivityEntry, 10)
		b := NewActivityBatcher(out)
		defer b.Close()

		b.FlushAll()
		synctest.Wait()

		if entries := drainBuffered(out); len(entries) != 0 {
			t.Errorf("expected no entry, got %+v", entries)
		}
	})
}

func TestActivityBatcher_TimerFires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Production window (15s) on the fake clock: no shortened test-only window.
		out := make(chan database.ActivityEntry, 10)
		b := NewActivityBatcher(out)
		defer b.Close()

		key := BatchKey{Type: "isbn-enrich", Source: "enricher", OperationID: "op-timer"}
		b.Submit(key, BatchItem{Name: "book", Count: 1})

		// One tick before the window ends nothing may have been emitted.
		time.Sleep(batchWindow - time.Nanosecond)
		synctest.Wait()
		if entries := drainBuffered(out); len(entries) != 0 {
			t.Fatalf("expected nothing one tick before the window ends, got %d entries", len(entries))
		}

		// The window ends: the timer flushes exactly one batched entry.
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		entries := drainBuffered(out)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 entry when the window ends, got %d", len(entries))
		}
		if entries[0].Details["batched"] != true {
			t.Errorf("expected batched=true, got %+v", entries[0].Details)
		}
	})
}

func TestActivityBatcher_CloseFlushes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan database.ActivityEntry, 10)
		b := NewActivityBatcher(out)

		key := BatchKey{Type: "metadata-apply", Source: "applier", OperationID: "op-close"}
		b.Submit(key, BatchItem{Name: "x", Count: 1})
		b.Submit(key, BatchItem{Name: "y", Count: 1})

		b.Close()
		synctest.Wait()

		entries := drainBuffered(out)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry after Close, got %d", len(entries))
		}

		if entries[0].Details["batched"] != true {
			t.Errorf("expected batched=true")
		}
	})
}

func TestActivityBatcher_Race(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan database.ActivityEntry, 20)
		b := NewActivityBatcher(out)
		defer b.Close()

		key := BatchKey{Type: "embedded-tag-load", Source: "loader", OperationID: "op-race"}

		const goroutines = 10
		const itemsEach = 5

		// The WaitGroup is created inside the bubble, so it is bubble-associated.
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
		synctest.Wait()

		entries := drainBuffered(out)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 merged entry, got %d", len(entries))
		}

		origCount := entries[0].Details["original_count"].(float64)
		if int(origCount) != goroutines*itemsEach {
			t.Errorf("expected original_count=%d, got %v", goroutines*itemsEach, origCount)
		}
	})
}

func TestActivityBatcher_Overflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan database.ActivityEntry, 10)
		b := NewActivityBatcher(out)
		defer b.Close()

		key := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-overflow"}

		// Submit maxBatchItems+1 = 201 items.
		total := maxBatchItems + 1
		for range total {
			b.Submit(key, BatchItem{Name: "file", Count: 1})
		}

		b.FlushAll()
		synctest.Wait()

		entries := drainBuffered(out)
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
	})
}
