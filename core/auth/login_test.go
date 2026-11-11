// file: core/auth/login_test.go
// version: 1.0.0
// guid: 2f3c7225-aeb7-459b-bb79-e61a022bc09a
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/storage"
)

// Password sign-in (05-authn §3.2, §4.9): HEAD's handlers/auth.go Login order
// behind its login gate, over core's one throttle and ceiling instance.

// withPassword stores plain (hashed by the fixture's hasher) as u's password.
func (f *fx) withPassword(u *auth.User, plain string) *auth.User {
	f.t.Helper()
	_, h, err := f.hasher.Hash(plain)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.withHash(u, h)
}

// withHash stores hash verbatim as u's password hash.
func (f *fx) withHash(u *auth.User, hash string) *auth.User {
	f.t.Helper()
	cur := f.reload(u.ID)
	cur.Version++
	f.commit(func(b *storage.Batch) error { return f.st.SetPassword(b, cur, hash) })
	return f.reload(u.ID)
}

// edit changes u's stored record behind the service.
func (f *fx) edit(u *auth.User, change func(*auth.User)) *auth.User {
	f.t.Helper()
	cur := f.reload(u.ID)
	change(cur)
	cur.Version++
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, cur) })
	return f.reload(u.ID)
}

func from(ip string) auth.ClientInfo {
	a := netip.MustParseAddr(ip)
	return auth.ClientInfo{Addr: a, Peer: a, UserAgent: "batcomputer"}
}

// loginFx is a fixture whose sign-in delays are recorded instead of slept.
func loginFx(t *testing.T) (*fx, *[]time.Duration) {
	f := newFx(t)
	var mu sync.Mutex
	slept := []time.Duration{}
	f.loginSleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); mu.Unlock() }
	f.svc.SetLoginSleep(f.loginSleep)
	return f, &slept
}

// login signs in and checks that the ceiling admission was released on the
// way out, whatever the answer.
func (f *fx) login(name, pw string, c auth.ClientInfo) (*auth.IssuedSession, error) {
	f.t.Helper()
	sess, err := f.svc.Login(context.Background(), auth.LoginInput{Username: name, Password: pw, Client: c})
	if n, sat := auth.LoginCeilingOf(f.svc).InFlight(auth.SourceKeyOf(c)); n != 0 || sat != 0 {
		f.t.Fatalf("Login(%q) left %d logins (%d saturated) in flight", name, n, sat)
	}
	return sess, err
}

func (f *fx) derivations() uint64 {
	n, _ := f.hasher.Derivations()
	return n
}

func TestLogin_SucceedsAndStampsTheVerifiedEpoch(t *testing.T) {
	f, _ := loginFx(t)
	u := f.withPassword(f.user("brucewayne"), "i-am-the-night")
	c := from("192.0.2.10")
	th := auth.LoginThrottleOf(f.svc)
	for range 3 {
		if _, err := f.login("brucewayne", "wrong", c); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("wrong password: %v", err)
		}
	}
	if th.AccountFailures(auth.AccountKey("brucewayne")) != 3 {
		t.Fatalf("3 failures not counted: %d", th.AccountFailures(auth.AccountKey("brucewayne")))
	}
	sess, err := f.login("  BruceWayne ", "i-am-the-night", c)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.Token == "" || sess.PublicID == "" || !sess.ExpiresAt.Equal(f.clock().Add(auth.DefaultSessionTTL)) {
		t.Fatalf("session %+v", sess)
	}
	if n := th.AccountFailures(auth.AccountKey("brucewayne")); n != 0 {
		t.Fatalf("success left %d failures on the account", n)
	}
	rec, err := f.st.SessionByToken(context.Background(), auth.Token(sess.Token))
	if err != nil {
		t.Fatal(err)
	}
	if rec.UserID != u.ID || rec.Origin != auth.OriginPassword || rec.CredentialEpoch != u.CredentialEpoch || rec.ClientAddr != c.Addr {
		t.Fatalf("session record %+v", rec)
	}
	// Remember me takes the longer lifetime.
	sess, err = f.svc.Login(context.Background(), auth.LoginInput{Username: "brucewayne", Password: "i-am-the-night", Remember: true, Client: c})
	if err != nil || !sess.ExpiresAt.Equal(f.clock().Add(auth.DefaultRememberTTL)) {
		t.Fatalf("remember: %+v %v", sess, err)
	}
}

