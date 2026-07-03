// file: internal/ai/retry_test.go
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-234567890123
// last-edited: 2026-07-03

package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsPermanentAIError_Classification table-tests isPermanentAIError
// against the permanent/transient matrix described in TASK-12: HTTP
// 400/401/403/404 and 429-with-insufficient_quota are permanent; plain 429s,
// 5xx, and non-*openai.Error errors (network/timeout) are transient.
func TestIsPermanentAIError_Classification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"400 bad request", &openai.Error{StatusCode: 400}, true},
		{"401 unauthorized", &openai.Error{StatusCode: 401}, true},
		{"403 forbidden", &openai.Error{StatusCode: 403}, true},
		{"404 not found", &openai.Error{StatusCode: 404}, true},
		{"429 insufficient_quota", &openai.Error{StatusCode: 429, Code: "insufficient_quota"}, true},
		{"429 plain rate limit (no code)", &openai.Error{StatusCode: 429}, false},
		{"429 different code", &openai.Error{StatusCode: 429, Code: "rate_limit_exceeded"}, false},
		{"500 internal server error", &openai.Error{StatusCode: 500}, false},
		{"503 service unavailable", &openai.Error{StatusCode: 503}, false},
		{"plain non-openai error", errors.New("boom"), false},
		{"context deadline exceeded", context.DeadlineExceeded, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isPermanentAIError(tc.err))
		})
	}
}

// TestDoWithRetry_PermanentErrorShortCircuits proves a permanent error
// returned by fn causes exactly one call (no retries, no additional
// backoff), and the returned error satisfies errors.As(..., &PermanentError{}).
func TestDoWithRetry_PermanentErrorShortCircuits(t *testing.T) {
	calls := 0
	permErr := &openai.Error{StatusCode: 401}
	err := DoWithRetry(context.Background(), 5, time.Millisecond, func() error {
		calls++
		return permErr
	})

	require.Error(t, err)
	assert.Equal(t, 1, calls, "permanent error must short-circuit after exactly one attempt")

	var pe *PermanentError
	require.True(t, errors.As(err, &pe), "returned error must satisfy errors.As(..., &PermanentError{})")
	assert.ErrorIs(t, err, permErr)
}

// TestDoWithRetry_TransientErrorStillRetries proves a transient error keeps
// the existing retry behavior — fn is called up to maxAttempts times.
func TestDoWithRetry_TransientErrorStillRetries(t *testing.T) {
	calls := 0
	transientErr := &openai.Error{StatusCode: 500}
	err := DoWithRetry(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return transientErr
	})

	require.Error(t, err)
	assert.Equal(t, 3, calls, "transient error must retry up to maxAttempts")

	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "transient error must not be wrapped as PermanentError")
}
