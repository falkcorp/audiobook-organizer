// file: internal/server/file_ops_handlers_test.go
// version: 1.1.1
// guid: 2c1f8b4d-9e3a-4f50-a7d6-8b1c5e0f9a23

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func TestHandleListPendingFileOps_NoPool(t *testing.T) {
	gin.SetMode(gin.TestMode)

	srv := &Server{router: gin.New()}
	srv.router.GET("/api/v1/file-ops/pending", srv.handleListPendingFileOps)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/file-ops/pending", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data struct {
			Count      int             `json:"count"`
			Operations []pendingFileOp `json:"operations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Count != 0 || len(resp.Data.Operations) != 0 {
		t.Errorf("expected empty result, got count=%d ops=%v", resp.Data.Count, resp.Data.Operations)
	}
}

func TestHandleListPendingFileOps_PopulatedPool(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Pool with no workers — Submit will overflow but we just want to populate
	// the in-memory `pending` map; we don't need the work to actually run.
	pool := &FileIOPool{
		ch:       make(chan fileIOJobEntry, 8),
		overflow: make(chan struct{}, 1),
	}
	pool.SubmitTyped("book-A", "apply_metadata", metafetch.Unattended, func() {})
	pool.SubmitTyped("book-B", "tag_writeback", metafetch.Unattended, func() {})

	srv := &Server{router: gin.New(), fileIOPool: pool}
	srv.router.GET("/api/v1/file-ops/pending", srv.handleListPendingFileOps)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/file-ops/pending", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Count      int             `json:"count"`
			Operations []pendingFileOp `json:"operations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Count != 2 {
		t.Fatalf("count = %d, want 2 (response: %s)", resp.Data.Count, w.Body.String())
	}

	seen := map[string]string{}
	for _, op := range resp.Data.Operations {
		seen[op.BookID] = op.OpType
	}
	if seen["book-A"] != "apply_metadata" {
		t.Errorf("book-A op_type = %q, want apply_metadata", seen["book-A"])
	}
	if seen["book-B"] != "tag_writeback" {
		t.Errorf("book-B op_type = %q, want tag_writeback", seen["book-B"])
	}
}

func TestHandleListPendingFileOps_SortedByStartedAt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool := &FileIOPool{
		ch:       make(chan fileIOJobEntry, 8),
		overflow: make(chan struct{}, 1),
	}
	// Submit A first — should sort earlier than B since started_at is set in SubmitTyped.
	pool.SubmitTyped("first", "apply_metadata", metafetch.Unattended, func() {})
	pool.SubmitTyped("second", "apply_metadata", metafetch.Unattended, func() {})
	pool.SubmitTyped("third", "apply_metadata", metafetch.Unattended, func() {})

	srv := &Server{router: gin.New(), fileIOPool: pool}
	srv.router.GET("/api/v1/file-ops/pending", srv.handleListPendingFileOps)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/file-ops/pending", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "first") || !strings.Contains(body, "second") || !strings.Contains(body, "third") {
		t.Fatalf("missing expected book IDs in response: %s", body)
	}

	var resp struct {
		Data struct {
			Count      int             `json:"count"`
			Operations []pendingFileOp `json:"operations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Operations) != 3 {
		t.Fatalf("got %d ops, want 3", len(resp.Data.Operations))
	}
	for i := 1; i < len(resp.Data.Operations); i++ {
		if resp.Data.Operations[i].StartedAt.Before(resp.Data.Operations[i-1].StartedAt) {
			t.Errorf("operations not sorted by started_at: %v before %v",
				resp.Data.Operations[i].StartedAt, resp.Data.Operations[i-1].StartedAt)
		}
	}
}
