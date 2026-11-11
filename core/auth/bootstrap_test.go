// file: core/auth/bootstrap_test.go
// version: 1.2.1
// guid: 5a0d8c64-2f1b-4e7a-9b35-6c8e1d4f7a20
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Bootstrap and setup (05-authn §4.6).

// stateFilesFake records what auth writes and removes through StateFiles.
type stateFilesFake struct {
	mu      sync.Mutex
	removed []string
	failRm  error
	// written is label/name -> the last data written; perms the mode.
	written   map[string][]byte
	perms     map[string]fs.FileMode
	failWrite error
}

func (s *stateFilesFake) WriteFile(label, name string, data []byte, perm fs.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWrite != nil {
		return s.failWrite
	}
	if s.written == nil {
		s.written, s.perms = map[string][]byte{}, map[string]fs.FileMode{}
	}
	s.written[label+"/"+name], s.perms[label+"/"+name] = append([]byte(nil), data...), perm
	return nil
}

// file is the last content written to label/name, "" when none.
func (s *stateFilesFake) file(label, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(string(s.written[label+"/"+name]))
}

func (s *stateFilesFake) Remove(label, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, label+"/"+name)
	return s.failRm
}

// bootFx is a fixture whose service has a state filesystem.
func bootFx(t *testing.T) (*fx, *stateFilesFake) {
	t.Helper()
	f := newFx(t)
	files := &stateFilesFake{}
	f.svc = f.serviceWith(f.st, files)
	return f, files
}

// serviceWith builds a service over st with files as its state filesystem.
func (f *fx) serviceWith(st auth.Store, files auth.StateFiles) *auth.Service {
	f.t.Helper()
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Hasher: f.hasher, Settings: f.liveSettings, StateFiles: files,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return svc
}

// bootToken stores a bootstrap token that lives ttl and returns its raw value.
func (f *fx) bootToken(ttl time.Duration) string {
	f.t.Helper()
	var raw auth.Token
	f.commit(func(b *storage.Batch) error {
		var err error
		raw, err = f.st.IssueOneTime(b, &auth.OneTimeToken{
			Kind: auth.OneTimeBootstrap, CreatedAt: f.clock(), ExpiresAt: f.clock().Add(ttl),
		})
		return err
	})
	return string(raw)
}

// tokenStored reports whether the bootstrap token raw is still stored.
func (f *fx) tokenStored(raw string) bool {
	f.t.Helper()
	_, err := f.st.LookupOneTime(context.Background(), auth.OneTimeBootstrap, auth.Token(raw))
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		f.t.Fatalf("LookupOneTime: %v", err)
	}
	return err == nil
}

func (f *fx) userCount() int {
	f.t.Helper()
	n := 0
	for u, err := range f.st.ScanUsers(context.Background()) {
		if err != nil {
			f.t.Fatal(err)
		}
		if u.Status != auth.StatusSystem {
			n++
		}
	}
	return n
}

func loopback() auth.ClientInfo {
	a := netip.MustParseAddr("127.0.0.1")
	return auth.ClientInfo{Addr: a, Peer: a, UserAgent: "batmobile"}
}

