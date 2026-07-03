// file: internal/ai/embedding_client_retry_test.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0123-defa-345678901234
// last-edited: 2026-07-03

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbedBatchRaw_PermanentErrorNoRetry proves embedBatchRaw makes exactly
// one HTTP call when the server returns a permanent error (429 with OpenAI
// error code "insufficient_quota") instead of exhausting all 3 attempts.
func TestEmbedBatchRaw_PermanentErrorNoRetry(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`))
	}))
	defer server.Close()

	c := NewEmbeddingClientWithOptions("k", "", server.URL)
	_, err := c.embedBatchRaw(context.Background(), []string{"x"})

	require.Error(t, err)
	var pe *PermanentError
	assert.True(t, errors.As(err, &pe), "expected a PermanentError, got %T: %v", err, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requests), "permanent error must not be retried")
}

// TestEmbedBatchRaw_TransientErrorRetriesAllAttempts proves the existing
// transient-error behavior is preserved: a 500 response still exhausts all 3
// attempts.
func TestEmbedBatchRaw_TransientErrorRetriesAllAttempts(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error","code":"internal_error"}}`))
	}))
	defer server.Close()

	c := NewEmbeddingClientWithOptions("k", "", server.URL)
	_, err := c.embedBatchRaw(context.Background(), []string{"x"})

	require.Error(t, err)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "transient error must not be wrapped as PermanentError")
	assert.Equal(t, int32(3), atomic.LoadInt32(&requests), "transient error must retry all 3 attempts")
}
