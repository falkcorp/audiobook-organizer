// file: internal/server/handlers/filesystem_autoscan_test.go
// version: 1.0.0
// guid: 7e3b1a58-64c0-4d29-9f17-2b8d5c0a4e63
// last-edited: 2026-08-22

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// ---------------------------------------------------------------------------
// Fakes. Hand-rolled rather than mockery-generated: the point of these tests is
// what the handler RETURNS, and a strict mock adds expectation bookkeeping
// without making that any clearer.
// ---------------------------------------------------------------------------

type autoscanStore struct{ handlers.FilesystemStore }

func (autoscanStore) UpdateImportPath(id int, p *database.ImportPath) error { return nil }

type autoscanPathCreator struct{}

func (autoscanPathCreator) CreateImportPath(path, name string) (*database.ImportPath, error) {
	return &database.ImportPath{ID: 7, Path: path, Name: name, Enabled: true}, nil
}

type autoscanEnqueuer struct {
	returnID  string
	returnErr error
	gotDefID  string
	gotParams any
	calls     int
}

func (e *autoscanEnqueuer) EnqueueOp(ctx context.Context, defID string, params any,
	opts ...opsregistry.EnqueueOption) (string, error) {
	e.calls++
	e.gotDefID = defID
	e.gotParams = params
	return e.returnID, e.returnErr
}

func addImportPath(t *testing.T, enq *autoscanEnqueuer) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handlers.NewFilesystemHandler(
		autoscanStore{}, nil, autoscanPathCreator{}, nil, enq, nil, "/library", false,
	)
	body, _ := json.Marshal(map[string]any{"path": "/library/new", "name": "New"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/import-paths", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.AddImportPath(c)
	return w
}

// ---------------------------------------------------------------------------

// THE REGRESSION. AddImportPath used to mint its own v1 operations row and
// return that id as scan_operation_id, discarding the one EnqueueOp returned.
// The client polls this id through api.getOperationStatus, which resolves
// against /operations/v2 only — so the poll could never complete and the folder
// list never refreshed its book count.
//
// Nothing covered this before: a mutation returning a hard-coded wrong id here
// survived the entire suite.
func TestAddImportPath_ReturnsTheEnqueuedOpID(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "01JENQUEUED000000000SCAN"}
	w := addImportPath(t, enq)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if enq.calls != 1 {
		t.Fatalf("expected exactly 1 enqueue, got %d", enq.calls)
	}
	if enq.gotDefID != "library.folder-auto-scan" {
		t.Errorf("wrong def enqueued: %q", enq.gotDefID)
	}
	if !strings.Contains(w.Body.String(), enq.returnID) {
		t.Fatalf("scan_operation_id must be the id EnqueueOp returned (%s), got %s",
			enq.returnID, w.Body.String())
	}
}

// The params must not carry a legacy_op_id. This struct is a cross-package
// mirror of server.folderAutoScanOpParams coupled only by JSON tags, so the
// compiler cannot catch the field coming back — only a marshalled check can.
func TestAddImportPath_ParamsCarryNoLegacyOpID(t *testing.T) {
	enq := &autoscanEnqueuer{returnID: "op-1"}
	addImportPath(t, enq)

	raw, err := json.Marshal(enq.gotParams)
	if err != nil {
		t.Fatalf("params must marshal: %v", err)
	}
	if bytes.Contains(raw, []byte("legacy_op_id")) {
		t.Fatalf("params must not carry legacy_op_id, got %s", raw)
	}
	if !bytes.Contains(raw, []byte("folder_path")) {
		t.Fatalf("params lost folder_path: %s", raw)
	}
}

// An enqueue failure falls through to the synchronous-scan branch rather than
// 500ing, which is the pre-existing contract — the folder WAS created and the
// caller must hear about it. Assert the folder still comes back and no
// scan_operation_id is advertised for a run that was never queued.
func TestAddImportPath_EnqueueFailureStillReturnsTheFolder(t *testing.T) {
	enq := &autoscanEnqueuer{returnErr: context.DeadlineExceeded}
	w := addImportPath(t, enq)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "scan_operation_id") {
		t.Fatalf("must not advertise a scan id for an op that failed to queue: %s", w.Body.String())
	}
}