// TestBootstrapRedeemedOnce (§4.6 item 2): a bootstrap token exchanges for
// one mck_ key scoped core:admin once. On an empty store it creates "admin"
// with role admin and a generated password returned once; the key, the
// password and the audit record never carry the token.
func TestBootstrapRedeemedOnce(t *testing.T) {
	f, files := bootFx(t)
	ctx := context.Background()
	tok := f.bootToken(auth.DefaultBootstrapTokenTTL)

	res, err := f.svc.RedeemBootstrap(ctx, tok, from("192.0.2.50"))
	if err != nil {
		t.Fatalf("RedeemBootstrap: %v", err)
	}
	if res.User == nil || res.User.Username != "admin" || len(res.User.Roles) != 1 || res.User.Roles[0] != rbac.RoleAdmin {
		t.Fatalf("created user = %+v, want admin holding role admin", res.User)
	}
	if res.GeneratedPassword == "" || len(res.GeneratedPassword) < 24 {
		t.Fatalf("generated password %q, want 24 or more characters", res.GeneratedPassword)
	}
	if err := auth.ParseCredential(auth.KindKey, res.Key); err != nil {
		t.Fatalf("key %q is not an mck_ credential: %v", res.Key, err)
	}
	if !f.svc.HasUsers() {
		t.Fatal("HasUsers is false after bootstrap created the admin")
	}
	if want := f.clock().Add(auth.DefaultBootstrapKeyTTL); !res.ExpiresAt.Equal(want) {
		t.Fatalf("key expires %s, want %s (8 h)", res.ExpiresAt, want)
	}
	k := f.key(res.KeyID)
	if k.Owner != res.User.ID || len(k.Scopes) != 1 || k.Scopes[0] != rbac.CoreAdmin || k.Status != auth.KeyActive {
		t.Fatalf("stored key = %+v, want an active core:admin key of %s", k, res.User.ID)
	}
	p := f.keyPrincipal(f.reload(res.User.ID), k)
	if !p.Perms.Has(rbac.CoreAdmin) || p.Method != auth.MethodAPIKey {
		t.Fatalf("the key's principal: core:admin %v method %s", p.Perms.Has(rbac.CoreAdmin), p.Method)
	}
	if _, err := f.login("admin", res.GeneratedPassword, from("192.0.2.50")); err != nil {
		t.Fatalf("the generated password does not sign in: %v", err)
	}
	if f.tokenStored(tok) {
		t.Fatal("the token is still stored after the redeem")
	}
	files.mu.Lock()
	removed := append([]string(nil), files.removed...)
	files.mu.Unlock()
	if len(removed) != 1 || removed[0] != "auth.bootstrap_token/bootstrap-token" {
		t.Fatalf("token file removals = %v, want the labelled bootstrap-token file once", removed)
	}

	_, err = f.svc.RedeemBootstrap(ctx, tok, from("192.0.2.50"))
	wantErr(t, "a second redeem of a spent token", err, auth.ErrBootstrapInvalid)
	if n := f.userCount(); n != 1 {
		t.Fatalf("users after a replay = %d, want 1", n)
	}
	f.auditCarries(tok)
	f.auditCarries(res.Key)
	f.auditCarries(res.GeneratedPassword)
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	var redeem *audit.Event
	for i := range f.audit.recorded {
		if f.audit.recorded[i].Action == "core.bootstrap.redeem" {
			redeem = &f.audit.recorded[i]
		}
	}
	if redeem == nil || redeem.Outcome != audit.Success || redeem.Details["created_user"] != true ||
		redeem.Target.ID != res.KeyID {
		t.Fatalf("redeem audit = %+v", redeem)
	}
}

// TestBootstrap_AdminIsCoreAdminHolder (§4.6 item 2): the key goes to the
// first active core:admin holder, role-resolved: not a user manager below
// admin, not a disabled or restricted admin. With such a holder there is no
// generated password and no new user.
func TestBootstrap_AdminIsCoreAdminHolder(t *testing.T) {
	f, _ := bootFx(t)
	ctx := context.Background()
	f.user("jim-gordon", roleCommissioner)
	f.edit(f.user("damian-wayne", rbac.RoleAdmin), func(u *auth.User) { u.Status = auth.StatusDisabled })
	// Sorts before bruce, so a pick that ignored the restriction would take it.
	f.edit(f.user("barbara-gordon", rbac.RoleAdmin), func(u *auth.User) { u.SignInRestricted = true })
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	f.user("dick-grayson", rbac.RoleAdmin)
	before := f.userCount()

	res, err := f.svc.RedeemBootstrap(ctx, f.bootToken(auth.DefaultBootstrapTokenTTL), from("192.0.2.51"))
	if err != nil {
		t.Fatal(err)
	}
	// Users scan in ID order: u-bruce-wayne sorts before u-dick-grayson.
	if res.User.ID != bruce.ID || res.GeneratedPassword != "" {
		t.Fatalf("redeem went to %s (generated %q), want the first holder %s with no password", res.User.ID, res.GeneratedPassword, bruce.ID)
	}
	if f.userCount() != before {
		t.Fatal("a redeem created a user although an active admin exists")
	}
	if got := f.key(res.KeyID); got.Owner != bruce.ID {
		t.Fatalf("key owner = %s", got.Owner)
	}
}

// TestBootstrap_NoUsableAdminCreatesOneWithAFreeName: when every admin is
// disabled or restricted, bootstrap creates one under the first free of
// admin, admin-2, ... (HEAD's rule).
func TestBootstrap_NoUsableAdminCreatesOneWithAFreeName(t *testing.T) {
	f, _ := bootFx(t)
	f.edit(f.user("admin", rbac.RoleAdmin), func(u *auth.User) { u.Status = auth.StatusDisabled })
	f.user("Admin-2", "gotham.detective") // folded: the same name
	res, err := f.svc.RedeemBootstrap(context.Background(), f.bootToken(auth.DefaultBootstrapTokenTTL), from("192.0.2.52"))
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Username != "admin-3" || res.GeneratedPassword == "" {
		t.Fatalf("created %q (password %v), want admin-3 with a password", res.User.Username, res.GeneratedPassword != "")
	}
}

