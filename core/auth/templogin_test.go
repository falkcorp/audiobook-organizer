// file: core/auth/templogin_test.go
// version: 1.0.0
// guid: c5b74eb3-61fb-41c9-8574-c5f1f428e944
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Temp-login and reset links (05-authn §3.2, §4.12, §4.13, §4.14).

// linkToken is the token of a minted fragment link, failing the test when
// the link is not /login/temp#token=<mct_ token>.
func linkToken(t testing.TB, link string) string {
	t.Helper()
	tok, ok := strings.CutPrefix(link, "/login/temp#token=")
	if !ok || auth.ParseCredential(auth.KindTempLogin, tok) != nil {
		t.Fatalf("link %q is not a temp-login fragment link", link)
	}
	return tok
}

// mintTemp mints a temp-login (or, with reset, a reset) link for target.
func (f *fx) mintTemp(actor *auth.Principal, target auth.UserID, reset bool) string {
	f.t.Helper()
	mint := f.svc.MintTempLogin
	if reset {
		mint = f.svc.MintResetLink
	}
	link, err := mint(context.Background(), actor, target)
	if err != nil {
		f.t.Fatalf("mint (reset=%v) for %s: %v", reset, target, err)
	}
	return linkToken(f.t, link)
}

// auditCarries fails when any recorded or emitted audit event carries secret.
func (f *fx) auditCarries(secret string) {
	f.t.Helper()
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	for _, events := range [][]audit.Event{f.audit.recorded, f.audit.emitted} {
		for _, e := range events {
			if strings.Contains(fmt.Sprintf("%+v", e), secret) {
				f.t.Fatalf("an audit event carries a credential: %+v", e)
			}
		}
	}
}

// §4.13: a temp-login link is a fragment link holding an mct_ token; it
// opens one delegated session of core.auth.session.ttl at the user's epoch,
// once. A password offered with it is refused and leaves it unspent.
func TestTempLogin_OpensOneDelegatedSession(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	barbara := f.user("barbara-gordon", "gotham.detective")
	tok := f.mintTemp(f.session(gordon), barbara.ID, false)

	wantErr(t, "a password with a plain link", func() error {
		_, err := f.svc.ConsumeTempLogin(ctx, tok, "oracle-1988", from("192.0.2.40"))
		return err
	}(), auth.ErrBadInput)
	sess, err := f.svc.ConsumeTempLogin(ctx, tok, "", from("192.0.2.40"))
	if err != nil {
		t.Fatalf("ConsumeTempLogin: %v", err)
	}
	rec, err := f.st.SessionByPublicID(ctx, sess.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UserID != barbara.ID || rec.Origin != auth.OriginTempLogin || rec.Origin.Method() != auth.MethodSessionDelegated ||
		rec.CredentialEpoch != barbara.CredentialEpoch || !rec.ExpiresAt.Equal(f.clock().Add(auth.DefaultSessionTTL)) {
		t.Fatalf("session %+v", rec)
	}
	if !strings.HasPrefix(sess.Token, string(auth.KindSession)) {
		t.Fatalf("token %q is not a session token", sess.Token)
	}
	_, err = f.svc.ConsumeTempLogin(ctx, tok, "", from("192.0.2.40"))
	wantErr(t, "a spent link", err, auth.ErrInvalidCredentials)
	if got := f.audit.recordedActions(); !containsAll(got, "core.templogin.mint", "core.templogin.consume") {
		t.Fatalf("recorded %v", got)
	}
	f.auditCarries(tok)
	f.auditCarries(sess.Token)
}

func containsAll(have []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			found = found || h == w
		}
		if !found {
			return false
		}
	}
	return true
}

