// file: internal/server/handlers/auth_login_timing_test.go
// version: 1.2.0
// guid: 2fc86d12-0fda-4c10-920b-0b83f4b2f534
// last-edited: 2026-11-06

package handlers_test

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/stretchr/testify/require"
)

// Two kinds of test guard the login's timing.
//
// The structural ones (…SpendsTheSameVerification…) run everywhere, -short and
// -race included. They count the argon2id derivations one login spends, through
// absauth.DerivationsForTest, and use a strict mock store, so they prove that
// every failed-login path does the same expensive work and no extra store call,
// whatever the machine's load. They are what fails if a path stops spending the
// dummy verification.
//
// The wall-clock ones (…Timing…, …TimesLikeAnyOther) check the end-to-end
// property a remote caller sees. Response time on a shared CI agent is not a
// stable measurement: at load ~29 under -race (2026-11-06) the median of five
// wrong-password logins for a real account came out at 2.29 s against 1.12 s
// for an account without a password, while both paths ran one identical
// derivation, and other -race runs, at loads up to ~130, give medians within
// about 10% of each other. So they compare each path's FASTEST sample (contention only ever adds
// time, so the minimum is the estimate of the work a path does) with the order
// rotated every round, and they skip under -short, which every CI test job
// passes. They still run in a plain `go test` (make test).

// loginWork is the password work one login spent.
type loginWork struct {
	derivations, kibPasses uint64
}

// workOf runs fn and returns the argon2id work it spent. The counters are
// process-wide, so it is only meaningful in a test that is not t.Parallel().
func workOf(fn func()) loginWork {
	n0, c0 := absauth.DerivationsForTest()
	fn()
	n1, c1 := absauth.DerivationsForTest()
	return loginWork{derivations: n1 - n0, kibPasses: c1 - c0}
}

// loginCase is one login a timing test makes: the account's name, the
// password submitted and the status the login must answer.
type loginCase struct {
	label, username, password string
	want                      int
}

// loginSource hands every login a source address of its own, so no test here
// comes near the per-source failure budget.
type loginSource struct{ n int }

func (s *loginSource) next() string {
	s.n++
	return "198.51.100." + strconv.Itoa(1+s.n%250) + ":4000"
}

// doLogin makes one login and requires its status.
func doLogin(t *testing.T, h *handlers.AuthHandler, src *loginSource, lc loginCase) {
	t.Helper()
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": lc.username, "password": lc.password})
	c.Request.RemoteAddr = src.next()
	h.Login(c)
	require.Equal(t, lc.want, w.Code, "%s: %s", lc.label, w.Body.String())
}

// fastestLogins makes rounds of logins, one per case per round, rotating the
// order every round so a periodic burst of load cannot land on the same case
// each time, and returns each case's fastest time.
func fastestLogins(t *testing.T, h *handlers.AuthHandler, cases []loginCase, rounds int) map[string]time.Duration {
	t.Helper()
	src := &loginSource{}
	fastest := make(map[string]time.Duration, len(cases))
	for r := 0; r < rounds; r++ {
		for k := range cases {
			lc := cases[(r+k)%len(cases)]
			start := time.Now()
			doLogin(t, h, src, lc)
			d := time.Since(start)
			if prev, ok := fastest[lc.label]; !ok || d < prev {
				fastest[lc.label] = d
			}
		}
	}
	return fastest
}

// S3: a failed login spends the same password work whether or not the name
// exists. A name with no user row and an account with no password credential (an
// OAuth user) each spend exactly one argon2id derivation at the cost the real
// account's wrong password does, make the one store lookup and no other store
// call (the mock is strict), and are charged once to the name, like the real one.
func TestLogin_FailedLoginSpendsTheSameVerificationWhetherTheNameExists(t *testing.T) {
	algo, hash, err := absauth.HashPassword("the-real-password")
	require.NoError(t, err)
	real := &database.User{ID: "u-real", Username: "real", PasswordHashAlgo: algo, PasswordHash: hash, Status: "active"}
	oauthOnly := &database.User{ID: "u-oauth", Username: "oauthy", PasswordHashAlgo: absauth.AlgoOAuth, Status: "active"}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("real").Return(real, nil).Once()
	store.EXPECT().GetUserByUsername("oauthy").Return(oauthOnly, nil).Once()
	store.EXPECT().GetUserByUsername("ghost").Return(nil, nil).Once()

	th := absauth.NewThrottle()
	th.SetSleep(func(time.Duration) {})
	h := handlers.NewAuthHandler(store, true) // builds the dummy hash before anything is counted
	h.SetThrottle(th)

	src := &loginSource{}
	work := map[string]loginWork{}
	for _, name := range []string{"real", "ghost", "oauthy"} {
		lc := loginCase{label: name, username: name, password: "wrong-password", want: http.StatusUnauthorized}
		work[name] = workOf(func() { doLogin(t, h, src, lc) })
		require.Equal(t, 1, th.AccountFailures(absauth.AccountKey(name)), "%s is charged once to the name", name)
	}
	t.Logf("work: real=%+v unknown=%+v oauth-only=%+v", work["real"], work["ghost"], work["oauthy"])
	require.Equal(t, uint64(1), work["real"].derivations, "a wrong password for a real account is one derivation")
	require.NotZero(t, work["real"].kibPasses)
	require.Equal(t, work["real"], work["ghost"], "an unknown name must spend what a real account's wrong password does")
	require.Equal(t, work["real"], work["oauthy"], "an account without a password must spend what a real account's wrong password does")
}