// An unknown name, an account without a password and a wrong password cost
// exactly one derivation each, are charged to the source and the submitted
// name, and answer the same error.
func TestLogin_FailuresCostTheSameAndAreCharged(t *testing.T) {
	f, slept := loginFx(t)
	f.withPassword(f.user("alfred"), "earl-grey")
	f.user("lucius") // no password (an OIDC-only account)
	th := auth.LoginThrottleOf(f.svc)
	for _, tc := range []struct{ name, pw string }{
		{"jokerjoker", "why-so-serious"},
		{"lucius", "applied-sciences"},
		{"alfred", "darjeeling"},
	} {
		before := f.derivations()
		_, err := f.login(tc.name, tc.pw, from("192.0.2.20"))
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("%s: %v, want ErrInvalidCredentials", tc.name, err)
		}
		if got := f.derivations() - before; got != 1 {
			t.Fatalf("%s: %d derivations, want 1", tc.name, got)
		}
		if n := th.AccountFailures(auth.AccountKey(tc.name)); n != 1 {
			t.Fatalf("%s: %d failures on the name, want 1", tc.name, n)
		}
		if len(*slept) != 0 {
			t.Fatalf("%s: a first failure is not delayed: %v", tc.name, *slept)
		}
	}
	// The source holds all three failures: 12 more spend its budget of 15.
	for range auth.DefaultMaxFailuresPerSource - 3 {
		_, _ = f.login("jokerjoker", "x", from("192.0.2.20"))
	}
	if !th.SourceBlocked("192.0.2.20") {
		t.Fatal("15 failures from one source did not spend its budget")
	}
	// The account's soft delay grows after SoftThreshold failures.
	if len(*slept) == 0 || (*slept)[0] != auth.DefaultSoftStep {
		t.Fatalf("soft delays %v, want the first to be one step", *slept)
	}
}

// A source past its budget is refused before any lookup or derivation, even
// with the right password, and told when to come back.
func TestLogin_BlockedSourceIsRefusedBeforeAnyWork(t *testing.T) {
	f, _ := loginFx(t)
	f.withPassword(f.user("selina"), "nine-lives")
	c := from("192.0.2.30")
	for range auth.DefaultMaxFailuresPerSource {
		_, _ = f.login("selina", "wrong", c)
	}
	before := f.derivations()
	_, err := f.login("selina", "nine-lives", c)
	var te *auth.ThrottledError
	if !errors.As(err, &te) || !errors.Is(err, auth.ErrThrottled) || te.RetryAfter != auth.LoginRetryAfter {
		t.Fatalf("blocked source: %v, want a ThrottledError retrying after %d", err, auth.LoginRetryAfter)
	}
	if f.derivations() != before {
		t.Fatal("a blocked source spent a derivation")
	}
	if _, err := f.login("selina", "nine-lives", from("192.0.2.31")); err != nil {
		t.Fatalf("another source is not blocked: %v", err)
	}
}

func TestLogin_RequiresUsernameAndPassword(t *testing.T) {
	f, _ := loginFx(t)
	for _, in := range [][2]string{{"", "pw"}, {"   ", "pw"}, {"dick", ""}} {
		before := f.derivations()
		if _, err := f.login(in[0], in[1], from("192.0.2.40")); !errors.Is(err, auth.ErrBadInput) {
			t.Fatalf("%q/%q: %v, want ErrBadInput", in[0], in[1], err)
		}
		if f.derivations() != before {
			t.Fatal("a malformed sign-in spent a derivation")
		}
	}
	if s, a := auth.LoginThrottleOf(f.svc).Len(); s != 0 || a != 0 {
		t.Fatalf("a malformed sign-in was charged: %d sources, %d accounts", s, a)
	}
}