// TestBootstrap_KeyTTLIsBounded (§4.6 item 2, §8): core.auth.bootstrap.key_ttl
// 8 h by default, never above 24 h, a non-positive value the default.
func TestBootstrap_KeyTTLIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  time.Duration
		want time.Duration
	}{
		{"unset", 0, 8 * time.Hour},
		{"negative", -time.Hour, 8 * time.Hour},
		{"two hours", 2 * time.Hour, 2 * time.Hour},
		{"a week", 7 * 24 * time.Hour, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := bootFx(t)
			f.setSettings(auth.Settings{BootstrapKeyTTL: tc.ttl})
			res, err := f.svc.RedeemBootstrap(context.Background(), f.bootToken(auth.DefaultBootstrapTokenTTL), from("192.0.2.53"))
			if err != nil {
				t.Fatal(err)
			}
			if got := res.ExpiresAt.Sub(f.clock()); got != tc.want {
				t.Fatalf("key lives %s, want %s", got, tc.want)
			}
		})
	}
}

// TestBootstrap_ExpiredTokenIsSpentAndRefused: past its expiry a token is
// refused, deleted and audited; it creates nothing.
func TestBootstrap_ExpiredTokenIsSpentAndRefused(t *testing.T) {
	f, files := bootFx(t)
	tok := f.bootToken(auth.DefaultBootstrapTokenTTL)
	f.advance(auth.DefaultBootstrapTokenTTL)
	_, err := f.svc.RedeemBootstrap(context.Background(), tok, from("192.0.2.54"))
	wantErr(t, "an expired token", err, auth.ErrBootstrapInvalid)
	if f.tokenStored(tok) || f.userCount() != 0 || f.svc.HasUsers() {
		t.Fatalf("expired redeem: token stored %v, users %d", f.tokenStored(tok), f.userCount())
	}
	if len(files.removed) != 0 {
		t.Fatalf("a refused redeem removed the token file: %v", files.removed)
	}
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	last := f.audit.recorded[len(f.audit.recorded)-1]
	if last.Action != "core.bootstrap.redeem" || last.Outcome != audit.Failure || last.Reason != "expired" {
		t.Fatalf("expiry audit = %+v", last)
	}
}

