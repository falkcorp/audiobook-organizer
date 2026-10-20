// file: internal/server/metadata_refresh_scan_test.go
// version: 1.0.1
// guid: 3c9e5a71-84d2-4f06-b1a7-6d20e8c4f953
// last-edited: 2026-10-20

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedRefreshScanBooks(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		_, err := database.GetGlobalStore().CreateBook(&database.Book{
			Title:    "Refresh Scan " + string(rune('A'+i)),
			FilePath: "/tmp/refresh-scan-" + string(rune('a'+i)) + ".m4b",
			Format:   "m4b",
		})
		require.NoError(t, err)
	}
}

func hasCompletionLine(p *recordingProgress) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.lines {
		if strings.HasPrefix(l.msg, "Found ") {
			return true
		}
	}
	for _, m := range p.messages {
		if strings.HasPrefix(m, "Found ") {
			return true
		}
	}
	return false
}

// The per-book checkpoint is what lets a user cancel, a shutdown, the watchdog
// or a lost scan stand-down stop the scan mid-library. A context that is
// already cancelled must end the run with that cancellation, never with the
// "Found N books" completion.
func TestRunMetadataRefreshScan_CancelledContextStopsBeforeCompleting(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	seedRefreshScanBooks(t, 3)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	progress := &recordingProgress{}
	err := server.runMetadataRefreshScan(ctx, progress)

	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, hasCompletionLine(progress), "a cancelled scan must not report completion")
}

// The cancellation cause (a lost stand-down hold cancels with
// ErrScanStandDownLost) must reach the caller, not a bare context.Canceled.
func TestRunMetadataRefreshScan_CancelCausePropagates(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	seedRefreshScanBooks(t, 3)

	cause := errors.New("scan stand-down lost")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	progress := &recordingProgress{}
	err := server.runMetadataRefreshScan(ctx, progress)

	require.ErrorIs(t, err, cause)
	assert.False(t, hasCompletionLine(progress))
}

func TestRunMetadataRefreshScan_CompletesWhenNotCancelled(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	seedRefreshScanBooks(t, 3)

	progress := &recordingProgress{}
	require.NoError(t, server.runMetadataRefreshScan(t.Context(), progress))
	assert.True(t, hasCompletionLine(progress))
}
