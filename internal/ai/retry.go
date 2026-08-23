// file: internal/ai/retry.go
// version: 1.4.0
// guid: f6a7b8c9-d0e1-2345-fabc-678901234567
// last-edited: 2026-08-23

// Package ai — shared retry helper for OpenAI / Ollama API calls.
package ai

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/openai/openai-go/v3"
)

// PermanentError wraps an error that is known to be non-retryable — e.g. an
// HTTP 400/401/403/404 response, or a 429 carrying a quota-exhaustion marker in
// either its "type" or "code" field (see quota429Markers). Callers can
// distinguish "will never succeed" from "exhausted retries" via
// errors.As(err, &PermanentError{}).
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	return e.Err.Error()
}

func (e *PermanentError) Unwrap() error {
	return e.Err
}

// isPermanentAIError reports whether err is an OpenAI API error that can
// never succeed on retry: HTTP 400/401/403/404, or a HTTP 429 that carries a
// quota-exhaustion marker (quota exhaustion is permanent; plain rate-limit 429s
// are transient — see isPermanentQuota429 for how the two are told apart).
// Errors that are not *openai.Error (network/timeout errors,
// context cancellation, etc.) are treated as transient/unknown and retried,
// matching prior behavior.
func isPermanentAIError(err error) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case 400, 401, 403, 404:
		return true
	case 429:
		return isPermanentQuota429(apiErr)
	default:
		return false
	}
}

// isPermanentQuota429 distinguishes the two errors that both arrive as HTTP 429:
// quota/credit exhaustion, which no retry can clear, from an ordinary rate limit,
// which is exactly what the backoff exists to absorb. Getting this wrong is costly
// in BOTH directions, which is why it is its own function:
//
//   - Too narrow (miss a real exhaustion): DoWithRetry burns the full attempt budget
//     with quadratic backoff against an API that cannot succeed. On 2026-08-16 this
//     failed all 77 batches of a scan, the phase reported no progress, and the
//     watchdog cancelled at five minutes — discarding a completed 3,917-file walk.
//   - Too broad (flag a plain rate limit): AI parsing is disabled for the remainder
//     of the run on one throttled call. Quieter, because the scan still completes,
//     but it silently degrades every batch after it.
//
// The field trap: openai-go's Error carries BOTH `Code` (json:"code") and
// `Type` (json:"type"). The captured production payload
// (internal/scanner/ai_failure_test.go:23, copied from the incident journal rather
// than composed) sets them to DIFFERENT values:
//
//	"type": "insufficient_quota",  "code": "credit_balance_exhausted"
//
// while a plain rate limit sets "type": "rate_limit_error", "code":
// "rate_limit_exceeded". Any predicate here must return true for the first and
// false for the second.
//
// Both fields are checked against every marker deliberately. Reading one field is
// what broke this before — until 2026-08-23 the branch was
// `apiErr.Code == "insufficient_quota"`, which is false on the payload above —
// and simply moving to `Type` would swap one single-field assumption for another.
// The markers are provider strings, not HTTP semantics, so a provider is free to
// report either one in either field.
func isPermanentQuota429(apiErr *openai.Error) bool {
	for _, m := range quota429Markers {
		if apiErr.Type == m || apiErr.Code == m {
			return true
		}
	}
	return false
}

// quota429Markers are the provider strings that mean the account's balance or
// quota is gone — a state the next request cannot clear. Ordinary throttling
// ("rate_limit_error" / "rate_limit_exceeded") is deliberately absent: it is
// transient and belongs to the backoff.
var quota429Markers = []string{
	"insufficient_quota",
	"credit_balance_exhausted",
}

// DoWithRetry calls fn up to maxAttempts times. Between attempts it sleeps
// attempt² × base (quadratic back-off), respecting ctx cancellation.
// Returns nil on the first success; returns the last error if all attempts fail.
//
// Usage pattern for closures that capture their result:
//
//	var result T
//	err := DoWithRetry(ctx, p.maxRetries+1, 2*time.Second, func() error {
//	    var innerErr error
//	    result, innerErr = callAPI(ctx, ...)
//	    return innerErr
//	})
func DoWithRetry(ctx context.Context, maxAttempts int, base time.Duration, fn func() error) error {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * base
			slog.Warn("ai call failed, retrying after backoff",
				"attempt", attempt, "max_attempts", maxAttempts, "backoff", backoff, "err", lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		if err := fn(); err != nil {
			if isPermanentAIError(err) {
				return &PermanentError{Err: err}
			}
			lastErr = err
			continue
		}
		return nil
	}
	slog.Error("ai call retries exhausted", "max_attempts", maxAttempts, "err", lastErr)
	return lastErr
}
