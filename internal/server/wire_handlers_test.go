// file: internal/server/wire_handlers_test.go
// version: 1.0.0
// guid: 1a9dc63b-e7ec-4a27-ad84-f117b891a41f
// last-edited: 2026-09-11

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAIScanMainStore is the aiscan.Store the pipeline reads authors from. The
// test never launches phase work, so an empty library is all it needs.
type fakeAIScanMainStore struct{}

var _ aiscan.Store = fakeAIScanMainStore{}

func (fakeAIScanMainStore) GetAllAuthors() ([]database.Author, error) { return nil, nil }
func (fakeAIScanMainStore) GetAuthorByID(int) (*database.Author, error) {
	return nil, nil
}
func (fakeAIScanMainStore) GetAllAuthorBookCounts() (map[int]int, error) {
	return map[int]int{}, nil
}
func (fakeAIScanMainStore) GetBooksByAuthorIDWithRoleCore(int) ([]database.BookCore, error) {
	return nil, nil
}

// attachSignalSink closes attached on the first progress report. RunScan
// reports "Re-attached ..." only AFTER it has registered the scan's cancel
// func, so a closed attached means CancelScan now has something to cancel.
type attachSignalSink struct {
	once     sync.Once
	attached chan struct{}
}

func (s *attachSignalSink) UpdateProgress(int, int, string) error {
	s.once.Do(func() { close(s.attached) })
	return nil
}

// TestWireHandlers_CancelOperationV2ReachesAIScanPipeline guards the
// handlers.WithAIScanCancellation call in wireHandlers.
//
// The handler's AI-scan branch is covered by
// TestCancelOperationV2_CancelsAnAIScanThroughThePipeline, but that test builds
// the handler itself. Nothing checked that the server, as setupRoutes actually
// constructs it, passes s.pipelineManager and s.aiScanStore through. Dropping
// that option fails in the worst direction: the cancel button is answered and
// the scan runs on.
//
// So this drives a real *aiscan.PipelineManager (with a real in-flight RunScan
// holding a registered cancel func) and a real *database.AIScanStore through
// the real router, and asserts the scan was actually stopped — RunScan
// returned ErrScanCanceled and the persisted status is "canceled" — not just
// that the DELETE came back 204.
//
// opRegistry is left nil on purpose: if the AI-scan branch is not wired, the
// request falls through to the registry and answers 500.
func TestWireHandlers_CancelOperationV2ReachesAIScanPipeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const opID = "op-under-test"

	scanStore, err := database.NewAIScanStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = scanStore.Close() })

	// nil parser: CancelScan only dereferences it for a phase with a submitted
	// OpenAI BatchID, and this scan never has one.
	pm := aiscan.NewPipelineManager(scanStore, fakeAIScanMainStore{}, nil)

	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	// LinkOperation is the production link between the scan and its v2 op, and
	// the only field CancelOperationV2 matches the requested id against.
	require.NoError(t, pm.LinkOperation(scan.ID, opID))
	// A started batch phase makes RunScan ATTACH (wait on existing work) rather
	// than launch phase goroutines that would call the nil parser. Empty
	// batchID keeps CancelScan away from the parser too.
	require.NoError(t, scanStore.UpdatePhaseStatus(scan.ID, "groups_scan", "processing", ""))

	sink := &attachSignalSink{attached: make(chan struct{})}
	runCtx, stopRun := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	runDone := make(chan struct{})
	go func() {
		runErr <- pm.RunScan(runCtx, scan.ID, sink)
		close(runDone)
	}()
	// On failure RunScan is still blocked; canceling its ctx releases it so the
	// goroutine does not outlive the test or race the store's Close.
	t.Cleanup(func() {
		stopRun()
		<-runDone
	})

	select {
	case <-sink.attached:
	case <-time.After(10 * time.Second):
		t.Fatal("RunScan never attached to the scan; the test cannot exercise cancel")
	}

	srv := &Server{
		router:          gin.New(),
		store:           dbmocks.NewMockStore(t), // no EXPECTs: cancel must not touch it
		pipelineManager: pm,
		aiScanStore:     scanStore,
		// opRegistry nil: see the doc comment.
	}
	srv.setupRoutes()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/operations/v2/"+opID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code,
		"DELETE /operations/v2/:id for an AI scan's op id did not take the AI-scan branch "+
			"(is handlers.WithAIScanCancellation still passed in wireHandlers?); body: %s", w.Body.String())

	select {
	case err := <-runErr:
		require.ErrorIs(t, err, aiscan.ErrScanCanceled,
			"RunScan ended, but not because the scan was canceled")
	case <-time.After(5 * time.Second):
		t.Fatal("cancel was answered but the running scan was never signalled: " +
			"RunScan is still blocked")
	}

	got, err := scanStore.GetScan(scan.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "canceled", got.Status, "persisted scan status after cancel")
}
