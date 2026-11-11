// file: core/integrations/providerhttp/errors.go
// version: 1.0.0
// guid: 8939849f-6501-4def-ad14-936946a2d4c7
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"errors"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// Reason says which stage of the gate refused an attempt (M2.8's refused_total
// "reason" label values, plus the two refusals that name no metric series).
type Reason string

// The refusal reasons.
const (
	ReasonHold     Reason = "hold"
	ReasonBreaker  Reason = "breaker"
	ReasonQuota    Reason = "quota"
	ReasonEgress   Reason = "egress"
	ReasonDisabled Reason = "disabled"
)

// ErrQuotaSpent: the daily allowance of the tier the attempt runs at is used
// up. A refusal made before anything was sent.
var ErrQuotaSpent = errors.New("providerhttp: daily quota spent")

// ErrBudgetUnavailable: a daily counter could not be read or persisted, so the
// attempt is refused. Over-counting is the safe direction: a quota the gate
// cannot read could be spent (M2.5, HEAD's dailyquota.ErrBudgetUnavailable).
var ErrBudgetUnavailable = errors.New("providerhttp: daily quota unavailable")

// ErrUnknownCounter: the attempt names a counter the instance's limits do not
// declare. Refused rather than counted nowhere.
var ErrUnknownCounter = errors.New("providerhttp: unknown quota counter")

// ErrResponseTooLarge: a response body is larger than the instance's limit
// (Policy.MaxBody). It is the read error past the limit, never a silent
// truncation.
var ErrResponseTooLarge = errors.New("providerhttp: response body exceeds the size limit")

// ErrHoldChanged: ReleaseHoldIf found a different hold in force than the one
// the caller saw (a longer one was set, or one replaced it), so it released
// nothing. The caller re-reads (HoldFor) and decides again.
var ErrHoldChanged = errors.New("providerhttp: the hold changed since it was read")

// Refusal is the cause inside the *integrations.Error (ClassRefusedLocal) of
// every attempt the gate refused before sending. Callers that need to tell a
// hold from an open breaker (the metadata chain's Outcome) read Reason with
// errors.As; nothing else about the gate is exposed through it.
type Refusal struct {
	Reason Reason
	// Until is when the refusal is expected to end (a hold's expiry, the
	// breaker's cooldown); zero when unknown.
	Until time.Time
	// Err is the specific cause (ErrQuotaSpent, ErrBudgetUnavailable, the
	// egress guard's ErrBlocked*), nil for a hold or an open breaker.
	Err error
}

func (r *Refusal) Error() string {
	msg := "providerhttp: refused by the " + string(r.Reason) + " gate"
	if r.Err != nil {
		msg += ": " + r.Err.Error()
	}
	return msg
}

// Unwrap returns the specific cause.
func (r *Refusal) Unwrap() error { return r.Err }

// refuse builds the *integrations.Error of one refusal.
func refuse(ref integrations.Ref, op string, why Reason, until time.Time, cause error) *integrations.Error {
	return &integrations.Error{Ref: ref, Op: op, Class: integrations.ClassRefusedLocal,
		Err: &Refusal{Reason: why, Until: until, Err: cause}}
}

// canceled builds the *integrations.Error of an attempt whose caller gave up.
func canceled(ref integrations.Ref, op string, cause error) *integrations.Error {
	return &integrations.Error{Ref: ref, Op: op, Class: integrations.ClassCanceled, Err: cause}
}

// reasonOf is the refusal reason in err's chain, "" when err is no refusal.
func reasonOf(err error) Reason {
	if r, ok := errors.AsType[*Refusal](err); ok {
		return r.Reason
	}
	return ""
}

// ReasonOf reports why the gate refused, from an error returned by a Client,
// a Gate or an adapter that wraps one: ("", false) when err is not a gate
// refusal.
func ReasonOf(err error) (Reason, bool) {
	r := reasonOf(err)
	return r, r != ""
}

// parentDone reports whether the caller's context ended (canceled or past its
// deadline): the attempt says nothing about the remote.
func parentDone(ctx context.Context) bool { return ctx.Err() != nil }
