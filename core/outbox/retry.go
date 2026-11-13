// file: core/outbox/retry.go
// version: 1.0.1
// guid: 5d0b7a6e-3c1f-4a7e-9b52-0e8f1c6d4a93
// last-edited: 2026-11-13

package outbox

import (
	"errors"
	"time"
)

// retryHint is an error a handler returns to steer the retry of its own
// failed delivery: when it next runs, and whether the failure used up an
// attempt.
type retryHint struct {
	err   error
	after time.Duration
	free  bool
}

func (h *retryHint) Error() string { return h.err.Error() }
func (h *retryHint) Unwrap() error { return h.err }

// RetryAfter wraps err so the failed message is retried after d instead of
// after the backoff curve's delay: a server's Retry-After, for one. The
// attempt still counts, and a permanent err (ErrPermanent) still dead-letters
// at once. d is raised to one millisecond when it is not positive.
func RetryAfter(d time.Duration, err error) error {
	if err == nil {
		return nil
	}
	return &retryHint{err: err, after: max(d, time.Millisecond)}
}

// RetryFree is RetryAfter for a delivery that could not even be tried because
// something the handler depends on is switched off: the message waits d and
// the failed delivery does not use up one of its attempts, so a long pause
// cannot dead-letter it. The handler owns the bound (an age limit, say) on
// how long such a message may wait. The failure still records the usual
// token and log line.
func RetryFree(d time.Duration, err error) error {
	if err == nil {
		return nil
	}
	return &retryHint{err: err, after: max(d, time.Millisecond), free: true}
}

// hintOf returns the retry hint carried by err, if any. A permanent error
// wins over a hint: the caller checks classify first.
func hintOf(err error) (*retryHint, bool) {
	if h, ok := errors.AsType[*retryHint](err); ok {
		return h, true
	}
	return nil, false
}
