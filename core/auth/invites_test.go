// file: core/auth/invites_test.go
// version: 1.0.0
// guid: 418f2c5b-2c61-42e5-a704-932003de2e96
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Invites (05-authn §4.13, SEC3-2; HEAD's handlers/user.go).

// invite creates an invite through actor and returns its token.
func (f *fx) invite(actor *auth.Principal, spec auth.InviteSpec) (string, auth.InviteView) {
	f.t.Helper()
	link, view, err := f.svc.CreateInvite(context.Background(), actor, spec)
	if err != nil {
		f.t.Fatalf("CreateInvite %+v: %v", spec, err)
	}
	tok, ok := strings.CutPrefix(link, "/invite#token=")
	if !ok || auth.ParseCredential(auth.KindInvite, tok) != nil {
		f.t.Fatalf("link %q is not an invite fragment link", link)
	}
	return tok, view
}

func (f *fx) accept(tok, pw string) (*auth.User, *auth.IssuedSession, error) {
	return f.svc.AcceptInvite(context.Background(), auth.AcceptInviteInput{Token: tok, Password: pw, Client: from("192.0.2.50")})
}

func detectiveInvite(name string) auth.InviteSpec {
	return auth.InviteSpec{Username: name, Role: "gotham.detective"}
}

// §4.13: an invite names the account its creator chose; accepting it with a
// password creates that account (e-mail unverified) and a delegated session
// in one batch, once. The token appears only in the link.
func TestInvite_CreatesTheAccountItNames(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	tok, view := f.invite(f.session(gordon), auth.InviteSpec{Username: " Cassandra-Cain ", Email: " Cass@Gotham.example ", Role: "gotham.detective"})
	if view.Username != "Cassandra-Cain" || view.Email != "cass@gotham.example" || view.Role != "gotham.detective" ||
		view.CreatedBy != gordon.ID || !view.ExpiresAt.Equal(f.clock().Add(auth.DefaultInviteTTL)) || view.ID == "" {
		t.Fatalf("view %+v", view)
	}
	looked, err := f.svc.LookupInvite(ctx, tok, from("192.0.2.50"))
	if err != nil || looked != view {
		t.Fatalf("LookupInvite = %+v, %v; want %+v", looked, err, view)
	}
	var listed []auth.InviteView
	for v, err := range f.svc.Invites(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, v)
	}
	if len(listed) != 1 || listed[0] != view {
		t.Fatalf("Invites = %+v", listed)
	}

	u, sess, err := f.accept(tok, "orphan-of-cain")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	stored := f.reload(u.ID)
	if stored.Username != "Cassandra-Cain" || stored.Email != "cass@gotham.example" || stored.EmailVerified ||
		!stored.HasPassword || stored.Status != auth.StatusActive || len(stored.Roles) != 1 || stored.Roles[0] != "gotham.detective" {
		t.Fatalf("account %+v", stored)
	}
	rec, err := f.st.SessionByPublicID(ctx, sess.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UserID != u.ID || rec.Origin != auth.OriginInvite || !rec.ExpiresAt.Equal(f.clock().Add(auth.DefaultRememberTTL)) {
		t.Fatalf("session %+v", rec)
	}
	if _, err := f.login("cassandra-cain", "orphan-of-cain", from("192.0.2.51")); err != nil {
		t.Fatalf("sign in as the new account: %v", err)
	}
	_, _, err = f.accept(tok, "orphan-of-cain")
	wantErr(t, "a spent invite", err, auth.ErrInviteInvalid)
	_, err = f.svc.LookupInvite(ctx, tok, from("192.0.2.50"))
	wantErr(t, "lookup of a spent invite", err, auth.ErrInviteInvalid)
	if got := f.audit.recordedActions(); !containsAll(got, "core.invite.create", "core.invite.accept") {
		t.Fatalf("recorded %v", got)
	}
	f.auditCarries(tok)
	f.auditCarries(sess.Token)
}

// §4.13: creating needs core:users.manage through an interactive method and
// CanAssign over the role; the username must be free of users and open
// invites; the e-mail is one plain address; the lifetime is the spec's or
// core.auth.invite.ttl.
func TestInvite_CreateRules(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	steph := f.user("stephanie-brown", "gotham.detective")
	f.user("Tim-Drake", "gotham.detective")
	_, gk := f.mint(f.session(gordon), gordon.ID, "core:users.manage", "gotham:patrol")
	create := func(actor *auth.Principal, spec auth.InviteSpec) error {
		_, _, err := f.svc.CreateInvite(ctx, actor, spec)
		return err
	}
	manager := f.session(gordon)
	wantErr(t, "a peer", create(f.session(steph), detectiveInvite("harper-row")), auth.ErrDenied)
	wantErr(t, "a manager's key", create(f.keyPrincipal(gordon, gk), detectiveInvite("harper-row")), auth.ErrDenied)
	wantErr(t, "an admin invite from a manager", create(manager, auth.InviteSpec{Username: "harper-row", Role: rbac.RoleAdmin}), auth.ErrScopeEscalation)
	wantErr(t, "no username", create(manager, detectiveInvite("  ")), auth.ErrBadInput)
	wantErr(t, "no role", create(manager, auth.InviteSpec{Username: "harper-row"}), auth.ErrBadInput)
	wantErr(t, "a negative lifetime", create(manager, auth.InviteSpec{Username: "harper-row", Role: "gotham.detective", TTL: -time.Hour}), auth.ErrBadInput)
	for _, bad := range []string{"harper@gotham.example\r\nBcc: joker@arkham.example", "Harper Row <harper@gotham.example>", "not-an-address"} {
		wantErr(t, "e-mail "+bad, create(manager, auth.InviteSpec{Username: "harper-row", Role: "gotham.detective", Email: bad}), auth.ErrBadInput)
	}
	wantErr(t, "a user's name", create(manager, detectiveInvite("tim-drake")), auth.ErrUserExists)
	f.invite(manager, auth.InviteSpec{Username: "Harper-Row", Role: "gotham.detective", TTL: time.Hour})
	wantErr(t, "an open invite's name", create(manager, detectiveInvite("harper-row")), auth.ErrUserExists)
	f.advance(time.Hour)
	_, v := f.invite(manager, detectiveInvite("harper-row"))
	if !v.ExpiresAt.Equal(f.clock().Add(auth.DefaultInviteTTL)) {
		t.Fatalf("an expired invite's name is free, but the new one expires at %v", v.ExpiresAt)
	}
	f.setSettings(auth.Settings{InviteTTL: 24 * time.Hour})
	if _, v = f.invite(manager, detectiveInvite("duke-thomas")); !v.ExpiresAt.Equal(f.clock().Add(24 * time.Hour)) {
		t.Fatalf("core.auth.invite.ttl: expires %v", v.ExpiresAt)
	}
}

// §4.13: delete by public ID; lookup is throttled per source, charges only
// a token that names nothing, and spends nothing; a short password leaves the
// invite unspent.
func TestInvite_DeleteLookupAndThrottle(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	steph := f.user("stephanie-brown", "gotham.detective")
	manager := f.session(gordon)
	tok, v := f.invite(manager, detectiveInvite("harper-row"))
	wantErr(t, "a peer deletes", f.svc.DeleteInvite(ctx, f.session(steph), v.ID), auth.ErrDenied)
	if err := f.svc.DeleteInvite(ctx, manager, v.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.LookupInvite(ctx, tok, from("192.0.2.52"))
	wantErr(t, "a deleted invite", err, auth.ErrInviteInvalid)
	wantErr(t, "a second delete", f.svc.DeleteInvite(ctx, manager, v.ID), auth.ErrNotFound)

	tok, _ = f.invite(manager, detectiveInvite("duke-thomas"))
	_, _, err = f.accept(tok, "short")
	wantErr(t, "a short password", err, auth.ErrBadInput)
	if _, err := f.svc.LookupInvite(ctx, tok, from("192.0.2.52")); err != nil {
		t.Fatalf("a refused short password spent the invite: %v", err)
	}

	f.setSettings(auth.Settings{Login: auth.ThrottleLimits{MaxFailuresPerSource: 2}})
	joker := from("192.0.2.66")
	for _, bad := range []string{"mci_joker", strings.Replace(tok, "mci_", "mct_", 1)} {
		_, err := f.svc.LookupInvite(ctx, bad, joker)
		wantErr(t, "a bad token", err, auth.ErrInviteInvalid)
	}
	_, err = f.svc.LookupInvite(ctx, tok, joker)
	wantErr(t, "a spent budget", err, auth.ErrThrottled)
	_, _, err = f.svc.AcceptInvite(ctx, auth.AcceptInviteInput{Token: tok, Password: "signal-man", Client: joker})
	wantErr(t, "an accept from a spent budget", err, auth.ErrThrottled)
	if _, _, err := f.accept(tok, "signal-man"); err != nil {
		t.Fatalf("the invite from another source: %v", err)
	}
	f.advance(auth.DefaultInviteTTL)
	late, _ := f.invite(manager, detectiveInvite("carrie-kelley"))
	f.advance(auth.DefaultInviteTTL)
	_, err = f.svc.LookupInvite(ctx, late, from("192.0.2.53"))
	wantErr(t, "an expired invite", err, auth.ErrInviteInvalid)
}

// TestInviteAcceptRechecksCanAssign (SEC3-2): accepting re-runs CanAssign
// for the creator over the role, and refuses, audited core.invite.accept
// denied, when it fails, when the creator no longer holds core:users.manage
// (a demotion bumps no epoch, §4.12) or no longer exists. An invite also
// dies with its creator's credential epoch, its expiry and its username
// being taken. Every refusal spends it and creates nothing.
func TestInviteAcceptRechecksCanAssign(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	bullock := f.user("harvey-bullock", roleCommissioner)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	refused := func(what, tok, name string, outcome audit.Outcome) {
		t.Helper()
		_, _, err := f.accept(tok, "gotham-city-1939")
		wantErr(t, what, err, auth.ErrInviteInvalid)
		_, _, err = f.accept(tok, "gotham-city-1939")
		wantErr(t, what+", again", err, auth.ErrInviteInvalid)
		if _, err := f.svc.UserByUsername(ctx, name); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("%s: %s exists or the read failed: %v", what, name, err)
		}
		f.audit.mu.Lock()
		last := f.audit.recorded[len(f.audit.recorded)-1]
		f.audit.mu.Unlock()
		if last.Action != "core.invite.accept" || last.Outcome != outcome || last.Reason != "invite_invalid" {
			t.Fatalf("%s: audited %+v", what, last)
		}
	}

	demoted, _ := f.invite(f.session(gordon), detectiveInvite("harper-row"))
	f.setRoles(gordon.ID, "relay-worker")
	refused("the creator was demoted", demoted, "harper-row", audit.Denied)

	// Demoted to the invited role itself: CanAssign still passes, since the
	// creator holds every scope of gotham.detective, but making the invite
	// needed core:users.manage, which it no longer holds.
	lucius := f.user("lucius-fox", roleCommissioner)
	unmanaged, _ := f.invite(f.session(lucius), detectiveInvite("selina-kyle"))
	f.setRoles(lucius.ID, "gotham.detective")
	refused("the creator lost core:users.manage", unmanaged, "selina-kyle", audit.Denied)

	orphan, _ := f.invite(f.session(bullock), detectiveInvite("duke-thomas"))
	f.commit(func(b *storage.Batch) error { return f.st.DeleteUser(b, f.reload(bullock.ID)) })
	refused("the creator is gone", orphan, "duke-thomas", audit.Denied)

	dick := f.user("dick-grayson", roleCommissioner)
	moved, _ := f.invite(f.session(dick), detectiveInvite("carrie-kelley"))
	if err := f.svc.RevokeAllSessions(ctx, f.signedIn(dick), dick.ID, ""); err != nil {
		t.Fatal(err)
	}
	refused("the creator signed out everywhere", moved, "carrie-kelley", audit.Failure)

	dick = f.reload(dick.ID)
	asleep, _ := f.invite(f.session(dick), detectiveInvite("cullen-row"))
	f.edit(dick, func(u *auth.User) { u.Status = auth.StatusDisabled })
	refused("the creator is disabled", asleep, "cullen-row", audit.Failure)

	late, _ := f.invite(f.session(bruce), detectiveInvite("jean-paul-valley"))
	f.advance(auth.DefaultInviteTTL)
	refused("an expired invite", late, "jean-paul-valley", audit.Failure)

	taken, _ := f.invite(f.session(bruce), detectiveInvite("kate-kane"))
	if _, err := f.svc.CreateUser(ctx, f.session(bruce), auth.NewUser{Username: "Kate-Kane", Roles: []rbac.RoleName{"gotham.detective"}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.accept(taken, "red-hair-1939")
	wantErr(t, "a taken username", err, auth.ErrUserExists)
}

// TestInviteRedeemedOnce (§7): two accepts of one invite yield one account.
// First the exact race (an accept staged and held while another commits,
// whose commit then conflicts and finds the invite spent), then concurrent
// accepts inside a synctest bubble.
func TestInviteRedeemedOnce(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	tok, _ := f.invite(f.session(bruce), detectiveInvite("harper-row"))
	held, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.audit.mu.Lock()
	f.audit.hold = func(e audit.Event) {
		if e.Action == "core.invite.accept" && calls.Add(1) == 1 {
			close(held)
			<-release
		}
	}
	f.audit.mu.Unlock()
	first := make(chan error, 1)
	go func() {
		_, _, err := f.accept(tok, "first-accept")
		first <- err
	}()
	<-held
	if _, _, err := f.accept(tok, "second-accept"); err != nil {
		t.Fatalf("the accept that committed first: %v", err)
	}
	close(release)
	wantErr(t, "the held accept", <-first, auth.ErrInviteInvalid)
	f.audit.mu.Lock()
	f.audit.hold = nil
	f.audit.mu.Unlock()
	if _, err := f.login("harper-row", "second-accept", from("192.0.2.54")); err != nil {
		t.Fatalf("the winning account: %v", err)
	}

	synctest.Test(t, func(t *testing.T) {
		f := newFx(t)
		bruce := f.user("bruce-wayne", rbac.RoleAdmin)
		tok, _ := f.invite(f.session(bruce), detectiveInvite("duke-thomas"))
		var wg sync.WaitGroup
		var won, lost atomic.Int32
		for range 8 {
			wg.Go(func() {
				_, _, err := f.accept(tok, "signal-1987")
				switch {
				case err == nil:
					won.Add(1)
				case errors.Is(err, auth.ErrInviteInvalid):
					lost.Add(1)
				default:
					t.Errorf("an accept: %v", err)
				}
			})
		}
		wg.Wait()
		if won.Load() != 1 || lost.Load() != 7 {
			t.Fatalf("%d accepts won, %d lost", won.Load(), lost.Load())
		}
	})
}
