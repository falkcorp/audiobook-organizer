// file: core/integrations/errors.go
// version: 1.0.0
// guid: 061cdbef-2b2e-4a42-9f4f-c1db219ab223
// last-edited: 2026-11-11

package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"syscall"
	"time"
)

// ---- error taxonomy -------------------------------------------------------

// Class answers ONE question per value; callers decide retry/failover/hold
// from the class, never from strings.
type Class uint8

const (
	ClassUnknown      Class = iota // unrecognised: treated as Permanent for retry, Transient for health
	ClassCanceled                  // our ctx was canceled or its deadline passed; says nothing about the remote
	ClassRefusedLocal              // we refused before sending (hold, open breaker, spent daily budget, egress guard)
	ClassTransient                 // transport failure, 5xx, 408, per-attempt timeout on a live parent ctx
	ClassRateLimited               // burst overrun (429 or rate-limit vocabulary in a 403); RetryAfter may be set
	ClassQuota                     // per-day/credit allowance spent; retry after the quota window
	ClassAuth                      // credentials rejected/expired; permanent until a human acts
	ClassNotFound                  // a definitive "no such item" answer; NOT a remote failure
	ClassInvalid                   // the request is wrong (400/404-without-JSON excluded, 422, protocol error code)
	ClassUnsupported               // the instance lacks the capability (caps say no)
	ClassBadOutput                 // the remote answered 2xx but the answer is unusable (bad JSON, refusal, short array)
)

// String names the class ("transient", "auth", ...), for logs and labels.
func (c Class) String() string {
	switch c {
	case ClassUnknown:
		return "unknown"
	case ClassCanceled:
		return "canceled"
	case ClassRefusedLocal:
		return "refused_local"
	case ClassTransient:
		return "transient"
	case ClassRateLimited:
		return "rate_limited"
	case ClassQuota:
		return "quota"
	case ClassAuth:
		return "auth"
	case ClassNotFound:
		return "not_found"
	case ClassInvalid:
		return "invalid"
	case ClassUnsupported:
		return "unsupported"
	case ClassBadOutput:
		return "bad_output"
	}
	return "class(" + strconv.Itoa(int(c)) + ")"
}

// Retryable reports Transient and RateLimited (same target, after backoff).
func (c Class) Retryable() bool { return c == ClassTransient || c == ClassRateLimited }

// CountsAgainstRemote reports Transient, RateLimited, Quota and Auth (breaker,
// hold and health input).
func (c Class) CountsAgainstRemote() bool {
	switch c {
	case ClassTransient, ClassRateLimited, ClassQuota, ClassAuth:
		return true
	}
	return false
}

// Error is the one error type adapters return for remote failures.
type Error struct {
	Ref        Ref
	Op         string // "add", "search", "generate", "send", ...
	Class      Class
	Status     int           // HTTP status or protocol code; 0 if none
	RetryAfter time.Duration // from Retry-After or the protocol; 0 if none
	Detail     string        // bounded (512 B), redacted body excerpt
	Err        error
}

// MaxDetail bounds Error.Detail (05-metadata M1.3).
const MaxDetail = 512

// Error describes e without its Detail: the excerpt of a remote body is for
// operators reading the instance's own log, never for an error string that a
// handler, a test result or a log line of another component might carry.
func (e *Error) Error() string {
	msg := "integrations: " + e.Ref.String()
	if e.Op != "" {
		msg += " " + e.Op
	}
	msg += ": " + e.Class.String()
	if e.Status != 0 {
		msg += " (status " + strconv.Itoa(e.Status) + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns e.Err.
func (e *Error) Unwrap() error { return e.Err }

// ClassOf classifies any error. parent is the CALLER's ctx: a done parent
// always yields ClassCanceled (same rule as aidispatch.Classify, classify.go:97).
//
// The order: nil is ClassUnknown; a done parent is ClassCanceled; an *Error
// in the chain gives its Class; the Registry's local refusals
// (ErrDisabled, ErrOperatorSecretWithheld, ErrNotConfigured,
// ErrUnknownAdapter) are ClassRefusedLocal; context.Canceled is
// ClassCanceled; a deadline or a transport failure on a live parent is
// ClassTransient; anything else is ClassUnknown.
func ClassOf(parent context.Context, err error) Class {
	if err == nil {
		return ClassUnknown
	}
	if parent != nil && parent.Err() != nil {
		return ClassCanceled
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Class
	}
	switch {
	case errors.Is(err, ErrDisabled), errors.Is(err, ErrOperatorSecretWithheld),
		errors.Is(err, ErrNotConfigured), errors.Is(err, ErrUnknownAdapter):
		return ClassRefusedLocal
	case errors.Is(err, context.Canceled):
		return ClassCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ClassTransient
	case transportError(err):
		return ClassTransient
	}
	return ClassUnknown
}

// transportError recognises a request that never got an answer: a network
// error, a refused or reset connection, a truncated response.
func transportError(err error) bool {
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

var (
	ErrNotConfigured  = errors.New("integrations: instance not configured")
	ErrDisabled       = errors.New("integrations: instance disabled")
	ErrUnknownAdapter = errors.New("integrations: unknown adapter")
	ErrFieldLocked    = errors.New("integrations: field is set by an operator value")
	// ErrOperatorSecretWithheld: an operator-sourced secret of an
	// instance is not served to a destination that is neither the operator's
	// (or the adapter default) nor the instance's OperatorSecretOrigin (M1.4
	// step 2, SEC7-3, R8-5).
	ErrOperatorSecretWithheld = errors.New("integrations: operator-supplied secret withheld from a destination that no operator or admin set")
)

// The sentinels F7-01 coins beside M1.3's, one per status M1.5 fixes, so the
// HTTP surface (F7-10) maps each to its status without reading messages.
var (
	// ErrInvalid (422): the instance or its settings fail validation (ID
	// grammar, adapter prefix, schema, a secret value in a non-secret
	// field, userinfo in a URL); nothing is written.
	ErrInvalid = errors.New("integrations: invalid instance")
	// ErrForbidden (403): the principal lacks core:integrations.manage, or a
	// write needs the ProtectAdmin check (core:admin and an interactive
	// principal) it does not pass: setting Local, keeping it across a
	// destination change, or a Test with Local=true.
	ErrForbidden = errors.New("integrations: forbidden")
	// ErrVersionConflict (409): the Put's Version is not the stored row's,
	// or another writer committed first.
	ErrVersionConflict = errors.New("integrations: version conflict")
	// ErrUnavailable (503): the instance store or the secrets backend cannot
	// be read or written, so nothing is written (a store that cannot be read
	// must not be mistaken for "no instances" and overwritten).
	ErrUnavailable = errors.New("integrations: instance store unavailable")
	// ErrClosed: the Registry is shut down; no lease is issued.
	ErrClosed = errors.New("integrations: registry closed")
)

// refused is an *Error of ClassRefusedLocal for ref wrapping err.
func refused(ref Ref, op string, err error) *Error {
	return &Error{Ref: ref, Op: op, Class: ClassRefusedLocal, Err: err}
}

// invalidf is ErrInvalid with a message.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