// S3, end to end: the response time of a failed login must not tell a caller
// whether the username exists. Before the fix the unknown name answered in
// microseconds and the real one after a full derivation. Wall clock: see the
// note at the top of the file for why it compares minimums and skips under
// -short; the structural test above carries the guarantee there.
func TestLogin_FailedLoginTimingDoesNotRevealWhetherTheNameExists(t *testing.T) {
	if testing.Short() {
		t.Skip("wall-clock comparison; TestLogin_FailedLoginSpendsTheSameVerificationWhetherTheNameExists proves the equal work under -short")
	}
	algo, hash, err := absauth.HashPassword("the-real-password")
	require.NoError(t, err)
	real := &database.User{ID: "u-real", Username: "real", PasswordHashAlgo: algo, PasswordHash: hash, Status: "active"}
	oauthOnly := &database.User{ID: "u-oauth", Username: "oauthy", PasswordHashAlgo: absauth.AlgoOAuth, Status: "active"}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("real").Return(real, nil).Maybe()
	store.EXPECT().GetUserByUsername("oauthy").Return(oauthOnly, nil).Maybe()
	store.EXPECT().GetUserByUsername("ghost").Return(nil, nil).Maybe()

	h := handlers.NewAuthHandler(store, true)
	h.SetFailureDelay(func(time.Duration) {})

	fastest := fastestLogins(t, h, []loginCase{
		{label: "real", username: "real", password: "wrong-password", want: http.StatusUnauthorized},
		{label: "ghost", username: "ghost", password: "wrong-password", want: http.StatusUnauthorized},
		{label: "oauthy", username: "oauthy", password: "wrong-password", want: http.StatusUnauthorized},
	}, 5)
	r, g, o := fastest["real"], fastest["ghost"], fastest["oauthy"]
	t.Logf("fastest: real=%v unknown=%v oauth-only=%v", r, g, o)
	require.GreaterOrEqual(t, int64(g), int64(r)/2, "an unknown name answered in %v, a real one in %v: timing gives the name away", g, r)
	require.GreaterOrEqual(t, int64(o), int64(r)/2, "an account without a password answered in %v, a real one in %v", o, r)
	require.LessOrEqual(t, int64(g), int64(r)*3, "and not wildly slower")
}

// N5: a store failure during the lookup is not a credential failure. It answers
// 500 and charges neither the source nor the name, so a flapping store cannot
// lock anybody out (the ABS login already behaved this way).
func TestLogin_StoreLookupErrorIsA500AndChargesNothing(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(nil, errors.New("pebble: closed")).Times(30)

	th := absauth.NewThrottle()
	th.SetSleep(func(time.Duration) {})
	h := handlers.NewAuthHandler(store, true)
	h.SetThrottle(th)
	for i := 0; i < 30; i++ { // past both the per-source and per-name budgets
		c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "alice", "password": "pw"})
		c.Request.RemoteAddr = "192.0.2.9:4000"
		h.Login(c)
		require.Equal(t, http.StatusInternalServerError, w.Code, "attempt %d", i)
	}
	require.Zero(t, th.AccountFailures(absauth.AccountKey("alice")), "nothing is charged to the name")
	require.False(t, th.IPBlocked("192.0.2.9"), "nothing is charged to the source")
}

// SF3: a correct password for a disabled account is answered 403 "account is
// not active" (the shape the ABS door uses), mints no session and does not take
// the success path's bookkeeping (Clear), and is not charged as a credential
// failure. Before the fix it answered 200 and logged the account in.
func TestLogin_DisabledAccountWithCorrectPasswordIs403AndMintsNothing(t *testing.T) {
	algo, hash, err := absauth.HashPassword("the-real-password")
	require.NoError(t, err)
	for _, status := range []string{"inactive", "revoked", " Disabled "} {
		t.Run(status, func(t *testing.T) {
			disabled := &database.User{ID: "u-off", Username: "off", PasswordHashAlgo: algo, PasswordHash: hash, Status: status}
			// No CreateSessionWithOrigin expectation: a call fails the mock.
			store := handlersmocks.NewMockAuthStore(t)
			store.EXPECT().GetUserByUsername("off").Return(disabled, nil)

			th := absauth.NewThrottle()
			th.SetSleep(func(time.Duration) {})
			h := handlers.NewAuthHandler(store, true)
			h.SetThrottle(th)
			const src = "192.0.2.77"
			// A pre-existing failure against the name: the success path would Clear
			// it, the 403 path must leave it alone.
			th.RecordFailure(absauth.AccountKey("off"), src)
			before := th.AccountFailures(absauth.AccountKey("off"))
			require.NotZero(t, before)

			c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "off", "password": "the-real-password"})
			c.Request.RemoteAddr = src + ":4000"
			h.Login(c)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "account is not active")
			require.Empty(t, w.Result().Cookies(), "no session cookie")
			require.Equal(t, before, th.AccountFailures(absauth.AccountKey("off")), "neither cleared nor charged")
		})
	}
}

