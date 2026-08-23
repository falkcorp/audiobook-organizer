// file: internal/ai/retry_test.go
// version: 1.2.0
// guid: c3d4e5f6-a7b8-9012-cdef-234567890123
// last-edited: 2026-08-23

package ai

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsPermanentAIError_Classification table-tests isPermanentAIError
// against the permanent/transient matrix described in TASK-12: HTTP
// 400/401/403/404 and a 429 carrying a quota-exhaustion marker are permanent;
// plain 429s, 5xx, and non-*openai.Error errors (network/timeout) are transient.
//
// ⚠️ The 429 cases below are built to the shape of a REAL captured response, not
// composed to satisfy the matcher. That distinction is the whole point here: the
// original table asserted `&openai.Error{StatusCode: 429, Code: "insufficient_quota"}`
// — a struct no OpenAI response actually produces — so it passed green for months
// while the classifier missed every genuine exhaustion in production. A constructed
// fixture proves only that the matcher matches itself. See the same warning,
// written before this bug was found, at internal/scanner/ai_failure_test.go:19-22.
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
		// The PRODUCTION shape, copied from internal/scanner/ai_failure_test.go's
		// prodQuotaError (itself taken from the 2026-08-16 incident journal): the
		// quota marker is in "type" and "code" holds a DIFFERENT string. Until
		// 2026-08-23 this case failed — the branch read Code alone, so the one
		// error the classifier exists to catch was retried as transient.
		{"429 real payload: quota in type, other code", &openai.Error{StatusCode: 429, Type: "insufficient_quota", Code: "credit_balance_exhausted"}, true},
		// Each field alone, because a provider may populate either one.
		{"429 quota marker in type only", &openai.Error{StatusCode: 429, Type: "insufficient_quota"}, true},
		{"429 quota marker in code only", &openai.Error{StatusCode: 429, Code: "insufficient_quota"}, true},
		{"429 credit_balance_exhausted in type only", &openai.Error{StatusCode: 429, Type: "credit_balance_exhausted"}, true},
		{"429 credit_balance_exhausted in code only", &openai.Error{StatusCode: 429, Code: "credit_balance_exhausted"}, true},
		// Discriminating controls: same status, must stay transient.
		{"429 plain rate limit (no code)", &openai.Error{StatusCode: 429}, false},
		{"429 real rate limit: both fields set", &openai.Error{StatusCode: 429, Type: "rate_limit_error", Code: "rate_limit_exceeded"}, false},
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

// TestDoWithRetry_LogsRetriesAndExhaustion proves C6 observability: each
// retry emits a Warn with attempt/backoff/err, and exhausting all attempts
// emits an Error. Uses a buffer-backed default slog handler.
func TestDoWithRetry_LogsRetriesAndExhaustion(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	transientErr := &openai.Error{StatusCode: 500}
	err := DoWithRetry(context.Background(), 3, time.Millisecond, func() error {
		return transientErr
	})
	require.Error(t, err)

	out := buf.String()
	assert.Contains(t, out, "ai call failed, retrying after backoff", "each retry must be logged at Warn")
	assert.Contains(t, out, "max_attempts=3")
	assert.Contains(t, out, "ai call retries exhausted", "exhaustion must be logged at Error")
}

// TestDoWithRetry_SuccessLogsNothing proves the happy path stays silent —
// no retry Warn and no exhaustion Error when fn succeeds first try.
func TestDoWithRetry_SuccessLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	err := DoWithRetry(context.Background(), 3, time.Millisecond, func() error { return nil })
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "successful first attempt must not log retry noise")
}
