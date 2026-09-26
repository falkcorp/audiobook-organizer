// file: internal/searchcache/changelog_memory_test.go
// version: 1.1.0
// guid: 5c8e2a17-4b6d-4f93-a1d0-9e7b3c6f2d48
// last-edited: 2026-09-26

package searchcache

import (
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"
	"unsafe"
)

// ringMemorySizes are the change-ring depths the 2026-09-26 memory audit
// compares: the old default, the current one, and a 4x raise.
var ringMemorySizes = []int{8192, 65536, 262144}

// heapInUse returns live heap bytes after a full collection.
func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// ulidLikeIDs returns n distinct 26-byte book IDs, each its own allocation,
// the shape of a ULID book ID decoded from the store.
func ulidLikeIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("01J%023d", i)
	}
	return ids
}

// BenchmarkChangeLogRingMemory measures what one ChangeLog costs at each ring
// depth, in two parts reported separately:
//
//   - ring-B: the []changeRecord slice NewChangeLog allocates up front. It is
//     paid at startup whether or not anything is ever recorded.
//   - ids-B: the book ID strings a FULL ring keeps reachable. Only IDs the
//     ring is the last holder of cost this in production; IDs still held by
//     memdb or a cached result list are shared.
//
// Run: go test ./internal/searchcache -run '^$' -bench ChangeLogRingMemory -benchtime 1x
func BenchmarkChangeLogRingMemory(b *testing.B) {
	b.Logf("sizeof(changeRecord) = %d bytes", unsafe.Sizeof(changeRecord{}))
	for _, size := range ringMemorySizes {
		b.Run(fmt.Sprintf("ring=%d", size), func(b *testing.B) {
			var ringBytes, idBytes float64
			for range b.N {
				base := heapInUse()
				cl := NewChangeLog(size)
				afterRing := heapInUse()

				ids := ulidLikeIDs(size)
				for _, id := range ids {
					cl.Record(id)
				}
				ids = nil
				afterFill := heapInUse()
				runtime.KeepAlive(cl)
				runtime.KeepAlive(ids)

				ringBytes = float64(afterRing - base)
				idBytes = float64(afterFill - afterRing)
			}
			b.ReportMetric(ringBytes, "ring-B")
			b.ReportMetric(idBytes, "ids-B")
			b.ReportMetric((ringBytes+idBytes)/float64(size), "B/entry-full")
			b.ReportMetric(ringBytes/float64(size), "B/entry-ring")
		})
	}
}

// BenchmarkChangedSinceFullRing is the per-lookup CPU side of a deeper ring:
// ChangedSince walks every live record under the ChangeLog mutex (skipping
// those at or below since), so a patch of an entry that is one generation old
// still scans the whole ring once it is full.
//
// Run: go test ./internal/searchcache -run '^$' -bench ChangedSinceFullRing
func BenchmarkChangedSinceFullRing(b *testing.B) {
	for _, size := range ringMemorySizes {
		b.Run(fmt.Sprintf("ring=%d", size), func(b *testing.B) {
			cl := NewChangeLog(size)
			for _, id := range ulidLikeIDs(size) {
				cl.Record(id)
			}
			since := cl.Generation() - 1
			b.ResetTimer()
			for range b.N {
				if _, _, ok := cl.ChangedSince(since); !ok {
					b.Fatal("ring should cover the last generation")
				}
			}
		})
	}
}

// BenchmarkChangedSinceWholeRing is the worst case: an entry as old as the
// oldest record, so every record is new to it and ChangedSince builds the
// deduped set of ALL of them -- which patch() then discards whenever it is
// larger than MaxPatchChanged (2048). ChangedSince does not stop early at
// that cap, so this cost grows with the ring, not with the cap.
//
// Run: go test ./internal/searchcache -run '^$' -bench ChangedSinceWholeRing -benchmem
func BenchmarkChangedSinceWholeRing(b *testing.B) {
	for _, size := range ringMemorySizes {
		b.Run(fmt.Sprintf("ring=%d", size), func(b *testing.B) {
			cl := NewChangeLog(size)
			since := cl.Generation()
			for _, id := range ulidLikeIDs(size) {
				cl.Record(id)
			}
			b.ResetTimer()
			for range b.N {
				if ids, _, ok := cl.ChangedSince(since); !ok || len(ids) != size {
					b.Fatalf("ChangedSince = %d ids, ok=%v; want %d, true", len(ids), ok, size)
				}
			}
		})
	}
}