// SF3: the status is read only after the password verified, so a disabled
// account costs the same password work as any other: a correct password for it
// (403) and a wrong one (401) each spend exactly the one derivation, at the
// same cost, that a wrong password for an active account does, and no other
// store call.
func TestLogin_DisabledAccountSpendsTheSameVerificationAsAWrongPassword(t *testing.T) {
	algo, hash, err := absauth.HashPassword("the-real-password")
	require.NoError(t, err)
	disabled := &database.User{ID: "u-off", Username: "off", PasswordHashAlgo: algo, PasswordHash: hash, Status: "inactive"}
	active := &database.User{ID: "u-on", Username: "on", PasswordHashAlgo: algo, PasswordHash: hash, Status: "active"}
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("on").Return(active, nil).Once()
	store.EXPECT().GetUserByUsername("off").Return(disabled, nil).Twice()

	h := handlers.NewAuthHandler(store, true)
	h.SetFailureDelay(func(time.Duration) {})

	src := &loginSource{}
	cases := []loginCase{
		{label: "wrong password, active", username: "on", password: "wrong-password", want: http.StatusUnauthorized},
		{label: "correct password, disabled", username: "off", password: "the-real-password", want: http.StatusForbidden},
		{label: "wrong password, disabled", username: "off", password: "wrong-password", want: http.StatusUnauthorized},
	}
	var base loginWork
	for i, lc := range cases {
		got := workOf(func() { doLogin(t, h, src, lc) })
		t.Logf("%s: %+v", lc.label, got)
		if i == 0 {
			base = got
			require.Equal(t, uint64(1), base.derivations, "a wrong password for an active account is one derivation")
			require.NotZero(t, base.kibPasses)
			continue
		}
		require.Equal(t, base, got, "%s must spend what a wrong password for an active account does", lc.label)
	}
}

// SF3, end to end: a wrong password for a disabled account is the same 401 as
// for any account (the status is not an oracle for someone who does not know
// the password), and the 403 takes as long as a wrong-password answer. Wall
// clock: see the note at the top of the file; the structural test above
// carries the guarantee under -short.
func TestLogin_DisabledAccountWrongPasswordIs401AndTimesLikeAnyOther(t *testing.T) {
	if testing.Short() {
		t.Skip("wall-clock comparison; TestLogin_DisabledAccountSpendsTheSameVerificationAsAWrongPassword proves the equal work under -short")
	}
	algo, hash, err := absauth.HashPassword("the-real-password")
	require.NoError(t, err)
	disabled := &database.User{ID: "u-off", Username: "off", PasswordHashAlgo: algo, PasswordHash: hash, Status: "inactive"}
	active := &database.User{ID: "u-on", Username: "on", PasswordHashAlgo: algo, PasswordHash: hash, Status: "active"}
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("off").Return(disabled, nil).Maybe()
	store.EXPECT().GetUserByUsername("on").Return(active, nil).Maybe()

	h := handlers.NewAuthHandler(store, true)
	h.SetFailureDelay(func(time.Duration) {})

	fastest := fastestLogins(t, h, []loginCase{
		{label: "wrong-active", username: "on", password: "wrong-password", want: http.StatusUnauthorized},
		{label: "disabled-correct", username: "off", password: "the-real-password", want: http.StatusForbidden},
		{label: "disabled-wrong", username: "off", password: "wrong-password", want: http.StatusUnauthorized},
	}, 5)
	a, d := fastest["wrong-active"], fastest["disabled-correct"]
	t.Logf("fastest: wrong-password active=%v disabled-correct=%v disabled-wrong=%v", a, d, fastest["disabled-wrong"])
	require.GreaterOrEqual(t, int64(d), int64(a)/2, "a disabled account answered in %v against %v for a wrong password", d, a)
	require.LessOrEqual(t, int64(d), int64(a)*3)
}

// A handler-level 429 carries Retry-After (the ceiling middleware's already did).
func TestLogin_PerSourceRefusalCarriesRetryAfter(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	th := absauth.NewThrottle()
	th.SetSleep(func(time.Duration) {})
	for range absauth.MaxFailuresPerIP {
		th.RecordFailure("", "192.0.2.78")
	}
	h := handlers.NewAuthHandler(store, true)
	h.SetThrottle(th)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "alice", "password": "pw"})
	c.Request.RemoteAddr = "192.0.2.78:4000"
	h.Login(c)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, absauth.RetryAfterSeconds, w.Header().Get("Retry-After"))
}
