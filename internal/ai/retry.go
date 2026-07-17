// file: internal/ai/retry.go
// version: 1.3.0
// guid: f6a7b8c9-d0e1-2345-fabc-678901234567
// last-edited: 2026-07-17

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
// HTTP 400/401/403/404 response or a 429 with OpenAI error code
// "insufficient_quota". Callers can distinguish "will never succeed" from
// "exhausted retries" via errors.As(err, &PermanentError{}).
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
// never succeed on retry: HTTP 400/401/403/404, or HTTP 429 with error code
// "insufficient_quota" (quota exhaustion is permanent; plain rate-limit 429s
// are transient). Errors that are not *openai.Error (network/timeout errors,
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
		return apiErr.Code == "insufficient_quota"
	default:
		return false
	}
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