// TestChangeLogRingPatchWindow pins the behavioural claim in the memory audit:
// the ring is sized in RECORDS, the patch cap in DISTINCT books, and
// ChangedSince dedupes. So the ring only decides patch-vs-rebuild when the
// burst repeats the same books: a burst of D distinct books written R times
// each overflows a ring of size S when D*R > S, while MaxPatchChanged forces
// a rebuild whenever D > 2048 regardless of S.
func TestChangeLogRingPatchWindow(t *testing.T) {
	cases := []struct {
		ring, distinct, repeats int
		wantPatchable           bool
	}{
		// 1,000 books written 10 times: 10,000 records.
		{8192, 1000, 10, false}, // ring overflow
		{65536, 1000, 10, true},
		// 2,000 books written 40 times: 80,000 records.
		{65536, 2000, 40, false},
		{262144, 2000, 40, true},
		// 5,000 books written once: within every ring but over the cap,
		// which the cache applies after ChangedSince (not modelled here).
		{8192, 5000, 1, true},
	}
	for _, tc := range cases {
		cl := NewChangeLog(tc.ring)
		since := cl.Record("seed")
		ids := ulidLikeIDs(tc.distinct)
		for range tc.repeats {
			for _, id := range ids {
				cl.Record(id)
			}
		}
		changed, _, ok := cl.ChangedSince(since)
		if ok != tc.wantPatchable {
			t.Errorf("ring=%d distinct=%d repeats=%d: ChangedSince ok=%v, want %v",
				tc.ring, tc.distinct, tc.repeats, ok, tc.wantPatchable)
		}
		if ok && len(changed) != tc.distinct {
			t.Errorf("ring=%d distinct=%d repeats=%d: %d changed IDs, want %d (deduped)",
				tc.ring, tc.distinct, tc.repeats, len(changed), tc.distinct)
		}
	}
}

// BenchmarkChangedSinceRepeatHeavy is a whole-ring lookup whose change set
// stays under MaxPatchChanged: 1,024 distinct books written 64 times fill a
// 65,536 ring. No early stop can fire, so this is the case where only
// shortening the lock hold (not the limit) helps a concurrent writer.
//
// Run: go test ./internal/searchcache -run '^$' -bench ChangedSinceRepeatHeavy -benchmem
func BenchmarkChangedSinceRepeatHeavy(b *testing.B) {
	const size, distinct = 65536, 1024
	cl := NewChangeLog(size)
	since := cl.Generation()
	ids := ulidLikeIDs(distinct)
	for range size / distinct {
		for _, id := range ids {
			cl.Record(id)
		}
	}
	b.ResetTimer()
	for range b.N {
		if got, _, ok := changedSinceCapped(cl, since); !ok || len(got) != distinct {
			b.Fatalf("ChangedSince = %d ids, ok=%v; want %d, true", len(got), ok, distinct)
		}
	}
}

// BenchmarkRecordWhileChangedSince times Record, the call every book write
// makes on its own goroutine, while another goroutine looks up a whole-ring
// change set in a loop. Record waits for the ChangeLog mutex, so its time per
// op is dominated by how long ChangedSince holds that mutex.
//
// Run: go test ./internal/searchcache -run '^$' -bench RecordWhileChangedSince
func BenchmarkRecordWhileChangedSince(b *testing.B) {
	for _, shape := range []struct {
		name     string
		distinct int
	}{
		{"distinct", 65536}, // every record a different book: past the cap
		{"repeats", 1024},   // 64 writes per book: under the cap
	} {
		b.Run(shape.name, func(b *testing.B) {
			const size = 65536
			cl := NewChangeLog(size)
			ids := ulidLikeIDs(shape.distinct)
			for i := range size {
				cl.Record(ids[i%len(ids)])
			}
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-stop:
						return
					default:
					}
					// A lookup at the oldest live generation walks the
					// whole ring without tripping the overflow check.
					cl.mu.Lock()
					oldest := cl.floor
					cl.mu.Unlock()
					changedSinceCapped(cl, oldest)
				}
			}()
			waits := make([]time.Duration, b.N)
			b.ResetTimer()
			for i := range b.N {
				t0 := time.Now()
				cl.Record(ids[i%len(ids)])
				waits[i] = time.Since(t0)
			}
			b.StopTimer()
			close(stop)
			<-done
			slices.Sort(waits)
			b.ReportMetric(float64(waits[len(waits)*999/1000].Microseconds()), "p99.9-µs")
			b.ReportMetric(float64(waits[len(waits)-1].Microseconds()), "max-µs")
		})
	}
}
