// file: internal/books/r0download_test.go
// version: 1.0.0
// guid: 7c3e9a52-4d1b-4f68-b0a7-2e5d8c6f1a93
// last-edited: 2026-11-09

package books

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadWithRetry_RecoversFromTransientStatuses(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusGatewayTimeout)
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			_, _ = w.Write([]byte("batmobile"))
		}
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "archive")
	require.NoError(t, downloadWithRetry(t.Context(), t.Logf, srv.Client(), srv.URL, dst, 5, time.Millisecond))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "batmobile", string(got))
	assert.Equal(t, int32(3), calls.Load())
}

func TestDownloadWithRetry_DoesNotRetryNotFound(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "archive")
	err := downloadWithRetry(t.Context(), t.Logf, srv.Client(), srv.URL, dst, 5, time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
	assert.Equal(t, int32(1), calls.Load(), "a 404 must not be retried")
	assert.NoFileExists(t, dst)
}
