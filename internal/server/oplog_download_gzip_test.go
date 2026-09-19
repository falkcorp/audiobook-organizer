// file: internal/server/oplog_download_gzip_test.go
// version: 1.0.0
// guid: 3f8e1c52-7a4d-4b96-9e0c-5d2a8b61f7c3
// last-edited: 2026-09-19

package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
)

// "Download full log (.gz)" on every operation saved a file gunzip rejected with
// "data stream error" (prod, 2026-09-19). The handler streams its own gzip body
// and the router-wide compressor wrapped it a second time while dropping
// Content-Encoding. The handler-level test calls DownloadOperationLogs with no
// middleware, so it could never see this; this one goes through the production
// compressor with the request a browser sends.
func TestOperationLogDownload_BrowserSavesAValidGzip(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Enough rows that the handler's gzip writer emits several chunks, which is
	// what split the stream into raw and re-compressed parts in production.
	const n = 5000
	rows := make([]database.OpLogV2Row, 0, n)
	base := time.Date(2026, 9, 19, 5, 0, 0, 0, time.UTC)
	for i := range n {
		rows = append(rows, database.OpLogV2Row{
			OperationID: "op1",
			Level:       "info",
			Message:     fmt.Sprintf("Scanning folder %d/%d: /library/book-%d", i+1, n, i),
			Attrs:       fmt.Sprintf(`{"phase":"progress","progress_cur":%d}`, i+1),
			CreatedAt:   base.Add(time.Duration(i) * time.Second),
		})
	}
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().GetOpLogsV2("op1", 0).Return(rows, nil)

	h := handlers.NewOperationsV2Handler(store, nil, nil, false)
	r := gin.New()
	r.Use(compressionMiddleware())
	r.GET("/api/v1/operations/v2/:id/logs/download", h.DownloadOperationLogs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/v2/op1/logs/download", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// A browser undoes Content-Encoding once, then writes what is left to disk.
	saved := rec.Body.Bytes()
	if strings.EqualFold(rec.Header().Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(saved))
		if err != nil {
			t.Fatalf("Content-Encoding is gzip but the body is not: %v", err)
		}
		if saved, err = io.ReadAll(zr); err != nil {
			t.Fatalf("transport gzip decode: %v", err)
		}
	}

	// The saved .log.gz must be one valid gzip file holding the whole transcript.
	zr, err := gzip.NewReader(bytes.NewReader(saved))
	if err != nil {
		t.Fatalf("saved file is not gzip: %v", err)
	}
	text, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip of the saved file failed (what the owner hit): %v", err)
	}
	if got := strings.Count(string(text), " INFO Scanning folder "); got != n {
		t.Fatalf("saved log holds %d of %d lines", got, n)
	}
	if !strings.Contains(string(text), fmt.Sprintf("Scanning folder %d/%d", n, n)) {
		t.Fatal("saved log is missing its last line")
	}
}
