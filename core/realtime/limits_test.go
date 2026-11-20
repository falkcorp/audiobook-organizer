// file: core/realtime/limits_test.go
// version: 0.1.0
// guid: 8e2d4b71-3f9a-4c05-a6e8-1b7c9d2f40e3
// last-edited: 2026-11-20

package realtime_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/realtime"
)

// TestHub_LiveLimitsApplyToTheNextStream: Options.Live is read at each
// subscribe and each refusal, so a reloaded MaxPerBucket, MaxClients or
// RetryAfter applies to the next stream with no restart; a zero field takes
// the default, and an out-of-range one keeps the value the hub was built with
// and is logged.
func TestHub_LiveLimitsApplyToTheNextStream(t *testing.T) {
	var cur atomic.Pointer[realtime.Limits]
	set := func(l realtime.Limits) { cur.Store(&l) }
	set(realtime.Limits{MaxClients: 5, MaxPerBucket: 1, RetryAfter: 7 * time.Second})
	lg := &logs{}
	c := newCast(t, hubOptions{
		opts: realtime.Options{MaxClients: 5, MaxPerBucket: 1, RetryAfter: 7 * time.Second, Live: func() realtime.Limits { return *cur.Load() }},
		deps: func(d *realtime.Deps) { d.Log = lg.logger() },
	})
	opened := func(who string) {
		t.Helper()
		c.open(t, who, "/events").expectOpened()
	}
	retryAfter := func(who string, status int, code, want string) {
		t.Helper()
		if ra := c.refused(t, who, "/events", status, code).Header.Get("Retry-After"); ra != want {
			t.Errorf("%s refused %d: Retry-After %q, want %q", who, status, ra, want)
		}
	}

	opened("lucius")
	retryAfter("lucius", 429, "throttled", "7")

	set(realtime.Limits{MaxClients: 5, MaxPerBucket: 2, RetryAfter: 3 * time.Second})
	opened("lucius")
	retryAfter("lucius", 429, "throttled", "3")

	// Zero is the default: RetryAfter 5s; two streams are open, the cap is 2.
	set(realtime.Limits{MaxClients: 2, MaxPerBucket: 2})
	retryAfter("dick", 503, "unavailable", "5")

	// Out of range: the built MaxClients (5, not the default) and RetryAfter
	// (7s), logged. Five streams open; the sixth is the built cap's 503.
	set(realtime.Limits{MaxClients: -3, MaxPerBucket: 2, RetryAfter: 2 * time.Hour})
	opened("dick")
	opened("barbara")
	opened("bruce")
	retryAfter("bruce", 503, "unavailable", "7")
	if !strings.Contains(lg.String(), "a reloaded limit is out of range") {
		t.Errorf("an out-of-range reload was not logged:\n%s", lg.String())
	}
}