// Status and a required reset are decided after the verification (the same
// work for every account) and are not charged; a wrong password on such an
// account is.
func TestLogin_StatusAndResetAreCheckedAfterVerifyAndNotCharged(t *testing.T) {
	f, _ := loginFx(t)
	disabled := f.edit(f.withPassword(f.user("harvey"), "two-face"), func(u *auth.User) { u.Status = auth.StatusDisabled })
	reset := f.edit(f.withPassword(f.user("jason"), "red-hood"), func(u *auth.User) { u.PasswordResetRequired = true })
	restricted := f.edit(f.withPassword(f.user("barbara"), "oracle"), func(u *auth.User) { u.SignInRestricted = true })
	for _, tc := range []struct {
		u      *auth.User
		pw     string
		want   error
		reason string
	}{
		{disabled, "two-face", auth.ErrInactiveUser, "inactive"},
		{reset, "red-hood", auth.ErrResetRequired, "reset_required"},
		{restricted, "oracle", auth.ErrResetRequired, "reset_required"},
	} {
		th := auth.LoginThrottleOf(f.svc)
		// One mistype first: a wrong password on any of these is charged, and
		// the refused correct one after it neither charges nor clears it.
		if _, err := f.login(tc.u.Username, "wrong", from("192.0.2.50")); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("%s: wrong password: %v", tc.u.Username, err)
		}
		if n := th.AccountFailures(auth.AccountKey(tc.u.Username)); n != 1 {
			t.Fatalf("%s: a wrong password was charged %d times, want 1", tc.u.Username, n)
		}
		before := f.derivations()
		sess, err := f.login(tc.u.Username, tc.pw, from("192.0.2.50"))
		if !errors.Is(err, tc.want) || sess != nil {
			t.Fatalf("%s: %v %v, want %v and no session", tc.u.Username, sess, err, tc.want)
		}
		if f.derivations()-before != 1 {
			t.Fatalf("%s: the refusal did not cost one derivation", tc.u.Username)
		}
		if n := th.AccountFailures(auth.AccountKey(tc.u.Username)); n != 1 {
			t.Fatalf("%s: a refused correct password left %d failures, want the 1 before it", tc.u.Username, n)
		}
		f.audit.mu.Lock()
		last := f.audit.emitted[len(f.audit.emitted)-1]
		f.audit.mu.Unlock()
		if last.Action != "core.auth.login" || last.Outcome != audit.Failure || last.Reason != tc.reason || last.Target.ID != string(tc.u.ID) {
			t.Fatalf("%s: refusal audited as %+v, want reason %q on the account", tc.u.Username, last, tc.reason)
		}
	}
	for range f.svc.Sessions(context.Background(), disabled.ID) {
		t.Fatal("a refused sign-in opened a session")
	}
}

// failingUserStore fails the user lookup or the hash read.
type failingUserStore struct {
	auth.Store
	byName, hash error
}

func (s failingUserStore) UserByUsername(ctx context.Context, fold string) (*auth.User, error) {
	if s.byName != nil {
		return nil, s.byName
	}
	return s.Store.UserByUsername(ctx, fold)
}

func (s failingUserStore) PasswordHash(ctx context.Context, id auth.UserID) (string, error) {
	if s.hash != nil {
		return "", s.hash
	}
	return s.Store.PasswordHash(ctx, id)
}

// A store failure is not a credential verdict: nothing is charged and no
// derivation is spent. A user deleted between the two reads is an unknown
// name.
func TestLogin_StoreFailureChargesNothing(t *testing.T) {
	f, _ := loginFx(t)
	f.withPassword(f.user("lucius"), "wayne-tech")
	disk := errors.New("the Batcomputer is down")
	for _, st := range []failingUserStore{{Store: f.st, byName: disk}, {Store: f.st, hash: disk}} {
		svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
			Store: st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock, Hasher: f.hasher,
		})
		if err != nil {
			t.Fatal(err)
		}
		before := f.derivations()
		_, err = svc.Login(context.Background(), auth.LoginInput{Username: "lucius", Password: "wayne-tech", Client: from("192.0.2.60")})
		if !errors.Is(err, auth.ErrStoreUnavailable) || !errors.Is(err, disk) {
			t.Fatalf("store failure: %v", err)
		}
		if f.derivations() != before {
			t.Fatal("a store failure spent a derivation")
		}
		if s, a := auth.LoginThrottleOf(svc).Len(); s != 0 || a != 0 {
			t.Fatalf("a store failure was charged: %d sources, %d accounts", s, a)
		}
		if n, _ := auth.LoginCeilingOf(svc).InFlight("192.0.2.60"); n != 0 {
			t.Fatal("a store failure kept its ceiling slot")
		}
	}
	gone := failingUserStore{Store: f.st, hash: fmt.Errorf("%w: deleted", auth.ErrNotFound)}
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: gone, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock, Hasher: f.hasher,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLoginSleep(func(time.Duration) {})
	before := f.derivations()
	if _, err := svc.Login(context.Background(), auth.LoginInput{Username: "lucius", Password: "wayne-tech", Client: from("192.0.2.61")}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("user deleted between the reads: %v", err)
	}
	if f.derivations()-before != 1 || auth.LoginThrottleOf(svc).AccountFailures("lucius") != 1 {
		t.Fatal("a user deleted between the reads was not treated as an unknown name")
	}
}

