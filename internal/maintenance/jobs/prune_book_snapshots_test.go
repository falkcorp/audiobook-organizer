// file: internal/maintenance/jobs/prune_book_snapshots_test.go
// version: 1.1.1
// guid: 8bb33b18-e9eb-4210-87e4-f87bb86bf4ae
// last-edited: 2026-09-02

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mirrors the unexported defaultKeepCount so the test states the expected value
// explicitly rather than reading it from the code under test.
const defaultKeepCountForTest = 10

func TestPruneBookSnapshotsJob_Registered(t *testing.T) {
	assertJobRegistered(t, "prune-book-snapshots")
}

// CanResume() and Policy().ResumePolicy must agree: since the v1 op minter was
// retired, Policy() is the only thing that resumes anything, so a job claiming
// CanResume() with ResumeDrop would simply never resume and nothing would report
// it. This job checkpoints nothing, so both must say "no".
func TestPruneBookSnapshotsJob_PolicyAgreesWithCanResume(t *testing.T) {
	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	p := j.Policy()
	assert.False(t, j.CanResume(), "the job does not checkpoint")
	assert.Equal(t, opsregistry.ResumeDrop, p.ResumePolicy,
		"a job that cannot resume must not be restarted or requeued")
	assert.Positive(t, p.Timeout, "a non-positive timeout means the registry default")
	assert.NotEmpty(t, p.Capabilities, "the job reads and writes the library")
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
	maps.Copy(out, r.pruned)
	return out
}

// keep_count must ride the LIVE params channel (WithRawParams), which
// maintenance_job_op.go populates from the v2 operation row.
//
// The first version of this test stubbed GetOperationParamsFunc instead. It
// passed, and the feature was still completely inert: GetOperationParams reads
// opstate:<opID>:params, which only operations.SaveParams writes, and the
// maintenance dispatcher has not called SaveParams since the v1 op minter was
// retired (#2784). The stub was a hook production never populates -- the test
// asserted against a channel that did not exist.
func TestPruneBookSnapshotsJob_HonoursKeepCountFromLiveParams(t *testing.T) {
	rec := &pruneRecorder{}
	store := &database.MockStore{
		ListBookIDsFunc:        func() ([]string, error) { return []string{"b1"}, nil },
		CountBookSnapshotsFunc: func(id string) (int, error) { return 100, nil },
		// Deliberately NOT stubbed: if the job ever regresses to reading
		// GetOperationParams, it gets the zero value and falls back to the
		// default, which this assertion catches.
		PruneBookVersionsFunc: func(id string, keep int) (int, error) {
			rec.record(id, keep)
			return 97, nil
		},
	}

	j, err := maintenance.Get("prune-book-snapshots")
	require.NoError(t, err)
	ctx := maintenance.WithRawParams(context.Background(), json.RawMessage(`{"keep_count":3}`))
	require.NoError(t, j.Run(ctx, store, &noopReporter{}, false))

	assert.Equal(t, map[string]int{"b1": 3}, rec.snapshot(),
		"keep_count from the live params channel must reach PruneBookSnapshots")
}

// A zero or negative keep_count must NOT mean "keep nothing" -- that would turn
// a retention tweak into a full history wipe. This repo has already taken a
// production outage from a 0 config value being treated as load-bearing.
func TestPruneBookSnapshotsJob_ZeroKeepCountFallsBackToDefault(t *testing.T) {
	for _, body := range []string{`{"keep_count":0}`, `{"keep_count":-5}`, `{}`, `not json`} {
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
		ctx := maintenance.WithRawParams(context.Background(), json.RawMessage(body))
		require.NoError(t, j.Run(ctx, store, &noopReporter{}, false))
		assert.Equal(t, map[string]int{"b1": defaultKeepCountForTest}, rec.snapshot(),
			"params %q must fall back to the default, never to 0", body)
	}
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
