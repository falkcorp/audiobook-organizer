// file: core/auth/compat_test.go
// version: 1.1.0
// guid: 3d9a1f58-7c02-4be6-a1d4-82e5b0c9f713
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Compat, the method-bound handle (05-authn §3.1, §4.10, §5 P7).

const absMethod auth.Method = "gotham.abs"

// compatFx is a fixture with the gotham.abs compat method registered.
func compatFx(t *testing.T) (*fx, auth.Compat) {
	t.Helper()
	f := newFx(t)
	// The fixture rebuilds its service when the first user is stored, which
	// would drop the registration below: store one first.
	f.user("alfred-pennyworth", "gotham.detective")
	f.svc.SetLoginSleep(func(time.Duration) {})
	c, err := f.svc.RegisterCompat("gotham", auth.MethodSpec{Method: absMethod})
	if err != nil {
		t.Fatalf("RegisterCompat: %v", err)
	}
	return f, c
}

type handoffFake struct{}

func (handoffFake) Allowed(string) bool { return true }
func (handoffFake) Mint(context.Context, *auth.User, string, string, string, string) (string, error) {
	return "", nil
}

// TestModuleAuthMethod_CannotBeInteractive (§3.3, §6, SEC2-16): a compat
// method that is interactive, may change credentials, verifies owners, is
// outside "<moduleID>." or is registered twice is refused with ErrMethodSpec
// and registers nothing; at most one method per app carries the OIDC
// hand-off.
func TestModuleAuthMethod_CannotBeInteractive(t *testing.T) {
	f := newFx(t)
	for name, tc := range map[string]struct {
		module string
		spec   auth.MethodSpec
	}{
		"interactive":        {"gotham", auth.MethodSpec{Method: "gotham.signal", Interactive: true}},
		"changes credential": {"gotham", auth.MethodSpec{Method: "gotham.signal", MayChangeCredentials: true}},
		"owner verifier":     {"gotham", auth.MethodSpec{Method: "gotham.signal", OwnerVerifier: true}},
		"another module's":   {"gotham", auth.MethodSpec{Method: "arkham.signal"}},
		"an extension name":  {"gotham", auth.MethodSpec{Method: "ext:signal"}},
		"a core method":      {"gotham", auth.MethodSpec{Method: auth.MethodAPIKey}},
		"no name":            {"gotham", auth.MethodSpec{Method: "gotham."}},
		"a path in the name": {"gotham", auth.MethodSpec{Method: "gotham.bat/signal"}},
		"no module":          {"", auth.MethodSpec{Method: ".signal"}},
		"a dotted module":    {"gotham.city", auth.MethodSpec{Method: "gotham.city.signal"}},
	} {
		if c, err := f.svc.RegisterCompat(tc.module, tc.spec); !errors.Is(err, auth.ErrMethodSpec) || c != nil {
			t.Errorf("%s: handle %v, err %v, want ErrMethodSpec", name, c, err)
		}
	}
	u := f.user("bruce-wayne", rbac.RoleAdmin)
	if _, err := f.svc.PrincipalFor(context.Background(), u, "gotham.signal", nil, u.CredentialEpoch); !errors.Is(err, auth.ErrMethodSpec) {
		t.Fatalf("a refused method builds principals: %v", err)
	}

	if _, err := f.svc.RegisterCompat("gotham", auth.MethodSpec{Method: "gotham.abs", AppHandoff: handoffFake{}}); err != nil {
		t.Fatalf("a valid method with the hand-off: %v", err)
	}
	if _, err := f.svc.RegisterCompat("gotham", auth.MethodSpec{Method: "gotham.abs"}); !errors.Is(err, auth.ErrMethodSpec) {
		t.Errorf("a method registered twice: %v", err)
	}
	if _, err := f.svc.RegisterCompat("gotham", auth.MethodSpec{Method: "gotham.other", AppHandoff: handoffFake{}}); !errors.Is(err, auth.ErrMethodSpec) {
		t.Errorf("a second hand-off: %v", err)
	}
	if _, err := f.svc.RegisterCompat("gotham", auth.MethodSpec{Method: "gotham.other"}); err != nil {
		t.Errorf("a plain second method: %v", err)
	}
}

