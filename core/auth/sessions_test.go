// file: core/auth/sessions_test.go
// version: 1.0.0
// guid: 7f584349-8aa0-429c-91e5-486755963259
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
)

// "Sign out everywhere" and stream revalidation (05-authn §3.2, §4.12).

// sessionGone fails unless the session publicID is no longer stored.
func (f *fx) sessionGone(publicID string) {
	f.t.Helper()
	if _, err := f.st.SessionByPublicID(context.Background(), publicID); err == nil {
		f.t.Fatalf("session %s is still stored", publicID)
	}
}

// §4.12: one batch bumps Bruce's epoch, deletes every other session of his,
// re-stamps the one he kept, and journals the bump. His API key, which is
// not epoch-bound, keeps working; Alfred's session is untouched.
func TestRevokeAllSessions_KeepsOnlyTheCallersSession(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", "gotham.detective")
	alfred := f.user("alfred-pennyworth", "gotham.detective")
	here := f.signedIn(bruce)
	cave, manor := f.signedIn(bruce), f.signedIn(bruce)
	butler := f.signedIn(alfred)
	_, belt := f.mint(f.session(bruce), bruce.ID, "gotham:patrol")

	if err := f.svc.RevokeAllSessions(ctx, here, bruce.ID, here.CredentialID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}
	after := f.reload(bruce.ID)
	if after.CredentialEpoch != bruce.CredentialEpoch+1 {
		t.Fatalf("epoch %d, want %d", after.CredentialEpoch, bruce.CredentialEpoch+1)
	}
	if got := f.sessionEpoch(here.CredentialID); got != after.CredentialEpoch {
		t.Fatalf("the kept session is at epoch %d, want %d", got, after.CredentialEpoch)
	}
	f.sessionGone(cave.CredentialID)
	f.sessionGone(manor.CredentialID)
	if err := f.svc.Revalidate(ctx, here); err != nil {
		t.Fatalf("the kept session's stream: %v", err)
	}
	if err := f.svc.Revalidate(ctx, f.keyPrincipal(after, belt)); err != nil {
		t.Fatalf("Bruce's key after signing out everywhere: %v", err)
	}
	if err := f.svc.Revalidate(ctx, butler); err != nil {
		t.Fatalf("Alfred's session: %v", err)
	}
	if n := f.jrnl.count(journal.CredentialEpochBumped); n != 1 {
		t.Fatalf("journaled %d epoch bumps, want 1: %+v", n, f.jrnl.lines)
	}
	f.audit.mu.Lock()
	recs := slices.Clone(f.audit.recorded)
	f.audit.mu.Unlock()
	last := recs[len(recs)-1]
	if last.Action != "core.session.revoke" || last.Target.ID != string(bruce.ID) ||
		last.Details["all"] != true || last.Details["revoked"] != 2 || last.Details["kept"] != here.CredentialID {
		t.Fatalf("audited %+v", last)
	}

	// No except: every session goes.
	if err := f.svc.RevokeAllSessions(ctx, f.session(after), bruce.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.sessionGone(here.CredentialID)
	wantErr(t, "the old stream", f.svc.Revalidate(ctx, here), auth.ErrRevoked)
}

// §4.12: except keeps only a live session of the user at the user's current
// epoch. An epoch-stale session, which a password change killed, is deleted,
// never re-stamped back to life; so is an expired one. Another user's
// session is refused and nothing is written.
func TestRevokeAllSessions_ExceptNeverRevivesOrCrossesUsers(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick-grayson", "gotham.detective")
	jason := f.user("jason-todd", "gotham.detective")
	dead := f.signedIn(dick)
	dick = f.edit(dick, func(u *auth.User) { u.CredentialEpoch++ }) // a password change elsewhere
	me := f.signedIn(dick)

	wantErr(t, "another user's session", f.svc.RevokeAllSessions(ctx, me, dick.ID, f.signedIn(jason).CredentialID), auth.ErrBadInput)
	if got := f.reload(dick.ID); got.Version != dick.Version {
		t.Fatalf("a refused sign-out wrote version %d", got.Version)
	}

	if err := f.svc.RevokeAllSessions(ctx, me, dick.ID, dead.CredentialID); err != nil {
		t.Fatal(err)
	}
	f.sessionGone(dead.CredentialID)
	f.sessionGone(me.CredentialID)

	dick = f.reload(dick.ID)
	old := f.signedIn(dick)
	f.advance(auth.DefaultSessionTTL)
	if err := f.svc.RevokeAllSessions(ctx, f.session(dick), dick.ID, old.CredentialID); err != nil {
		t.Fatal(err)
	}
	f.sessionGone(old.CredentialID)

	// A gone except keeps nothing and refuses nothing.
	if err := f.svc.RevokeAllSessions(ctx, f.session(f.reload(dick.ID)), dick.ID, old.CredentialID); err != nil {
		t.Fatalf("a gone except: %v", err)
	}
}

// §3.2: the epoch is credential state, so an API key may not sign anyone
// out, not even its owner; another user's sessions need core:users.manage
// and dominance; _system and a missing user are refused.
func TestRevokeAllSessions_ActorRules(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	tim := f.user("tim-drake", "gotham.detective")
	steph := f.user("stephanie-brown", "gotham.detective")
	_, k := f.mint(f.session(tim), tim.ID, "gotham:patrol")

	wantErr(t, "a key, its own owner", f.svc.RevokeAllSessions(ctx, f.keyPrincipal(tim, k), tim.ID, ""), auth.ErrDenied)
	wantErr(t, "a peer", f.svc.RevokeAllSessions(ctx, f.session(steph), tim.ID, ""), auth.ErrDenied)
	wantErr(t, "a manager over an admin", f.svc.RevokeAllSessions(ctx, f.session(gordon), bruce.ID, ""), auth.ErrDominance)
	wantErr(t, "no actor", f.svc.RevokeAllSessions(ctx, nil, tim.ID, ""), auth.ErrDenied)
	if err := f.svc.RevokeAllSessions(ctx, f.session(gordon), tim.ID, ""); err != nil {
		t.Fatalf("a manager over a detective: %v", err)
	}
	if got := f.reload(tim.ID).CredentialEpoch; got != tim.CredentialEpoch+1 {
		t.Fatalf("Tim's epoch %d", got)
	}
	system := f.edit(f.user("_system"), func(u *auth.User) { u.Status = auth.StatusSystem })
	wantErr(t, "the system user", f.svc.RevokeAllSessions(ctx, f.session(bruce), system.ID, ""), auth.ErrBadInput)
	wantErr(t, "a missing user", f.svc.RevokeAllSessions(ctx, f.session(bruce), "u-nobody", ""), auth.ErrNotFound)
}

// §3.2: a long-lived stream's principal stands while its user is active and
// unrestricted and its credential is alive. A session follows its own
// record (expiry, deletion, epoch); a key follows its own status and not the
// epoch; any other method follows the epoch it was built at.
func TestRevalidate_FollowsTheCredentialAndTheUser(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	barbara := f.withPassword(f.user("barbara-gordon", "gotham.detective"), "batgirl-1967")
	manager := f.session(gordon)
	stream := f.signedIn(barbara)
	_, k := f.mint(f.session(barbara), barbara.ID, "gotham:patrol")
	keyed := f.keyPrincipal(barbara, k)
	ext := &auth.Principal{User: barbara, Method: "ext:oracle", Perms: stream.Perms}
	stands := func(what string, p *auth.Principal) {
		t.Helper()
		if err := f.svc.Revalidate(ctx, p); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	stands("a fresh session", stream)
	stands("a fresh key", keyed)
	stands("a fresh extension principal", ext)

	// Her own password change re-stamps her session: its stream stands; the
	// key is not epoch-bound; the extension principal's epoch is gone.
	if err := f.svc.ChangeOwnPassword(ctx, stream, "batgirl-1967", "oracle-1988"); err != nil {
		t.Fatal(err)
	}
	stands("the session that changed the password", stream)
	stands("the key after a password change", keyed)
	wantErr(t, "an extension principal after a password change", f.svc.Revalidate(ctx, ext), auth.ErrStaleEpoch)

	// Disabled: everything falls; re-enabled: the key stands again, the
	// session (two bumps behind) does not.
	if err := f.svc.SetStatus(ctx, manager, barbara.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a disabled user's session", f.svc.Revalidate(ctx, stream), auth.ErrInactiveUser)
	wantErr(t, "a disabled user's key", f.svc.Revalidate(ctx, keyed), auth.ErrInactiveUser)
	if err := f.svc.SetStatus(ctx, manager, barbara.ID, auth.StatusActive); err != nil {
		t.Fatal(err)
	}
	stands("the key after a re-enable", keyed)
	wantErr(t, "the session after a re-enable", f.svc.Revalidate(ctx, stream), auth.ErrStaleEpoch)

	// Restricted by a restore: refused whatever the credential.
	barbara = f.edit(barbara, func(u *auth.User) { u.SignInRestricted = true })
	wantErr(t, "a restricted user's key", f.svc.Revalidate(ctx, keyed), auth.ErrResetRequired)
	barbara = f.edit(barbara, func(u *auth.User) { u.SignInRestricted = false })

	// The key's own status and expiry; a key ID of another owner.
	if err := f.svc.RevokeAPIKey(ctx, f.session(barbara), k.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a revoked key", f.svc.Revalidate(ctx, keyed), auth.ErrRevoked)
	_, gk := f.mint(manager, gordon.ID, "gotham:patrol")
	forged := f.keyPrincipal(barbara, k)
	forged.CredentialID = gk.ID
	wantErr(t, "another owner's key", f.svc.Revalidate(ctx, forged), auth.ErrRevoked)

	// A session's expiry, its deletion and another user's session.
	fresh := f.signedIn(barbara)
	f.advance(auth.DefaultSessionTTL)
	wantErr(t, "an expired session", f.svc.Revalidate(ctx, fresh), auth.ErrExpired)
	borrowed := f.signedIn(barbara)
	borrowed.CredentialID = f.signedIn(gordon).CredentialID
	wantErr(t, "another user's session", f.svc.Revalidate(ctx, borrowed), auth.ErrRevoked)
	gone := f.signedIn(barbara)
	if err := f.svc.RevokeSession(ctx, gone, gone.CredentialID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a revoked session", f.svc.Revalidate(ctx, gone), auth.ErrRevoked)

	// The user deleted; no principal at all.
	if err := f.svc.DeleteUser(ctx, manager, barbara.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a deleted user", f.svc.Revalidate(ctx, keyed), auth.ErrRevoked)
	wantErr(t, "no principal", f.svc.Revalidate(ctx, nil), auth.ErrInvalidCredentials)
	wantErr(t, "no user", f.svc.Revalidate(ctx, &auth.Principal{Method: auth.MethodSession}), auth.ErrInvalidCredentials)
}
