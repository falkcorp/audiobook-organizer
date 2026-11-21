// file: core/auth/profile_test.go
// version: 0.1.0
// guid: c4a7e913-6b25-4d08-8f3e-1a92d5b7c640
// last-edited: 2026-11-20

package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// mailUser stores an active detective holding email.
func (f *fx) mailUser(name, email string, verified bool) *auth.User {
	f.t.Helper()
	u := f.user(name, "gotham.detective")
	u.Email, u.EmailVerified, u.Version = email, verified, u.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, u) })
	return f.reload(u.ID)
}

func (f *fx) profile(p *auth.Principal, in auth.ProfileUpdate) (*auth.User, error) {
	f.t.Helper()
	return f.svc.UpdateProfile(context.Background(), p, in)
}

// unchanged fails the test unless the stored user is exactly before.
func (f *fx) unchanged(before *auth.User) {
	f.t.Helper()
	got := f.reload(before.ID)
	if got.Email != before.Email || got.EmailVerified != before.EmailVerified || got.DisplayName != before.DisplayName ||
		got.Version != before.Version || got.CredentialEpoch != before.CredentialEpoch {
		f.t.Fatalf("a refused change wrote: before %+v, after %+v", before, got)
	}
}

func TestUpdateProfile_DisplayNameOnly(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	got, err := f.profile(f.session(dick), auth.ProfileUpdate{DisplayName: new("  Nightwing  ")})
	if err != nil {
		t.Fatal(err)
	}
	st := f.reload(dick.ID)
	if got.DisplayName != "Nightwing" || st.DisplayName != "Nightwing" || st.Email != dick.Email || !st.EmailVerified ||
		st.Version != dick.Version+1 || st.CredentialEpoch != dick.CredentialEpoch {
		t.Fatalf("returned %+v stored %+v", got, st)
	}
	// Setting what is already there writes nothing.
	before := f.reload(dick.ID)
	if _, err := f.profile(f.session(before), auth.ProfileUpdate{DisplayName: new("Nightwing")}); err != nil {
		t.Fatal(err)
	}
	f.unchanged(before)
}

// An e-mail change is an identity change: the new address is not verified,
// the account's credentials stay valid (05-authn §4.14 lists what bumps the
// epoch, and this is not on it), and nothing is journaled.
func TestUpdateProfile_EmailChangeClearsVerifiedAndKeepsCredentials(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	p, _ := f.withLiveSession(dick)
	lines := len(f.jrnl.lines)
	got, err := f.profile(p, auth.ProfileUpdate{Email: new(" Richard@Gotham.Example ")})
	if err != nil {
		t.Fatal(err)
	}
	st := f.reload(dick.ID)
	if got.Email != "Richard@Gotham.Example" || st.Email != "Richard@Gotham.Example" || st.EmailVerified || got.EmailVerified {
		t.Fatalf("returned %+v stored %+v: a new address must be unverified", got, st)
	}
	if st.CredentialEpoch != dick.CredentialEpoch || st.Version != dick.Version+1 {
		t.Fatalf("epoch %d version %d, was %d %d", st.CredentialEpoch, st.Version, dick.CredentialEpoch, dick.Version)
	}
	if len(f.jrnl.lines) != lines {
		t.Fatalf("journal lines %v: an e-mail change is not a revocation", f.jrnl.lines[lines:])
	}
	if _, err := f.svc.PrincipalFor(context.Background(), st, auth.MethodSession, nil, dick.CredentialEpoch); err != nil {
		t.Fatalf("the account's sessions stopped standing: %v", err)
	}
	// Clearing is not undone by changing the spelling back: the address the
	// account now holds was never proven.
	if _, err := f.profile(f.session(st), auth.ProfileUpdate{Email: new("richard@gotham.example")}); err != nil {
		t.Fatal(err)
	}
	if f.reload(dick.ID).EmailVerified {
		t.Fatal("a spelling change verified an unproven address")
	}
}

// withLiveSession is a session principal for u whose stored session exists.
func (f *fx) withLiveSession(u *auth.User) (*auth.Principal, *auth.User) {
	f.t.Helper()
	return f.signedIn(u), u
}

// A change of spelling only is the same address by SameEmail: what was
// verified stays verified.
func TestUpdateProfile_SpellingOnlyKeepsVerified(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	if _, err := f.profile(f.session(dick), auth.ProfileUpdate{Email: new("Dick@Gotham.Example")}); err != nil {
		t.Fatal(err)
	}
	st := f.reload(dick.ID)
	if st.Email != "Dick@Gotham.Example" || !st.EmailVerified {
		t.Fatalf("%+v", st)
	}
}

