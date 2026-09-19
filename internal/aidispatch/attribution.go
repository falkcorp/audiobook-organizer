// file: internal/aidispatch/attribution.go
// version: 1.0.0
// guid: 57fa401e-8fd5-43ef-bca8-1401eaaaa424
// last-edited: 2026-09-19

package aidispatch

import (
	"maps"
	"sync"
	"time"
)

// EndpointAttribution is what one endpoint has actually served since process
// start. It exists so an operator can PROVE work landed on a given endpoint
// (GET /api/v1/ai/endpoints/status), which the Prometheus counters alone do
// not answer without a scrape.
type EndpointAttribution struct {
	// Requests counts every attempt handed to this endpoint, successful or not.
	Requests int64 `json:"requests"`
	// Failures counts attempts that ended in anything other than success or a
	// caller stop (cancel): transport errors, 5xx, deadlines, quota, and
	// quality failures alike.
	Failures int64 `json:"failures"`
	// LastUsed is when the most recent attempt finished; zero if never used.
	LastUsed time.Time `json:"last_used"`
	// LastCapability is the capability of that attempt.
	LastCapability string `json:"last_capability,omitempty"`
	// LastOutcome is the Class of that attempt ("ok", "failover", ...).
	LastOutcome string `json:"last_outcome,omitempty"`
	// ByCapability counts attempts per capability ID.
	ByCapability map[string]int64 `json:"by_capability"`
}

// Attribution is a per-endpoint request ledger keyed by endpoint ID. Like
// Slots and Health it is process-wide by default, because the status handler
// builds a fresh Dispatcher per request and every caller builds its own: state
// on the Dispatcher itself would always read as zero.
type Attribution struct {
	mu sync.Mutex
	m  map[string]*EndpointAttribution
}

// NewAttribution returns an empty ledger.
func NewAttribution() *Attribution {
	return &Attribution{m: map[string]*EndpointAttribution{}}
}

var defaultAttribution = NewAttribution()

// DefaultAttribution is the process-wide ledger.
func DefaultAttribution() *Attribution { return defaultAttribution }

// Record notes one finished attempt.
func (a *Attribution) Record(endpointID, capability string, class Class, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.m[endpointID]
	if e == nil {
		e = &EndpointAttribution{ByCapability: map[string]int64{}}
		a.m[endpointID] = e
	}
	e.Requests++
	if class != ClassOK && class != ClassStop {
		e.Failures++
	}
	e.LastUsed = at
	e.LastCapability = capability
	e.LastOutcome = class.String()
	e.ByCapability[capability]++
}

// Snapshot returns a copy of endpointID's counters (zero value, with a non-nil
// ByCapability, if it has never served anything).
func (a *Attribution) Snapshot(endpointID string) EndpointAttribution {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.m[endpointID]
	if e == nil {
		return EndpointAttribution{ByCapability: map[string]int64{}}
	}
	out := *e
	out.ByCapability = maps.Clone(e.ByCapability)
	return out
}
