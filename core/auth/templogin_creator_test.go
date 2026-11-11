// file: core/auth/templogin_creator_test.go
// version: 1.0.0
// guid: 5d2a7c3e-9b14-4f60-8e21-3c7a9d0b6f45
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Creator parity for temp-login and reset links (05-authn §3.2, the
// temp-login twin of an invite's SEC3-2 re-check): a link is only as good as
// the account that minted it. The store-level cases change the creator behind
// the service's back, so they prove the consume-time re-check alone; the
// service-level cases prove the eager deletion that goes with it.

// linksCreatedBy counts the stored temp-login links id minted.
func (f *fx) linksCreatedBy(id auth.UserID) int {
	f.t.Helper()
	n := 0
	for tok, err := range f.st.ScanOneTime(context.Background(), auth.OneTimeTempLogin) {
		if err != nil {
			f.t.Fatal(err)
		}
		if tok.CreatedBy == id {
			n++
		}
	}
	return n
}

// lastConsume is the newest recorded core.templogin.consume event.
func (f *fx) lastConsume() audit.Event {
	f.t.Helper()
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	for i := len(f.audit.recorded) - 1; i >= 0; i-- {
		if e := f.audit.recorded[i]; e.Action == "core.templogin.consume" {
			return e
		}
	}
	f.t.Fatal("no core.templogin.consume event recorded")
	return audit.Event{}
}

// refusedForItsCreator consumes link and fails the test unless it was refused
// for its creator: ErrInvalidCredentials, audited denied with reason
// creator_changed, no session and no password stored for the target.
func (f *fx) refusedForItsCreator(what, link string, reset bool, target auth.UserID) {
	f.t.Helper()
	pw := ""
	if reset {
		pw = "gotham-after-dark-1"
	}
	before := f.reload(target)
	sess, err := f.svc.ConsumeTempLogin(context.Background(), link, pw, from("192.0.2.70"))
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		f.t.Fatalf("%s: err = %v, want %v", what, err, auth.ErrInvalidCredentials)
	}
	if sess != nil {
		f.t.Fatalf("%s: opened a session", what)
	}
	if e := f.lastConsume(); e.Outcome != audit.Denied || e.Reason != "creator_changed" {
		f.t.Fatalf("%s: audited %s/%q, want denied/creator_changed", what, e.Outcome, e.Reason)
	}
	if after := f.reload(target); after.HasPassword != before.HasPassword || after.CredentialEpoch != before.CredentialEpoch {
		f.t.Fatalf("%s: the target changed: %+v -> %+v", what, before, after)
	}
}

// TestTempLogin_RecheckAFormerCreatorAtConsume: a link whose creator changed
// after minting opens nothing and is spent. Probe 1 and 2 of the F4-02b
// review (a disabled creator's reset link set a password; a commissioner's
// reset link set the password of a user since promoted above it).
func TestTempLogin_RecheckAFormerCreatorAtConsume(t *testing.T) {
	cases := []struct {
		name   string
		change func(f *fx, creator, target *auth.User)
	}{
		{"creator disabled", func(f *fx, c, _ *auth.User) {
			f.edit(c, func(u *auth.User) { u.Status = auth.StatusDisabled })
		}},
		{"creator signed out everywhere or changed password", func(f *fx, c, _ *auth.User) {
			f.bumpEpoch(c.ID, c.CredentialEpoch+1)
		}},
		{"creator sign-in restricted", func(f *fx, c, _ *auth.User) {
			f.edit(c, func(u *auth.User) { u.SignInRestricted = true })
		}},
		{"creator lost core:users.manage", func(f *fx, c, _ *auth.User) {
			f.setRoles(c.ID, "gotham.detective")
		}},
		{"creator merged or deleted away", func(f *fx, c, _ *auth.User) {
			f.commit(func(b *storage.Batch) error { return f.st.DeleteUser(b, f.reload(c.ID)) })
		}},
		{"target promoted above the creator", func(f *fx, _, tgt *auth.User) {
			f.setRoles(tgt.ID, rbac.RoleAdmin)
		}},
	}
	for _, tc := range cases {
		for _, reset := range []bool{false, true} {
			name := tc.name + "/plain"
			if reset {
				name = tc.name + "/reset"
			}
			t.Run(name, func(t *testing.T) {
				f := newFx(t)
				f.user("bruce-wayne", rbac.RoleAdmin)
				gordon := f.user("jim-gordon", roleCommissioner)
				tim := f.user("tim-drake", "gotham.detective")
				link := f.mintTemp(f.session(gordon), tim.ID, reset)
				tim = f.reload(tim.ID) // a reset link bumped tim's epoch at mint
				tc.change(f, gordon, tim)
				f.refusedForItsCreator(tc.name, link, reset, tim.ID)
				// The refusal spent the link: a second try finds nothing.
				_, err := f.svc.ConsumeTempLogin(context.Background(), link, map[bool]string{true: "gotham-after-dark-2"}[reset], from("192.0.2.71"))
				wantErr(t, "a second try", err, auth.ErrInvalidCredentials)
				if f.linksCreatedBy(gordon.ID) != 0 {
					t.Fatal("the refused link is still stored")
				}
			})
		}
	}
}