// TestCompat_ABSLoginHonoursRestrictions (§4.10, §10): the password, key and
// Cloudflare-style PrincipalFor paths each refuse a SignInRestricted user, a
// PasswordResetRequired user and a needs_confirmation key. A refused
// PrincipalFor call records no throttle failure.
func TestCompat_ABSLoginHonoursRestrictions(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.70")
	restricted := f.edit(f.withPassword(f.user("tim-drake", "gotham.detective"), "red-robin-9"), func(u *auth.User) { u.SignInRestricted = true })
	reset := f.edit(f.withPassword(f.user("jason-todd", "gotham.detective"), "red-hood-9"), func(u *auth.User) { u.PasswordResetRequired = true })
	dick := f.user("dick-grayson", "gotham.detective")
	raw, _ := f.mint(f.session(dick), dick.ID, "gotham:patrol")

	_, err := c.VerifyPassword(ctx, "tim-drake", "red-robin-9", cl)
	wantErr(t, "password, restricted", err, auth.ErrResetRequired)
	_, err = c.VerifyPassword(ctx, "jason-todd", "red-hood-9", cl)
	wantErr(t, "password, reset required", err, auth.ErrResetRequired)

	_, err = c.PrincipalFor(ctx, restricted, nil, restricted.CredentialEpoch, cl)
	wantErr(t, "principal, restricted", err, auth.ErrResetRequired)
	_, err = c.PrincipalFor(ctx, reset, nil, reset.CredentialEpoch, cl)
	wantErr(t, "principal, reset required", err, auth.ErrResetRequired)

	for _, k := range f.keysOf(dick.ID) {
		k.Status, k.Version = auth.KeyNeedsConfirmation, k.Version+1
		f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, k) })
	}
	_, err = c.AuthenticateKey(ctx, raw, cl)
	wantErr(t, "key, needs_confirmation", err, auth.ErrRevoked)

	// None of those refusals was a credential guess: the source is not near
	// its budget, however many PrincipalFor refusals it made.
	for range 3 * auth.DefaultMaxFailuresPerSource {
		_, _ = c.PrincipalFor(ctx, restricted, nil, restricted.CredentialEpoch, cl)
	}
	if auth.LoginThrottleOf(f.svc).SourceBlocked(auth.SourceKeyOf(cl)) {
		t.Fatal("refused PrincipalFor calls were charged to the source")
	}
}

