// file: internal/metadata/throttle.go
// version: 1.0.0
// guid: 589d6eef-182c-4245-9722-d24bbd3dcf06
// last-edited: 2026-09-03

package metadata

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// Global provider throttling, modelled on Bazarr's provider_throttle_map and
// throttled_providers.dat.
//
// 🔴 WHY. Measured on prod 2026-09-03: 635 "circuit breaker open" events in 12
// minutes, all Google Books, while its DAILY quota was exhausted. The breaker
// was built with a 30-second cooldown, so every 30 seconds it half-opened and
// spent a real API call to probe a quota that would not reset for hours. That
// probe traffic is what turns a quota block into a harder block.
//
// Three properties the breaker does not have and this does:
//
//   - The duration matches the FAILURE, not one global constant. A daily quota
//     is not a 500 is not a dial timeout.
//   - It is PERSISTED. Prod restarted 146 times in 30 days; an in-memory
//     breaker forgets the block on every restart and resumes hammering.
//   - A throttled provider is REMOVED FROM THE CHAIN rather than called and
//     failed. The breaker left it in, so a blocked provider produced one
//     fetch_error per book — 22,934 of them on the run that had to be
//     cancelled — instead of simply being skipped.

// ThrottleReason classifies why a provider is being left alone.
type ThrottleReason string

const (
	// ThrottleDailyQuota is a per-day allowance that is spent. Retrying before
	// the window rolls over cannot succeed.
	ThrottleDailyQuota ThrottleReason = "daily-quota"
	// ThrottleRateLimit is a burst overrun: the provider will serve us again
	// shortly.
	ThrottleRateLimit ThrottleReason = "rate-limit"
	// ThrottleAuth is a credential or configuration problem. It needs a human,
	// so the hold is long — retrying cannot fix it and looks like an attack.
	ThrottleAuth ThrottleReason = "auth"
	// ThrottleUnavailable is a server-side fault (5xx).
	ThrottleUnavailable ThrottleReason = "unavailable"
	// ThrottleTransport is a dial failure, timeout or truncated response.
	ThrottleTransport ThrottleReason = "transport"
)

// throttleDurations is the analogue of Bazarr's provider_throttle_map: one
// duration per failure class, deliberately spread across three orders of
// magnitude because the underlying problems are.
var throttleDurations = map[ThrottleReason]time.Duration{
	ThrottleDailyQuota:  4 * time.Hour,
	ThrottleRateLimit:   15 * time.Minute,
	ThrottleAuth:        6 * time.Hour,
	ThrottleUnavailable: 30 * time.Minute,
	ThrottleTransport:   5 * time.Minute,
}

// DurationFor returns the hold for a reason, or 0 when unknown.
func DurationFor(r ThrottleReason) time.Duration { return throttleDurations[r] }

// dailyQuotaMarkers are the phrases providers use for an exhausted per-day
// allowance, as opposed to a burst limit. Google Books sends
// "Quota exceeded for quota metric 'Queries' and limit 'Queries per day'";
// its machine-readable reason is "dailyLimitExceeded".
var dailyQuotaMarkers = []string{
	"per day",
	"daily limit",
	"dailylimitexceeded",
	"quota exceeded for quota metric",
	"quotaexceeded",
}

// ProviderThrottle is one provider's hold.
type ProviderThrottle struct {
	ProviderID string         `json:"provider_id"`
	Reason     ThrottleReason `json:"reason"`
	Detail     string         `json:"detail,omitempty"`
	Until      time.Time      `json:"until"`
	SetAt      time.Time      `json:"set_at"`
}

// Active reports whether the hold still applies at t.
func (p ProviderThrottle) Active(t time.Time) bool { return t.Before(p.Until) }

// Remaining is how much longer the hold lasts, floored at zero.
func (p ProviderThrottle) Remaining(t time.Time) time.Duration {
	if d := p.Until.Sub(t); d > 0 {
		return d
	}
	return 0
}

// ClassifyProviderError maps a provider failure to a throttle reason and how
// long to stand off. ok is false for errors that say nothing about the
// provider's health — a cancelled context is OUR doing, not theirs, and must
// never throttle anyone.
func ClassifyProviderError(err error) (reason ThrottleReason, hold time.Duration, ok bool) {
	if err == nil {
		return "", 0, false
	}
	// Our own cancellation, and our own deadline. Throttling on either would let
	// one cancelled bulk run lock every provider out.
	//
	// The deadline case is subtle and cost us a design pass: when a PARENT
	// context's deadline fires mid-request, net/http surfaces it as a
	// *url.Error wrapping context.DeadlineExceeded, and *url.Error reports
	// Timeout() == true -- so it satisfies net.Error and is indistinguishable
	// from a provider that genuinely hung. Checking errors.Is FIRST gives up
	// throttling on real provider hangs that arrive this way, and keeps us from
	// locking a healthy provider out because WE ran out of time. Dial refusals,
	// DNS failures and connection resets do not wrap these sentinels and still
	// classify as transport below.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", 0, false
	}

	var se *ProviderStatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == 429:
			low := strings.ToLower(se.Body)
			for _, m := range dailyQuotaMarkers {
				if strings.Contains(low, m) {
					return ThrottleDailyQuota, throttleDurations[ThrottleDailyQuota], true
				}
			}
			// Honour Retry-After when the provider tells us how long, but never
			// shorter than the burst default -- a server that says "1 second"
			// while refusing every call just reproduces the hammering.
			hold := throttleDurations[ThrottleRateLimit]
			if se.RetryAfter > hold {
				hold = se.RetryAfter
			}
			return ThrottleRateLimit, hold, true
		case se.Status == 401 || se.Status == 403:
			return ThrottleAuth, throttleDurations[ThrottleAuth], true
		case se.Status >= 500:
			return ThrottleUnavailable, throttleDurations[ThrottleUnavailable], true
		}
		// Any other non-2xx (404, 400) is about the QUERY, not the provider.
		return "", 0, false
	}

	// Transport-level trouble: dial refused, DNS, connection reset.
	var ne net.Error
	if errors.As(err, &ne) {
		return ThrottleTransport, throttleDurations[ThrottleTransport], true
	}
	return "", 0, false
}

// ThrottleStore persists holds so they survive a restart.
//
// Deliberately a typed KV over JSON payloads rather than an interface in terms
// of ProviderThrottle. internal/metadata already imports internal/database, so
// a database-side implementation cannot name this package's types without a
// cycle. Keeping the port at []byte leaves the domain type here, where it
// belongs, and leaves the store a dumb keyspace -- and it adds NOTHING to
// database.Store, which is 398 methods wide and gated by an interface-width
// ratchet. Consumers capability-assert for it.
type ThrottleStore interface {
	// LoadProviderThrottles returns every persisted payload, keyed by provider id.
	LoadProviderThrottles() (map[string][]byte, error)
	SaveProviderThrottle(providerID string, payload []byte) error
	DeleteProviderThrottle(providerID string) error
}
