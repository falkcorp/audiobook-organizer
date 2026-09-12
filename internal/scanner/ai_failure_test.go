// file: internal/scanner/ai_failure_test.go
// version: 1.3.0
// guid: 6b3d81e0-5a29-4c74-9e18-7f2a0c46bd35
// last-edited: 2026-09-12

package scanner

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// TestIsPermanentAIFailure_ReplyParseErrorIsNeverPermanent: a reply the model
// sent that we could not decode quotes up to 300 bytes of that reply in its
// Error(). A book title echoed into it can contain any provider marker, and
// the classifier used to abort the whole AI phase on that text. It must read
// the type, not the words.
func TestIsPermanentAIFailure_ReplyParseErrorIsNeverPermanent(t *testing.T) {
	for _, marker := range permanentAIFailureMarkers {
		replyErr := &ai.ReplyParseError{
			Err:     errors.New("got 1 result(s) for 2 filename(s), and results are matched to filenames by position"),
			Excerpt: `{"results": [{"title": "The ` + marker + ` Chronicles"}]}`,
		}
		if !strings.Contains(replyErr.Error(), marker) {
			t.Fatalf("precondition: Error() %q does not quote %q", replyErr.Error(), marker)
		}
		if isPermanentAIFailure(replyErr) {
			t.Errorf("a reply parse error quoting %q was classified permanent -- a book title aborted the AI phase", marker)
		}
		if isPermanentAIFailure(fmt.Errorf("ai parser chain: %w", replyErr)) {
			t.Errorf("a wrapped reply parse error quoting %q was classified permanent", marker)
		}
	}

	// Control: the same marker, written by the provider, is still permanent.
	if !isPermanentAIFailure(errors.New(`anthropic: permission_error: this key cannot use the model`)) {
		t.Error("a real provider permission_error is no longer permanent")
	}
}

// TestIsPermanentAIFailure_WrappedReplyParseErrorTextIsNeverRead: callers
// wrap errors with %w (the chain, the phase, anything added later). Every such
// wrapper's Error() embeds the excerpt, so a check that only special-cased the
// bare type would match the model's words again one wrap up.
func TestIsPermanentAIFailure_WrappedReplyParseErrorTextIsNeverRead(t *testing.T) {
	for _, marker := range permanentAIFailureMarkers {
		replyErr := &ai.ReplyParseError{
			Err:     errors.New("result 0 is not a JSON object"),
			Excerpt: `{"results": ["The ` + marker + ` Chronicles"]}`,
		}
		for name, err := range map[string]error{
			"one %w":                fmt.Errorf("batch 3/7: %w", replyErr),
			"two %w":                fmt.Errorf("ai parser chain: %w", fmt.Errorf("remote rung: %w", replyErr)),
			"multi-%w with timeout": fmt.Errorf("remote: %w; local: %w", errors.New("context deadline exceeded"), replyErr),
			"join with timeout":     errors.Join(errors.New("dial tcp: i/o timeout"), replyErr),
			"%w around a join":      fmt.Errorf("both rungs failed: %w", errors.Join(errors.New("connection refused"), replyErr)),
		} {
			if !strings.Contains(err.Error(), marker) {
				t.Fatalf("%s: precondition: %q does not quote %q", name, err.Error(), marker)
			}
			if isPermanentAIFailure(err) {
				t.Errorf("%s: model text %q was string-matched as a provider marker", name, marker)
			}
		}
	}
}

// TestIsPermanentAIFailure_ReplyParseErrorDoesNotMaskAPermanentBranch: a
// reply error next to a real permanent failure must not hide it.
func TestIsPermanentAIFailure_ReplyParseErrorDoesNotMaskAPermanentBranch(t *testing.T) {
	replyErr := &ai.ReplyParseError{Err: errors.New("result 0 is not a JSON object"), Excerpt: `{"results": ["x"]}`}
	for name, err := range map[string]error{
		"join, typed permanent":      errors.Join(&ai.PermanentError{Err: errors.New("401 Unauthorized")}, replyErr),
		"join, typed permanent last": errors.Join(replyErr, &ai.PermanentError{Err: errors.New("403 Forbidden")}),
		"join, provider marker text": errors.Join(errors.New(`anthropic: permission_error: key lacks access`), replyErr),
		"multi-%w, provider marker":  fmt.Errorf("remote: %w; local: %w", errors.New(`401 {"code": "invalid_api_key"}`), replyErr),
		"%w around a join, typed":    fmt.Errorf("rungs: %w", errors.Join(replyErr, &ai.PermanentError{Err: errors.New("quota")})),
		"%w around a join, marker":   fmt.Errorf("rungs: %w", errors.Join(replyErr, errors.New("insufficient_quota"))),
	} {
		if !isPermanentAIFailure(err) {
			t.Errorf("%s: a permanent failure next to a reply parse error was classified transient", name)
		}
	}
}

// TestChainFallsThroughOnAReplyParseError is the call-site half of the test
// above: parserChain.ParseBatch stops at the first permanent error, so a reply
// parse error quoting a marker used to stop the fallthrough to the next rung.
func TestChainFallsThroughOnAReplyParseError(t *testing.T) {
	replyErr := &ai.ReplyParseError{
		Err:     errors.New("result 0 is not a JSON object"),
		Excerpt: `{"results": ["invalid_api_key"]}`,
	}
	remote := &countingParser{err: replyErr}
	local := &countingParser{result: []*ai.ParsedMetadata{{Title: "from local"}}}

	c := newParserChain(logger.New("test"), rung("remote", remote), rung("local", local))
	results, err := c.ParseBatch(t.Context(), []string{"book.m4b"})
	if err != nil {
		t.Fatalf("chain returned %v; the next rung should have answered", err)
	}
	if local.calls.Load() != 1 {
		t.Fatalf("local rung called %d time(s), want 1", local.calls.Load())
	}
	if len(results) != 1 || results[0].Title != "from local" {
		t.Errorf("results = %+v", results)
	}
}

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
