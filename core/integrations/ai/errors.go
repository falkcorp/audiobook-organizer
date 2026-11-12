// file: core/integrations/ai/errors.go
// version: 1.0.0
// guid: 4e91b6a3-2d58-4c07-b3f1-9a6c0d8e5f14
// last-edited: 2026-11-12

package ai

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/integrations"
)

// The errors of the package (05-ai section 3).
var (
	ErrNoCapableEndpoint   = errors.New("ai: no capable endpoint")
	ErrUnknownCapability   = errors.New("ai: unregistered capability")
	ErrEmbedModelNotPinned = errors.New("ai: embedding call has no pinned model")
	ErrNoBatchApply        = errors.New("ai: capability has no registered BatchApplyTask")
	ErrJobConflict         = errors.New("ai: the job is no longer in the expected state")
)

// The errors this row coins beside the spec's.
var (
	// ErrInvalidCapability: a CapabilitySpec fails RegisterCapability (an ID
	// outside the module's prefix, a duplicate, a missing text, an unknown
	// kind or feature).
	ErrInvalidCapability = errors.New("ai: invalid capability")
	// ErrCatalogFrozen: RegisterCapability after FreezeCatalog.
	ErrCatalogFrozen = errors.New("ai: the capability catalog is frozen")
	// ErrJobNotFound: no stored job has the ID.
	ErrJobNotFound = errors.New("ai: no such batch job")
	// ErrNoJobStore: a batch operation on a Router built without a JobStore.
	ErrNoJobStore = errors.New("ai: the router has no job store")
	// ErrInvalidEndpoint: EndpointSettings that Router.ValidateEndpoint refuses.
	ErrInvalidEndpoint = errors.New("ai: invalid endpoint settings")
	// ErrSubmitUnconfirmed: SubmitBatch could not tell whether the provider
	// accepted the batch (a timeout, a dropped connection, a failed commit of
	// the handle). The job exists and SubmitBatch returns its ID with this
	// error: the poll finds the remote batch by its tag and records the handle,
	// or fails the job after a grace period. Do not resubmit the items: that
	// could pay for them twice.
	ErrSubmitUnconfirmed = errors.New("ai: batch submission not confirmed")
)

// Refusal is one endpoint the router considered and why it said no.
type Refusal struct {
	Endpoint integrations.Ref
	Reason   string // human text
}

// NoCapableEndpointError is returned when selection leaves zero endpoints.
type NoCapableEndpointError struct { // Is(ErrNoCapableEndpoint)
	Capability string
	Refusals   []Refusal
}

// Error lists every refusal.
func (e *NoCapableEndpointError) Error() string {
	if len(e.Refusals) == 0 {
		return fmt.Sprintf("%v for %s: no AI endpoints configured", ErrNoCapableEndpoint, e.Capability)
	}
	parts := make([]string, 0, len(e.Refusals))
	for _, r := range e.Refusals {
		parts = append(parts, r.Endpoint.String()+": "+r.Reason)
	}
	return fmt.Sprintf("%v for %s (%s)", ErrNoCapableEndpoint, e.Capability, strings.Join(parts, "; "))
}

// Is makes errors.Is(err, ErrNoCapableEndpoint) true.
func (e *NoCapableEndpointError) Is(target error) bool { return target == ErrNoCapableEndpoint }

// AttemptError records one endpoint's failure inside a call.
type AttemptError struct {
	Endpoint integrations.Ref
	Class    integrations.Class
	Err      error
}

// ExhaustedError is returned when every capable endpoint was tried and each
// failed with an error that allowed failover.
type ExhaustedError struct { // Unwrap() []error over attempts
	Capability string
	Attempts   []AttemptError
}

// Error lists every attempt.
func (e *ExhaustedError) Error() string {
	parts := make([]string, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		parts = append(parts, fmt.Sprintf("%s (%s): %v", a.Endpoint, a.Class, a.Err))
	}
	return fmt.Sprintf("all %d AI endpoint attempt(s) for %s failed: %s", len(e.Attempts), e.Capability, strings.Join(parts, "; "))
}

// Unwrap exposes every attempt's error to errors.Is and errors.As.
func (e *ExhaustedError) Unwrap() []error {
	out := make([]error, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		out = append(out, a.Err)
	}
	return out
}

// QualityError marks "the endpoint answered and the answer is the problem": an
// unparseable reply, a refusal, a short result array, a failed validation. It
// is ClassBadOutput and never fails over: another model would hide the
// quality problem and double the load.
//
// The class is carried where integrations.ClassOf looks for it, not only in
// the router's own classify: Unwrap puts an *integrations.Error of
// ClassBadOutput ahead of the wrapped error, so a module, a task or the HTTP
// surface that classifies the returned error reads BadOutput (integrations
// cannot know this type), and it wins over the class of an error the wrapped
// one itself carries.
type QualityError struct{ Err error }

// Error is the wrapped error's text.
func (e *QualityError) Error() string { return e.Err.Error() }

// Unwrap returns the bad-output marker and the wrapped error, in that order.
func (e *QualityError) Unwrap() []error {
	return []error{&integrations.Error{Op: "quality", Class: integrations.ClassBadOutput, Err: e.Err}}
}

// Quality marks err as a quality failure. Nil stays nil.
func Quality(err error) error {
	if err == nil {
		return nil
	}
	return &QualityError{Err: err}
}