// A token that records no creator (a pre-F4-02b row, or one written behind
// the service) is refused: the check fails closed.
func TestTempLogin_NoRecordedCreatorFailsClosed(t *testing.T) {
	f := newFx(t)
	tim := f.user("tim-drake", "gotham.detective")
	var tok auth.Token
	f.commit(func(b *storage.Batch) error {
		var err error
		tok, err = f.st.IssueOneTime(b, &auth.OneTimeToken{
			Kind: auth.OneTimeTempLogin, UserID: tim.ID, CredentialEpoch: tim.CredentialEpoch, ExpiresAt: f.clock().Add(time.Hour),
		})
		return err
	})
	f.refusedForItsCreator("a link with no creator", string(tok), false, tim.ID)
}

// A link that lifts SignInRestricted was minted under the owner gate
// (AssuranceOwner is a per-request attribute the store cannot replay): once
// core.auth.owner.required is turned off it is refused, with its creator
// unchanged, and the restriction stays.
func TestResetLink_LiftingARestrictionNeedsTheOwnerGateAtConsume(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	lucius := f.edit(f.user("lucius-fox", "gotham.detective"), func(u *auth.User) { u.SignInRestricted = true })
	required := true
	f.setSettings(auth.Settings{OwnerRequired: &required})
	owner := f.session(gordon)
	owner.Assurance |= auth.AssuranceOwner
	link := f.mintTemp(owner, lucius.ID, true)
	lucius = f.reload(lucius.ID)

	notRequired := false
	f.setSettings(auth.Settings{OwnerRequired: &notRequired})
	f.refusedForItsCreator("the owner gate removed", link, true, lucius.ID)
	if !f.reload(lucius.ID).SignInRestricted {
		t.Fatal("the refused link lifted the restriction")
	}

	// A plain reset link of the same creator is not owner-bound: it survives
	// the gate being off.
	plain := f.mintTemp(f.session(gordon), f.user("harper-row", "gotham.detective").ID, true)
	if _, err := f.svc.ConsumeTempLogin(ctx, plain, "gotham-after-dark-3", from("192.0.2.72")); err != nil {
		t.Fatalf("a plain reset link with the gate off: %v", err)
	}
}

// An administrator resetting itself is both creator and target: the link
// carries the creator's epoch after the bump the mint made, so it opens.
func TestResetLink_SelfResetKeepsItsOwnCreatorEpoch(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	link := f.mintTemp(f.session(bruce), bruce.ID, true)
	if _, err := f.svc.ConsumeTempLogin(context.Background(), link, "the-bat-signal-1", from("192.0.2.73")); err != nil {
		t.Fatalf("consume a self-reset link: %v", err)
	}
	if f.reload(bruce.ID).CredentialEpoch != bruce.CredentialEpoch+2 {
		t.Fatalf("epoch %d, want mint bump + consume bump", f.reload(bruce.ID).CredentialEpoch)
	}
}

