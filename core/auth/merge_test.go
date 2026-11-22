// file: core/auth/merge_test.go
// version: 1.0.0
// guid: 7e98df33-3742-47a9-b04a-24c79e67a40e
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Admin.StageUserMerge (05-authn §3.1, §4.8; DATA4-9).

// keyRow is the merge plan row for key id: the stored bytes a preview read.
func (f *fx) keyRow(id string) auth.MergeRow {
	f.t.Helper()
	seen, err := f.st.Keyspace().Get(context.Background(), keys.Join(keys.Str("apikey"), keys.Str(id)))
	if err != nil {
		f.t.Fatalf("read apikey %s: %v", id, err)
	}
	return auth.MergeRow{Kind: auth.MergeAPIKey, ID: id, Seen: seen}
}

// planMerge is the plan Admin.PlanUserMerge gives actor for source into target.
func (f *fx) planMerge(actor *auth.Principal, source, target auth.UserID) auth.UserMergePlan {
	f.t.Helper()
	plan, _, err := f.svc.PlanUserMerge(auth.WithPrincipal(context.Background(), actor), source, target)
	if err != nil {
		f.t.Fatalf("PlanUserMerge(%s, %s): %v", source, target, err)
	}
	return plan
}

// rawPlan is a plan assembled by hand over the users' current versions, for
// the refusals a real preview would stop before it could return one.
func (f *fx) rawPlan(source, target auth.UserID, rows ...auth.MergeRow) auth.UserMergePlan {
	f.t.Helper()
	return auth.NewMergePlanForTest(source, target, f.reload(source).Version, f.reload(target).Version, rows...)
}

// currentPlan is a plan assembled by hand over the users' current versions and
// the source's current rows, for a refusal a real preview would stop before it
// could return a plan: a plan that is stale in no respect, so the refusal under
// test is the one reported (a stale plan is reported as ErrMergeConflict).
func (f *fx) currentPlan(source, target auth.UserID) auth.UserMergePlan {
	f.t.Helper()
	rows, err := f.st.MergeRows(context.Background(), source)
	if err != nil {
		f.t.Fatalf("MergeRows(%s): %v", source, err)
	}
	return f.rawPlan(source, target, rows...)
}

// signable gives u a password: the merge's no-sign-in rule refuses a target
// with no password and no linked sign-in.
func (f *fx) signable(u *auth.User) *auth.User {
	f.t.Helper()
	return f.withPassword(u, "the-batcave-1939")
}

// stageMerge stages plan as actor into a fresh batch and commits it when
// staging succeeded, then runs the merge's AfterCommit as a caller must. It
// returns the staging error, else the commit's.
func (f *fx) stageMerge(actor *auth.Principal, plan auth.UserMergePlan) error {
	f.t.Helper()
	ctx := context.Background()
	if actor != nil {
		ctx = auth.WithPrincipal(ctx, actor)
	}
	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	after, err := f.svc.StageUserMerge(ctx, b, plan)
	if err != nil {
		if after != nil {
			f.t.Errorf("StageUserMerge returned an AfterCommit with its error %v", err)
		}
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return err
	}
	after(ctx)
	return nil
}