// TestBootstrap_BadTokensAreChargedToTheSource (§4.9): a malformed or unknown
// token is a failed credential check; a source past its budget is refused
// with a retry hint before its token is even read, and a real token stays
// unspent.
func TestBootstrap_BadTokensAreChargedToTheSource(t *testing.T) {
	f, _ := bootFx(t)
	ctx := context.Background()
	good := f.bootToken(auth.DefaultBootstrapTokenTTL)
	unknown, err := auth.MintCredential(auth.KindBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	c := from("192.0.2.55")
	for i := range auth.DefaultMaxFailuresPerSource {
		bad := string(unknown)
		if i%2 == 1 {
			bad = "mcb_not-a-token"
		}
		_, err := f.svc.RedeemBootstrap(ctx, bad, c)
		wantErr(t, "a bad token", err, auth.ErrBootstrapInvalid)
	}
	_, err = f.svc.RedeemBootstrap(ctx, good, c)
	var te *auth.ThrottledError
	if !errors.As(err, &te) || !errors.Is(err, auth.ErrThrottled) || te.RetryAfter < 1 {
		t.Fatalf("redeem from a spent source = %v, want ThrottledError", err)
	}
	if !f.tokenStored(good) {
		t.Fatal("a throttled redeem spent the token")
	}
	if _, err := f.svc.RedeemBootstrap(ctx, good, from("192.0.2.56")); err != nil {
		t.Fatalf("another source: %v", err)
	}
}

// failingKeys fails IssueKey, the last write of a redeem's batch.
type failingKeys struct{ auth.Store }

func (failingKeys) IssueKey(*storage.Batch, *auth.APIKey) (auth.Token, error) {
	return "", errors.New("the grappling hook snapped")
}

// TestBootstrap_FailedCreationLeavesTheTokenUnspent: the token is spent in
// the batch that creates the admin and the key, so a failure leaves the token
// and no admin (HEAD wrote the hash back).
func TestBootstrap_FailedCreationLeavesTheTokenUnspent(t *testing.T) {
	f, files := bootFx(t)
	tok := f.bootToken(auth.DefaultBootstrapTokenTTL)
	broken := f.serviceWith(failingKeys{f.st}, files)
	if _, err := broken.RedeemBootstrap(context.Background(), tok, from("192.0.2.57")); err == nil {
		t.Fatal("redeem succeeded although the key could not be issued")
	}
	if !f.tokenStored(tok) || f.userCount() != 0 || len(files.removed) != 0 {
		t.Fatalf("after a failed redeem: token stored %v, users %d, file removals %v", f.tokenStored(tok), f.userCount(), files.removed)
	}
	if _, err := f.svc.RedeemBootstrap(context.Background(), tok, from("192.0.2.57")); err != nil {
		t.Fatalf("redeem of the surviving token: %v", err)
	}
}

// TestBootstrapRedeemedOnce_Concurrent: of many concurrent redeems of one
// token exactly one wins, and exactly one admin and one key exist.
func TestBootstrapRedeemedOnce_Concurrent(t *testing.T) {
	f, _ := bootFx(t)
	tok := f.bootToken(auth.DefaultBootstrapTokenTTL)
	var wins, refused atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, err := f.svc.RedeemBootstrap(context.Background(), tok, from("192.0.2."+string(rune('1'+i))+"0"))
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, auth.ErrBootstrapInvalid):
				refused.Add(1)
			default:
				t.Errorf("redeem: %v", err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 || refused.Load() != 7 {
		t.Fatalf("%d redeems won and %d were refused, want 1 and 7", wins.Load(), refused.Load())
	}
	n := 0
	for _, err := range f.svc.APIKeys(context.Background(), "") {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if f.userCount() != 1 || n != 1 {
		t.Fatalf("users %d, keys %d, want 1 and 1", f.userCount(), n)
	}
}

// TestSetup_RequiresBootstrapToken (§4.6 item 3): setup needs HasUsers false
// and the token; a missing token closes it, a wrong one is refused and
// charged, the right one creates the admin and is spent with it.
func TestSetup_RequiresBootstrapToken(t *testing.T) {
	f, files := bootFx(t)
	ctx := context.Background()
	tok := f.bootToken(auth.DefaultBootstrapTokenTTL)
	in := auth.SetupInput{Username: " lucius-fox ", Password: "wayne-tech-2026", Email: "lucius@wayne.example", Client: from("192.0.2.60")}

	_, err := f.svc.Setup(ctx, in)
	wantErr(t, "no token", err, auth.ErrSetupClosed)
	in.BootstrapToken = "mcb_not-a-token"
	_, err = f.svc.Setup(ctx, in)
	wantErr(t, "a malformed token", err, auth.ErrBootstrapInvalid)
	short := in
	short.BootstrapToken, short.Password = tok, "abc"
	_, err = f.svc.Setup(ctx, short)
	wantErr(t, "a short password", err, auth.ErrBadInput)
	if !f.tokenStored(tok) {
		t.Fatal("a refused setup spent the token")
	}

	in.BootstrapToken = tok
	u, err := f.svc.Setup(ctx, in)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if u.Username != "lucius-fox" || len(u.Roles) != 1 || u.Roles[0] != rbac.RoleAdmin || u.Status != auth.StatusActive ||
		u.Email != "lucius@wayne.example" || u.EmailVerified || !u.HasPassword {
		t.Fatalf("setup user = %+v", u)
	}
	if _, err := f.login("lucius-fox", "wayne-tech-2026", from("192.0.2.60")); err != nil {
		t.Fatalf("the chosen password does not sign in: %v", err)
	}
	if f.tokenStored(tok) || !f.svc.HasUsers() || len(files.removed) != 1 {
		t.Fatalf("after setup: token stored %v, HasUsers %v, file removals %v", f.tokenStored(tok), f.svc.HasUsers(), files.removed)
	}
	_, err = f.svc.Setup(ctx, in)
	wantErr(t, "a second setup", err, auth.ErrSetupClosed)
	_, err = f.svc.RedeemBootstrap(ctx, tok, from("192.0.2.60"))
	wantErr(t, "the setup's token redeemed", err, auth.ErrBootstrapInvalid)
	f.auditCarries(tok)
	f.auditCarries("wayne-tech-2026")
}

// TestSetup_ClosedOnceAUserExists: with a user stored setup refuses before
// it reads the token, which stays unspent.
func TestSetup_ClosedOnceAUserExists(t *testing.T) {
	f, _ := bootFx(t)
	f.user("bruce-wayne", rbac.RoleAdmin)
	tok := f.bootToken(auth.DefaultBootstrapTokenTTL)
	_, err := f.svc.Setup(context.Background(), auth.SetupInput{
		Username: "selina-kyle", Password: "catwoman-2026", BootstrapToken: tok, Client: from("192.0.2.61"),
	})
	wantErr(t, "setup with a user", err, auth.ErrSetupClosed)
	if !f.tokenStored(tok) || f.userCount() != 1 {
		t.Fatalf("closed setup: token stored %v, users %d", f.tokenStored(tok), f.userCount())
	}
}

// TestSetup_LoopbackExceptionRefusesForwarded (§4.6 items 3-4, SEC-30): with
// core.auth.setup_requires_token=false only a Local request sets up without
// the token: a loopback peer with no forwarding header and not a trusted
// proxy. By default no request does. A token that is presented is checked
// whether or not it was needed.
func TestSetup_LoopbackExceptionRefusesForwarded(t *testing.T) {
	off := false
	remote := from("192.0.2.62")
	fwd := loopback()
	fwd.Forwarded = true
	proxy := loopback()
	proxy.TrustedProxy = true
	in := func(c auth.ClientInfo, name string) auth.SetupInput {
		return auth.SetupInput{Username: name, Password: "robin-2026-ok", Client: c}
	}

	f, _ := bootFx(t)
	_, err := f.svc.Setup(context.Background(), in(loopback(), "oracle"))
	wantErr(t, "a local request, token required by default", err, auth.ErrSetupClosed)

	f.setSettings(auth.Settings{SetupRequiresToken: &off})
	for name, c := range map[string]auth.ClientInfo{"remote": remote, "forwarded": fwd, "trusted proxy": proxy} {
		_, err := f.svc.Setup(context.Background(), in(c, "oracle"))
		wantErr(t, name+" without the token", err, auth.ErrSetupClosed)
	}
	bad := in(loopback(), "oracle")
	bad.BootstrapToken = "mcb_not-a-token"
	_, err = f.svc.Setup(context.Background(), bad)
	wantErr(t, "a local request with a wrong token", err, auth.ErrBootstrapInvalid)
	if f.userCount() != 0 {
		t.Fatal("a refused setup created a user")
	}
	u, err := f.svc.Setup(context.Background(), in(loopback(), "oracle"))
	if err != nil || u.Username != "oracle" {
		t.Fatalf("the local request without a token: %v, %v", u, err)
	}
}

// TestSetup_ConcurrentCreatesOneAdmin: two simultaneous token-less local
// setups create one user between them.
func TestSetup_ConcurrentCreatesOneAdmin(t *testing.T) {
	f, _ := bootFx(t)
	off := false
	f.setSettings(auth.Settings{SetupRequiresToken: &off})
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, name := range []string{"jason-todd", "tim-drake", "stephanie-brown", "cassandra-cain"} {
		wg.Go(func() {
			_, err := f.svc.Setup(context.Background(), auth.SetupInput{Username: name, Password: "gotham-nights-26", Client: loopback()})
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, auth.ErrSetupClosed):
			default:
				t.Errorf("Setup %s: %v", name, err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 || f.userCount() != 1 {
		t.Fatalf("%d setups won, %d users, want 1 and 1", wins.Load(), f.userCount())
	}
}

// TestBootstrap_OwnerDemotedBeforeCommitIsRepicked (§4.6 item 2): the admin
// the key is minted for is guarded in the redeem's batch, so a demotion that
// commits between the pick and the commit fails the commit and the retry
// picks again, never a key for an account that is no longer an admin.
func TestBootstrap_OwnerDemotedBeforeCommitIsRepicked(t *testing.T) {
	f, _ := bootFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", rbac.RoleAdmin)
	var once sync.Once
	f.audit.mu.Lock()
	f.audit.hold = func(e audit.Event) {
		if e.Action != "core.bootstrap.redeem" {
			return
		}
		once.Do(func() {
			f.edit(f.reload(bruce.ID), func(u *auth.User) { u.Status = auth.StatusDisabled })
		})
	}
	f.audit.mu.Unlock()

	res, err := f.svc.RedeemBootstrap(context.Background(), f.bootToken(auth.DefaultBootstrapTokenTTL), from("192.0.2.53"))
	if err != nil {
		t.Fatal(err)
	}
	if res.User.ID != dick.ID || f.key(res.KeyID).Owner != dick.ID {
		t.Fatalf("the key went to %s, want the remaining admin %s", res.User.ID, dick.ID)
	}
}