// TestTempLogin_CreatorChangesDeleteItsLinksEagerly: the service operations
// that end a creator's right to mint delete its links in their own batch;
// another creator's links, and the user's own links when it keeps
// core:users.manage, stay.
func TestTempLogin_CreatorChangesDeleteItsLinksEagerly(t *testing.T) {
	ops := map[string]struct {
		run      func(f *fx, bruce, gordon *auth.User) error
		wantKept bool // gordon's links survive
	}{
		"disable": {run: func(f *fx, bruce, gordon *auth.User) error {
			return f.svc.SetStatus(context.Background(), f.session(bruce), gordon.ID, auth.StatusDisabled)
		}},
		"sign out everywhere": {run: func(f *fx, _, gordon *auth.User) error {
			return f.svc.RevokeAllSessions(context.Background(), f.signedIn(gordon), gordon.ID, "")
		}},
		"merged away": {run: func(f *fx, bruce, gordon *auth.User) error {
			return f.stageMerge(f.signedIn(bruce), auth.UserMergePlan{Source: gordon.ID, Target: f.reload("u-dick-grayson").ID})
		}},
		"loses core:users.manage": {run: func(f *fx, bruce, gordon *auth.User) error {
			return f.svc.SetRoles(context.Background(), f.session(bruce), gordon.ID, []rbac.RoleName{"gotham.detective"})
		}},
		"keeps core:users.manage": {wantKept: true, run: func(f *fx, bruce, gordon *auth.User) error {
			return f.svc.SetRoles(context.Background(), f.session(bruce), gordon.ID, []rbac.RoleName{roleCommissioner, "gotham.detective"})
		}},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			bruce := f.user("bruce-wayne", rbac.RoleAdmin)
			alfred := f.user("alfred-pennyworth", rbac.RoleAdmin)
			gordon := f.user("jim-gordon", roleCommissioner)
			f.user("dick-grayson", "gotham.detective")
			tim := f.user("tim-drake", "gotham.detective")
			jason := f.user("jason-todd", "gotham.detective")
			reset := f.mintTemp(f.session(gordon), tim.ID, true)
			plain := f.mintTemp(f.session(gordon), jason.ID, false)
			bystander := f.mintTemp(f.session(alfred), f.user("harper-row", "gotham.detective").ID, false)
			if err := op.run(f, bruce, f.reload(gordon.ID)); err != nil {
				t.Fatal(err)
			}
			want := 0
			if op.wantKept {
				want = 2
			}
			if got := f.linksCreatedBy(gordon.ID); got != want {
				t.Fatalf("gordon's stored links = %d, want %d", got, want)
			}
			if got := f.linksCreatedBy(alfred.ID); got != 1 {
				t.Fatalf("alfred's stored links = %d, want 1: another creator's link must stay", got)
			}
			ctx := context.Background()
			if _, err := f.svc.ConsumeTempLogin(ctx, bystander, "", from("192.0.2.74")); err != nil {
				t.Fatalf("the bystander's link: %v", err)
			}
			if op.wantKept {
				if _, err := f.svc.ConsumeTempLogin(ctx, plain, "", from("192.0.2.75")); err != nil {
					t.Fatalf("a link of a creator that kept users.manage: %v", err)
				}
				return
			}
			_, err := f.svc.ConsumeTempLogin(ctx, reset, "gotham-after-dark-4", from("192.0.2.76"))
			wantErr(t, "a former creator's reset link", err, auth.ErrInvalidCredentials)
			_, err = f.svc.ConsumeTempLogin(ctx, plain, "", from("192.0.2.77"))
			wantErr(t, "a former creator's temp-login link", err, auth.ErrInvalidCredentials)
		})
	}
}

// Probe 1 end to end, through the service: a disabled creator's links stay
// dead after the creator is enabled again, and a demotion afterwards voids
// links minted since.
func TestTempLogin_CreatorDisabledThenReenabledStaysDead(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	alfred := f.user("alfred-pennyworth", rbac.RoleAdmin)
	dick := f.user("dick-grayson", "gotham.detective")
	jason := f.user("jason-todd", "gotham.detective")
	reset := f.mintTemp(f.session(alfred), dick.ID, true)
	plain := f.mintTemp(f.session(alfred), jason.ID, false)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.svc.SetStatus(ctx, f.session(bruce), alfred.ID, auth.StatusDisabled))
	must(f.svc.SetStatus(ctx, f.session(bruce), alfred.ID, auth.StatusActive))
	_, err := f.svc.ConsumeTempLogin(ctx, reset, "gotham-after-dark-5", from("192.0.2.78"))
	wantErr(t, "a reset link across a disable and re-enable", err, auth.ErrInvalidCredentials)
	_, err = f.svc.ConsumeTempLogin(ctx, plain, "", from("192.0.2.79"))
	wantErr(t, "a temp-login link across a disable and re-enable", err, auth.ErrInvalidCredentials)
	if f.reload(dick.ID).HasPassword {
		t.Fatal("a dead reset link set a password")
	}

	// Minted after the re-enable, the next link is good until alfred is
	// demoted; the demotion deletes it.
	tim := f.user("tim-drake", "gotham.detective")
	fresh := f.mintTemp(f.session(f.reload(alfred.ID)), tim.ID, false)
	must(f.svc.SetRoles(ctx, f.session(bruce), alfred.ID, []rbac.RoleName{"gotham.detective"}))
	_, err = f.svc.ConsumeTempLogin(ctx, fresh, "", from("192.0.2.80"))
	wantErr(t, "a link of a demoted creator", err, auth.ErrInvalidCredentials)
}
