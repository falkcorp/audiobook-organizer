// file: internal/server/batch_poller_aiscan_test.go
// version: 1.1.0
// guid: 4a008296-7c1f-4478-bfaa-bbcba3a27e55
// last-edited: 2026-09-19

package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TestBatchPollerRoutesAuthorScanBatches pins the dispatch that makes
// batch-mode ai.author-scan results collectable at all. The scan's batches are
// tagged author_review (groups_scan) and author_dedup (full_scan); for as long
// as PollBatchPhases was registered only under "pipeline" — a type no code
// creates — the poller logged "no handler" for every one of them.
func TestBatchPollerRoutesAuthorScanBatches(t *testing.T) {
	s := &Server{batchPoller: newTestPoller(t, newPollerTestStore(t), &fakeBatchClient{})}
	s.registerBatchPollerHandlers()

	for _, typ := range []string{"author_review", "author_dedup"} {
		h, ok := s.batchPoller.handlers[typ]
		require.True(t, ok, "no poller handler for %s batches", typ)
		require.NotNil(t, h)
	}
}

// inProgressLLM is an aiscan.LLM whose every batch is still running.
type inProgressLLM struct{}

func (inProgressLLM) ReviewAuthorDuplicates(context.Context, []ai.AuthorDedupInput) ([]ai.AuthorDedupSuggestion, error) {
	return nil, nil
}
func (inProgressLLM) DiscoverAuthorDuplicates(context.Context, []ai.AuthorDiscoveryInput) ([]ai.AuthorDiscoverySuggestion, error) {
	return nil, nil
}
func (inProgressLLM) CreateBatchAuthorReview(context.Context, []ai.AuthorDedupInput, map[string]string) (string, error) {
	return "", nil
}
func (inProgressLLM) CreateBatchAuthorDedup(context.Context, []ai.AuthorDiscoveryInput, map[string]string) (string, error) {
	return "", nil
}
func (inProgressLLM) CheckBatchStatus(context.Context, string) (string, string, error) {
	return "in_progress", "", nil
}
func (inProgressLLM) CancelBatch(context.Context, string) error { return nil }
func (inProgressLLM) DownloadBatchGroupsResults(context.Context, string) ([]ai.AuthorDedupSuggestion, error) {
	return nil, nil
}
func (inProgressLLM) DownloadBatchResults(context.Context, string) ([]ai.AuthorDiscoverySuggestion, error) {
	return nil, nil
}

// TestAuthorScanHandlerDoesNotJournalUncollectedBatch: the poller journals a
// batch as handled whenever its handler returns nil. The author-scan handler
// must therefore fail while the batch's phase is still owed results, or the
// batch drops out of the poller's view for good.
func TestAuthorScanHandlerDoesNotJournalUncollectedBatch(t *testing.T) {
	scanStore, err := database.NewAIScanStore(filepath.Join(t.TempDir(), "aiscan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = scanStore.Close() })
	pm := aiscan.NewPipelineManager(scanStore, fakeAIScanMainStore{}, inProgressLLM{})
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	require.NoError(t, scanStore.UpdateScanStatus(scan.ID, "scanning"))
	require.NoError(t, scanStore.UpdatePhaseStatus(scan.ID, "full_scan", "submitted", "batch_b1"))

	s := &Server{batchPoller: newTestPoller(t, newPollerTestStore(t), &fakeBatchClient{}), pipelineManager: pm}
	s.registerBatchPollerHandlers()

	err = s.batchPoller.handlers["author_dedup"](context.Background(), "batch_b1", "file_b1")
	require.Error(t, err, "a batch whose phase is still submitted is not handled")
}
