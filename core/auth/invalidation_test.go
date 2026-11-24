// file: core/auth/invalidation_test.go
// version: 0.1.0
// guid: 1c7f5a28-94e3-4b06-a2d9-e58b03c6f714
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Batman names only: the Batcave's sessions and keys.

// invEvent is one Deps.OnInvalidated call.
type invEvent struct{ User, Cred, Reason string }

func (e invEvent) String() string { return fmt.Sprintf("%s/%q/%s", e.User, e.Cred, e.Reason) }

// invalFake records Deps.OnInvalidated's calls.
type invalFake struct {
	mu     sync.Mutex
	events []invEvent
}

// callback is the Deps.OnInvalidated the fixture builds the service with;
// nil when f is nil, so a test can build a service with no callback.
func (f *invalFake) callback() func(ctx context.Context, userID, credentialID, reason string) {
	if f == nil {
		return nil
	}
	return func(_ context.Context, userID, credentialID, reason string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, invEvent{userID, credentialID, reason})
	}
}

// take returns the calls since the last take and forgets them.
func (f *invalFake) take() []invEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.events
	f.events = nil
	return out
}

// wantEvents fails unless got holds exactly want, in any order.
func wantEvents(t *testing.T, what string, got []invEvent, want ...invEvent) {
	t.Helper()
	cmp := func(a, b invEvent) int {
		return cmpStr(a.User+"\x00"+a.Cred+"\x00"+a.Reason, b.User+"\x00"+b.Cred+"\x00"+b.Reason)
	}
	g, w := slices.Clone(got), slices.Clone(want)
	slices.SortFunc(g, cmp)
	slices.SortFunc(w, cmp)
	if !slices.Equal(g, w) {
		t.Fatalf("%s: published %v, want %v", what, g, w)
	}
}

func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// standsSession reports whether a request presenting session publicID of u
// would still stand: a fresh principal from the stored user, revalidated.
func (f *fx) standsSession(u auth.UserID, publicID string) bool {
	f.t.Helper()
	cur, err := f.st.GetUser(context.Background(), u)
	if err != nil {
		return false
	}
	p, err := f.svc.PrincipalFor(context.Background(), cur, auth.MethodSession, nil, cur.CredentialEpoch)
	if err != nil {
		return false
	}
	p.CredentialID = publicID
	return f.svc.Revalidate(context.Background(), p) == nil
}

// standsKey reports whether a request presenting key id of u would still
// stand.
func (f *fx) standsKey(u auth.UserID, id string) bool {
	f.t.Helper()
	cur, err := f.st.GetUser(context.Background(), u)
	if err != nil {
		return false
	}
	k, err := f.st.APIKeyByID(context.Background(), id)
	if err != nil {
		return false
	}
	p, err := f.svc.PrincipalFor(context.Background(), cur, auth.MethodAPIKey, k.Scopes, cur.CredentialEpoch)
	if err != nil {
		return false
	}
	p.CredentialID = k.ID
	return f.svc.Revalidate(context.Background(), p) == nil
}

// wantRevoked fails unless every named session is dead and every survivor
// stands: each event names only what its write really revoked.
func (f *fx) wantSessions(t *testing.T, what string, u auth.UserID, dead, alive []string) {
	t.Helper()
	for _, id := range dead {
		if f.standsSession(u, id) {
			t.Errorf("%s: session %s was published as revoked but still stands", what, id)
		}
	}
	for _, id := range alive {
		if !f.standsSession(u, id) {
			t.Errorf("%s: session %s survives the write but does not stand", what, id)
		}
	}
}