// TestUserMergeDeletesSourceInvites (DATA4-9): a merge whose source has an
// open invite succeeds and deletes it, never re-owning it; another creator's
// invite stays. The same batch re-owns the plan's key, disables the source
// with its credential epoch bumped and journaled, and audits the deletion
// count.
func TestUserMergeDeletesSourceInvites(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", roleCommissioner)
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))

	sourceInvite, _ := f.invite(f.session(jason), detectiveInvite("harper-row"))
	_, kept := f.invite(f.session(bruce), detectiveInvite("cassandra-cain"))
	_, k := f.mint(f.session(jason), jason.ID, "gotham:patrol")
	epoch := f.reload(jason.ID).CredentialEpoch
	f.jrnl.mu.Lock()
	linesBefore := len(f.jrnl.lines)
	f.jrnl.mu.Unlock()

	plan := f.planMerge(f.signedIn(bruce), jason.ID, dick.ID)
	if err := f.stageMerge(f.signedIn(bruce), plan); err != nil {
		t.Fatalf("StageUserMerge: %v", err)
	}

	var open []string
	for v, err := range f.svc.Invites(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, v.ID)
	}
	if len(open) != 1 || open[0] != kept.ID {
		t.Fatalf("open invites after the merge = %v, want only %s", open, kept.ID)
	}
	if _, _, err := f.accept(sourceInvite, "red-hood-1988"); !errors.Is(err, auth.ErrInviteInvalid) {
		t.Fatalf("accepting the source's invite after the merge = %v, want ErrInviteInvalid", err)
	}
	if got := f.key(k.ID); got.Owner != dick.ID {
		t.Fatalf("key %s owner = %s, want %s", k.ID, got.Owner, dick.ID)
	}
	src := f.reload(jason.ID)
	if src.Status != auth.StatusDisabled || src.CredentialEpoch != epoch+1 {
		t.Fatalf("source after the merge: status %s epoch %d, want disabled at %d", src.Status, src.CredentialEpoch, epoch+1)
	}

	f.jrnl.mu.Lock()
	lines := append([]journal.Revocation(nil), f.jrnl.lines[linesBefore:]...)
	f.jrnl.mu.Unlock()
	kinds := map[journal.Kind]bool{}
	for _, l := range lines {
		if l.Subject.Kind == "user" && l.Subject.ID == string(jason.ID) && l.Epoch == epoch+1 {
			kinds[l.Kind] = true
		}
	}
	if !kinds[journal.UserDisabled] || !kinds[journal.CredentialEpochBumped] {
		t.Fatalf("journal lines for the merge = %+v, want user.disabled and credential_epoch.bumped at %d", lines, epoch+1)
	}

	f.audit.mu.Lock()
	last := f.audit.recorded[len(f.audit.recorded)-1]
	f.audit.mu.Unlock()
	if last.Action != "core.user.disable" || last.Outcome != audit.Success || last.Reason != "merged" ||
		last.Details["invites_deleted"] != 1 || last.Details["into"] != string(dick.ID) || last.Details["rows"] != 1 {
		t.Fatalf("merge audit = %+v", last)
	}
}

// TestUserMerge_ChangedRowFailsTheMerge (DATA10-9): a record changed after the
// plan read it is refused at staging (ErrMergeConflict) when it changed before
// StageUserMerge, and fails the commit with storage.ErrConflict when it
// changed between staging and the commit. Either way nothing of the merge
// lands: the source stays active with its invite.
func TestUserMerge_ChangedRowFailsTheMerge(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", roleCommissioner)
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))
	f.invite(f.session(jason), detectiveInvite("harper-row"))
	_, k := f.mint(f.session(jason), jason.ID, "gotham:patrol")
	plan := f.planMerge(f.signedIn(bruce), jason.ID, dick.ID)

	rename := func(name string) {
		renamed := f.key(k.ID)
		renamed.Name, renamed.Version = name, renamed.Version+1
		f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, renamed) })
	}
	rename("grappling hook")
	err := f.stageMerge(f.signedIn(bruce), plan)
	wantErr(t, "a merge staged over a changed key", err, auth.ErrMergeConflict)

	// Replanned over the changed key, then changed again between staging and
	// the commit: the batch's own Expect fails the commit.
	plan = f.planMerge(f.signedIn(bruce), jason.ID, dick.ID)
	ctx := auth.WithPrincipal(context.Background(), f.signedIn(bruce))
	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	if _, err := f.svc.StageUserMerge(ctx, b, plan); err != nil {
		t.Fatalf("StageUserMerge: %v", err)
	}
	rename("batarang")
	wantErr(t, "a commit over a key changed after staging", b.Commit(ctx, storage.Sync), storage.ErrConflict)

	if src := f.reload(jason.ID); src.Status != auth.StatusActive || src.MergedInto != "" {
		t.Fatalf("source after a failed merge: %+v", src)
	}
	if got := f.key(k.ID); got.Owner != jason.ID || got.Name != "batarang" {
		t.Fatalf("key after a failed merge: %+v", got)
	}
	n := 0
	for _, err := range f.svc.Invites(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("open invites after a failed merge = %d, want 1", n)
	}
}

