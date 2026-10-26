// file: internal/server/maintenance_backfill_hashes_test.go
// version: 2.0.0
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
func postBackfillHashes(t *testing.T, s *Server, body string) string {
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
				return opID
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
	return ""
}

// Through the real handler and the real job: no body is a preview that hashes
// and stores nothing, an apply stores the hash of a file that exists, and a file
// that does not exist is reported, counted, and written nowhere (no hash, and
// not Missing).
func TestRunMaintenanceJob_BackfillFileHashesReportsNotFoundAndWritesNothingForIt(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	prev := config.AppConfig.RootDir
	config.Mutate(func(c *config.Config) { c.RootDir = root })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.RootDir = prev }) })

	gone := filepath.Join(root, "Gotham", "Batman Year One.m4b") // never created
	here := filepath.Join(root, "Gotham", "Batman The Long Halloween.m4b")
	require.NoError(t, os.WriteFile(here, []byte("long halloween bytes"), 0o644))
	book, err := server.store.CreateBook(&database.Book{Title: "Batman: Year One", Format: "m4b", FilePath: gone})
	require.NoError(t, err)
	goneFile := &database.BookFile{BookID: book.ID, FilePath: gone, Format: "m4b"}
	hereFile := &database.BookFile{BookID: book.ID, FilePath: here, Format: "m4b"}
	require.NoError(t, server.store.CreateBookFile(goneFile))
	require.NoError(t, server.store.CreateBookFile(hereFile))
	row := func(id string) *database.BookFile {
		t.Helper()
		got, gerr := server.store.GetBookFileByID(book.ID, id)
		require.NoError(t, gerr)
		require.NotNil(t, got)
		return got
	}

	opID := postBackfillHashes(t, server, "")
	require.Empty(t, row(hereFile.ID).FileHash, "no body is a preview: nothing is stored")
	report := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	b, err := os.ReadFile(report)
	require.NoError(t, err, "a preview writes the report too")
	require.Contains(t, string(b), "not-found\t"+goneFile.ID+"\t"+book.ID+"\t"+gone)

	postBackfillHashes(t, server, `{"dry_run":false}`)
	require.NotEmpty(t, row(hereFile.ID).FileHash, "an apply stores the hash of the file that exists")
	require.Empty(t, row(goneFile.ID).FileHash, "nothing is stored for the file that is gone")
	require.False(t, row(goneFile.ID).Missing, "this job never flags a row missing")
}