// TestInvalidation_KeyRevokeAndDisableNameTheKey: revoking or disabling a key
// publishes that key, with its owner and the journal kind; the other key
// still stands; re-enabling a key publishes nothing.
func TestInvalidation_KeyRevokeAndDisableNameTheKey(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	lucius := f.user("lucius-fox", "gotham.detective")
	actor := f.signedIn(lucius)
	_, belt := f.mint(actor, lucius.ID, "gotham:patrol")
	_, cowl := f.mint(actor, lucius.ID, "gotham:patrol")
	f.inval.take()

	if err := f.svc.RevokeAPIKey(ctx, actor, belt.ID); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "revoke", f.inval.take(), invEvent{string(lucius.ID), belt.ID, string(journal.APIKeyRevoked)})
	if f.standsKey(lucius.ID, belt.ID) || !f.standsKey(lucius.ID, cowl.ID) {
		t.Fatalf("after the revoke: belt stands %v, cowl stands %v; want false, true", f.standsKey(lucius.ID, belt.ID), f.standsKey(lucius.ID, cowl.ID))
	}

	if err := f.svc.SetAPIKeyStatus(ctx, actor, cowl.ID, auth.KeyDisabled); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "disable", f.inval.take(), invEvent{string(lucius.ID), cowl.ID, string(journal.APIKeyDisabled)})
	if f.standsKey(lucius.ID, cowl.ID) {
		t.Fatal("the disabled key still stands")
	}

	if err := f.svc.SetAPIKeyStatus(ctx, actor, cowl.ID, auth.KeyActive); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "re-enable", f.inval.take())
}

// TestInvalidation_SessionRevokeNamesTheSession: revoking one session
// publishes that session only; the user's other session still stands.
func TestInvalidation_SessionRevokeNamesTheSession(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	one, two := f.signedIn(dick), f.signedIn(dick)
	f.inval.take()

	if err := f.svc.RevokeSession(context.Background(), one, one.CredentialID); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "session revoke", f.inval.take(), invEvent{string(dick.ID), one.CredentialID, string(journal.SessionRevoked)})
	f.wantSessions(t, "session revoke", dick.ID, []string{one.CredentialID}, []string{two.CredentialID})
}

// TestInvalidation_SignOutEverywhereNamesEachSessionButTheKept: an epoch bump
// is never user-wide: signing out everywhere publishes every other session,
// not the kept one and not the user's API key, both of which still stand.
func TestInvalidation_SignOutEverywhereNamesEachSessionButTheKept(t *testing.T) {
	f := newFx(t)
	tim := f.user("tim-drake", "gotham.detective")
	kept, two, three := f.signedIn(tim), f.signedIn(tim), f.signedIn(tim)
	_, k := f.mint(kept, tim.ID, "gotham:patrol")
	f.inval.take()

	if err := f.svc.RevokeAllSessions(context.Background(), kept, tim.ID, kept.CredentialID); err != nil {
		t.Fatal(err)
	}
	bump := string(journal.CredentialEpochBumped)
	wantEvents(t, "sign out everywhere", f.inval.take(),
		invEvent{string(tim.ID), two.CredentialID, bump}, invEvent{string(tim.ID), three.CredentialID, bump})
	f.wantSessions(t, "sign out everywhere", tim.ID, []string{two.CredentialID, three.CredentialID}, []string{kept.CredentialID})
	if !f.standsKey(tim.ID, k.ID) {
		t.Error("the API key does not stand after an epoch bump; keys are not epoch-bound")
	}
}

// TestInvalidation_OwnPasswordChangeKeepsThePresentingSession: a self-service
// password change publishes the user's other sessions, never the session
// that presented the change (re-stamped) nor an API key.
func TestInvalidation_OwnPasswordChangeKeepsThePresentingSession(t *testing.T) {
	f := newFx(t)
	barbara := f.withPassword(f.user("barbara-gordon", "gotham.detective"), "batgirl-1967")
	actor, other := f.signedIn(barbara), f.signedIn(barbara)
	_, k := f.mint(actor, barbara.ID, "gotham:patrol")
	f.inval.take()

	if err := f.svc.ChangeOwnPassword(context.Background(), actor, "batgirl-1967", "oracle-1988-x"); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "own password change", f.inval.take(), invEvent{string(barbara.ID), other.CredentialID, string(journal.PasswordChanged)})
	f.wantSessions(t, "own password change", barbara.ID, []string{other.CredentialID}, []string{actor.CredentialID})
	if !f.standsKey(barbara.ID, k.ID) {
		t.Error("the API key does not stand after a password change")
	}
}