// TestUserMerge_Gates: holding the handle grants nothing. The ctx principal
// needs core:users.manage through an interactive method, §4.8's owner rule,
// and dominance over both users; the plan needs two distinct users and the
// caller's batch; and the last-admin rule holds for the disabled source.
func TestUserMerge_Gates(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))
	plan := f.planMerge(f.signedIn(bruce), jason.ID, dick.ID)
	owner := func(p *auth.Principal) *auth.Principal { p.Assurance |= auth.AssuranceOwner; return p }

	wantErr(t, "no principal", f.stageMerge(nil, plan), auth.ErrDenied)
	_, k := f.mint(f.session(bruce), bruce.ID, "core:users.manage")
	wantErr(t, "an admin's key", f.stageMerge(f.keyPrincipal(bruce, k), plan), auth.ErrDenied)
	wantErr(t, "a user manager below admin, no owner gate", f.stageMerge(f.signedIn(gordon), plan), auth.ErrDenied)
	wantErr(t, "a user manager who is not an admin, dominating neither admin",
		f.stageMerge(owner(f.signedIn(gordon)), f.rawPlan(jason.ID, bruce.ID)), auth.ErrDominance)
	_, noBatch := f.svc.StageUserMerge(auth.WithPrincipal(context.Background(), f.signedIn(bruce)), nil, plan)
	wantErr(t, "no batch", noBatch, auth.ErrBadInput)
	// The sole admin, so a merge that went on would meet the last-admin rule.
	wantErr(t, "one user twice", f.stageMerge(f.signedIn(bruce), f.rawPlan(bruce.ID, bruce.ID)), auth.ErrBadInput)
	wantErr(t, "the zero plan", f.stageMerge(f.signedIn(bruce), auth.UserMergePlan{}), auth.ErrBadInput)
	alfred := f.edit(f.user("alfred-pennyworth"), func(u *auth.User) { u.Status = auth.StatusSystem })
	wantErr(t, "the system user", f.stageMerge(f.signedIn(bruce), f.rawPlan(alfred.ID, dick.ID)), auth.ErrBadInput)
	wantErr(t, "into the system user", f.stageMerge(f.signedIn(bruce), f.rawPlan(jason.ID, alfred.ID)), auth.ErrBadInput)
	// Core keeps the last-admin rule although the app cannot reach it: the books
	// side refuses a survivor that is not an admin (internal/usermerge plan.go),
	// and a sole-admin source merged into an admin survivor leaves that survivor
	// as the other holder. Here the survivor is not an admin, which only a caller
	// that skipped the books rule could ask for, and core refuses it on its own.
	wantErr(t, "the sole admin merging itself away",
		f.stageMerge(f.signedIn(bruce), f.currentPlan(bruce.ID, dick.ID)), auth.ErrLastAdmin)
	// PlanUserMerge runs the same refusals, so none of these returns a plan.
	for what, args := range map[string][2]auth.UserID{
		"the sole admin merging itself away": {bruce.ID, dick.ID}, "one user twice": {bruce.ID, bruce.ID},
		"the system user": {alfred.ID, dick.ID}, "into the system user": {jason.ID, alfred.ID},
	} {
		if _, _, err := f.svc.PlanUserMerge(auth.WithPrincipal(context.Background(), f.signedIn(bruce)), args[0], args[1]); err == nil {
			t.Errorf("PlanUserMerge(%s) returned no error", what)
		}
	}
	if _, _, err := f.svc.PlanUserMerge(context.Background(), jason.ID, dick.ID); !errors.Is(err, auth.ErrDenied) {
		t.Errorf("PlanUserMerge with no principal = %v, want ErrDenied", err)
	}
	if _, _, err := f.svc.PlanUserMerge(auth.WithPrincipal(context.Background(), f.signedIn(gordon)), jason.ID, dick.ID); !errors.Is(err, auth.ErrDenied) {
		t.Errorf("PlanUserMerge by a user manager below admin = %v, want ErrDenied", err)
	}

	required := true
	f.setSettings(auth.Settings{OwnerRequired: &required})
	wantErr(t, "an admin when the owner gate is required", f.stageMerge(f.signedIn(bruce), plan), auth.ErrDenied)
	if err := f.stageMerge(owner(f.signedIn(gordon)), plan); err != nil {
		t.Fatalf("the owner, a user manager dominating both users: %v", err)
	}
	if got := f.reload(jason.ID); got.Status != auth.StatusDisabled {
		t.Fatalf("source after the owner's merge: %s", got.Status)
	}
}

