// file: internal/metafetch/dedup_after_apply_test.go
// version: 1.0.0
// guid: 2f6b9a3d-1e48-4c75-9d02-8a3c5e7b1f64
// last-edited: 2026-10-16

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type recordingEnqueuer struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingEnqueuer) EnqueueOp(_ context.Context, defID string, params any, _ ...registry.EnqueueOption) (string, error) {
	raw, _ := json.Marshal(params)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, defID+" "+string(raw))
	return "", nil
}

func (r *recordingEnqueuer) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// applyDedupProbe wires an engine whose CheckBook reports the book ID and
// stops, plus a recording enqueuer, onto svc.
func applyDedupProbe(t *testing.T, svc *Service) (<-chan string, *recordingEnqueuer) {
	t.Helper()
	checked := make(chan string, 4)
	engine, err := dedup.NewEngine(nil, &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			checked <- id
			return nil, errors.New("probe: stop here")
		},
	}, nil, nil, nil, unified.DefaultScoreConfig())
	require.NoError(t, err)
	svc.SetDedupEngine(engine)
	enq := &recordingEnqueuer{}
	svc.SetOpEnqueuer(enq)
	return checked, enq
}

func TestRenameOnly_DedupViaScheduler_EnqueuesOpAndStartsNoGoroutine(t *testing.T) {
	fx, svc := newRenameFixture(t)
	config.AppConfig.Dedup.OnImportViaScheduler = true
	checked, enq := applyDedupProbe(t, svc)

	stale := fx.stored
	require.NoError(t, svc.RunApplyPipelineRenameOnly(context.Background(), "b1", &stale))

	require.Equal(t, []string{`dedup.check-book {"book_id":"b1"}`}, enq.snapshot())
	select {
	case id := <-checked:
		t.Fatalf("CheckBook ran for %s in a goroutine; the op must replace it", id)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRenameOnly_DedupFlagOff_RunsGoroutineAndEnqueuesNothing(t *testing.T) {
	fx, svc := newRenameFixture(t)
	config.AppConfig.Dedup.OnImportViaScheduler = false
	checked, enq := applyDedupProbe(t, svc)

	stale := fx.stored
	require.NoError(t, svc.RunApplyPipelineRenameOnly(context.Background(), "b1", &stale))

	select {
	case id := <-checked:
		require.Equal(t, "b1", id)
	case <-time.After(5 * time.Second):
		t.Fatal("CheckBook never ran on the flag-off path")
	}
	require.Empty(t, enq.snapshot())
}