// keysOf lists owner's stored keys.
func (f *fx) keysOf(owner auth.UserID) []*auth.APIKey {
	f.t.Helper()
	var out []*auth.APIKey
	for k, err := range f.st.ScanAPIKeys(context.Background(), owner) {
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

// TestCompat_VerifyPasswordSharesTheWebBudget (§4.9): failed passwords through
// the compat handle and through Login are one count, so 15 failures through
// one door close the other; a good password returns the user and no session,
// and a bcrypt hash is re-stored as argon2id.
func TestCompat_VerifyPasswordSharesTheWebBudget(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	legacy, err := bcrypt.GenerateFromPassword([]byte("utility-belt-7"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	tim := f.withHash(f.user("tim-drake", "gotham.detective"), string(legacy))
	cl := from("192.0.2.71")

	u, err := c.VerifyPassword(ctx, "tim-drake", "utility-belt-7", cl)
	if err != nil || u.ID != tim.ID {
		t.Fatalf("VerifyPassword = %v, %v", u, err)
	}
	if h, _ := f.st.PasswordHash(ctx, tim.ID); password.AlgoOf(h) != password.AlgoArgon2id {
		t.Fatalf("hash after a compat sign-in is %q, want argon2id", password.AlgoOf(h))
	}
	if n := countSessions(t, f, tim.ID); n != 0 {
		t.Fatalf("VerifyPassword opened %d sessions", n)
	}

	for range auth.DefaultMaxFailuresPerSource - 1 {
		_, err = c.VerifyPassword(ctx, "tim-drake", "wrong", cl)
		wantErr(t, "a wrong password", err, auth.ErrInvalidCredentials)
	}
	_, err = f.login("alfred-pennyworth", "wrong", cl)
	wantErr(t, "the web door's failure", err, auth.ErrInvalidCredentials)
	_, err = c.VerifyPassword(ctx, "tim-drake", "utility-belt-7", cl)
	wantErr(t, "the compat door after the budget is spent", err, auth.ErrThrottled)
	_, err = f.login("tim-drake", "utility-belt-7", cl)
	wantErr(t, "the web door after the budget is spent", err, auth.ErrThrottled)
}

func countSessions(t *testing.T, f *fx, u auth.UserID) int {
	t.Helper()
	n := 0
	for _, err := range f.svc.Sessions(context.Background(), u) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

// TestCompat_AuthenticateKeyAppliesEveryStage12Rule (§4.10, SEC2-4): an mck_
// or imported key authenticates to a principal whose Method is the handle's,
// scoped to the key; a revoked, disabled, expired or unconfirmed key, a
// disabled owner and a malformed or unknown key are refused. Only the last
// two are charged to the source.
func TestCompat_AuthenticateKeyAppliesEveryStage12Rule(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.72")
	dick := f.user("dick-grayson", "gotham.detective")
	sess := f.session(dick)
	raw, k := f.mint(sess, dick.ID, "gotham:patrol")

	p, err := c.AuthenticateKey(ctx, raw, cl)
	if err != nil {
		t.Fatalf("AuthenticateKey: %v", err)
	}
	if p.Method != absMethod || p.User.ID != dick.ID || p.CredentialID != k.ID || !p.Perms.Has("gotham:patrol") ||
		p.Perms.Has("gotham:cases.read") {
		t.Fatalf("key principal = %+v", p)
	}

	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	imported := "abk_" + base64.RawURLEncoding.EncodeToString(buf)
	f.commit(func(b *storage.Batch) error {
		return f.st.ImportAPIKey(b, &auth.APIKey{
			ID: "key-oracle", Owner: dick.ID, Name: "Oracle relay", Scopes: []rbac.Permission{"gotham:patrol"},
			Status: auth.KeyActive, CreatedAt: f.clock(), ExpiresAt: f.clock().Add(time.Hour), Imported: true,
		}, auth.HashCredential(auth.Token(imported)))
	})
	if p, err = c.AuthenticateKey(ctx, imported, cl); err != nil || p.CredentialID != "key-oracle" {
		t.Fatalf("imported key: %v, %v", p, err)
	}

	for _, tc := range []struct {
		name   string
		status auth.KeyStatus
		want   error
	}{
		{"revoked", auth.KeyRevoked, auth.ErrRevoked},
		{"disabled", auth.KeyDisabled, auth.ErrRevoked},
		{"needs_confirmation", auth.KeyNeedsConfirmation, auth.ErrRevoked},
	} {
		rawS, kS := f.mint(sess, dick.ID, "gotham:patrol")
		cur := f.key(kS.ID)
		cur.Status, cur.Version = tc.status, cur.Version+1
		f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, cur) })
		_, err := c.AuthenticateKey(ctx, rawS, cl)
		wantErr(t, tc.name+" key", err, tc.want)
	}
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	rawB, _ := f.mint(f.session(bruce), bruce.ID, "gotham:patrol")
	raw2, _ := f.mint(sess, dick.ID, "gotham:patrol")
	f.advance(auth.DefaultKeyTTL + time.Minute)
	_, err = c.AuthenticateKey(ctx, raw2, cl)
	wantErr(t, "an expired key", err, auth.ErrExpired)

	// None of the refusals above was a credential guess.
	if auth.LoginThrottleOf(f.svc).SourceBlocked(auth.SourceKeyOf(cl)) {
		t.Fatal("stale credentials were charged to the source")
	}
	unknown, _ := auth.MintCredential(auth.KindKey)
	for i := range auth.DefaultMaxFailuresPerSource {
		bad := string(unknown)
		if i%2 == 1 {
			bad = "mck_garbage"
		}
		_, err = c.AuthenticateKey(ctx, bad, cl)
		wantErr(t, "an invalid key", err, auth.ErrInvalidCredentials)
	}
	_, err = c.AuthenticateKey(ctx, rawB, cl)
	wantErr(t, "a good key from a spent source", err, auth.ErrThrottled)
}

// TestCompat_DisabledOwnerRefusesItsKey: a key stops working while its owner
// is disabled (stage 12's rule).
func TestCompat_DisabledOwnerRefusesItsKey(t *testing.T) {
	f, c := compatFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	raw, _ := f.mint(f.session(dick), dick.ID, "gotham:patrol")
	f.edit(dick, func(u *auth.User) { u.Status = auth.StatusDisabled })
	_, err := c.AuthenticateKey(context.Background(), raw, from("192.0.2.73"))
	wantErr(t, "a disabled owner's key", err, auth.ErrInactiveUser)
}

// TestCompat_PrincipalForUsesTheStoredUser (SEC-1): the roles of a principal
// come from the store, never from the user value the module hands in; the
// epoch is the stored one; an unknown user is refused.
func TestCompat_PrincipalForUsesTheStoredUser(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.74")
	dick := f.user("dick-grayson", "gotham.detective")

	forged := *dick
	forged.Roles = []rbac.RoleName{rbac.RoleAdmin}
	p, err := c.PrincipalFor(ctx, &forged, nil, dick.CredentialEpoch, cl)
	if err != nil {
		t.Fatal(err)
	}
	if p.Perms.Has(rbac.CoreAdmin) || !p.Perms.Has("gotham:patrol") || p.Method != absMethod ||
		len(p.User.Roles) != 1 || p.User.Roles[0] != "gotham.detective" {
		t.Fatalf("principal built from a forged user: roles %v admin %v", p.User.Roles, p.Perms.Has(rbac.CoreAdmin))
	}
	narrow, err := c.PrincipalFor(ctx, dick, []rbac.Permission{"gotham:patrol"}, dick.CredentialEpoch, cl)
	if err != nil || narrow.Perms.Has("gotham:cases.read") {
		t.Fatalf("scoped principal: %v, %v", narrow, err)
	}
	_, err = c.PrincipalFor(ctx, dick, nil, dick.CredentialEpoch+1, cl)
	wantErr(t, "a wrong epoch", err, auth.ErrStaleEpoch)
	_, err = c.PrincipalFor(ctx, &auth.User{ID: "u-ghost"}, nil, 0, cl)
	wantErr(t, "an unknown user", err, auth.ErrInactiveUser)
	_, err = c.PrincipalFor(ctx, nil, nil, 0, cl)
	wantErr(t, "no user", err, auth.ErrInvalidCredentials)
	f.edit(dick, func(u *auth.User) { u.Status = auth.StatusDisabled })
	_, err = c.PrincipalFor(ctx, dick, nil, dick.CredentialEpoch, cl)
	wantErr(t, "a disabled user", err, auth.ErrInactiveUser)
}

// TestCompat_PrincipalForThrottledSourceIsRefusedFirst: a source past its
// failure budget gets ErrThrottled from PrincipalFor before any other check.
func TestCompat_PrincipalForThrottledSourceIsRefusedFirst(t *testing.T) {
	f, c := compatFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	cl := from("192.0.2.75")
	th := auth.LoginThrottleOf(f.svc)
	for range auth.DefaultMaxFailuresPerSource {
		th.RecordFailure("", auth.SourceKeyOf(cl))
	}
	_, err := c.PrincipalFor(context.Background(), dick, nil, dick.CredentialEpoch, cl)
	wantErr(t, "a throttled source", err, auth.ErrThrottled)
	_, err = c.PrincipalFor(context.Background(), dick, nil, dick.CredentialEpoch, from("192.0.2.76"))
	if err != nil {
		t.Fatalf("another source: %v", err)
	}
}

// TestCompat_CheckCredentialFor (§4.10, SEC2-4): a confined principal reaches
// only a route naming its scope; a compat key principal re-reads its key, so
// a revocation after authentication is seen; a principal of another method is
// refused.
func TestCompat_CheckCredentialFor(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.77")
	worker := f.user("relay-user", "relay-worker")
	raw, wk := f.mint(f.session(f.user("bruce-wayne", rbac.RoleAdmin)), worker.ID, "gotham:relay.worker")
	p, err := c.AuthenticateKey(ctx, raw, cl)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckCredentialFor(p, rbac.Need("gotham:relay.worker")); err != nil {
		t.Fatalf("the worker's own route: %v", err)
	}
	wantErr(t, "a confined key on another route", c.CheckCredentialFor(p, rbac.Need("gotham:patrol")), auth.ErrConfined)

	cur := f.key(wk.ID)
	cur.Status, cur.Version = auth.KeyRevoked, cur.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, cur) })
	wantErr(t, "a key revoked after it authenticated", c.CheckCredentialFor(p, rbac.Need("gotham:relay.worker")), auth.ErrRevoked)

	dick := f.user("dick-grayson", "gotham.detective")
	other := f.session(dick) // Method session, not this handle's
	wantErr(t, "another method's principal", c.CheckCredentialFor(other, rbac.Need("gotham:patrol")), auth.ErrInvalidCredentials)
	wantErr(t, "no principal", c.CheckCredentialFor(nil, rbac.Need("gotham:patrol")), auth.ErrInvalidCredentials)
}

