// file: internal/server/handlers/abs/login_throttle_key_test.go
// version: 1.0.0
// guid: e4d306c4-3290-49d2-b0b1-c9afae9e0fef
// last-edited: 2026-10-21

package abs_test

import (
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	"github.com/gin-gonic/gin"
)

// visitorHeader stands in, in these tests, for what the server's ThrottleKey
// derives from CF-Connecting-IP behind a tunnel connector: who the visitor is.
const visitorHeader = "X-Test-Visitor"

func withVisitorThrottleKey() harnessOpt {
	return func(o *abshandler.Options) {
		o.ThrottleKey = func(c *gin.Context) string { return c.GetHeader(visitorHeader) }
	}
}

func failLogin(t *testing.T, h *harness, visitor string) int {
	t.Helper()
	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/login",
		body:    map[string]any{"username": "owner", "password": "wrong"},
		headers: map[string]string{visitorHeader: visitor},
	})
	return w.Code
}

func failRefresh(t *testing.T, h *harness, visitor string) int {
	t.Helper()
	// No refresh token at all: a genuinely dead token, charged to the throttle.
	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{visitorHeader: visitor},
	})
	return w.Code
}

// The control. With no ThrottleKey every request comes from the same peer
// (httptest's fixed RemoteAddr, the stand-in for a tunnel connector), so one
// visitor spending the budget locks out every other: the weakness this fixes.
func TestLoginThrottle_DefaultKeyIsOneBucketPerPeer(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil)
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := range absauth.MaxFailuresPerIP {
		if code := failLogin(t, h, "192.0.2.10"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, code)
		}
	}
	if code := failLogin(t, h, "192.0.2.11"); code != http.StatusTooManyRequests {
		t.Fatalf("a second visitor behind the same peer: %d, want 429 (shared bucket)", code)
	}
}

func TestLoginThrottle_ThrottleKeySeparatesVisitorsBehindOnePeer(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey())
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := range absauth.MaxFailuresPerIP {
		if code := failLogin(t, h, "192.0.2.10"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, code)
		}
	}
	if code := failLogin(t, h, "192.0.2.10"); code != http.StatusTooManyRequests {
		t.Fatalf("the visitor past its budget: %d, want 429", code)
	}
	if code := failLogin(t, h, "192.0.2.11"); code != http.StatusUnauthorized {
		t.Fatalf("another visitor through the same peer: %d, want 401 (its own budget)", code)
	}
}

// /auth/refresh charges the same throttle: the visitor's key, not the peer's,
// so one map never holds two kinds of key, and the budget is one budget across
// both endpoints.
func TestRefreshThrottle_UsesTheSameVisitorKeyAsLogin(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey())
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := range absauth.MaxFailuresPerIP {
		if code := failRefresh(t, h, "192.0.2.10"); code != http.StatusUnauthorized {
			t.Fatalf("refresh attempt %d: %d, want 401", i, code)
		}
	}
	if code := failRefresh(t, h, "192.0.2.10"); code != http.StatusTooManyRequests {
		t.Fatalf("refresh past the visitor's budget: %d, want 429", code)
	}
	if code := failRefresh(t, h, "192.0.2.11"); code != http.StatusUnauthorized {
		t.Fatalf("refresh by another visitor through the same peer: %d, want 401", code)
	}
	// The budget the refresh failures spent is the one login charges.
	if code := failLogin(t, h, "192.0.2.10"); code != http.StatusTooManyRequests {
		t.Fatalf("login by the visitor that spent its budget on refresh: %d, want 429 (one throttle, one key)", code)
	}
	if code := failLogin(t, h, "192.0.2.11"); code != http.StatusUnauthorized {
		t.Fatalf("login by the other visitor: %d, want 401", code)
	}
}

// An empty answer falls back to the peer, so a key function that cannot name
// the visitor never turns the throttle off.
func TestLoginThrottle_EmptyThrottleKeyFallsBackToThePeer(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey())
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := range absauth.MaxFailuresPerIP {
		if code := failLogin(t, h, ""); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, code)
		}
	}
	if code := failLogin(t, h, ""); code != http.StatusTooManyRequests {
		t.Fatalf("past the budget with no visitor key: %d, want 429", code)
	}
}
