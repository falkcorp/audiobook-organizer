// file: internal/server/handlers/abs/identity_throttle_test.go
// version: 0.1.0
// guid: 6e1b4c93-7d28-4a05-9f4e-b3a8d2c6e150
// last-edited: 2026-11-21

package abs_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// F4-08 B10b1, A8: under core identity the ABS limiters key on the visitor
// bucket the wiring builds from cfaccess.ClientAddrTrust, not on the gin copy
// (ThrottleKey, which reads a binder marker that does not run there) and not
// on the raw peer. Every request in these tests comes from the same peer
// (httptest's fixed RemoteAddr, a tunnel connector's stand-in), so a limiter
// keyed on the peer or on the gin copy puts every visitor in one bucket.

const ginCopyBucket = "gin-copy-bucket"

// withBothKeys sets the gin copy to ONE shared bucket and the identity key to
// the per-visitor header, so the two answers differ for every request.
func withBothKeys() harnessOpt {
	return func(o *abshandler.Options) {
		o.ThrottleKey = func(*gin.Context) string { return ginCopyBucket }
		o.IdentityThrottleKey = func(r *http.Request) string { return r.Header.Get(visitorHeader) }
	}
}

func spend(t *testing.T, h *harness, fail func(*testing.T, *harness, string) int) {
	t.Helper()
	for i := range coreauth.DefaultMaxFailuresPerSource {
		if code := fail(t, h, "192.0.2.10"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, code)
		}
	}
}

// The failure throttle of POST /login and POST /auth/refresh.
func TestIdentityThrottle_FailureBucketIsTheIdentityKey(t *testing.T) {
	for name, fail := range map[string]func(*testing.T, *harness, string) int{"login": failLogin, "refresh": failRefresh} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "cf,jwt", nil, withBothKeys())
			h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
			h.underCoreIdentity(t)
			spend(t, h, fail)
			if code := fail(t, h, "192.0.2.10"); code != http.StatusTooManyRequests {
				t.Fatalf("the visitor past its budget: %d, want 429", code)
			}
			if code := fail(t, h, "192.0.2.11"); code != http.StatusUnauthorized {
				t.Fatalf("another visitor behind the same peer: %d, want 401 (its own bucket, not the gin copy's or the peer's)", code)
			}
		})
	}
}

// Without an identity the gin copy is still authoritative (it is deleted in
// B10h): the identity key is not read.
func TestIdentityThrottle_WithoutAnIdentityTheGinCopyDecides(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withBothKeys())
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	spend(t, h, failLogin)
	if code := failLogin(t, h, "192.0.2.11"); code != http.StatusTooManyRequests {
		t.Fatalf("another visitor on a server without an identity: %d, want 429 (the gin copy's one shared bucket)", code)
	}
}

// An identity key that cannot name the visitor never turns the throttle off:
// the request falls back to the gin copy, then the peer.
func TestIdentityThrottle_EmptyIdentityKeyFallsBack(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withBothKeys())
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	h.underCoreIdentity(t)
	for i := range coreauth.DefaultMaxFailuresPerSource {
		if code := failLogin(t, h, ""); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, code)
		}
	}
	if code := failLogin(t, h, ""); code != http.StatusTooManyRequests {
		t.Fatalf("past the budget with no visitor key: %d, want 429", code)
	}
}

func hop1As(t *testing.T, h *harness, visitor string) int {
	t.Helper()
	w, _ := h.do(t, request{
		method: http.MethodGet, path: authorizePath(s256(oidcVerifier), ""),
		headers: map[string]string{cfaccess.CFAccessHeader: oidcPersonAssertion, visitorHeader: visitor},
	})
	return w.Code
}

// Hop 1's per-credential rate limiter: the credential and the visitor bucket.
func TestIdentityThrottle_Hop1RateKeyCarriesTheIdentityKey(t *testing.T) {
	h := newOIDCHarness(t, withBothKeys())
	h.underCoreIdentity(t)
	for i := range 5 {
		if code := hop1As(t, h, "192.0.2.10"); code != http.StatusFound {
			t.Fatalf("request %d = %d, want 302 within the burst", i, code)
		}
	}
	if code := hop1As(t, h, "192.0.2.10"); code != http.StatusBadRequest {
		t.Fatalf("over the burst = %d, want 400 rate-limited", code)
	}
	if code := hop1As(t, h, "192.0.2.11"); code != http.StatusFound {
		t.Fatalf("the same credential from another visitor = %d, want 302 (its own bucket, not the peer's)", code)
	}
}

// Without an identity hop 1 keeps c.ClientIP(): one bucket per peer.
func TestIdentityThrottle_Hop1WithoutAnIdentityKeysOnThePeer(t *testing.T) {
	h := newOIDCHarness(t, withBothKeys())
	for i := range 5 {
		if code := hop1As(t, h, "192.0.2.10"); code != http.StatusFound {
			t.Fatalf("request %d = %d, want 302 within the burst", i, code)
		}
	}
	if code := hop1As(t, h, "192.0.2.11"); code != http.StatusBadRequest {
		t.Fatalf("another visitor on a server without an identity = %d, want 400 (the peer's shared bucket)", code)
	}
}