// TestCompatKey_RevalidateFollowsTheKey: a compat key principal carries a key
// under its own Method, so Revalidate must read the key the way it does for
// api_key: a revoked, disabled, expired or deleted key, and a key moved to
// another owner, end the principal; its user's epoch alone does not decide.
func TestCompatKey_RevalidateFollowsTheKey(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.78")
	dick := f.user("dick-grayson", "gotham.detective")
	sess := f.session(dick)

	for _, tc := range []struct {
		name   string
		status auth.KeyStatus
		want   error
	}{
		{"revoked", auth.KeyRevoked, auth.ErrRevoked},
		{"disabled", auth.KeyDisabled, auth.ErrRevoked},
		{"needs_confirmation", auth.KeyNeedsConfirmation, auth.ErrRevoked},
	} {
		raw, k := f.mint(sess, dick.ID, "gotham:patrol")
		p, err := c.AuthenticateKey(ctx, raw, cl)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.svc.Revalidate(ctx, p); err != nil {
			t.Fatalf("%s: a live compat key principal: %v", tc.name, err)
		}
		cur := f.key(k.ID)
		cur.Status, cur.Version = tc.status, cur.Version+1
		f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, cur) })
		wantErr(t, tc.name+" compat key", f.svc.Revalidate(ctx, p), tc.want)
	}

	raw, _ := f.mint(sess, dick.ID, "gotham:patrol")
	p, err := c.AuthenticateKey(ctx, raw, cl)
	if err != nil {
		t.Fatal(err)
	}
	f.advance(auth.DefaultKeyTTL + time.Minute)
	wantErr(t, "an expired compat key", f.svc.Revalidate(ctx, p), auth.ErrExpired)
}

