// file: internal/applycap/applycap.go
// version: 1.0.0
// guid: 4c1e8f2a-9b7d-4e63-a5c0-2f8d6b1e9a37
// last-edited: 2026-09-02
//
// Package applycap is the fail-safe ceiling on how many books one bulk APPLY
// may touch. It exists because several apply surfaces (metadata review, review
// queue, auto-match, batch update, reconcile) have at one time or another been
// triggered with "the whole list" as their target — a filter that matched
// everything, a queued-merge union, an empty scope that meant "all" — and
// nothing between the request and the writes ever asked "is that a plausible
// amount of work for a human to have meant?".
//
// The cap is a REFUSAL, never a truncation: an over-cap request is answered
// with an *ExceededError and ZERO writes happen. Truncating to the first N
// would silently apply a partial batch, which is exactly the class of surprise
// the cap is there to prevent. A caller that really wants more than the cap
// raises `bulk_apply_max_items` in config and re-issues the request.
//
// This package deliberately does not import internal/config. Callers pass the
// configured value in; a leaf package can be used from the review handler,
// which is intentionally decoupled from the global config (see
// handlers/review.New's applyEnabled gate for the pattern this follows).
package applycap

import (
	"fmt"
	"sync/atomic"
)

// Default is the ceiling used when the configured value is unset or invalid.
// 5,000 is roughly a tenth of the production library: large enough that any
// deliberately-selected batch fits, small enough that "everything" never does.
const Default = 5000

// Effective returns the cap to enforce for a configured value. Zero or a
// negative value means "not configured" and yields Default — it never means
// "unlimited". A zero written by a full-struct config save (see
// feedback: a `0` config value can become a silent kill switch) must not be
// able to disable the fail-safe.
func Effective(configured int) int {
	if configured <= 0 {
		return Default
	}
	return configured
}

// ExceededError is returned when a bulk apply would touch more items than the
// cap allows. It is a refusal: the caller must not have written anything.
type ExceededError struct {
	// Op names the surface that refused, for logs and the HTTP body.
	Op string
	// Requested is how many items the caller wanted to apply to.
	Requested int
	// Cap is the ceiling that was in force.
	Cap int
}

// Error renders the refusal in one line that names the config key, so the
// operator reading it knows both what happened and how to lift it on purpose.
func (e *ExceededError) Error() string {
	return fmt.Sprintf("%s: refusing to apply to %d items — the bulk apply cap is %d "+
		"(raise bulk_apply_max_items to allow more); nothing was applied", e.Op, e.Requested, e.Cap)
}

// Refuse returns the refusal for a batch of `requested` items when it exceeds
// the effective cap for `configured`, or nil when the whole batch may proceed.
// It returns the concrete pointer (not `error`) so HTTP handlers can hand it
// straight to httputil.RespondWithApplyCapExceeded without a type assertion.
func Refuse(op string, requested, configured int) *ExceededError {
	limit := Effective(configured)
	if requested > limit {
		return &ExceededError{Op: op, Requested: requested, Cap: limit}
	}
	return nil
}

// Check is Refuse for callers that propagate an `error` (op Run functions).
// It never returns a typed-nil interface: a passing check is a plain nil.
func Check(op string, requested, configured int) error {
	if ex := Refuse(op, requested, configured); ex != nil {
		return ex
	}
	return nil
}

// Fits reports whether `requested` items are within the effective cap. Used by
// queued-parameter merge functions, which must DECLINE a union that would
// exceed the cap (the registry then queues a second run) rather than error.
func Fits(requested, configured int) bool {
	return requested <= Effective(configured)
}

// Counter admits applies one at a time for streaming ops whose target set is
// not known up front (auto-match walks the whole library and decides per book).
// The (cap+1)th Admit returns *ExceededError; the caller must stop the op there.
// It is safe for concurrent use.
type Counter struct {
	op       string
	limit    int
	admitted atomic.Int64
}

// NewCounter builds a Counter enforcing the effective cap for `configured`.
func NewCounter(op string, configured int) *Counter {
	return &Counter{op: op, limit: Effective(configured)}
}

// Admit records one more apply and refuses it when it would be the (cap+1)th.
// On refusal the count is not advanced, so Admitted stays equal to the number
// of applies that were actually allowed.
func (c *Counter) Admit() error {
	n := c.admitted.Add(1)
	if n > int64(c.limit) {
		c.admitted.Add(-1)
		return &ExceededError{Op: c.op, Requested: int(n), Cap: c.limit}
	}
	return nil
}

// Admitted returns how many applies have been allowed so far.
func (c *Counter) Admitted() int { return int(c.admitted.Load()) }

// Cap returns the ceiling this counter enforces.
func (c *Counter) Cap() int { return c.limit }
