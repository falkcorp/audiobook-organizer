// file: internal/maintenance/jobs/retention_operations_single_pass_test.go
// version: 1.1.0
// guid: 3c81f27a-5d94-4e60-b1a8-7f26c0d95ab3
// last-edited: 2026-08-17

package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// deleteOldOperations used to walk ListOperations in 500-row pages. Two things
// were wrong with that, and each test below pins one of them.
//
// PebbleStore.ListOperations reads the ENTIRE "operation:" prefix into memory,
// unmarshals every row and sorts the whole slice, then returns one page out of
// the result. Paging over a method with that shape re-does the full scan once
// per page: N/500 scans of N rows. Production held 10,163 operations when this
// was written, so one retention run did 21 full scans — roughly 213,000
// unmarshals to collect a set that one pass yields.
//
// The second problem is a correctness one. Operations come back newest-first,
// so an operation created while phase 1 is paging shifts every existing row to
// a HIGHER index. A fixed, increasing offset sequence read over a right-shifting
// slice re-reads rows it already saw, so the same ID lands in the delete list
// twice. Deleting an already-deleted key is a no-op in Pebble, so nothing is
// lost — but the count the job reports counts it twice, and the summary then
// disagrees with what actually happened.
//
// (The mirror-image hazard, a left shift skipping rows, needs a concurrent
// DELETION. The two-phase collect-then-delete design already rules that out,
// which is exactly what the function's doc comment says it is for.)

// pagingProbeStore serves ListOperations from a slice and records how it was
// called. Optionally it simulates another part of the system creating an
// operation between calls, which is what makes the window shift.
type pagingProbeStore struct {
	*database.MockStore

	ops           []database.Operation
	calls         int
	limitsSeen    []int
	insertPerCall bool
	deleted       []string
}

func newPagingProbeStore(ops []database.Operation, insertPerCall bool) *pagingProbeStore {
	s := &pagingProbeStore{
		MockStore:     &database.MockStore{},
		ops:           ops,
		insertPerCall: insertPerCall,
	}

	s.MockStore.ListOperationsFunc = func(limit, offset int) ([]database.Operation, int, error) {
		// A concurrent creation lands at the front, because the listing is
		// ordered newest-first. This is what pushes already-read rows to
		// higher indices.
		if s.insertPerCall && s.calls > 0 {
			fresh := database.Operation{
				ID:        fmt.Sprintf("concurrent-%d", s.calls),
				CreatedAt: time.Now(),
			}
			s.ops = append([]database.Operation{fresh}, s.ops...)
		}
		s.calls++
		s.limitsSeen = append(s.limitsSeen, limit)

		total := len(s.ops)
		if offset >= total {
			return nil, total, nil
		}
		// Mirror PebbleStore.ListOperations: limit <= 0 means "no limit".
		// A fake that does not mirror the real sentinel cannot validate code
		// that relies on it.
		end := total
		if limit > 0 {
			end = offset + limit
			if end > total {
				end = total
			}
		}
		return s.ops[offset:end], total, nil
	}

	s.MockStore.DeleteOperationWithLogsFunc = func(id string) error {
		s.deleted = append(s.deleted, id)
		return nil
	}
	return s
}

// oldOperations builds n operations that are all comfortably older than cutoff,
// ordered newest-first the way ListOperations returns them.
func oldOperations(n int, cutoff time.Time) []database.Operation {
	ops := make([]database.Operation, 0, n)
	for i := 0; i < n; i++ {
		ops = append(ops, database.Operation{
			ID: fmt.Sprintf("old-%04d", i),
			// Each is a minute older than the previous, so the slice is
			// already in newest-first order.
			CreatedAt: cutoff.Add(-time.Duration(i+1) * time.Minute),
		})
	}
	return ops
}

