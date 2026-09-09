// file: internal/ai/retry_test.go
// version: 1.3.0
// guid: c3d4e5f6-a7b8-9012-cdef-234567890123
// last-edited: 2026-09-09

package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"syscall"
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

// unreachableHost is an RFC 5737 TEST-NET-1 address, reserved for documentation
// and guaranteed never to route. Standing in for the real host here is not just
// hygiene for a public repo: it also keeps the test from depending on any
// address that could one day answer.
const unreachableHost = "192.0.2.1"

// prodDialError rebuilds the exact error shape production returned on
// 2026-09-09, when the configured local base URL had a one-digit typo in its
// host and named an address nothing listened on. Every layer is here because
// every layer has to be unwrapped for the classifier to see the dial:
// openai_parser.go wraps with fmt.Errorf("...: %w"), net/http wraps with
// *url.Error, and only underneath that is the *net.OpError that says "dial".
//
// Built to the captured shape rather than composed to satisfy the matcher —
// the same discipline the 429 table above documents, and for the same reason.
func prodDialError() error {
	return fmt.Errorf("OpenAI API call failed: %w", &url.Error{
		Op:  "Post",
		URL: fmt.Sprintf("http://%s:11434/v1/chat/completions", unreachableHost),
		Err: &net.OpError{
			Op:   "dial",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.ParseIP(unreachableHost), Port: 11434},
			Err:  syscall.EHOSTUNREACH,
		},
	})
}

// TestIsUnreachableError_Classification covers the line the predicate draws:
// "no connection was established" is true, everything else is false. The two
// cases that matter most are the last two — a read failure on an OPEN
// connection, and a dial cut short by our own deadline. Both are transient and
// misfiling either one would silently disable retries for a healthy backend.
func TestIsUnreachableError_Classification(t *testing.T) {
	dial := func(inner error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: inner}
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"production dial failure, fully wrapped", prodDialError(), true},
		{"connection refused", dial(syscall.ECONNREFUSED), true},
		{"no route to host", dial(syscall.EHOSTUNREACH), true},
		{"network unreachable", dial(syscall.ENETUNREACH), true},
		{"bare errno with no OpError wrapper", syscall.ECONNREFUSED, true},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "ollama.invalid"}, true},

		{"nil", nil, false},
		{"plain error", errors.New("something went wrong"), false},
		{"api error", &openai.Error{StatusCode: 500}, false},
		{"read failure on an established connection", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}, false},
		{"our own deadline", context.DeadlineExceeded, false},
		{"our own cancellation", context.Canceled, false},
		{"dial aborted by our deadline", dial(context.DeadlineExceeded), false},
		{"dial aborted by our cancellation", dial(context.Canceled), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isUnreachableError(tt.err))
		})
	}
}

// TestDoWithRetry_UnreachableBackendIsNotRetried is the regression test for the
// production failure. Before this, an unroutable host cost 3 attempts plus 2s +
// 8s of backoff, which consumed the parser chain's whole 30s deadline and made
// budgetRemains skip every fallback rung — defeating the chain in exactly the
// case its own doc comment says it exists for.
//
// The attempt count is the assertion that has teeth: a base of 2s means a
// regression here does not merely slow the test down, it hangs it for 10s, so
// the elapsed-time bound is checked too.
func TestDoWithRetry_UnreachableBackendIsNotRetried(t *testing.T) {
	calls := 0
	start := time.Now()
	err := DoWithRetry(context.Background(), 3, 2*time.Second, func() error {
		calls++
		return prodDialError()
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, 1, calls, "an unreachable backend must be dialed once, not once per attempt")
	assert.Less(t, elapsed, time.Second, "no backoff may be spent on a backend that was never reached")

	unreachable, ok := errors.AsType[*UnreachableError](err)
	require.True(t, ok, "the caller must be able to tell 'never reached' from 'answered badly'")
	assert.ErrorIs(t, unreachable, syscall.EHOSTUNREACH, "the cause must survive wrapping")
}

// TestDoWithRetry_UnreachableIsNotPermanent guards the distinction the parser
// chain depends on. isPermanentAIFailure (internal/scanner) aborts the entire
// AI phase on a *PermanentError; if UnreachableError were ever folded into that
// type, a single dead backend would stop the phase instead of falling through
// to the next rung — the precise opposite of this change's intent.
func TestDoWithRetry_UnreachableIsNotPermanent(t *testing.T) {
	err := DoWithRetry(context.Background(), 3, time.Millisecond, func() error {
		return prodDialError()
	})
	require.Error(t, err)

	_, permanent := errors.AsType[*PermanentError](err)
	assert.False(t, permanent, "an unreachable backend must not abort the phase")
}

// TestDoWithRetry_TransientErrorsStillRetry pins the negative half: the new
// branch must not swallow the ordinary errors the backoff exists for.
func TestDoWithRetry_TransientErrorsStillRetry(t *testing.T) {
	calls := 0
	err := DoWithRetry(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return &openai.Error{StatusCode: 500}
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls, "a 5xx is transient and must still exhaust its attempts")
}
