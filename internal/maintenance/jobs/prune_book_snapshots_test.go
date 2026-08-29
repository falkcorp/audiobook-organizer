// file: internal/maintenance/jobs/prune_book_snapshots_test.go
// version: 1.0.0
// guid: 8bb33b18-e9eb-4210-87e4-f87bb86bf4ae
// last-edited: 2026-08-29

package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPruneBookSnapshotsJob_Registered(t *testing.T) {
	assertJobRegistered(t, "prune-book-snapshots")
}

// The policy is load-bearing, not decorative: keep_count is read back off the
// operation row, so a ResumePolicy that mints a fresh operation id would silently
// drop the retention depth on a restart.
func TestPruneBookSnapshotsJob_PolicyKeepsParamsAcrossRestart(t *testing.T) {
	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	p := j.Policy()
	assert.Equal(t, opsregistry.ResumeRestart, p.ResumePolicy,
		"ResumeRestart re-dispatches the same row so keep_count survives; Requeue/Drop lose it")
	assert.NotEmpty(t, p.ConcurrencyKey,
		"two concurrent runs would issue overlapping deletes over the same book_ver: prefix")
}

// recordingStore captures prune calls race-safely: Run fans out over NumCPU
// workers, so the hooks are invoked from many goroutines at once.
type pruneRecorder struct {
	mu     sync.Mutex
	pruned map[string]int // book id -> keepCount it was asked to keep
}

func (r *pruneRecorder) record(id string, keep int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pruned == nil {
		r.pruned = map[string]int{}
	}
	r.pruned[id] = keep
}

func (r *pruneRecorder) snapshot() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for k, v := range r.pruned {
		out[k] = v
	}
	return out
}

func TestPruneBookSnapshotsJob_HonoursKeepCountFromOperationParams(t *testing.T) {
	rec := &pruneRecorder{}
	store := &database.MockStore{
		ListBookIDsFunc:            func() ([]string, error) { return []string{"b1"}, nil },
		CountBookSnapshotsFunc:     func(id string) (int, error) { return 100, nil },
		GetOperationParamsFunc:     func(opID string) ([]byte, error) { return []byte(`{"keep_count":3}`), nil },
		PruneBookVersionsFunc: func(id string, keep int) (int, error) {
			rec.record(id, keep)
			return 97, nil
		},
	}

	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	ctx := maintenance.WithOperationID(context.Background(), "op-1")
	require.NoError(t, j.Run(ctx, store, &noopReporter{}, false))

	assert.Equal(t, map[string]int{"b1": 3}, rec.snapshot(),
		"keep_count from the operation row must reach PruneBookSnapshots, not the default")
}

func TestPruneBookSnapshotsJob_DefaultsWhenNoParams(t *testing.T) {
	rec := &pruneRecorder{}
	store := &database.MockStore{
		ListBookIDsFunc:        func() ([]string, error) { return []string{"b1"}, nil },
		CountBookSnapshotsFunc: func(id string) (int, error) { return 100, nil },
		PruneBookVersionsFunc: func(id string, keep int) (int, error) {
			rec.record(id, keep)
			return 90, nil
		},
	}
	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.Equal(t, map[string]int{"b1": 10}, rec.snapshot(), "default keep_count is 10")
}

func TestPruneBookSnapshotsJob_DryRunDeletesNothing(t *testing.T) {
	rec := &pruneRecorder{}
	store := &database.MockStore{
		ListBookIDsFunc:        func() ([]string, error) { return []string{"b1", "b2"}, nil },
		CountBookSnapshotsFunc: func(id string) (int, error) { return 50, nil },
		PruneBookVersionsFunc: func(id string, keep int) (int, error) {
			rec.record(id, keep)
			return 40, nil
		},
	}
	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, true))
	assert.Empty(t, rec.snapshot(), "dry_run must not call PruneBookSnapshots at all")
}

func TestPruneBookSnapshotsJob_SkipsBooksAtOrBelowKeepCount(t *testing.T) {
	rec := &pruneRecorder{}
	counts := map[string]int{"under": 9, "exact": 10, "over": 11}
	store := &database.MockStore{
		ListBookIDsFunc:        func() ([]string, error) { return []string{"under", "exact", "over"}, nil },
		CountBookSnapshotsFunc: func(id string) (int, error) { return counts[id], nil },
		PruneBookVersionsFunc: func(id string, keep int) (int, error) {
			rec.record(id, keep)
			return 1, nil
		},
	}
	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))

	got := rec.snapshot()
	assert.Contains(t, got, "over", "a book with more snapshots than keep_count must be pruned")
	assert.NotContains(t, got, "exact", "a book exactly at keep_count has nothing to delete")
	assert.NotContains(t, got, "under", "a book below keep_count has nothing to delete")
}

// A prune failure must surface. A library-wide job that swallows every error and
// returns nil reports success while deleting nothing.
func TestPruneBookSnapshotsJob_ReportsFailures(t *testing.T) {
	store := &database.MockStore{
		ListBookIDsFunc:        func() ([]string, error) { return []string{"b1"}, nil },
		CountBookSnapshotsFunc: func(id string) (int, error) { return 100, nil },
		PruneBookVersionsFunc: func(id string, keep int) (int, error) {
			return 0, errors.New("pebble: write failed")
		},
	}
	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	err = j.Run(context.Background(), store, &noopReporter{}, false)
	require.Error(t, err, "a run whose deletes all failed must not report success")
	assert.Contains(t, err.Error(), "1 failures")
}

// The fan-out must cover every book, not just the first worker's share.
func TestPruneBookSnapshotsJob_VisitsEveryBook(t *testing.T) {
	const n = 500
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("b%d", i)
	}
	rec := &pruneRecorder{}
	store := &database.MockStore{
		ListBookIDsFunc:        func() ([]string, error) { return ids, nil },
		CountBookSnapshotsFunc: func(id string) (int, error) { return 20, nil },
		PruneBookVersionsFunc: func(id string, keep int) (int, error) {
			rec.record(id, keep)
			return 10, nil
		},
	}
	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))
	assert.Len(t, rec.snapshot(), n, "every book must be visited exactly once")
}
