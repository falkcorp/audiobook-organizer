// file: internal/aidispatch/health.go
// version: 1.0.0
// guid: eacb4ad5-2ec7-46df-b430-903591356e6c
// last-edited: 2026-09-13

package aidispatch

import (
	"sync"
	"time"
)

// Cooldown policy, lifted unchanged from internal/transcribe/dispatcher.go: an
// endpoint that fails is benched for CooldownBase × consecutive-failures,
// capped at CooldownMax, so a dead box is retried occasionally without being
// hammered on every page.
const (
	CooldownBase = 30 * time.Second
	CooldownMax  = 5 * time.Minute
	// QuotaCooldown benches an endpoint whose provider said its quota or
	// credit is gone. That is not cleared by the next request, so the normal
	// 30s step would only re-learn the same answer every half minute.
	QuotaCooldown = 30 * time.Minute
)

type endpointHealth struct {
	consecFails   int
	cooldownUntil time.Time
}

// Health tracks per-endpoint consecutive failures and cooldown windows. It is
// keyed by endpoint ID. The package default instance is process-wide so the
// bench persists across dispatches, which is what makes it useful.
type Health struct {
	mu  sync.Mutex
	m   map[string]*endpointHealth
	now func() time.Time
}

// NewHealth returns an empty tracker using the wall clock.
func NewHealth() *Health {
	return &Health{m: map[string]*endpointHealth{}, now: time.Now}
}

// newHealthWithClock is for tests that need to step time.
func newHealthWithClock(now func() time.Time) *Health {
	h := NewHealth()
	h.now = now
	return h
}

var defaultHealth = NewHealth()

// DefaultHealth is the process-wide tracker.
func DefaultHealth() *Health { return defaultHealth }

func (h *Health) entry(id string) *endpointHealth {
	e := h.m[id]
	if e == nil {
		e = &endpointHealth{}
		h.m[id] = e
	}
	return e
}

// MarkFailure benches id for CooldownBase × consecutive failures, capped at
// CooldownMax.
func (h *Health) MarkFailure(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entry(id)
	e.consecFails++
	d := min(time.Duration(e.consecFails)*CooldownBase, CooldownMax)
	e.cooldownUntil = h.now().Add(d)
}

// MarkQuotaExhausted benches id for QuotaCooldown.
func (h *Health) MarkQuotaExhausted(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entry(id)
	e.consecFails++
	e.cooldownUntil = h.now().Add(QuotaCooldown)
}

// MarkSuccess clears id's failure count and cooldown.
func (h *Health) MarkSuccess(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.m[id]; e != nil {
		e.consecFails = 0
		e.cooldownUntil = time.Time{}
	}
}

// InCooldown reports whether id is currently benched.
func (h *Health) InCooldown(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.m[id]
	return e != nil && h.now().Before(e.cooldownUntil)
}

// CooldownUntil returns when id's bench ends (zero if never benched).
func (h *Health) CooldownUntil(id string) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.m[id]; e != nil {
		return e.cooldownUntil
	}
	return time.Time{}
}