// §3.2, SEC-4 (TestUsersManage_CannotTempLoginAdmin): minting needs
// core:users.manage through an interactive method and dominance; the target
// must be active and need nothing a plain link cannot give; a restricted
// target needs a reset link from an owner-verified actor.
func TestTempLogin_MintRules(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	tim := f.user("tim-drake", "gotham.detective")
	steph := f.user("stephanie-brown", "gotham.detective")
	_, gk := f.mint(f.session(gordon), gordon.ID, "core:users.manage", "gotham:patrol")
	mint := func(actor *auth.Principal, target auth.UserID) error {
		_, err := f.svc.MintTempLogin(ctx, actor, target)
		return err
	}
	reset := func(actor *auth.Principal, target auth.UserID) error {
		_, err := f.svc.MintResetLink(ctx, actor, target)
		return err
	}
	wantErr(t, "a peer", mint(f.session(steph), tim.ID), auth.ErrDenied)
	wantErr(t, "a manager's key", mint(f.keyPrincipal(gordon, gk), tim.ID), auth.ErrDenied)
	wantErr(t, "a manager over an admin", mint(f.session(gordon), bruce.ID), auth.ErrDominance)
	wantErr(t, "a reset of an admin by a manager", reset(f.session(gordon), bruce.ID), auth.ErrDominance)
	wantErr(t, "no actor", mint(nil, tim.ID), auth.ErrDenied)
	wantErr(t, "a missing user", mint(f.session(bruce), "u-nobody"), auth.ErrNotFound)
	system := f.edit(f.user("_system"), func(u *auth.User) { u.Status = auth.StatusSystem })
	wantErr(t, "the system user", mint(f.session(bruce), system.ID), auth.ErrBadInput)
	if err := f.svc.SetStatus(ctx, f.session(bruce), steph.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a disabled user", mint(f.session(bruce), steph.ID), auth.ErrInactiveUser)

	tim = f.edit(tim, func(u *auth.User) { u.PasswordResetRequired = true })
	wantErr(t, "a plain link for a reset-required user", mint(f.session(gordon), tim.ID), auth.ErrResetRequired)
	if err := reset(f.session(gordon), tim.ID); err != nil {
		t.Fatalf("a reset link for a reset-required user: %v", err)
	}
	tim = f.edit(f.reload(tim.ID), func(u *auth.User) { u.PasswordResetRequired, u.SignInRestricted = false, true })
	wantErr(t, "a plain link for a restricted user", mint(f.session(gordon), tim.ID), auth.ErrResetRequired)
	wantErr(t, "a reset link for a restricted user, no owner", reset(f.session(gordon), tim.ID), auth.ErrResetRequired)
	owner := f.session(gordon)
	owner.Assurance |= auth.AssuranceOwner
	if err := reset(owner, tim.ID); err != nil {
		t.Fatalf("an owner's reset link for a restricted user: %v", err)
	}
	if err := mint(f.session(gordon), gordon.ID); err != nil {
		t.Fatalf("a manager's link for itself: %v", err)
	}
}

// §4.12: a stored token is spent whatever the answer. Expired, epoch-moved,
// disabled and deleted users each refuse it, and it is gone afterwards.
func TestTempLogin_RefusalsSpendTheToken(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	jason := f.user("jason-todd", "gotham.detective")
	admin := f.session(bruce)
	consume := func(tok string) error {
		_, err := f.svc.ConsumeTempLogin(ctx, tok, "", from("192.0.2.41"))
		return err
	}
	spent := func(what, tok string, want error) {
		t.Helper()
		wantErr(t, what, consume(tok), want)
		wantErr(t, what+", again", consume(tok), auth.ErrInvalidCredentials)
	}

	late := f.mintTemp(admin, dick.ID, false)
	f.advance(auth.DefaultTempLoginTTL)
	spent("an expired link", late, auth.ErrExpired)
	f.setSettings(auth.Settings{TempLoginTTL: time.Hour})
	longer := f.mintTemp(admin, dick.ID, false)
	f.advance(30 * time.Minute)
	if err := consume(longer); err != nil {
		t.Fatalf("core.auth.temp_login.ttl: %v", err)
	}

	stale := f.mintTemp(admin, dick.ID, false)
	if err := f.svc.RevokeAllSessions(ctx, f.signedIn(dick), dick.ID, ""); err != nil {
		t.Fatal(err)
	}
	spent("a link minted before sign-out-everywhere", stale, auth.ErrStaleEpoch)

	gone := f.mintTemp(admin, jason.ID, false)
	if err := f.svc.SetStatus(ctx, admin, jason.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	spent("a disabled user's link", gone, auth.ErrInactiveUser)
	if err := f.svc.SetStatus(ctx, admin, jason.ID, auth.StatusActive); err != nil {
		t.Fatal(err)
	}
	deleted := f.mintTemp(admin, jason.ID, false)
	if err := f.svc.DeleteUser(ctx, admin, jason.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a deleted user's link", consume(deleted), auth.ErrInvalidCredentials)

	// A token behind the service's back: a user that is gone, and a
	// restriction-lifting token that is not a reset link (never minted).
	var orphan, forged auth.Token
	f.commit(func(b *storage.Batch) error {
		var err error
		exp := f.clock().Add(time.Hour)
		if orphan, err = f.st.IssueOneTime(b, &auth.OneTimeToken{Kind: auth.OneTimeTempLogin, UserID: "u-nobody", ExpiresAt: exp}); err != nil {
			return err
		}
		forged, err = f.st.IssueOneTime(b, &auth.OneTimeToken{
			Kind: auth.OneTimeTempLogin, UserID: dick.ID, CredentialEpoch: f.reload(dick.ID).CredentialEpoch, ClearsRestriction: true, ExpiresAt: exp,
		})
		return err
	})
	spent("an orphan link", string(orphan), auth.ErrInvalidCredentials)
	spent("a lifting link that is not a reset link", string(forged), auth.ErrInvalidCredentials)
}

// §4.9: a token that does not parse or names nothing is a failed credential
// check charged to the source; a spent budget refuses even a good token,
// which stays unspent.
func TestTempLogin_ThrottlesTheSource(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	f.setSettings(auth.Settings{Login: auth.ThrottleLimits{MaxFailuresPerSource: 2}})
	good := f.mintTemp(f.session(bruce), dick.ID, false)
	joker := from("192.0.2.66")
	tampered := good[:len(good)-1] + map[bool]string{true: "b", false: "a"}[strings.HasSuffix(good, "a")]
	for _, bad := range []string{"mct_joker", tampered} {
		_, err := f.svc.ConsumeTempLogin(ctx, bad, "", joker)
		wantErr(t, "a bad token", err, auth.ErrInvalidCredentials)
	}
	_, err := f.svc.ConsumeTempLogin(ctx, good, "", joker)
	wantErr(t, "a spent budget", err, auth.ErrThrottled)
	if _, err := f.svc.ConsumeTempLogin(ctx, good, "", from("192.0.2.67")); err != nil {
		t.Fatalf("the good token from another source: %v", err)
	}
}

// §4.13, §4.12: a reset link bumps the epoch when minted, so the user's
// sessions die at once; consuming it takes a new password (refused when
// missing or short, leaving it unspent), stores it with a second bump in the
// session's batch, clears PasswordResetRequired, and journals both lines.
func TestResetLink_SetsThePasswordAndKillsEveryOtherCredential(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	barbara := f.withPassword(f.user("barbara-gordon", "gotham.detective"), "batgirl-1967")
	old := f.signedIn(barbara)
	plain := f.mintTemp(f.session(gordon), barbara.ID, false)
	barbara = f.edit(barbara, func(u *auth.User) { u.PasswordResetRequired = true })

	tok := f.mintTemp(f.session(gordon), barbara.ID, true)
	if got := f.reload(barbara.ID).CredentialEpoch; got != barbara.CredentialEpoch+1 {
		t.Fatalf("minting a reset link left the epoch at %d", got)
	}
	wantErr(t, "a session from before the reset link", f.svc.Revalidate(ctx, old), auth.ErrStaleEpoch)
	_, err := f.svc.ConsumeTempLogin(ctx, plain, "", from("192.0.2.42"))
	wantErr(t, "a temp-login link from before the reset link", err, auth.ErrStaleEpoch)

	for what, pw := range map[string]string{"no password": "", "a short one": "oracle"} {
		_, err := f.svc.ConsumeTempLogin(ctx, tok, pw, from("192.0.2.42"))
		wantErr(t, what, err, auth.ErrBadInput)
	}
	sess, err := f.svc.ConsumeTempLogin(ctx, tok, "oracle-1988", from("192.0.2.42"))
	if err != nil {
		t.Fatalf("consume the reset link: %v", err)
	}
	after := f.reload(barbara.ID)
	if after.CredentialEpoch != barbara.CredentialEpoch+2 || after.PasswordResetRequired || !after.HasPassword {
		t.Fatalf("after the reset: %+v", after)
	}
	if got := f.sessionEpoch(sess.PublicID); got != after.CredentialEpoch {
		t.Fatalf("the reset's session is at epoch %d, want %d", got, after.CredentialEpoch)
	}
	if _, err := f.login("barbara-gordon", "oracle-1988", from("192.0.2.42")); err != nil {
		t.Fatalf("sign in with the new password: %v", err)
	}
	_, err = f.login("barbara-gordon", "batgirl-1967", from("192.0.2.43"))
	wantErr(t, "the old password", err, auth.ErrInvalidCredentials)
	if f.jrnl.count(journal.PasswordChanged) != 1 || f.jrnl.count(journal.CredentialEpochBumped) != 2 {
		t.Fatalf("journal %+v", f.jrnl.lines)
	}
}

// §4.14 (SEC4-12): only a reset link an owner-verified actor minted lifts
// SignInRestricted; it clears the restriction and the reset flag together.
func TestResetLink_LiftsARestrictionOnlyFromTheOwner(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	lucius := f.edit(f.user("lucius-fox", "gotham.detective"), func(u *auth.User) { u.SignInRestricted = true })
	owner := f.session(gordon)
	owner.Assurance |= auth.AssuranceOwner
	tok := f.mintTemp(owner, lucius.ID, true)
	if _, err := f.svc.ConsumeTempLogin(ctx, tok, "applied-sciences", from("192.0.2.44")); err != nil {
		t.Fatalf("consume the owner's reset link: %v", err)
	}
	if got := f.reload(lucius.ID); got.SignInRestricted || got.PasswordResetRequired {
		t.Fatalf("after the owner's reset: %+v", got)
	}
	if _, err := f.login("lucius-fox", "applied-sciences", from("192.0.2.44")); err != nil {
		t.Fatalf("sign in after the owner's reset: %v", err)
	}

	// A reset link a non-owner minted for an unrestricted user does not
	// lift a restriction set after it was minted.
	tok = f.mintTemp(f.session(gordon), lucius.ID, true)
	f.edit(f.reload(lucius.ID), func(u *auth.User) { u.SignInRestricted = true })
	_, err := f.svc.ConsumeTempLogin(ctx, tok, "wayne-tech-r-and-d", from("192.0.2.44"))
	wantErr(t, "a non-owner reset link after a restriction", err, auth.ErrResetRequired)
}