// A hasher still saturated after the 2 s wait answers ErrBusy and charges
// nothing.
func TestLogin_BusyHasherAnswersBusy(t *testing.T) {
	f, _ := loginFx(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	busy := password.New(password.Options{Permits: 1, Time: 1, MemoryKiB: 64,
		Compute: func(pw, salt []byte, tm, mem uint32, th uint8, kl uint32) []byte {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return make([]byte, kl)
		}})
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock, Hasher: busy,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.withPassword(f.user("victor"), "mr-freeze")
	done := make(chan error, 1)
	go func() {
		_, err := svc.Login(context.Background(), auth.LoginInput{Username: "victor", Password: "x", Client: from("192.0.2.70")})
		done <- err
	}()
	<-entered
	_, err = svc.Login(context.Background(), auth.LoginInput{Username: "victor", Password: "mr-freeze", Client: from("192.0.2.71")})
	if !errors.Is(err, auth.ErrBusy) {
		t.Fatalf("saturated hasher: %v, want ErrBusy", err)
	}
	if auth.LoginThrottleOf(svc).AccountFailures("victor") != 0 {
		t.Fatal("a busy answer was charged")
	}
	close(release)
	<-done
}

// A bcrypt hash is re-stored as argon2id on a successful sign-in, without a
// new epoch; the next sign-in verifies the new hash.
func TestLogin_RehashesBcryptWithoutANewEpoch(t *testing.T) {
	f, _ := loginFx(t)
	legacy, err := bcrypt.GenerateFromPassword([]byte("utility-belt"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := f.withHash(f.user("tim"), string(legacy))
	if _, err := f.login("tim", "utility-belt", from("192.0.2.80")); err != nil {
		t.Fatalf("bcrypt sign-in: %v", err)
	}
	h, err := f.st.PasswordHash(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if password.AlgoOf(h) != password.AlgoArgon2id {
		t.Fatalf("hash after sign-in is %q, want argon2id", password.AlgoOf(h))
	}
	if got := f.reload(u.ID); got.CredentialEpoch != u.CredentialEpoch {
		t.Fatalf("the rehash bumped the epoch %d -> %d", u.CredentialEpoch, got.CredentialEpoch)
	}
	if _, err := f.login("tim", "utility-belt", from("192.0.2.80")); err != nil {
		t.Fatalf("sign-in after the rehash: %v", err)
	}
	if h2, _ := f.st.PasswordHash(context.Background(), u.ID); h2 != h {
		t.Fatal("an argon2id hash was re-stored")
	}
}

// A password changed between the bcrypt verification and the transparent
// rehash wins: the rehash of the old password is skipped, so a sign-in can
// never put back a password an admin or the user just replaced.
func TestLogin_RehashNeverRevertsAPasswordChangedMeanwhile(t *testing.T) {
	f, _ := loginFx(t)
	legacy, err := bcrypt.GenerateFromPassword([]byte("utility-belt"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := f.withHash(f.user("tim"), string(legacy))
	var once sync.Once
	f.audit.hold = func(e audit.Event) {
		if e.Action == "core.auth.login" {
			// The session batch is staged; the password changes before the
			// rehash, which runs after the session commits.
			once.Do(func() { f.withPassword(u, "red-robin-2008") })
		}
	}
	if _, err := f.login("tim", "utility-belt", from("192.0.2.81")); err != nil {
		t.Fatalf("bcrypt sign-in: %v", err)
	}
	f.audit.hold = nil
	if _, err := f.login("tim", "utility-belt", from("192.0.2.82")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("the replaced password signs in again after the rehash: %v", err)
	}
	if _, err := f.login("tim", "red-robin-2008", from("192.0.2.83")); err != nil {
		t.Fatalf("the new password does not sign in: %v", err)
	}
}

// fillConnector spends the connector ceiling of conn with failures from
// distinct visitors.
func fillConnector(t *testing.T, l *auth.LoginCeiling, conn string) {
	t.Helper()
	for i := range auth.LoginConnectorCeiling {
		adm, d := l.Admit(fmt.Sprintf("visitor-%d", i), nil, conn)
		if adm == nil {
			t.Fatalf("fill %d refused: %+v", i, d)
		}
		adm.Done(auth.LoginFailed, "")
	}
}

// While a ceiling is spent a sign-in through it is delayed by the submitted
// name's failures before any lookup, and refused once the name has failed
// SoftThreshold times; the refusal costs no derivation.
func TestLogin_SaturatedIsDelayedThenRefused(t *testing.T) {
	f, slept := loginFx(t)
	f.withPassword(f.user("dinah"), "canary-cry")
	conn := netip.MustParseAddr("192.0.2.200")
	via := func(ip string) auth.ClientInfo {
		return auth.ClientInfo{Addr: netip.MustParseAddr(ip), Peer: conn, Forwarded: true, TrustedProxy: true}
	}
	fillConnector(t, auth.LoginCeilingOf(f.svc), "192.0.2.200")
	if _, err := f.login("dinah", "canary-cry", via("192.0.2.90")); err != nil {
		t.Fatalf("saturated sign-in with the right password: %v", err)
	}
	if len(*slept) != 1 || (*slept)[0] != auth.SaturationDelayBase {
		t.Fatalf("saturated delays %v, want one base delay", *slept)
	}
	th := auth.LoginThrottleOf(f.svc)
	for range auth.DefaultSoftThreshold {
		th.RecordFailure(auth.AccountKey("dinah"), "")
	}
	before := f.derivations()
	_, err := f.login("dinah", "canary-cry", via("192.0.2.91"))
	var te *auth.ThrottledError
	if !errors.As(err, &te) || te.RetryAfter != auth.LoginRetryAfter {
		t.Fatalf("saturated sign-in for a spent name: %v", err)
	}
	if f.derivations() != before {
		t.Fatal("the saturation refusal spent a derivation")
	}
	// The refusal counts as a saturated failure: the visitor's next attempt
	// is refused by the ceiling itself.
	if _, err := f.login("robin", "x", via("192.0.2.91")); !errors.Is(err, auth.ErrThrottled) {
		t.Fatalf("visitor after a saturation refusal: %v", err)
	}
	// A direct caller is not behind the spent connector ceiling.
	*slept = (*slept)[:0]
	if _, err := f.login("dinah", "wrong", from("192.0.2.92")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("direct caller: %v", err)
	}
}

// The visitor's in-flight cap refuses with the ceiling's own Retry-After.
func TestLogin_CeilingRefusalCarriesItsRetryAfter(t *testing.T) {
	f, _ := loginFx(t)
	l := auth.LoginCeilingOf(f.svc)
	var held []*auth.LoginAdmission
	for range auth.MaxLoginInFlightPerVisitor {
		adm, _ := l.Admit("192.0.2.95", nil, "")
		held = append(held, adm)
	}
	_, err := f.svc.Login(context.Background(), auth.LoginInput{Username: "bane", Password: "venom", Client: from("192.0.2.95")})
	var te *auth.ThrottledError
	if !errors.As(err, &te) || te.RetryAfter < 1 || te.RetryAfter == auth.LoginRetryAfter {
		t.Fatalf("in-flight cap: %v, want the ceiling's own Retry-After", err)
	}
	for _, a := range held {
		a.Done(auth.LoginOther, "")
	}
}

// core.auth.login.* are read on every sign-in.
func TestLogin_ReadsTheThrottleSettingsLive(t *testing.T) {
	f, _ := loginFx(t)
	f.setSettings(auth.Settings{Login: auth.ThrottleLimits{MaxFailuresPerSource: 2}})
	c := from("192.0.2.97")
	for range 2 {
		_, _ = f.login("ra's", "lazarus", c)
	}
	if _, err := f.login("ra's", "lazarus", c); !errors.Is(err, auth.ErrThrottled) {
		t.Fatalf("after 2 failures under a budget of 2: %v", err)
	}
	f.setSettings(auth.Settings{})
	if _, err := f.login("ra's", "lazarus", c); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("back at the default budget: %v", err)
	}
}

func TestNew_RefusesUnusableSaturatedLimits(t *testing.T) {
	f := newFx(t)
	for _, st := range []auth.Settings{
		{SaturatedRate: auth.MinSaturatedRate / 2},
		{SaturatedRate: auth.MaxSaturatedRate * 2},
		{SaturatedRate: -1},
		{SaturatedBurst: -1},
		{SaturatedBurst: auth.MaxSaturatedBurst + 1},
	} {
		_, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
			Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl,
			Settings: func() auth.Settings { return st },
		})
		if !errors.Is(err, auth.ErrBadInput) {
			t.Fatalf("%+v: %v, want ErrBadInput", st, err)
		}
	}
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl,
		Settings: func() auth.Settings { return auth.Settings{SaturatedRate: 5, SaturatedBurst: 20} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, b := auth.LoginCeilingOf(svc).SaturatedBound(); r != 5 || b != 20 {
		t.Fatalf("saturated bound %v/%d, want 5/20", r, b)
	}
	svc, err = auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, b := auth.LoginCeilingOf(svc).SaturatedBound(); r != auth.DefaultSaturatedRate || b != auth.DefaultSaturatedBurst {
		t.Fatalf("default saturated bound %v/%d", r, b)
	}
}

// The source, saturated and connector keys (HEAD's clientPrefixKey,
// clientSaturatedKey and loginCeilingConnectorKey).
func TestClientInfo_SignInKeys(t *testing.T) {
	for _, tt := range []struct {
		name               string
		c                  auth.ClientInfo
		source, sat, connK string
	}{
		{"none", auth.ClientInfo{}, "", "", ""},
		{"ipv4", from("192.0.2.5"), "192.0.2.5", "192.0.2.5", ""},
		{"mapped ipv4", auth.ClientInfo{Addr: netip.MustParseAddr("::ffff:192.0.2.6")}, "192.0.2.6", "192.0.2.6", ""},
		{"public ipv6", from("2001:db8:1:2:3:4:5:6"), "2001:db8:1:2::/64", "2001:db8:1::/48", ""},
		{"ula", from("fd12:3456:789a:1::7"), "fd12:3456:789a:1::7", "fd12:3456:789a:1::7", ""},
		{"link-local", auth.ClientInfo{Addr: netip.MustParseAddr("fe80::1%eth0")}, "fe80::1", "fe80::1", ""},
		{"loopback", from("::1"), "::1", "::1", ""},
		{"on-link global", auth.ClientInfo{Addr: netip.MustParseAddr("2001:db8:1:2::9"), OnLink: true}, "2001:db8:1:2::9", "2001:db8:1:2::9", ""},
		{"trusted proxy", auth.ClientInfo{Addr: netip.MustParseAddr("192.0.2.7"), Peer: netip.MustParseAddr("192.0.2.200"), TrustedProxy: true}, "192.0.2.7", "192.0.2.7", "192.0.2.200"},
		{"untrusted peer", auth.ClientInfo{Addr: netip.MustParseAddr("192.0.2.8"), Peer: netip.MustParseAddr("192.0.2.201"), Forwarded: true}, "192.0.2.8", "192.0.2.8", ""},
	} {
		if got := auth.SourceKeyOf(tt.c); got != tt.source {
			t.Errorf("%s: source %q, want %q", tt.name, got, tt.source)
		}
		if got := auth.SaturatedKeyOf(tt.c); got != tt.sat {
			t.Errorf("%s: saturated %q, want %q", tt.name, got, tt.sat)
		}
		if got := auth.ConnectorKeyOf(tt.c); got != tt.connK {
			t.Errorf("%s: connector %q, want %q", tt.name, got, tt.connK)
		}
	}
}
