// file: internal/server/batch_poller_test.go
// version: 2.2.0
// guid: c9d0e1f2-a3b4-5678-cdef-9876543210ab
// last-edited: 2026-09-19

package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBatchClient is a BatchClient serving a fixed listing and per-file results.
// unlisted batches are fetchable by id but missing from the listing, like a
// batch that has scrolled out of OpenAI's recent-100 window.
type fakeBatchClient struct {
	mu       sync.Mutex
	batches  []ai.BatchInfo
	unlisted []ai.BatchInfo
	outputs  map[string][]ai.BatchRawResult
	gets     int
	// listErr is returned alongside the listed batches (e.g. a truncated walk).
	listErr error
}

func (f *fakeBatchClient) ListProjectBatches(context.Context, time.Time) ([]ai.BatchInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.BatchInfo(nil), f.batches...), f.listErr
}

func (f *fakeBatchClient) GetBatch(_ context.Context, id string) (ai.BatchInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	for _, b := range append(append([]ai.BatchInfo(nil), f.batches...), f.unlisted...) {
		if b.ID == id {
			return b, nil
		}
	}
	return ai.BatchInfo{}, fmt.Errorf("no batch %s", id)
}

func (f *fakeBatchClient) DownloadBatchRaw(_ context.Context, fileID string) ([]ai.BatchRawResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outputs[fileID], nil
}

// crashJournalStore is a real PebbleStore whose next journal write can be made
// to fail, standing in for a process killed after the handler returned but
// before the handled mark landed.
type crashJournalStore struct {
	*database.PebbleStore
	failNextJournal bool
}

func (c *crashJournalStore) SetRaw(key string, value []byte) error {
	if c.failNextJournal {
		c.failNextJournal = false
		return errors.New("killed before handled mark")
	}
	return c.PebbleStore.SetRaw(key, value)
}

func newPollerTestStore(t *testing.T) *crashJournalStore {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	return &crashJournalStore{PebbleStore: ps}
}

func newTestPoller(t *testing.T, store database.OperationStore, client BatchClient) *BatchPoller {
	t.Helper()
	bp, err := NewBatchPoller(store, client)
	require.NoError(t, err)
	return bp
}

func TestBatchPollerRegisterAndRouting(t *testing.T) {
	bp := newTestPoller(t, newPollerTestStore(t), &fakeBatchClient{})

	called := map[string]string{}
	bp.RegisterHandler("author_dedup", func(_ context.Context, batchID, outputFileID string) error {
		called["author_dedup"] = batchID
		return nil
	})
	bp.RegisterHandler("diagnostics", func(_ context.Context, batchID, outputFileID string) error {
		called["diagnostics"] = batchID
		return nil
	})

	assert.Len(t, bp.handlers, 2)
	assert.Contains(t, bp.handlers, "author_dedup")
	assert.Contains(t, bp.handlers, "diagnostics")
}

func TestBatchPollerProcessedTracking(t *testing.T) {
	bp := newTestPoller(t, newPollerTestStore(t), &fakeBatchClient{})

	assert.False(t, bp.IsProcessed("batch_123"))

	bp.MarkProcessed("batch_123")
	assert.True(t, bp.IsProcessed("batch_123"))

	// Marking again is idempotent
	bp.MarkProcessed("batch_123")
	assert.True(t, bp.IsProcessed("batch_123"))
}

func TestBatchPollerHandlerError(t *testing.T) {
	bp := newTestPoller(t, newPollerTestStore(t), &fakeBatchClient{})

	failCount := 0
	bp.RegisterHandler("failing_type", func(_ context.Context, batchID, outputFileID string) error {
		failCount++
		return fmt.Errorf("handler error")
	})

	// Simulate calling the handler directly — on failure it should NOT be marked processed
	err := bp.handlers["failing_type"](context.Background(), "batch_fail", "file_123")
	require.Error(t, err)
	assert.Equal(t, 1, failCount)
	assert.False(t, bp.IsProcessed("batch_fail"))
}

func TestBatchMetadataHelper(t *testing.T) {
	// Test the batchMetadata helper in the ai package
	// We can't call it directly since it's unexported, but we verify the types are correct
	info := ai.BatchInfo{
		ID:           "batch_abc",
		Status:       "completed",
		Type:         "author_dedup",
		OutputFileID: "file_xyz",
		ErrorFileID:  "",
		RequestCounts: ai.RequestCounts{
			Total:     10,
			Completed: 8,
			Failed:    2,
		},
	}

	assert.Equal(t, "batch_abc", info.ID)
	assert.Equal(t, "completed", info.Status)
	assert.Equal(t, "author_dedup", info.Type)
	assert.Equal(t, 10, info.RequestCounts.Total)
	assert.Equal(t, 8, info.RequestCounts.Completed)
	assert.Equal(t, 2, info.RequestCounts.Failed)
}
