// file: internal/aidispatch/errors.go
// version: 1.1.0
// guid: 5db963f7-aa57-4c7b-8ccf-3c7b1f34b8fb
// last-edited: 2026-09-19

package aidispatch

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSlotWait marks a failure to ACQUIRE an in-flight slot, as distinct from a
// failure of the endpoint itself. Nothing was sent, so callers must not treat
// it as evidence the server is unhealthy: benching an endpoint on it would make
// the next run report "all endpoints in cooldown" about servers that were never
// contacted. (Moved here from internal/transcribe, which re-exports it.)
var ErrSlotWait = errors.New("in-flight slot not acquired")

// ErrNoCapableEndpoint means the feature is switched on but no configured
// endpoint is allowed and able to do this work. It is a fail-closed refusal:
// operations mark the item failed (never complete), handlers return 503.
// Match it with errors.Is; the concrete *NoCapableEndpointError carries the
// per-endpoint reasons.
var ErrNoCapableEndpoint = errors.New("no capable AI endpoint")

// ErrUnknownCapability means the capability is not in the registry. Only the
// zero Capability can reach the dispatcher this way, since the constructor is
// unexported, so this is always a programming error.
var ErrUnknownCapability = errors.New("unregistered AI capability")

// ErrEmbedModelNotPinned means an embedding call was made without
// WithPinnedModel. Embedding endpoints are interchangeable only when they
// serve the SAME model, and spillover picks whichever endpoint has a free
// slot, so an unpinned embed call could land vectors from two models in one
// store. Call refuses it before contacting anything.
var ErrEmbedModelNotPinned = errors.New("embedding call has no pinned model (use WithPinnedModel)")

// Refusal is one endpoint the dispatcher considered and why it said no.
type Refusal struct {
	EndpointID string
	Reason     string
}

// NoCapableEndpointError is returned when selection leaves zero endpoints.
type NoCapableEndpointError struct {
	Capability string
	Refusals   []Refusal
}

func (e *NoCapableEndpointError) Error() string {
	if len(e.Refusals) == 0 {
		return fmt.Sprintf("%v for %s: no AI endpoints configured", ErrNoCapableEndpoint, e.Capability)
	}
	parts := make([]string, 0, len(e.Refusals))
	for _, r := range e.Refusals {
		parts = append(parts, r.EndpointID+": "+r.Reason)
	}
	return fmt.Sprintf("%v for %s (%s)", ErrNoCapableEndpoint, e.Capability, strings.Join(parts, "; "))
}

// Is makes errors.Is(err, ErrNoCapableEndpoint) true.
func (e *NoCapableEndpointError) Is(target error) bool { return target == ErrNoCapableEndpoint }

// AttemptError records one endpoint's failure inside a Call.
type AttemptError struct {
	EndpointID string
	Class      Class
	Err        error
}

// ExhaustedError is returned when every capable endpoint was tried and each
// failed with an error that allowed failover.
type ExhaustedError struct {
	Capability string
	Attempts   []AttemptError
}

func (e *ExhaustedError) Error() string {
	parts := make([]string, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		parts = append(parts, fmt.Sprintf("%s (%s): %v", a.EndpointID, a.Class, a.Err))
	}
	return fmt.Sprintf("all %d AI endpoint attempt(s) for %s failed: %s",
		len(e.Attempts), e.Capability, strings.Join(parts, "; "))
}

// Unwrap exposes every attempt's error to errors.Is / errors.As.
func (e *ExhaustedError) Unwrap() []error {
	out := make([]error, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		out = append(out, a.Err)
	}
	return out
}

// QualityError marks an error the endpoint answered with, but which is about
// the ANSWER: an unparseable reply, a refusal, a short result array, a failed
// validation. It never fails over -- retrying on another model would hide the
// quality problem and double the load. Callers wrap with Quality.
type QualityError struct{ Err error }

func (e *QualityError) Error() string { return e.Err.Error() }
func (e *QualityError) Unwrap() error { return e.Err }

// Quality marks err as a quality failure. Nil stays nil.
func Quality(err error) error {
	if err == nil {
		return nil
	}
	return &QualityError{Err: err}
}

// EndpointError marks an error the caller knows is about the endpoint, not the
// request (for example a shared-path "outside root" reply). It fails over and
// benches the endpoint. Callers wrap with EndpointFailure.
type EndpointError struct{ Err error }

func (e *EndpointError) Error() string { return e.Err.Error() }
func (e *EndpointError) Unwrap() error { return e.Err }

// EndpointFailure marks err as an endpoint-level failure. Nil stays nil.
func EndpointFailure(err error) error {
	if err == nil {
		return nil
	}
	return &EndpointError{Err: err}
}

// StatusError carries an HTTP status from a transport that is not the OpenAI
// SDK (the whisper server, raw Ollama calls), so the classifier can apply the
// same status table to every protocol. Type and Code hold provider error
// strings when the body had them; they are checked for quota markers.
type StatusError struct {
	Status int
	Type   string
	Code   string
	Err    error
}

func (e *StatusError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("HTTP %d: %v", e.Status, e.Err)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

func (e *StatusError) Unwrap() error { return e.Err }