// TestExpiredOperationIDs_ScansOnce is the amplification guard. The fixture is
// deliberately larger than the old 500-row page size, so a paging
// implementation cannot satisfy it: it would need at least two calls.
//
// The listing call stayed inside expiredOperationIDs rather than moving up into
// Run when the helper was split, precisely so this assertion keeps a narrow
// function to point at. Hoisting it would have left the single-pass guarantee
// testable only through Run, which needs the full database.Store.
func TestExpiredOperationIDs_ScansOnce(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	const total = 1200 // > 2x the old pageSize of 500
	store := newPagingProbeStore(oldOperations(total, cutoff), false)

	ids, err := expiredOperationIDs(context.Background(), store, cutoff)
	if err != nil {
		t.Fatalf("expiredOperationIDs: %v", err)
	}
	if len(ids) != total {
		t.Errorf("eligible count: got %d, want %d", len(ids), total)
	}
	if store.calls != 1 {
		t.Errorf("ListOperations must be called exactly once for a full scan; got %d calls with limits %v",
			store.calls, store.limitsSeen)
	}
	// The single call has to ask for everything, not for a page.
	if len(store.limitsSeen) > 0 && store.limitsSeen[0] > 0 {
		t.Errorf("the scan must request an unbounded listing (limit <= 0); got limit=%d", store.limitsSeen[0])
	}
}

// TestExpiredOperationIDs_ConcurrentCreateDoesNotDoubleCount pins the
// correctness half. With an operation created between calls, a paging
// implementation re-reads rows and reports more deletions than there are
// operations.
//
// It chains both halves of the split — scan, then delete what the scan
// returned — because the bug being guarded is a DISAGREEMENT between the
// reported count and the deletions actually made. Asserting only one side
// would not see it.
func TestExpiredOperationIDs_ConcurrentCreateDoesNotDoubleCount(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	const total = 1200
	store := newPagingProbeStore(oldOperations(total, cutoff), true)

	ids, err := expiredOperationIDs(context.Background(), store, cutoff)
	if err != nil {
		t.Fatalf("expiredOperationIDs: %v", err)
	}
	count, err := deleteOperations(context.Background(), store, ids)
	if err != nil {
		t.Fatalf("deleteOperations: %v", err)
	}

	// The freshly-created operations are newer than the cutoff, so they are
	// never eligible; the eligible set is exactly the original rows.
	if count != total {
		t.Errorf("reported count: got %d, want %d (a repeated row inflates this)", count, total)
	}

	seen := make(map[string]int, len(store.deleted))
	for _, id := range store.deleted {
		seen[id]++
	}
	var dupes []string
	for id, n := range seen {
		if n > 1 {
			dupes = append(dupes, fmt.Sprintf("%s x%d", id, n))
		}
	}
	if len(dupes) > 0 {
		t.Errorf("no operation may be queued for deletion twice; duplicates: %v", dupes)
	}
	if len(store.deleted) != total {
		t.Errorf("delete calls: got %d, want %d", len(store.deleted), total)
	}
}

// TestExpiredOperationIDs_RespectsCutoffAcrossLargeSet guards the premise of
// the two tests above: with a mixed set, only the rows older than the cutoff
// may be collected. Without this, "collected everything" would satisfy the
// count assertions for the wrong reason.
func TestExpiredOperationIDs_RespectsCutoffAcrossLargeSet(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)

	var ops []database.Operation
	const recent, stale = 700, 800
	// Newest-first: the recent ones come first.
	for i := 0; i < recent; i++ {
		ops = append(ops, database.Operation{
			ID:        fmt.Sprintf("keep-%04d", i),
			CreatedAt: cutoff.Add(time.Duration(i+1) * time.Minute),
		})
	}
	ops = append(ops, oldOperations(stale, cutoff)...)

	store := newPagingProbeStore(ops, false)
	ids, err := expiredOperationIDs(context.Background(), store, cutoff)
	if err != nil {
		t.Fatalf("expiredOperationIDs: %v", err)
	}
	count, err := deleteOperations(context.Background(), store, ids)
	if err != nil {
		t.Fatalf("deleteOperations: %v", err)
	}
	if count != stale {
		t.Errorf("eligible count: got %d, want %d", count, stale)
	}
	for _, id := range store.deleted {
		if len(id) < 4 || id[:4] != "old-" {
			t.Errorf("deleted an operation newer than the cutoff: %s", id)
		}
	}
}