// TestCompatKey_CannotMintOrManageKeys: a compat surface's key principal has
// no business mutating credentials, and the key rules that bind api_key
// principals bind it too: it cannot mint a key (wider than itself or not), and
// cannot re-enable, disable or revoke any key, so a narrow key never revives a
// broader one (#4590 M1). The refusal is outright (ErrDenied).
func TestCompatKey_CannotMintOrManageKeys(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.79")
	dick := f.user("dick-grayson", "gotham.detective")
	sess := f.session(dick)
	broad, bk := f.mint(sess, dick.ID, "gotham:patrol", "gotham:cases.read")
	_ = broad
	rawN, _ := f.mint(sess, dick.ID, "gotham:patrol")
	disabled := f.key(bk.ID)
	disabled.Status, disabled.Version = auth.KeyDisabled, disabled.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, disabled) })

	p, err := c.AuthenticateKey(ctx, rawN, cl)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.svc.CreateAPIKey(ctx, p, auth.KeySpec{Owner: dick.ID, Name: "wider", Scopes: []rbac.Permission{"gotham:patrol", "gotham:cases.read"}})
	wantErr(t, "a wider key minted by a compat key", err, auth.ErrDenied)
	_, _, err = f.svc.CreateAPIKey(ctx, p, auth.KeySpec{Owner: dick.ID, Name: "same", Scopes: []rbac.Permission{"gotham:patrol"}})
	wantErr(t, "an equal key minted by a compat key", err, auth.ErrDenied)
	wantErr(t, "a broader key re-enabled by a compat key", f.svc.SetAPIKeyStatus(ctx, p, bk.ID, auth.KeyActive), auth.ErrDenied)
	wantErr(t, "a key revoked by a compat key", f.svc.RevokeAPIKey(ctx, p, bk.ID), auth.ErrDenied)
	if got := f.key(bk.ID).Status; got != auth.KeyDisabled {
		t.Fatalf("the broader key is now %q", got)
	}
}

// TestKeyPrincipal_NarrowKeyCannotReviveABroaderOne is the api_key twin of the
// compat refusal above: the key rules themselves, through keyRef, hold for the
// native method.
func TestKeyPrincipal_NarrowKeyCannotReviveABroaderOne(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick-grayson", "gotham.detective")
	sess := f.session(dick)
	_, bk := f.mint(sess, dick.ID, "gotham:patrol", "gotham:cases.read")
	_, nk := f.mint(sess, dick.ID, "gotham:patrol")
	disabled := f.key(bk.ID)
	disabled.Status, disabled.Version = auth.KeyDisabled, disabled.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, disabled) })

	p := f.keyPrincipal(dick, nk)
	_, _, err := f.svc.CreateAPIKey(ctx, p, auth.KeySpec{Owner: dick.ID, Name: "wider", Scopes: []rbac.Permission{"gotham:patrol", "gotham:cases.read"}})
	wantErr(t, "a wider key minted by a narrow key", err, auth.ErrScopeEscalation)
	wantErr(t, "a broader key managed by a narrow key", f.svc.RevokeAPIKey(ctx, p, bk.ID), auth.ErrScopeEscalation)
}