// TestInvalidation_ResetLinkMintAndConsume: minting a reset link bumps the
// epoch and publishes every session of the target (the actor's are another
// user's and untouched); consuming it publishes the stored sessions again
// but never the session the consume itself opens, which stands.
func TestInvalidation_ResetLinkMintAndConsume(t *testing.T) {
	f := newFx(t)
	gordon := f.user("jim-gordon", roleCommissioner)
	jason := f.user("jason-todd", "gotham.detective")
	one, two := f.signedIn(jason), f.signedIn(jason)
	f.inval.take()

	tok := f.mintTemp(f.session(gordon), jason.ID, true)
	bump := string(journal.CredentialEpochBumped)
	wantEvents(t, "reset link minted", f.inval.take(),
		invEvent{string(jason.ID), one.CredentialID, bump}, invEvent{string(jason.ID), two.CredentialID, bump})
	f.wantSessions(t, "reset link minted", jason.ID, []string{one.CredentialID, two.CredentialID}, nil)

	sess, err := f.svc.ConsumeTempLogin(context.Background(), tok, "red-hood-2005", from("192.0.2.42"))
	if err != nil {
		t.Fatal(err)
	}
	pw := string(journal.PasswordChanged)
	wantEvents(t, "reset link consumed", f.inval.take(),
		invEvent{string(jason.ID), one.CredentialID, pw}, invEvent{string(jason.ID), two.CredentialID, pw})
	f.wantSessions(t, "reset link consumed", jason.ID, nil, []string{sess.PublicID})
}

// TestInvalidation_DisableAndDeleteAreUserWide: disabling a user publishes
// the whole user (no credential); re-enabling publishes nothing; deleting
// publishes the whole user.
func TestInvalidation_DisableAndDeleteAreUserWide(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	admin := f.signedIn(bruce)
	selina := f.user("selina-kyle", "gotham.detective")
	s := f.signedIn(selina)
	f.inval.take()

	if err := f.svc.SetStatus(ctx, admin, selina.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "disable", f.inval.take(), invEvent{string(selina.ID), "", string(journal.UserDisabled)})
	f.wantSessions(t, "disable", selina.ID, []string{s.CredentialID}, nil)

	if err := f.svc.SetStatus(ctx, admin, selina.ID, auth.StatusActive); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "re-enable", f.inval.take())

	if err := f.svc.DeleteUser(ctx, admin, selina.ID); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "delete", f.inval.take(), invEvent{string(selina.ID), "", string(journal.UserDeleted)})
}

// TestInvalidation_RoleChangePublishesNothing: replacing a user's roles is a
// change of rights, not a credential revocation: no event (its journal
// line, role.unassigned, notwithstanding).
func TestInvalidation_RoleChangePublishesNothing(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	harvey := f.user("harvey-dent", "gotham.detective", roleCommissioner)
	f.signedIn(harvey)
	f.inval.take()
	if err := f.svc.SetRoles(context.Background(), f.signedIn(bruce), harvey.ID, []rbac.RoleName{"gotham.detective"}); err != nil {
		t.Fatal(err)
	}
	if f.jrnl.count(journal.RoleUnassigned) == 0 {
		t.Fatal("the role change journaled no role.unassigned line; the test does not test what it says")
	}
	wantEvents(t, "role change", f.inval.take())
}

