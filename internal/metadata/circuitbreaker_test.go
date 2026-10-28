// file: internal/metadata/circuitbreaker_test.go
// version: 1.0.1
// guid: f3a4b5c6-d7e8-9012-fa34-567890abcdef
// last-edited: 2026-10-27

package metadata

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeClock is a manually advanced clock for the breaker's cooldown.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// newClockedBreaker returns a breaker whose clock is a fakeClock, so a test
// controls exactly when the cooldown has elapsed.
func newClockedBreaker(threshold int, cooldown time.Duration) (*CircuitBreaker, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	cb := NewCircuitBreaker("test", threshold, cooldown)
	cb.now = clk.Now
	return cb, clk
}

func TestCircuitBreaker_ClosedAllowsRequests(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, time.Second)
	assert.NoError(t, cb.AllowRequest())
	assert.Equal(t, "closed", cb.StateName())
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, time.Second)

	cb.RecordFailure()
	cb.RecordFailure()
	assert.NoError(t, cb.AllowRequest(), "should still allow before threshold")

	cb.RecordFailure()
	assert.ErrorIs(t, cb.AllowRequest(), ErrCircuitOpen)
	assert.Equal(t, "open", cb.StateName())
}

func TestCircuitBreaker_ResetsOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker("test", 2, time.Second)

	cb.RecordFailure()
	cb.RecordSuccess()
	assert.Equal(t, 0, cb.Failures())
	assert.Equal(t, "closed", cb.StateName())
}

func TestCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	cb, clk := newClockedBreaker(1, 10*time.Millisecond)

	cb.RecordFailure()
	assert.ErrorIs(t, cb.AllowRequest(), ErrCircuitOpen)

	// Exactly at the cooldown the circuit is still open (the check is strict).
	clk.Advance(10 * time.Millisecond)
	assert.ErrorIs(t, cb.AllowRequest(), ErrCircuitOpen)

	clk.Advance(20 * time.Millisecond)
	assert.NoError(t, cb.AllowRequest(), "should allow probe after cooldown")
	assert.Equal(t, "half-open", cb.StateName())

	// Second request while half-open should be rejected
	assert.ErrorIs(t, cb.AllowRequest(), ErrCircuitOpen)
}

func TestCircuitBreaker_HalfOpenSuccessCloses(t *testing.T) {
	cb, clk := newClockedBreaker(1, 10*time.Millisecond)

	cb.RecordFailure()
	clk.Advance(20 * time.Millisecond)
	_ = cb.AllowRequest() // transitions to half-open

	cb.RecordSuccess()
	assert.Equal(t, "closed", cb.StateName())
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cb, clk := newClockedBreaker(1, 10*time.Millisecond)

	cb.RecordFailure()
	clk.Advance(20 * time.Millisecond)
	_ = cb.AllowRequest() // transitions to half-open

	cb.RecordFailure()
	assert.Equal(t, "open", cb.StateName())
}
