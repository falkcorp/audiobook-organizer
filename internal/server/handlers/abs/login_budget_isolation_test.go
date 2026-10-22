// file: internal/server/handlers/abs/login_budget_isolation_test.go
// version: 1.0.0
// guid: c465aeef-af62-4e03-afc5-818c97f4bb37
// last-edited: 2026-10-22

package abs_test

import (
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// S1: refresh failures have a budget of their own (key "refresh:"+visitor).
// Password typos at /login never lock the same visitor's refresh out, because
// a refresh failure is a stale token (routine, e.g. an expired session) and a
// login typo is a guess, and each is counted against its own kind.
func TestRefreshBudget_LoginTyposDoNotSpendIt(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := range absauth.MaxFailuresPerIP {
		if code := failLogin(t, h, "192.0.2.20"); code != http.StatusUnauthorized {
			t.Fatalf("login typo %d: %d, want 401", i, code)
		}
	}
	if code := failLogin(t, h, "192.0.2.20"); code != http.StatusTooManyRequests {
		t.Fatalf("login past its budget: %d, want 429", code)
	}
	if code := failRefresh(t, h, "192.0.2.20"); code != http.StatusUnauthorized {
		t.Fatalf("refresh by a visitor whose login budget is spent: %d, want 401 (its own budget)", code)
	}
}

// The other direction keeps the old behaviour: a visitor that has burned its
// refresh budget is a caller hammering the endpoint with dead tokens, and the
// ABS login door refuses it too.
func TestRefreshBudget_SpentRefreshBudgetAlsoRefusesAbsLogin(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for range absauth.MaxFailuresPerIP {
		failRefresh(t, h, "192.0.2.21")
	}
	if code := okLogin(h, t, "192.0.2.21", "owner", "pw-pw-pw-pw"); code != http.StatusTooManyRequests {
		t.Fatalf("login by the visitor that spent its refresh budget: %d, want 429", code)
	}
	if code := okLogin(h, t, "192.0.2.22", "owner", "pw-pw-pw-pw"); code != http.StatusOK {
		t.Fatalf("another visitor's login: %d, want 200", code)
	}
}

// B-2 holds for the refresh budget: no success path hands refresh failures back.
func TestRefreshBudget_ASuccessfulLoginDoesNotRefundIt(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := 0; i < absauth.MaxFailuresPerIP-1; i++ {
		if code := failRefresh(t, h, "192.0.2.23"); code != http.StatusUnauthorized {
			t.Fatalf("refresh %d: %d, want 401", i, code)
		}
	}
	if code := okLogin(h, t, "192.0.2.23", "owner", "pw-pw-pw-pw"); code != http.StatusOK {
		t.Fatalf("login with one failure of budget left: %d, want 200", code)
	}
	if code := failRefresh(t, h, "192.0.2.23"); code != http.StatusUnauthorized {
		t.Fatalf("the last refresh failure of the budget: %d, want 401", code)
	}
	if code := failRefresh(t, h, "192.0.2.23"); code != http.StatusTooManyRequests {
		t.Fatalf("refresh after a login that must not have refunded anything: %d, want 429", code)
	}
}

// S3: a failed ABS login takes as long for a name that does not exist as for a
// real account with the wrong password (a real argon2id derivation either way).
func TestLogin_FailedAbsLoginTimingDoesNotRevealWhetherTheNameExists(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	const samples = 5
	timeLogin := func(name string, i int) time.Duration {
		start := time.Now()
		w, _ := h.do(t, request{
			method: http.MethodPost, path: "/login",
			body:    map[string]any{"username": name, "password": "wrong-password"},
			headers: map[string]string{visitorHeader: "198.51.100." + string(rune('1'+i%9))},
		})
		d := time.Since(start)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d, want 401", name, w.Code)
		}
		return d
	}
	var realD, ghostD []time.Duration
	for i := 0; i < samples; i++ { // interleaved so drift hits both alike
		realD = append(realD, timeLogin("owner", i))
		ghostD = append(ghostD, timeLogin("ghost", i))
	}
	median := func(ds []time.Duration) time.Duration {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2]
	}
	r, g := median(realD), median(ghostD)
	t.Logf("median: real=%v unknown=%v", r, g)
	if int64(g) < int64(r)/2 {
		t.Fatalf("an unknown name answered in %v, a real one in %v: timing gives the name away", g, r)
	}
	if int64(g) > int64(r)*3 {
		t.Fatalf("an unknown name answered in %v, a real one in %v: not wildly slower", g, r)
	}
}