// TestUserMerge_OwnerGateDefault (§4.8, SEC4-14): unset, the owner gate is
// required exactly when an OwnerVerifier extension reports an owner set. An
// explicit false lifts it even then.
func TestUserMerge_OwnerGateDefault(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ownerSet bool
		setting  *bool
		want     error
	}{
		{name: "owner set", ownerSet: true, want: auth.ErrDenied},
		{name: "no owner set", ownerSet: false},
		{name: "owner set, gate turned off", ownerSet: true, setting: new(bool)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			bruce := f.user("bruce-wayne", rbac.RoleAdmin)
			jason := f.user("jason-todd", "gotham.detective")
			dick := f.signable(f.user("dick-grayson", "gotham.detective"))
			plan := f.planMerge(f.signedIn(bruce), jason.ID, dick.ID)
			f.setSettings(auth.Settings{OwnerRequired: tc.setting})
			svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
				Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
				Hasher: f.hasher, Settings: f.liveSettings,
				Extensions: []auth.Extension{{
					Authenticator: fakeExt{}, Order: 150,
					Spec:     auth.MethodSpec{Method: "ext:signal", OwnerVerifier: true},
					OwnerSet: func() bool { return tc.ownerSet },
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			f.svc = svc
			err = f.stageMerge(f.session(bruce), plan)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("an admin's merge = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestService_OwnerRequiredIsTheMergeRulesReading: the exported accessor stage
// 13 and the owner-guarded routes take answers exactly what the merge rule
// reads, per call, so a setting changed at runtime reaches all of them.
func TestService_OwnerRequiredIsTheMergeRulesReading(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ownerSet bool
		setting  *bool
		want     bool
	}{
		{name: "unset, no owner set", want: false},
		{name: "unset, owner set", ownerSet: true, want: true},
		{name: "false, owner set", ownerSet: true, setting: new(bool), want: false},
		{name: "true, no owner set", setting: func() *bool { v := true; return &v }(), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			f.setSettings(auth.Settings{OwnerRequired: tc.setting})
			svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
				Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
				Hasher: f.hasher, Settings: f.liveSettings,
				Extensions: []auth.Extension{{
					Authenticator: fakeExt{}, Order: 150,
					Spec:     auth.MethodSpec{Method: "ext:signal", OwnerVerifier: true},
					OwnerSet: func() bool { return tc.ownerSet },
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := svc.OwnerRequired(); got != tc.want {
				t.Fatalf("OwnerRequired() = %v, want %v", got, tc.want)
			}
			// Per call: flipping the live setting is seen by the next read.
			flipped := !tc.want
			f.setSettings(auth.Settings{OwnerRequired: &flipped})
			if got := svc.OwnerRequired(); got != flipped {
				t.Fatalf("after the setting changed, OwnerRequired() = %v, want %v", got, flipped)
			}
		})
	}
}

// TestOwnerRule_FallbackIsAnInteractiveAdmin (§4.8): without the owner gate,
// the rule falls back to core:admin through an interactive method; with it,
// only AssuranceOwner passes.
func TestOwnerRule_FallbackIsAnInteractiveAdmin(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	viaKey := f.session(bruce)
	viaKey.Method = auth.MethodAPIKey
	owner := f.session(gordon)
	owner.Assurance |= auth.AssuranceOwner
	for _, tc := range []struct {
		what     string
		p        *auth.Principal
		required bool
		want     bool
	}{
		{"an admin's session", f.session(bruce), false, true},
		{"an admin's key", viaKey, false, false},
		{"a user manager's session", f.session(gordon), false, false},
		{"the owner", owner, false, true},
		{"an admin's session, gate required", f.session(bruce), true, false},
		{"the owner, gate required", owner, true, true},
	} {
		required := tc.required
		f.setSettings(auth.Settings{OwnerRequired: &required})
		if got := f.svc.OwnerSatisfied(tc.p); got != tc.want {
			t.Errorf("%s: owner rule = %v, want %v", tc.what, got, tc.want)
		}
	}
}
