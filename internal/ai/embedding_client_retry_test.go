// file: internal/ai/embedding_client_retry_test.go
// version: 1.2.0
// guid: d4e5f6a7-b8c9-0123-defa-345678901234
// last-edited: 2026-09-11

package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbedBatchRaw_PermanentErrorNoRetry proves embedBatchRaw makes exactly
// one HTTP call when the server returns a permanent error (429 with OpenAI
// error code "insufficient_quota") instead of exhausting all 3 attempts.
func TestEmbedBatchRaw_PermanentErrorNoRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
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
	assert.Equal(t, int32(1), requests.Load(), "permanent error must not be retried")
}

// TestEmbedBatchRaw_TransientErrorRetriesAllAttempts proves the existing
// transient-error behavior is preserved: a 500 response still exhausts all 3
// attempts.
func TestEmbedBatchRaw_TransientErrorRetriesAllAttempts(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
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
	assert.Equal(t, int32(3), requests.Load(), "transient error must retry all 3 attempts")
}

// TestEmbedBatchRaw_ConfiguredTimeoutBoundsEachAttempt proves the value from
// embedding.request_timeout_seconds is what actually cuts off a hung request,
// end to end: config -> ResolveEmbeddingRequestTimeout -> the embedclient
// service's WithRequestTimeout -> the per-attempt child context. The server
// never answers; it just records how long the client held the first request
// open. With the configured 1 s the first attempt is abandoned at ~1 s. With
// the 30 s default it would be held until the 3 s parent deadline, so the
// discriminator is "first request cut off well before 3 s".
func TestEmbedBatchRaw_ConfiguredTimeoutBoundsEachAttempt(t *testing.T) {
	var firstHeld atomic.Int64 // nanoseconds the server held the FIRST request
	var requests atomic.Int32
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		start := time.Now()
		// Drain the body first: net/http only starts the background read that
		// notices a client hang-up (and cancels r.Context()) once the handler
		// has consumed the request body. Without this the context never fires,
		// the handler never returns, and the deferred server.Close() waits on
		// it forever — that hang cost this test a 10-minute package timeout on
		// 2026-09-11.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done(): // client gave up (per-attempt timeout or parent ctx)
		case <-stop: // test is over; do not hold the server open
		}
		if n == 1 {
			firstHeld.Store(int64(time.Since(start)))
		}
	}))
	defer server.Close()
	defer close(stop)

	cfg := &config.Config{}
	cfg.AIBackend.EmbeddingMode = config.AIBackendModeLocal
	cfg.AIBackend.LocalBaseURL = server.URL
	cfg.AIBackend.LocalEmbeddingModel = "bge-m3"
	cfg.Embedding.RequestTimeoutSeconds = 1
	c := buildEmbedClient(t, cfg)
	require.NotNil(t, c)

	const parentBudget = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), parentBudget)
	defer cancel()
	_, err := c.embedBatchRaw(ctx, []string{"x"})
	require.Error(t, err, "a server that never answers must not yield embeddings")

	held := time.Duration(firstHeld.Load())
	require.GreaterOrEqual(t, requests.Load(), int32(1), "server saw no request")
	assert.GreaterOrEqual(t, held, 500*time.Millisecond, "first attempt was cut off before its 1 s budget: %s", held)
	assert.Less(t, held, parentBudget-500*time.Millisecond,
		"first attempt held for %s: the configured 1 s budget did not bound it (the 30 s default would run to the %s parent deadline)", held, parentBudget)
}
