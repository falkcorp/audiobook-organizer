// file: internal/httputil/rangeserve_gin_test.go
// version: 1.0.0
// guid: e38eefca-0afc-45b2-8487-76407aeda942
// last-edited: 2026-07-29

package httputil

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func ginRequest(t *testing.T, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/file", nil)
	if mutate != nil {
		mutate(c.Request)
	}
	if err := ServeFileWithRangeGin(c, path, Options{}); err != nil {
		t.Fatalf("ServeFileWithRangeGin: %v", err)
	}
	return w
}

func TestServeFileWithRangeGin_FullBody200(t *testing.T) {
	content := deterministicContent(5000)
	path := newTempFile(t, "gin-full.bin", content)

	w := ginRequest(t, path, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), content) {
		t.Errorf("body mismatch")
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
}

func TestServeFileWithRangeGin_PartialRange206(t *testing.T) {
	content := deterministicContent(5000)
	path := newTempFile(t, "gin-partial.bin", content)

	w := ginRequest(t, path, func(r *http.Request) {
		r.Header.Set("Range", "bytes=100-199")
	})

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	wantCR := fmt.Sprintf("bytes 100-199/%d", len(content))
	if got := w.Header().Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if !bytes.Equal(w.Body.Bytes(), content[100:200]) {
		t.Errorf("body mismatch for gin bytes=100-199")
	}
}

func TestServeFileWithRangeGin_PropagatesError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/file", nil)

	err := ServeFileWithRangeGin(c, "relative/not-absolute.bin", Options{})
	if err == nil {
		t.Fatal("expected error for non-absolute path, got nil")
	}
}
