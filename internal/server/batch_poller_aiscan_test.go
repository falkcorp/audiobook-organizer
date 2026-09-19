// file: internal/server/batch_poller_aiscan_test.go
// version: 1.0.0
// guid: 4a008296-7c1f-4478-bfaa-bbcba3a27e55
// last-edited: 2026-09-19

package server

import (
	"testing"

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
