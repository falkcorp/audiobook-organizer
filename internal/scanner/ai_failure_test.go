// file: internal/scanner/ai_failure_test.go
// version: 1.1.0
// guid: 6b3d81e0-5a29-4c74-9e18-7f2a0c46bd35
// last-edited: 2026-08-22

package scanner

import (
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
)

// prodQuotaError is the error the scanner actually received on 2026-08-16,
// copied from the journal rather than composed here.
//
// A constructed "insufficient_quota" string would pass any matcher that looks
// for it, including one that could never fire in production -- the real error
// arrives wrapped by fmt.Errorf and carries the provider's JSON body inline,
// which is the shape the matcher has to survive.
const prodQuotaError = `OpenAI API call failed: POST "https://api.openai.com/v1/chat/completions": 429 Too Many Requests {
        "message": "You have no credits remaining. Add credits to continue using the API at https://platform.openai.com/settings/organization/billing/.",
        "type": "insufficient_quota",
        "param": null,
        "code": "credit_balance_exhausted"
    }`

func TestIsPermanentAIFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "the real production quota error, wrapped as the scanner sees it",
			err:  fmt.Errorf("ai call retries exhausted: %w", errors.New(prodQuotaError)),
			want: true,
		},
		{
			name: "revoked key",
			err:  errors.New(`OpenAI API call failed: 401 Unauthorized {"code": "invalid_api_key"}`),
			want: true,
		},
		{
			name: "anthropic auth failure",
			err:  errors.New(`anthropic: authentication_error: invalid x-api-key`),
			want: true,
		},
		{
			// Transient failures must NOT match, or one flaky call disables AI
			// parsing for the whole scan -- the opposite defect, and a quieter
			// one because the scan still completes.
			name: "connection reset is transient",
			err:  errors.New(`OpenAI API call failed: dial tcp: connection reset by peer`),
			want: false,
		},
		{
			name: "server error is transient",
			err:  errors.New(`OpenAI API call failed: 503 Service Unavailable`),
			want: false,
		},
		{
			name: "ordinary rate limit is transient",
			err:  errors.New(`OpenAI API call failed: 429 Too Many Requests {"type": "rate_limit_error"}`),
			want: false,
		},
		{
			name: "context deadline is transient",
			err:  errors.New("context deadline exceeded"),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanentAIFailure(tc.err); got != tc.want {
				t.Errorf("isPermanentAIFailure() = %v, want %v\nerror: %v", got, tc.want, tc.err)
			}
		})
	}
}

// TestPlainRateLimitIsNotConfusedWithQuota is the discriminating control for
// the pair of 429s above.
//
// Both errors are "429 Too Many Requests". If the matcher keyed on the status
// code it would pass every other case in this file while disabling AI parsing
// on an ordinary rate limit -- exactly the behaviour the retry/backoff exists
// to handle. The two must be told apart by the provider's error code, not the
// HTTP status.
func TestPlainRateLimitIsNotConfusedWithQuota(t *testing.T) {
	quota := errors.New(prodQuotaError)
	rateLimit := errors.New(`OpenAI API call failed: 429 Too Many Requests {"type": "rate_limit_error", "code": "rate_limit_exceeded"}`)

	if !isPermanentAIFailure(quota) {
		t.Error("exhausted credits treated as retryable — the scan will burn every batch against it")
	}
	if isPermanentAIFailure(rateLimit) {
		t.Error("an ordinary rate limit treated as permanent — backoff would never get its chance")
	}
}

// TestIsPermanentAIFailure_TypedPermanentError proves the typed path is
// actually checked and not just the text path: the wrapped message contains
// none of permanentAIFailureMarkers, so only errors.As(err, &ai.PermanentError{})
// can make this return true.
func TestIsPermanentAIFailure_TypedPermanentError(t *testing.T) {
	err := &ai.PermanentError{Err: errors.New("whatever")}
	if !isPermanentAIFailure(err) {
		t.Error("a *ai.PermanentError — internal/ai's own typed classification — was not recognized as permanent")
	}
}

// TestIsPermanentAIFailure_TransientErrorNotFlagged is the anti-over-suppression
// control for the typed check above: a known-good transient input (a plain
// network-timeout error, not *ai.PermanentError, no marker substring) must
// still return false with the new guard in place, or the phase stops retrying
// failures that would have succeeded on the next attempt.
func TestIsPermanentAIFailure_TransientErrorNotFlagged(t *testing.T) {
	err := errors.New("dial tcp 1.2.3.4:443: i/o timeout")
	if isPermanentAIFailure(err) {
		t.Error("a plain network timeout was flagged permanent — the phase will stop retrying transient failures")
	}
}
