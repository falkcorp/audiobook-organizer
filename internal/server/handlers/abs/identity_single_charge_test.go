// file: internal/server/handlers/abs/identity_single_charge_test.go
// version: 0.1.0
// guid: 4b9c7e12-8d35-4a60-b1f7-2e6a0c93d584
// last-edited: 2026-11-21

package abs_test

import (
	"net/http"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"

	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// F4-08 B10c, item 7: under core identity a wrong password is charged ONCE. Core
// charges it inside Compat.VerifyPassword, to the client the identity names; the
// surface's own throttle is not charged a second time, which would lock a source
// out at half the budget HEAD gave it (B10 finding: double throttle, ruled to
// core's single bucket).

func withSurfaceThrottle(th *coreauth.Throttle) harnessOpt {
	return func(o *abshandler.Options) { o.Throttle = th }
}

// N failures = N charges: core's throttle counts N, the surface's counts none.
func TestIdentityLogin_EachFailureIsChargedOnce(t *testing.T) {
	const n = 3
	surface := coreauth.NewThrottle(coreauth.ThrottleLimits{}, nil, nil)
	h := newHarness(t, "cf,jwt", nil, withSurfaceThrottle(surface))
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	compat := h.underCoreIdentity(t)
	for range n {
		if code := failLogin(t, h, "192.0.2.10"); code != http.StatusUnauthorized {
			t.Fatalf("a wrong password: %d, want 401", code)
		}
	}
	if got := compat.charges("owner"); got != n {
		t.Errorf("core counted %d failures for %d wrong passwords, want %d", got, n, n)
	}
	if got := surface.AccountFailures(coreauth.AccountKey("owner")); got != 0 {
		t.Errorf("the surface's own throttle counted %d of the %d failures core already charged, want 0: each failure must be charged once", got, n)
	}
}

// The budget core grants a source is the whole budget: the failure that exhausts
// it is the DefaultMaxFailuresPerSource-th, not the half of it a second charge
// would reach.
func TestIdentityLogin_TheSourceKeepsCoresWholeBudget(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	h.underCoreIdentity(t)
	for i := range coreauth.DefaultMaxFailuresPerSource {
		if code := failLogin(t, h, "192.0.2.10"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d of %d: %d, want 401 (not throttled early)", i+1, coreauth.DefaultMaxFailuresPerSource, code)
		}
	}
	if code := failLogin(t, h, "192.0.2.10"); code != http.StatusTooManyRequests {
		t.Fatalf("past core's budget: %d, want 429", code)
	}
}

// What core never sees stays the surface's to count, as at HEAD: an empty
// credential never reaches VerifyPassword, so the surface's throttle is its only
// counter, and it is charged once per request.
func TestIdentityLogin_AnEmptyCredentialIsStillChargedBySurface(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	compat := h.underCoreIdentity(t)
	for i := range coreauth.DefaultMaxFailuresPerSource {
		w, _ := h.do(t, request{
			method: http.MethodPost, path: "/login",
			body:    map[string]any{"username": "", "password": ""},
			headers: map[string]string{visitorHeader: "192.0.2.20"},
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("empty credential %d: %d, want 401", i+1, w.Code)
		}
	}
	if len(compat.verified()) != 0 {
		t.Fatalf("an empty credential reached core: %+v", compat.verified())
	}
	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/login",
		body:    map[string]any{"username": "", "password": ""},
		headers: map[string]string{visitorHeader: "192.0.2.20"},
	})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("past the surface's budget for empty credentials: %d, want 429", w.Code)
	}
}