func TestUpdateProfile_RefusesAnAddressAnotherAccountHoldsVerified(t *testing.T) {
	f := newFx(t)
	f.mailUser("alfred", "alfred@gotham.example", true)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	for _, e := range []string{"alfred@gotham.example", "ALFRED@Gotham.Example", "  alfred@gotham.example"} {
		_, err := f.profile(f.session(dick), auth.ProfileUpdate{Email: new(e), DisplayName: new("Robin")})
		wantErr(t, e, err, auth.ErrEmailInUse)
		f.unchanged(dick) // the display name in the same patch did not land either
	}
	// An address another account only typed in (unverified) is not a claim
	// on it: HEAD never refused it, and the OIDC resolver displaces such a
	// holder when the address is verified.
	f.mailUser("selina", "selina@gotham.example", false)
	if _, err := f.profile(f.session(dick), auth.ProfileUpdate{Email: new("selina@gotham.example")}); err != nil {
		t.Fatalf("an unverified holder blocked the change: %v", err)
	}
	// Your own address is never in conflict with yourself.
	if _, err := f.profile(f.session(f.reload(dick.ID)), auth.ProfileUpdate{Email: new("selina@gotham.example")}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateProfile_RefusesANonPlainAddress(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	for name, e := range map[string]string{
		"kelvin sign": "Kate@gotham.example",
		"fullwidth":   "ｄick@gotham.example",
		"accent":      "dïck@gotham.example",
		"inner space": "di ck@gotham.example",
		"tab":         "di\tck@gotham.example",
		"control":     "dick\x00@gotham.example",
		"ligature":    "ﬁsh@gotham.example",
	} {
		_, err := f.profile(f.session(dick), auth.ProfileUpdate{Email: new(e), DisplayName: new("Robin")})
		if !errors.Is(err, auth.ErrEmailNotPlain) {
			t.Errorf("%s: %v, want ErrEmailNotPlain", name, err)
		}
		f.unchanged(dick)
	}
}

func TestUpdateProfile_RefusesAnEmptyRequest(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	for name, in := range map[string]auth.ProfileUpdate{
		"empty patch":    {},
		"empty address":  {Email: new("")},
		"blank address":  {Email: new(" \t ")},
		"blank + a name": {Email: new(" "), DisplayName: new("Robin")},
	} {
		_, err := f.profile(f.session(dick), in)
		if !errors.Is(err, auth.ErrBadInput) {
			t.Errorf("%s: %v, want ErrBadInput", name, err)
		}
		f.unchanged(dick)
	}
}

// The auth-disabled principal has no user, and a nil actor has nothing: both
// are refused before any user is read or written.
func TestUpdateProfile_RefusesTheAuthDisabledPrincipal(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	users := f.userCount()
	off := &auth.Principal{Method: auth.MethodAuthDisabled, Perms: f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}})}
	for name, p := range map[string]*auth.Principal{"auth off": off, "nil": nil} {
		_, err := f.profile(p, auth.ProfileUpdate{DisplayName: new("Robin"), Email: new("robin@gotham.example")})
		wantErr(t, name, err, auth.ErrDenied)
	}
	if f.userCount() != users {
		t.Fatalf("users %d, was %d: a refused principal created an account", f.userCount(), users)
	}
	f.unchanged(dick)
}

// A key may not edit the account it signs in for: the route is Interactive
// and so is the Service.
func TestUpdateProfile_RefusesAKey(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	_, k := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	_, err := f.profile(f.keyPrincipal(f.reload(dick.ID), k), auth.ProfileUpdate{Email: new("robin@gotham.example")})
	wantErr(t, "a key", err, auth.ErrDenied)
	f.unchanged(dick)
}

func TestUpdateProfile_AGoneAccountIsNotFound(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	p := f.session(dick)
	f.commit(func(b *storage.Batch) error { return f.st.DeleteUser(b, f.reload(dick.ID)) })
	_, err := f.profile(p, auth.ProfileUpdate{DisplayName: new("Robin")})
	wantErr(t, "a deleted account", err, auth.ErrNotFound)
}

