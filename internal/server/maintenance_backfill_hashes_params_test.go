// file: internal/server/maintenance_backfill_hashes_params_test.go
// version: 1.0.0
// guid: 7c3e9a15-4d28-4b60-9f17-e2a8d05b6c43
// last-edited: 2026-10-25

package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// postBackfillHashes POSTs backfill-file-hashes through the real handler and
// waits for its operation to reach a terminal state, failing unless it completed.
// Waiting for completion is what makes a "nothing was recorded" assertion mean
// something: without it, a job that never ran would satisfy it.
func postBackfillHashes(t *testing.T, s *Server, body string) {
	t.Helper()
	w := postMaintenanceJob(t, s, "backfill-file-hashes", body)
	require.Equal(t, http.StatusAccepted, w.Code, "body = %s", w.Body.String())
	var resp struct {
		Data struct {
			OperationID string `json:"operation_id"`
		} `json:"data"`
		OperationID string `json:"operation_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "body = %s", w.Body.String())
	opID := resp.Data.OperationID
	if opID == "" {
		opID = resp.OperationID
	}
	require.NotEmpty(t, opID, "body = %s", w.Body.String())

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		row, err := s.Ops().GetOperationV2(opID)
		require.NoError(t, err)
		if row != nil {
			switch row.Status {
			case "completed":
				return
			case "failed", "canceled", "cancelled", "interrupted":
				msg := ""
				if row.ErrorMessage != nil {
					msg = *row.ErrorMessage
				}
				t.Fatalf("operation %s ended %q: %s", opID, row.Status, msg)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("operation %s did not finish within 30s", opID)
}

// The parameters the Manual Fixes card and the Fingerprint button send reach the
// real job through the real handler, and each does what the owner approved:
//
//   - no body is a preview, so nothing is recorded;
//   - {"dry_run":false,"mark_absent":false} (the Fingerprint button) hashes but
//     records no absence;
//   - {"dry_run":false} (the card's Apply) records it;
//   - {"dry_run":false,"clear_markers":true} undoes it.
//
// The job-level tests drive Run with hand-built params; only this path proves
// the dispatcher keeps mark_absent and clear_markers, including a literal false,
// on their way to RawParamsFromCtx.
func TestRunMaintenanceJob_BackfillFileHashesParamsReachTheRealJob(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	prev := config.AppConfig.RootDir
	config.Mutate(func(c *config.Config) { c.RootDir = root })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.RootDir = prev }) })

	gone := filepath.Join(root, "Gotham", "Batman Year One.m4b") // never created: absent on disk
	book, err := server.store.CreateBook(&database.Book{Title: "Batman: Year One", Format: "m4b", FilePath: gone})
	require.NoError(t, err)
	file := &database.BookFile{BookID: book.ID, FilePath: gone, Format: "m4b"}
	require.NoError(t, server.store.CreateBookFile(file))
	marked := func() bool {
		t.Helper()
		got, gerr := server.store.GetBookFileByID(book.ID, file.ID)
		require.NoError(t, gerr)
		require.NotNil(t, got)
		require.False(t, got.Missing, "this job must never write Missing")
		return got.HashAbsentAt != nil
	}

	postBackfillHashes(t, server, "")
	require.False(t, marked(), "no body is a preview: nothing may be recorded")

	postBackfillHashes(t, server, `{"dry_run":false,"mark_absent":false}`)
	require.False(t, marked(), "mark_absent=false must reach the job and record nothing")

	postBackfillHashes(t, server, `{"dry_run":false}`)
	require.True(t, marked(), "an apply with default params records the absence")

	postBackfillHashes(t, server, `{"dry_run":false,"clear_markers":true}`)
	require.False(t, marked(), "clear_markers must reach the job and undo the recording")
}
