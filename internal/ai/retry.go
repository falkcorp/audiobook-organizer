// file: internal/ai/retry.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-fabc-678901234567
// last-edited: 2026-06-22

// Package ai — shared retry helper for OpenAI / Ollama API calls.
package ai

import (
	"context"
	"time"
)

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
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		if err := fn(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}
