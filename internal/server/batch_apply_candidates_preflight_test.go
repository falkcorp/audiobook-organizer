// file: internal/server/batch_apply_candidates_preflight_test.go
// version: 1.0.0
// guid: 5d2a8e41-9c07-4b3f-a6e2-71f0c4d98b35
// last-edited: 2026-09-13
//
// The rename preflight on /metadata/batch-apply-candidates: the handler writes
// the database and then queues the file job that renames, which is the order
// of the 2026-09-13 failure. Driven through the real handler and a real
// *metafetch.Service, so the preflight that runs is the production one.

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// preflightFixture is one book with two files under the library root, and an
// op result whose "matched" candidate passes the certainty gate. The rename
// plan is made to fail with a malformed file-naming format spec, the plan
// error reachable with a mock store (see apply_rename_collision_test.go).
type preflightFixture struct {
	store *database.MockStore
	files []string

	mu            sync.Mutex
	updates       int
	appliedResult int
}

func newPreflightFixture(t *testing.T, store *database.MockStore, fileNamingPattern string) *preflightFixture {
	t.Helper()
	withBulkApplyCapServer(t, 100)
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	root := t.TempDir()
	// All config is set before the handler runs and not touched again: the
	// handler's workers read it concurrently.
	config.AppConfig.RootDir = root
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.AutoWriteTagsOnApply = false
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = fileNamingPattern

	dir := filepath.Join(root, "Someone", "Old")
	a, b := filepath.Join(dir, "1.mp3"), filepath.Join(dir, "2.mp3")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(a, []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("two"), 0o644))

	dur := 36000
	book := database.Book{ID: "b1", Title: "Old", FilePath: dir, Author: &database.Author{ID: 1, Name: "Someone"}, Duration: &dur}
	cand := metafetch.MetadataCandidate{Title: "Old", Author: "Someone", Score: 0.95, DurationSec: dur, Source: "test"}
	cr := CandidateResult{Status: "matched", Candidate: &cand}
	cr.Book.Title, cr.Book.Author = "Old", "Someone"
	resultJSON, err := json.Marshal(cr)
	require.NoError(t, err)

	f := &preflightFixture{store: store, files: []string{a, b}}
	store.GetOperationResultsFunc = func(string) ([]database.OperationResult, error) {
		return []database.OperationResult{{OperationID: "op", BookID: "b1", ResultJSON: string(resultJSON), Status: "matched"}}, nil
	}
	store.GetBookByIDFunc = func(string) (*database.Book, error) { c := book; return &c, nil }
	store.GetBookFilesFunc = func(string) ([]database.BookFile, error) {
		return []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: a, Format: "mp3", TrackNumber: 1},
			{ID: "f2", BookID: "b1", FilePath: b, Format: "mp3", TrackNumber: 2},
		}, nil
	}
	return f
}

// countWrites records the two writes the apply path makes: the book update
// and the "applied" op-result row.
func (f *preflightFixture) countWrites() {
	f.store.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) {
		f.mu.Lock()
		f.updates++
		f.mu.Unlock()
		return b, nil
	}
	f.store.CreateOperationResultFunc = func(r *database.OperationResult) error {
		if r.Status == "applied" {
			f.mu.Lock()
			f.appliedResult++
			f.mu.Unlock()
		}
		return nil
	}
}

// stoppedPool is a pool whose Submit drops every job: the gate sees a pool
// (so a file sequel is expected) but no background file job runs, so the test
// never races the job against its own config restore.
func stoppedPool() *FileIOPool {
	p := NewFileIOPool(1)
	p.Stop()
	return p
}

func runBatchApplyCandidates(t *testing.T, store *database.MockStore, pool *FileIOPool) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &Server{store: store, metadataFetchService: metafetch.NewService(store), scanStandDownGateOverride: &sdGate{}, fileIOPool: pool}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/metadata/batch-apply-candidates",
		strings.NewReader(`{"operation_id":"op","book_ids":["b1"],"dry_run":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchApplyCandidates(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return w
}

// The 2026-09-13 shape: the rename after the apply is known to fail. The book
// is refused before ANY store write -- no metadata, no "applied" op-result row
// -- and reported as blocked with the shared reason, not as an error.
func TestBatchApplyCandidates_RenamePreflightRefusesBeforeAnyWrite(t *testing.T) {
	store := failOnWriteStore(t) // every write method fails the test
	f := newPreflightFixture(t, store, "{title} - {track:x}")

	w := runBatchApplyCandidates(t, store, stoppedPool())

	body := w.Body.String()
	require.Contains(t, body, `"applied":0`, body)
	require.Contains(t, body, `"blocked_count":1`, body)
	require.Contains(t, body, metafetch.ApplyRefusedReasonFileWorkWouldFail, body)
	require.Contains(t, body, `"error_count":0`, body)
	for _, p := range f.files {
		_, err := os.Stat(p)
		require.NoError(t, err, "preflight moved %s", p)
	}
}

// A passing preflight changes nothing: the book is applied and its "applied"
// op-result row written, as before.
func TestBatchApplyCandidates_PassingPreflightApplies(t *testing.T) {
	store := &database.MockStore{}
	f := newPreflightFixture(t, store, "{title} - {track:02d}")
	f.countWrites()

	w := runBatchApplyCandidates(t, store, stoppedPool())

	body := w.Body.String()
	require.Contains(t, body, `"applied":1`, body)
	require.NotContains(t, body, metafetch.ApplyRefusedReasonFileWorkWouldFail, body)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Positive(t, f.updates, "the apply must write the book")
	require.Equal(t, 1, f.appliedResult, "the applied op-result row must be written")
}

// With no file-IO pool no file job is queued, so no rename follows and the
// preflight is not consulted: the same would-fail plan still applies.
func TestBatchApplyCandidates_NoFilePoolSkipsRenamePreflight(t *testing.T) {
	store := &database.MockStore{}
	f := newPreflightFixture(t, store, "{title} - {track:x}")
	f.countWrites()

	w := runBatchApplyCandidates(t, store, nil)

	body := w.Body.String()
	require.Contains(t, body, `"applied":1`, body)
	require.NotContains(t, body, metafetch.ApplyRefusedReasonFileWorkWouldFail, body)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Positive(t, f.updates)
	require.Equal(t, 1, f.appliedResult)
}