// TestInvalidation_MergeAfterCommit: StageUserMerge's AfterCommit publishes
// the source user-wide once the caller's batch has committed, and nothing
// when the caller's commit never happened or failed, even when the caller
// runs it anyway.
func TestInvalidation_MergeAfterCommit(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))
	ctx := auth.WithPrincipal(context.Background(), f.signedIn(bruce))
	f.inval.take()

	t.Run("not committed", func(t *testing.T) {
		b := f.st.NewBatch()
		after, err := f.svc.StageUserMerge(ctx, b, f.planMerge(f.signedIn(bruce), jason.ID, dick.ID))
		if err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
		after(ctx)
		wantEvents(t, "a merge whose batch was closed uncommitted", f.inval.take())
	})
	t.Run("commit conflicted", func(t *testing.T) {
		b := f.st.NewBatch()
		defer func() { _ = b.Close() }()
		after, err := f.svc.StageUserMerge(ctx, b, f.planMerge(f.signedIn(bruce), jason.ID, dick.ID))
		if err != nil {
			t.Fatal(err)
		}
		f.setRoles(jason.ID, "gotham.detective") // a write behind the plan's back
		if err := b.Commit(ctx, storage.Sync); err == nil {
			t.Fatal("the commit did not conflict; the test does not test what it says")
		}
		after(ctx)
		wantEvents(t, "a merge whose commit failed", f.inval.take())
	})
	t.Run("committed", func(t *testing.T) {
		if err := f.stageMerge(f.signedIn(bruce), f.planMerge(f.signedIn(bruce), jason.ID, dick.ID)); err != nil {
			t.Fatal(err)
		}
		wantEvents(t, "a committed merge", f.inval.take(), invEvent{string(jason.ID), "", string(journal.UserDisabled)})
	})
	// A merge that never committed, while another write moved the source's
	// epoch to the merge's own bumped value: only the source's status (still
	// active) tells the step the merge did not land.
	t.Run("not committed, epoch moved by another write", func(t *testing.T) {
		steph := f.user("stephanie-brown", "gotham.detective")
		b := f.st.NewBatch()
		after, err := f.svc.StageUserMerge(ctx, b, f.planMerge(f.signedIn(bruce), steph.ID, dick.ID))
		if err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
		own := f.signedIn(steph)
		if err := f.svc.RevokeAllSessions(context.Background(), own, steph.ID, ""); err != nil {
			t.Fatal(err)
		}
		if got := f.reload(steph.ID); got.CredentialEpoch != steph.CredentialEpoch+1 || got.Status != auth.StatusActive {
			t.Fatalf("source after sign-out everywhere: epoch %d status %s, want %d active", got.CredentialEpoch, got.Status, steph.CredentialEpoch+1)
		}
		f.inval.take()
		after(ctx)
		wantEvents(t, "an uncommitted merge after another epoch bump", f.inval.take())
	})
	// A source that was already disabled reads as disabled whether or not
	// the merge committed: only the bumped epoch tells the step it did.
	t.Run("disabled source, not committed", func(t *testing.T) {
		cass := f.edit(f.user("cassandra-cain", "gotham.detective"), func(u *auth.User) { u.Status = auth.StatusDisabled })
		b := f.st.NewBatch()
		after, err := f.svc.StageUserMerge(ctx, b, f.planMerge(f.signedIn(bruce), cass.ID, dick.ID))
		if err != nil {
			t.Fatalf("staging a merge of a disabled source: %v", err)
		}
		_ = b.Close()
		after(ctx)
		wantEvents(t, "a disabled source's uncommitted merge", f.inval.take())
	})
}

// TestInvalidation_FailedAttemptsPublishNothing: a write whose every attempt
// conflicts (ErrContended) publishes nothing; a write whose first attempt
// conflicts and second commits publishes the second attempt's events once.
// The conflict comes from a write between the stage (events recorded) and
// the commit.
func TestInvalidation_FailedAttemptsPublishNothing(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	admin := f.signedIn(bruce)
	selina := f.user("selina-kyle", "gotham.detective")
	f.inval.take()

	f.audit.hold = func(e audit.Event) {
		if e.Target.ID == string(selina.ID) {
			f.setRoles(selina.ID, "gotham.detective")
		}
	}
	err := f.svc.SetStatus(ctx, admin, selina.ID, auth.StatusDisabled)
	wantErr(t, "every attempt conflicting", err, auth.ErrContended)
	wantEvents(t, "every attempt conflicting", f.inval.take())

	once := true
	f.audit.hold = func(e audit.Event) {
		if e.Target.ID == string(selina.ID) && once {
			once = false
			f.setRoles(selina.ID, "gotham.detective")
		}
	}
	if err := f.svc.SetStatus(ctx, admin, selina.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	f.audit.hold = nil
	wantEvents(t, "a conflict, then a commit", f.inval.take(), invEvent{string(selina.ID), "", string(journal.UserDisabled)})
}

// TestInvalidation_NoCallbackPublishesNothing: a service built with no
// OnInvalidated revokes as before.
func TestInvalidation_NoCallbackPublishesNothing(t *testing.T) {
	f := newFx(t)
	f.inval = nil
	f.rebuild()
	dick := f.user("dick-grayson", "gotham.detective")
	s := f.signedIn(dick)
	if err := f.svc.RevokeSession(context.Background(), s, s.CredentialID); err != nil {
		t.Fatal(err)
	}
	if f.standsSession(dick.ID, s.CredentialID) {
		t.Fatal("the revoked session still stands")
	}
}