// An e-mail change waits for the account e-mail lock (LockEmails), the lock
// every creator and the OIDC resolver hold around "no other account holds
// this address verified". A name-only change takes no part in that and does
// not wait.
func TestUpdateProfile_EmailChangeTakesTheEmailLock(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	p := f.session(dick)
	unlock := f.svc.LockEmails()
	nameDone := make(chan error, 1)
	go func() {
		_, err := f.profile(p, auth.ProfileUpdate{DisplayName: new("Robin")})
		nameDone <- err
	}()
	select {
	case err := <-nameDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		unlock()
		t.Fatal("a name-only change waited for the e-mail lock")
	}
	mailDone := make(chan error, 1)
	go func() {
		_, err := f.profile(p, auth.ProfileUpdate{Email: new("robin@gotham.example")})
		mailDone <- err
	}()
	select {
	case err := <-mailDone:
		unlock()
		t.Fatalf("an e-mail change ran while the e-mail lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if got := f.reload(dick.ID).Email; got != "dick@gotham.example" {
		unlock()
		t.Fatalf("the address changed to %q under the lock", got)
	}
	unlock()
	if err := <-mailDone; err != nil {
		t.Fatal(err)
	}
	if got := f.reload(dick.ID).Email; got != "robin@gotham.example" {
		t.Fatalf("the address is %q after the lock was released", got)
	}
}

// The uniqueness check is made under the lock, after whoever held it before:
// a verified claim that lands while the change waits is seen by it.
func TestUpdateProfile_SeesAClaimThatLandsWhileItWaits(t *testing.T) {
	f := newFx(t)
	dick := f.mailUser("dick", "dick@gotham.example", true)
	selina := f.mailUser("selina", "selina@gotham.example", false)
	p := f.session(dick)
	unlock := f.svc.LockEmails()
	done := make(chan error, 1)
	go func() {
		_, err := f.profile(p, auth.ProfileUpdate{Email: new("catwoman@gotham.example")})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	selina.Email, selina.EmailVerified, selina.Version = "catwoman@gotham.example", true, selina.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, selina) })
	unlock()
	wantErr(t, "a claim that landed first", <-done, auth.ErrEmailInUse)
	f.unchanged(dick)
}

// Two accounts changing to one address at once, with a verified claim to it
// racing them (run with -race). Whatever the order: nobody but the claimant
// holds it verified, a change that was refused wrote nothing, and one that
// landed is unverified. A claim that lands first refuses the change; a change
// that lands first is an unverified holder the claim then displaces
// (CreateUser here, the OIDC resolver in production), the order HEAD allows.
func TestUpdateProfile_ConcurrentChangesToOneAddress(t *testing.T) {
	const rounds = 25
	refused, landed := 0, 0
	for i := range rounds {
		f := newFx(t)
		admin := f.user("gordon", rbac.RoleAdmin)
		a := f.mailUser("dick", "dick@gotham.example", true)
		b := f.mailUser("tim", "tim@gotham.example", true)
		pa, pb, pc := f.session(a), f.session(b), f.session(admin)
		var wg sync.WaitGroup
		var errA, errB, errC error
		start := make(chan struct{})
		for _, fn := range []func(){
			func() { _, errA = f.profile(pa, auth.ProfileUpdate{Email: new("batgirl@gotham.example")}) },
			func() { _, errB = f.profile(pb, auth.ProfileUpdate{Email: new("batgirl@gotham.example")}) },
			func() {
				_, errC = f.svc.CreateUser(context.Background(), pc, auth.NewUser{
					Username: "barbara", Email: "batgirl@gotham.example", EmailVerified: true, Roles: []rbac.RoleName{"gotham.detective"}})
			},
		} {
			wg.Go(func() { ; <-start; fn() })
		}
		close(start)
		wg.Wait()
		if errC != nil {
			t.Fatalf("round %d: the verified claim failed: %v", i, errC)
		}
		verified := 0
		for u, err := range f.st.ScanUsers(context.Background()) {
			if err != nil {
				t.Fatal(err)
			}
			if u.EmailVerified && auth.SameEmail(u.Email, "batgirl@gotham.example") {
				verified++
				if u.Username != "barbara" {
					t.Fatalf("round %d: %s holds the address verified", i, u.Username)
				}
			}
		}
		if verified != 1 {
			t.Fatalf("round %d: %d verified holders, want exactly the claimant", i, verified)
		}
		for who, c := range map[string]struct {
			err error
			u   *auth.User
		}{"dick": {errA, a}, "tim": {errB, b}} {
			now := f.reload(c.u.ID)
			switch {
			case errors.Is(c.err, auth.ErrEmailInUse):
				refused++
				if now.Email != c.u.Email || now.Version != c.u.Version {
					t.Fatalf("round %d: %s was refused and still changed: %+v", i, who, now)
				}
			case c.err == nil:
				landed++
				if now.Email != "batgirl@gotham.example" || now.EmailVerified {
					t.Fatalf("round %d: %s landed as %+v", i, who, now)
				}
			default:
				t.Fatalf("round %d: %s: %v", i, who, c.err)
			}
		}
	}
	t.Logf("%d changes refused, %d landed over %d rounds", refused, landed, rounds)
}
