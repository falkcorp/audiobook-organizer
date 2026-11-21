// file: internal/server/handlers/abs/login_budget_isolation_test.go
// version: 1.3.1
// guid: c465aeef-af62-4e03-afc5-818c97f4bb37
// last-edited: 2026-11-10

package abs_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	coreauth "github.com/falkcorp/mediacore/auth"
	authpw "github.com/falkcorp/mediacore/auth/password"
	"golang.org/x/crypto/bcrypt"
)

// S1: refresh failures have a budget of their own (key "refresh:"+visitor).
// Password typos at /login never lock the same visitor's refresh out, because
// a refresh failure is a stale token (routine, e.g. an expired session) and a
// login typo is a guess, and each is counted against its own kind.
func TestRefreshBudget_LoginTyposDoNotSpendIt(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := range coreauth.DefaultMaxFailuresPerSource {
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

// Both directions are isolated. A visitor that has burned its refresh budget on
// dead tokens (routinely: an expired session) must still be able to sign in
// with its password, which is exactly how it obtains a fresh token. Coupling
// the two let a stale client lock itself out of the only way to recover.
func TestRefreshBudget_SpentRefreshBudgetDoesNotRefuseAbsLogin(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for range coreauth.DefaultMaxFailuresPerSource {
		failRefresh(t, h, "192.0.2.21")
	}
	if code := failRefresh(t, h, "192.0.2.21"); code != http.StatusTooManyRequests {
		t.Fatalf("refresh past its own budget: %d, want 429 (the budget is really spent)", code)
	}
	if code := okLogin(h, t, "192.0.2.21", "owner", "pw-pw-pw-pw"); code != http.StatusOK {
		t.Fatalf("login by the visitor that spent its refresh budget: %d, want 200 (the budgets are separate)", code)
	}
	if code := failLogin(t, h, "192.0.2.21"); code != http.StatusUnauthorized {
		t.Fatalf("a login typo by that visitor: %d, want 401, not 429", code)
	}
	if code := okLogin(h, t, "192.0.2.22", "owner", "pw-pw-pw-pw"); code != http.StatusOK {
		t.Fatalf("another visitor's login: %d, want 200", code)
	}
}

// B-2 holds for the refresh budget: no success path hands refresh failures back.
func TestRefreshBudget_ASuccessfulLoginDoesNotRefundIt(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for i := range coreauth.DefaultMaxFailuresPerSource - 1 {
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

// absLoginWork is the password work one ABS login spent: argon2id
// derivations and KiB-passes, bcrypt comparisons and rounds. The counters are
// process-wide, so it is only meaningful in a test that is not t.Parallel().
type absLoginWork struct{ derivations, kibPasses, compares, rounds uint64 }

func absWorkOf(fn func()) absLoginWork {
	n0, c0 := absauth.Passwords.Derivations()
	b0, r0 := absauth.Passwords.BcryptCompares()
	fn()
	n1, c1 := absauth.Passwords.Derivations()
	b1, r1 := absauth.Passwords.BcryptCompares()
	return absLoginWork{n1 - n0, c1 - c0, b1 - b0, r1 - r0}
}

// seedTimingAccounts seeds the account shapes a failed login can land on:
// "owner" (argon2id), "oracle" (a legacy bcrypt row), "huntress" (an OAuth
// user with no password) and "clayface" (an argon2id row that does not parse).
// Any other name has no user.
func seedTimingAccounts(t *testing.T, h *harness) {
	t.Helper()
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	legacy, err := bcrypt.GenerateFromPassword([]byte("pw-pw-pw-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	h.store.addUser(&database.User{ID: "u2", Username: "oracle", Status: "active",
		PasswordHashAlgo: authpw.AlgoBcrypt, PasswordHash: string(legacy)})
	h.store.addUser(&database.User{ID: "u3", Username: "huntress", Status: "active",
		PasswordHashAlgo: authpw.AlgoOAuth})
	h.store.addUser(&database.User{ID: "u4", Username: "clayface", Status: "active",
		PasswordHashAlgo: authpw.AlgoArgon2id, PasswordHash: "$argon2id$v=19$m=8192,t=1,p=2$!!!$!!!"})
}

// S3, structurally: a failed ABS login spends the same password work, and
// answers with the same bytes, whether the name is a real argon2id account,
// a legacy bcrypt account, an account with no password, an account whose hash
// does not parse, or no account at all: one argon2id derivation and one
// bcrypt comparison each, and the same status, body and headers (no
// Set-Cookie). Load cannot move these counts, so this is the test that names
// a path which stopped spending the dummy work.
func TestLogin_FailedAbsLoginSpendsTheSameWorkAndAnswersTheSameBytes(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	seedTimingAccounts(t, h)
	type answer struct {
		code   int
		body   string
		header http.Header
	}
	var base absLoginWork
	var baseAnswer answer
	for i, name := range []string{"owner", "ghost", "oracle", "huntress", "clayface"} {
		var w *httptest.ResponseRecorder
		got := absWorkOf(func() {
			w, _ = h.do(t, request{
				method: http.MethodPost, path: "/login",
				body:    map[string]any{"username": name, "password": "wrong-password"},
				headers: map[string]string{visitorHeader: "198.51.100." + strconv.Itoa(10+i)},
			})
		})
		a := answer{w.Code, w.Body.String(), w.Header().Clone()}
		t.Logf("%s: %+v %d %s", name, got, a.code, a.body)
		if i == 0 {
			base, baseAnswer = got, a
			if got.derivations != 1 || got.kibPasses == 0 || got.compares != 1 || got.rounds == 0 {
				t.Fatalf("a wrong password for a real account spent %+v, want one derivation and one bcrypt comparison", got)
			}
			if a.code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
				t.Fatalf("a wrong password for a real account: %d, cookies %v", a.code, w.Result().Cookies())
			}
			continue
		}
		if got != base {
			t.Errorf("%s spent %+v, a real account's wrong password %+v: the work gives the name away", name, got, base)
		}
		if !reflect.DeepEqual(a, baseAnswer) {
			t.Errorf("%s answered %+v, a real account's wrong password %+v: the response gives the name away", name, a, baseAnswer)
		}
	}
}

// S3, end to end: a failed ABS login takes as long for a name that does not
// exist, or a legacy bcrypt account, as for a real argon2id account with the
// wrong password. Before the dummy verification the unknown name answered in
// microseconds and the real one after a full derivation, a gap of two orders
// of magnitude.
//
// Wall clock on a loaded machine is noisy: on 2026-11-21 a full -p 4 run
// gave medians of 24.79 ms (real) against 9.71 ms (unknown) from five
// pairs, real always first, while both paths ran one identical derivation
// (50 rotated pairs on the same code: the real name slower in 25, the
// unknown/real ratio 1.01 at p50). So, as in the web login's timing tests,
// each case's FASTEST sample is compared (contention only ever adds time, so
// the minimum estimates the work a path does), over enough rounds that every
// case meets a quiet moment, with the order rotated every round so a periodic
// burst cannot land on one case, and a fresh visitor per request so no
// source budget is spent. The bounds (within 2x either way) pass noise and
// fail the missing-dummy shape by about 100x.
func TestLogin_FailedAbsLoginTimingDoesNotRevealWhetherTheNameExists(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	seedTimingAccounts(t, h)
	const rounds = 25
	names := []string{"owner", "ghost", "oracle"}
	fastest := map[string]time.Duration{}
	visitor := 0
	for r := range rounds {
		for k := range names {
			name := names[(r+k)%len(names)]
			visitor++
			start := time.Now()
			w, _ := h.do(t, request{
				method: http.MethodPost, path: "/login",
				body:    map[string]any{"username": name, "password": "wrong-password"},
				headers: map[string]string{visitorHeader: "timing-visitor-" + strconv.Itoa(visitor)},
			})
			d := time.Since(start)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s: %d, want 401", name, w.Code)
			}
			if prev, ok := fastest[name]; !ok || d < prev {
				fastest[name] = d
			}
		}
	}
	real := fastest["owner"]
	t.Logf("fastest of %d: real=%v unknown=%v bcrypt=%v", rounds, real, fastest["ghost"], fastest["oracle"])
	for _, name := range []string{"ghost", "oracle"} {
		got := fastest[name]
		if got < real/2 {
			t.Errorf("%s answered in %v at best, a real account in %v: timing gives the name away", name, got, real)
		}
		if got > real*2 {
			t.Errorf("%s answered in %v at best, a real account in %v: timing gives the name away (slower)", name, got, real)
		}
	}
}

// Handler-level 429s carry Retry-After: the login door's per-source refusal and
// the refresh endpoint's.
func TestAbsHandlerRefusals_CarryRetryAfter(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	for range coreauth.DefaultMaxFailuresPerSource {
		failLogin(t, h, "192.0.2.24")
		failRefresh(t, h, "192.0.2.25")
	}
	for name, req := range map[string]request{
		"login": {method: http.MethodPost, path: "/login",
			body:    map[string]any{"username": "owner", "password": "wrong"},
			headers: map[string]string{visitorHeader: "192.0.2.24"}},
		"refresh": {method: http.MethodPost, path: "/auth/refresh",
			headers: map[string]string{visitorHeader: "192.0.2.25"}},
	} {
		w, _ := h.do(t, req)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s past its budget: %d, want 429", name, w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != absauth.RetryAfterSeconds {
			t.Fatalf("%s 429 Retry-After = %q, want %q", name, got, absauth.RetryAfterSeconds)
		}
	}
}

// A successful refresh is not a password login and never gives back refresh
// failures (the refresh budget has no refund path at all).
func TestRefreshBudget_ASuccessfulRefreshDoesNotRefundIt(t *testing.T) {
	h := newHarness(t, "cf,jwt", nil, withVisitorThrottleKey(), func(o *abshandler.Options) { o.Throttle = quickThrottle() })
	h.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	const visitor = "192.0.2.26"
	login := func() string {
		w, body := h.do(t, request{
			method: http.MethodPost, path: "/login",
			body:    map[string]any{"username": "owner", "password": "pw-pw-pw-pw"},
			headers: map[string]string{visitorHeader: visitor},
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login: %d", w.Code)
		}
		return str(t, userObj(t, body), "refreshToken")
	}
	for range coreauth.DefaultMaxFailuresPerSource - 1 {
		failRefresh(t, h, visitor)
	}
	w, _ := h.do(t, request{method: http.MethodPost, path: "/auth/refresh",
		headers: map[string]string{visitorHeader: visitor, "x-refresh-token": login()}})
	if w.Code != http.StatusOK {
		t.Fatalf("a valid refresh: %d, want 200", w.Code)
	}
	if code := failRefresh(t, h, visitor); code != http.StatusUnauthorized {
		t.Fatalf("the last failure of the budget: %d, want 401", code)
	}
	if code := failRefresh(t, h, visitor); code != http.StatusTooManyRequests {
		t.Fatalf("refresh after a successful refresh that must not have refunded anything: %d, want 429", code)
	}
}
