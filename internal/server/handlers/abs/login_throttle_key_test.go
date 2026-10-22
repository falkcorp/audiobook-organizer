// file: internal/server/handlers/abs/login_throttle_key_test.go
// version: 1.2.0
// guid: e4d306c4-3290-49d2-b0b1-c9afae9e0fef
// last-edited: 2026-10-22

package abs_test

import (
	"net/http"
	"testing"
	"time"

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
// so one map never holds two kinds of key. Refresh failures are counted under
// their own key (absauth.RefreshKey) and the ABS login door honours it too; the
// reverse (login typos locking refresh) is TestRefreshBudget_LoginTyposDoNotSpendIt.
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
	// The ABS login door also refuses a visitor whose refresh budget is spent.
	if code := failLogin(t, h, "192.0.2.10"); code != http.StatusTooManyRequests {
		t.Fatalf("login by the visitor that spent its budget on refresh: %d, want 429 (the refresh budget is honoured at login)", code)
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

// quickThrottle is a shared Throttle whose failure delay is a no-op.
func quickThrottle() *absauth.Throttle {
	t := absauth.NewThrottle()
	t.SetSleep(func(time.Duration) {})
	return t
}

func okLogin(h *harness, t *testing.T, visitor, user, password string) int {
	t.Helper()
	w, _ := h.do(t, request{
		method: http.MethodPost, path: "/login",
		body:    map[string]any{"username": user, "password": password},
		headers: map[string]string{visitorHeader: visitor},
	})
	return w.Code
}

// B-2: a successful login must not wipe the visitor's failure budget. A caller
// holding one valid account who logs in between rounds of guesses at another
// still runs out of budget.
func TestLoginThrottle_SuccessDoesNotResetTheVisitorBudget(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	h.seedPasswordUser(t, "u2", "attacker", "pw-pw-pw-pw")
	blocked := false
	for round := 0; round < 50 && !blocked; round++ {
		for g := 0; g < 3; g++ {
			w, _ := h.do(t, request{
				method: http.MethodPost, path: "/login",
				body:    map[string]any{"username": "owner", "password": "guess"},
				headers: map[string]string{visitorHeader: "192.0.2.50"},
			})
			if w.Code == http.StatusTooManyRequests {
				blocked = true
			}
		}
		if !blocked {
			switch code := okLogin(h, t, "192.0.2.50", "attacker", "pw-pw-pw-pw"); code {
			case http.StatusOK:
			case http.StatusTooManyRequests:
				blocked = true
			default:
				t.Fatalf("round %d: own login answered %d", round, code)
			}
		}
	}
	if !blocked {
		t.Fatal("the visitor never ran out of budget although it keeps logging in")
	}
}

// Mistyping your own password and then getting in costs nothing.
func TestLoginThrottle_SuccessTakesBackOwnMistypes(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for round := 0; round < 4; round++ {
		for range 4 {
			if code := okLogin(h, t, "192.0.2.51", "owner", "typo"); code != http.StatusUnauthorized {
				t.Fatalf("round %d: typo answered %d", round, code)
			}
		}
		if code := okLogin(h, t, "192.0.2.51", "owner", "pw-pw-pw-pw"); code != http.StatusOK {
			t.Fatalf("round %d: login answered %d", round, code)
		}
	}
}

// The web login and this one share ONE Throttle (Options.Throttle): failures the
// web login recorded count here.
func TestLoginThrottle_SharedThrottleCountsFailuresFromElsewhere(t *testing.T) {
	shared := quickThrottle()
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = shared })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for range absauth.MaxFailuresPerIP {
		shared.RecordFailure(absauth.AccountKey("owner"), "192.0.2.52") // as the web login would
	}
	if code := okLogin(h, t, "192.0.2.52", "owner", "pw-pw-pw-pw"); code != http.StatusTooManyRequests {
		t.Fatalf("a source spent through the other login: %d, want 429", code)
	}
}
