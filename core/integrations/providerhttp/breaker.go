// file: core/integrations/providerhttp/breaker.go
// version: 1.0.0
// guid: 731b6fc1-8ef0-48f4-97b3-dd5cd564eb40
// last-edited: 2026-11-11

package providerhttp

import (
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// verdict is what one finished attempt says about the remote's health.
type verdict uint8

const (
	// verdictNone: the attempt reached nobody (canceled, refused, unknown):
	// not counted, and a probe's lease is merely released.
	verdictNone verdict = iota
	// verdictOK: the remote answered, with a success or with an answer about
	// the QUERY (not found, invalid, bad output): it is alive.
	verdictOK
	// verdictFail: Transient, RateLimited, Quota or Auth (CountsAgainstRemote).
	verdictFail
)

// verdictOf maps a class to what the breaker learns. success is a 2xx/3xx.
func verdictOf(c integrations.Class, success bool) verdict {
	if success {
		return verdictOK
	}
	switch {
	case c.CountsAgainstRemote():
		return verdictFail
	case c == integrations.ClassNotFound, c == integrations.ClassInvalid, c == integrations.ClassBadOutput,
		c == integrations.ClassUnsupported:
		return verdictOK
	}
	return verdictNone
}

// breaker is one scope's circuit breaker with a probe lease. The caller
// (scopeState) serialises access. State changes are evaluated lazily at the
// next admit or read; nothing runs on a timer.
//
// The three HEAD defects it replaces (05-metadata M2.2): a half-open breaker
// whose one probe never reports back is reopened when its lease expires and
// counts as a failure; a canceled attempt is never counted; the state lives
// with the scope, not with a chain build.
type breaker struct {
	state      integrations.BreakerState
	fails      int
	openedAt   time.Time
	leaseHeld  bool
	leaseUntil time.Time
	epoch      uint64 // numbers the leases, so a stale probe's outcome is ignored
}

// expire reopens a half-open breaker whose probe lease ran out unanswered.
func (b *breaker) expire(now time.Time) {
	if b.state == integrations.BreakerHalfOpen && b.leaseHeld && !now.Before(b.leaseUntil) {
		b.state = integrations.BreakerOpen
		b.openedAt = b.leaseUntil
		b.leaseHeld = false
	}
}

// admit decides whether an attempt may start. probe is true when it holds the
// single half-open lease; epoch names that lease.
func (b *breaker) admit(now time.Time, pol integrations.BreakerPolicy) (probe bool, epoch uint64, until time.Time, ok bool) {
	b.expire(now)
	if b.state == integrations.BreakerOpen {
		if now.Before(b.openedAt.Add(pol.Cooldown)) {
			return false, 0, b.openedAt.Add(pol.Cooldown), false
		}
		b.state = integrations.BreakerHalfOpen
		b.leaseHeld = false
	}
	if b.state == integrations.BreakerHalfOpen {
		if b.leaseHeld {
			return false, 0, b.leaseUntil, false
		}
		b.leaseHeld = true
		b.leaseUntil = now.Add(pol.ProbeTimeout)
		b.epoch++
		return true, b.epoch, time.Time{}, true
	}
	return false, 0, time.Time{}, true
}

// record applies one finished attempt. probe and epoch are admit's.
func (b *breaker) record(now time.Time, pol integrations.BreakerPolicy, probe bool, epoch uint64, v verdict) {
	b.expire(now)
	if probe {
		if b.state != integrations.BreakerHalfOpen || !b.leaseHeld || b.epoch != epoch {
			return // its lease expired and was reopened or replaced
		}
		switch v {
		case verdictOK:
			b.state, b.fails, b.leaseHeld = integrations.BreakerClosed, 0, false
		case verdictFail:
			b.state, b.openedAt, b.leaseHeld = integrations.BreakerOpen, now, false
		default:
			b.leaseHeld = false
		}
		return
	}
	if b.state != integrations.BreakerClosed {
		return
	}
	switch v {
	case verdictOK:
		b.fails = 0
	case verdictFail:
		b.fails++
		if b.fails >= pol.Threshold {
			b.state, b.openedAt = integrations.BreakerOpen, now
		}
	}
}

// view is the state a reader sees at now, without changing it.
func (b *breaker) view(now time.Time) integrations.BreakerState {
	c := *b
	c.expire(now)
	return c.state
}
