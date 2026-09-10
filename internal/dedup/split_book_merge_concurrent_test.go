// file: internal/dedup/split_book_merge_concurrent_test.go
// version: 1.0.0
// guid: e3a91f7c-5b2d-4a6e-9c81-0f4d2b7e8a15
// last-edited: 2026-09-10

package dedup

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
)

// TestMergeSplitBookCluster_SharesLockWithMergeService is the regression test
// for DA-01: MergeSplitBookCluster does an unguarded GetBookByID -> ... ->
// UpdateBook -> SoftDeleteBook read-modify-write over shared book rows, the
// same failure class merge.LockMergeRMW exists to guard (see
// internal/merge/serialize.go and TestDedupMergeBooks_SharesLockWithMergeService
// in book_dedup_concurrent_test.go, which pins the same invariant for
// dedup.MergeBooks). It reuses that file's dedupSerializeProbe/
// newConcurrentTestStore helpers: half the goroutines run
// MergeSplitBookCluster, the other half run merge.Service.MergeBooks, all
// through one shared probe store wrapping GetBookByID/UpdateBook. Each
// goroutine gets its own disjoint pair of book IDs, so maxActive>1 can only
// mean the two read-modify-writes overlapped -- i.e. MergeSplitBookCluster
// did NOT take the shared lock.
func TestMergeSplitBookCluster_SharesLockWithMergeService(t *testing.T) {
	real := newConcurrentTestStore(t)
	probe := &dedupSerializeProbe{Store: real}
	ms := merge.NewService(probe)

	const goroutines = 16
	type pair struct{ keep, src string }
	pairs := make([]pair, goroutines)
	for i := range pairs {
		keep := ulid.Make().String()
		src := ulid.Make().String()
		if _, err := real.CreateBook(&database.Book{ID: keep, Title: "Keep", Format: "m4b", FilePath: "/tmp/" + keep + ".m4b"}); err != nil {
			t.Fatalf("CreateBook keep: %v", err)
		}
		if _, err := real.CreateBook(&database.Book{ID: src, Title: "Src", Format: "mp3", FilePath: "/tmp/" + src + ".mp3"}); err != nil {
			t.Fatalf("CreateBook src: %v", err)
		}
		pairs[i] = pair{keep, src}
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func() {
			defer wg.Done()
			p := pairs[i]
			if i%2 == 0 {
				// MergeSplitBookCluster: move files, recompute duration, soft-delete src.
				_, _ = MergeSplitBookCluster(probe, p.keep, []string{p.src}, "")
			} else {
				// merge.Service.MergeBooks: version-group merge (keep is m4b winner).
				_, _ = ms.MergeBooks([]string{p.keep, p.src}, "")
			}
		}()
	}
	wg.Wait()

	if got := probe.maxActive.Load(); got != 1 {
		t.Fatalf("MergeSplitBookCluster did NOT share the merge lock: maxActive=%d, want 1 "+
			"(a split-book merge overlapped a merge.Service merge on the shared probe)", got)
	}
}
