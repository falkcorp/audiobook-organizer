// file: internal/ai/ai_metrics_test.go
// version: 1.0.0
// guid: 2b8e4a16-93d5-4f7c-8e0a-6c41d5b7a9f2
// last-edited: 2026-10-10

package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/openai/openai-go/v3"
)

// TestOutcomeAndReasonTable runs real error values through the classifier. The
// reply-quality rows have outcome "ok": those errors are produced after a
// successful SDK call (see recordReplyFailure).
func TestOutcomeAndReasonTable(t *testing.T) {
	for _, c := range []struct {
		name        string
		err         error
		wantOutcome string
		wantReason  string
	}{
		{"success", nil, "ok", ""},
		{"429 plain rate limit", &openai.Error{StatusCode: 429, Type: "rate_limit_error", Code: "rate_limit_exceeded"}, "error", "rate_limit"},
		{"429 quota in type", &openai.Error{StatusCode: 429, Type: "insufficient_quota"}, "error", "quota"},
		{"429 quota in code", &openai.Error{StatusCode: 429, Type: "x", Code: "credit_balance_exhausted"}, "error", "quota"},
		{"401", &openai.Error{StatusCode: 401}, "error", "permanent"},
		{"500", &openai.Error{StatusCode: 500}, "error", "other"},
		{"connection refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "error", "unreachable"},
		{"connection refused, wrapped", fmt.Errorf("embedding attempt 1: %w", &url.Error{Op: "Post", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}), "error", "unreachable"},
		{"deadline", context.DeadlineExceeded, "timeout", "timeout"},
		{"deadline, wrapped by the http client", &url.Error{Op: "Post", Err: context.DeadlineExceeded}, "timeout", "timeout"},
		{"canceled", context.Canceled, "canceled", ""},
		{"canceled, wrapped", &url.Error{Op: "Post", Err: context.Canceled}, "canceled", ""},
		{"reply parse", newReplyParseError(errors.New("response is not a JSON object"), "x"), "ok", "reply_parse"},
		{"result count", newReplyParseError(&ResultCountError{Got: 2, Expected: 3}, "x"), "ok", "count_mismatch"},
		{"generic", errors.New("boom"), "error", "other"},
	} {
		t.Run(c.name, func(t *testing.T) {
			outcome, reason := classifyCall(c.err)
			if outcome != c.wantOutcome || reason != c.wantReason {
				t.Errorf("classifyCall = (%q, %q), want (%q, %q)", outcome, reason, c.wantOutcome, c.wantReason)
			}
		})
	}
}

// observe records the failure of a transport error exactly once, and records
// no failure for a cancellation.
func TestObserveFailureAccounting(t *testing.T) {
	tel := installAITelemetry(t)
	meta := callMeta{provider: "local", model: "m", task: TaskEmbed}
	for _, err := range []error{
		&openai.Error{StatusCode: 429, Type: "insufficient_quota"},
		&openai.Error{StatusCode: 429, Type: "rate_limit_error"},
		context.DeadlineExceeded,
		context.Canceled,
	} {
		_, got := observe(context.Background(), meta, func(context.Context) (int, error) { return 0, err }, nil)
		if got != err {
			t.Fatalf("observe changed the error: %v -> %v", err, got)
		}
	}
	rm := tel.collect(t)
	for _, c := range []struct {
		metric string
		attrs  map[string]string
		want   int64
	}{
		{"ai.requests", map[string]string{"outcome": "error"}, 2},
		{"ai.requests", map[string]string{"outcome": "timeout"}, 1},
		{"ai.requests", map[string]string{"outcome": "canceled"}, 1},
		{"ai.failures", map[string]string{"reason": "quota"}, 1},
		{"ai.failures", map[string]string{"reason": "rate_limit"}, 1},
		{"ai.failures", map[string]string{"reason": "timeout"}, 1},
		{"ai.failures", nil, 3}, // the cancellation is not a failure
	} {
		if got := counterSum(rm, c.metric, c.attrs); got != c.want {
			t.Errorf("%s%v = %d, want %d", c.metric, c.attrs, got, c.want)
		}
	}
	if got := histogramCount(rm, "ai.request.duration", nil); got != 4 {
		t.Errorf("duration observations = %d, want 4 (every attempt is timed)", got)
	}
}
