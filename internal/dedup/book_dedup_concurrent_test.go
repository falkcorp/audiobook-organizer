// file: internal/dedup/book_dedup_concurrent_test.go
// version: 1.0.0
// guid: 9f2c7b41-6d38-4e05-a1b9-3c7e0d2f5a64
// last-edited: 2026-07-13

package dedup

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
)

// dedupSerializeProbe wraps a real database.Store and counts the maximum number
// of goroutines simultaneously inside the merge read-modify-write. It overrides
// the two methods BOTH dedup.MergeBooks and merge.Service.MergeBooks drive
// (GetBookByID / UpdateBook) and delegates the rest. A tiny sleep on entry widens
// the interleave window so that, if the two paths did NOT share one lock, their
// bodies would overlap and push maxActive to 2+. Sharing merge's package-level
// lock keeps at most one goroutine inside at a time -> maxActive == 1.
type dedupSerializeProbe struct {
	database.Store
	active    int32
	maxActive int32
}

func (p *dedupSerializeProbe) enter() {
	n := atomic.AddInt32(&p.active, 1)
	for {
		m := atomic.LoadInt32(&p.maxActive)
		if n <= m || atomic.CompareAndSwapInt32(&p.maxActive, m, n) {
			break
		}
	}
	time.Sleep(500 * time.Microsecond)
}

func (p *dedupSerializeProbe) leave() { atomic.AddInt32(&p.active, -1) }

func (p *dedupSerializeProbe) GetBookByID(id string) (*database.Book, error) {
	p.enter()
	defer p.leave()
	return p.Store.GetBookByID(id)
}

func (p *dedupSerializeProbe) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	p.enter()
	defer p.leave()
	return p.Store.UpdateBook(id, b)
}

func newConcurrentTestStore(t *testing.T) database.Store {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	orig := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(orig)
		_ = store.Close()
	})
	return store
}

// TestDedupMergeBooks_SharesLockWithMergeService is the load-bearing regression
// test for Path 2 of the #1930 follow-up. dedup.MergeBooks is a package-level
// function (NOT merge.Service.MergeBooks) with its own read-modify-write, and it
// can run concurrently with merge.Service.MergeBooks on a shared book row. It
// must therefore take the SAME process-wide lock those Service methods use.
//
// Each goroutine gets its OWN disjoint pair, so maxActive>1 can only mean two
// read-modify-writes overlapped — i.e. the lock was NOT shared. Even goroutines
// run dedup.MergeBooks, odd goroutines run merge.Service.MergeBooks, all through
// one shared probe store. WITH the shared lock (merge.LockMergeRMW) maxActive
// stays 1. Discriminates: giving dedup.MergeBooks its own separate mutex lets a
// dedup merge overlap a Service merge and maxActive reaches 2 (verified during
// development). The even goroutines also exercise dedup-vs-dedup serialization.
func TestDedupMergeBooks_SharesLockWithMergeService(t *testing.T) {
	real := newConcurrentTestStore(t)
	probe := &dedupSerializeProbe{Store: real}
	ms := merge.NewService(probe)

	const goroutines = 16
	type pair struct{ keep, drop string }
	pairs := make([]pair, goroutines)
	for i := range pairs {
		keep := ulid.Make().String()
		drop := ulid.Make().String()
		if _, err := real.CreateBook(&database.Book{ID: keep, Title: "Keep", Format: "m4b", FilePath: "/tmp/" + keep + ".m4b"}); err != nil {
			t.Fatalf("CreateBook keep: %v", err)
		}
		if _, err := real.CreateBook(&database.Book{ID: drop, Title: "Drop", Format: "mp3", FilePath: "/tmp/" + drop + ".mp3"}); err != nil {
			t.Fatalf("CreateBook drop: %v", err)
		}
		pairs[i] = pair{keep, drop}
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			p := pairs[i]
			if i%2 == 0 {
				// dedup.MergeBooks: hard-delete drop, transfer metadata into keep.
				_, _ = MergeBooks(context.Background(), probe, "", p.keep, []string{p.drop}, nil)
			} else {
				// merge.Service.MergeBooks: version-group merge (keep is m4b winner).
				_, _ = ms.MergeBooks([]string{p.keep, p.drop}, "")
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&probe.maxActive); got != 1 {
		t.Fatalf("dedup.MergeBooks did NOT share the merge lock: maxActive=%d, want 1 "+
			"(a dedup merge overlapped a merge.Service merge on the shared probe)", got)
	}

	// Consistency spot-check: pair[0] went through dedup.MergeBooks -> keep alive,
	// drop hard-deleted.
	if k0, err := real.GetBookByID(pairs[0].keep); err != nil || k0 == nil {
		t.Fatalf("dedup keep book missing: %v", err)
	}
	if d0, _ := real.GetBookByID(pairs[0].drop); d0 != nil {
		t.Fatalf("dedup drop book was not deleted")
	}
}
