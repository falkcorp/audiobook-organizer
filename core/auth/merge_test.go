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

// stageMerge stages plan as actor into a fresh batch and commits it when
// staging succeeded. It returns the staging error, else the commit's.
func (f *fx) stageMerge(actor *auth.Principal, plan auth.UserMergePlan) error {
	f.t.Helper()
	ctx := context.Background()
	if actor != nil {
		ctx = auth.WithPrincipal(ctx, actor)
	}
	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := f.svc.StageUserMerge(ctx, b, plan); err != nil {
		return err
	}
	return b.Commit(ctx, storage.Sync)
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
	dick := f.user("dick-grayson", "gotham.detective")

	sourceInvite, _ := f.invite(f.session(jason), detectiveInvite("harper-row"))
	_, kept := f.invite(f.session(bruce), detectiveInvite("cassandra-cain"))
	_, k := f.mint(f.session(jason), jason.ID, "gotham:patrol")
	epoch := f.reload(jason.ID).CredentialEpoch
	f.jrnl.mu.Lock()
	linesBefore := len(f.jrnl.lines)
	f.jrnl.mu.Unlock()

	plan := auth.UserMergePlan{Source: jason.ID, Target: dick.ID, Rows: []auth.MergeRow{f.keyRow(k.ID)}}
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

// TestUserMerge_ChangedRowFailsTheCommit (DATA10-9): a record changed after
// the plan read it fails the merge's commit with storage.ErrConflict, and
// nothing of the merge lands: the source stays active with its invite.
func TestUserMerge_ChangedRowFailsTheCommit(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", roleCommissioner)
	dick := f.user("dick-grayson", "gotham.detective")
	f.invite(f.session(jason), detectiveInvite("harper-row"))
	_, k := f.mint(f.session(jason), jason.ID, "gotham:patrol")
	plan := auth.UserMergePlan{Source: jason.ID, Target: dick.ID, Rows: []auth.MergeRow{f.keyRow(k.ID)}}

	renamed := f.key(k.ID)
	renamed.Name, renamed.Version = "grappling hook", renamed.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, renamed) })

	err := f.stageMerge(f.signedIn(bruce), plan)
	wantErr(t, "a merge over a changed key", err, storage.ErrConflict)
	if src := f.reload(jason.ID); src.Status != auth.StatusActive {
		t.Fatalf("source after a failed merge: %s", src.Status)
	}
	if got := f.key(k.ID); got.Owner != jason.ID || got.Name != "grappling hook" {
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
	dick := f.user("dick-grayson", "gotham.detective")
	plan := auth.UserMergePlan{Source: jason.ID, Target: dick.ID}
	owner := func(p *auth.Principal) *auth.Principal { p.Assurance |= auth.AssuranceOwner; return p }

	wantErr(t, "no principal", f.stageMerge(nil, plan), auth.ErrDenied)
	_, k := f.mint(f.session(bruce), bruce.ID, "core:users.manage")
	wantErr(t, "an admin's key", f.stageMerge(f.keyPrincipal(bruce, k), plan), auth.ErrDenied)
	wantErr(t, "a user manager below admin, no owner gate", f.stageMerge(f.signedIn(gordon), plan), auth.ErrDenied)
	wantErr(t, "a user manager who is not an admin, dominating neither admin",
		f.stageMerge(owner(f.signedIn(gordon)), auth.UserMergePlan{Source: jason.ID, Target: bruce.ID}), auth.ErrDominance)
	wantErr(t, "no batch", f.svc.StageUserMerge(auth.WithPrincipal(context.Background(), f.signedIn(bruce)), nil, plan), auth.ErrBadInput)
	// The sole admin, so a merge that went on would meet the last-admin rule.
	wantErr(t, "one user twice", f.stageMerge(f.signedIn(bruce), auth.UserMergePlan{Source: bruce.ID, Target: bruce.ID}), auth.ErrBadInput)
	alfred := f.edit(f.user("alfred-pennyworth"), func(u *auth.User) { u.Status = auth.StatusSystem })
	wantErr(t, "the system user", f.stageMerge(f.signedIn(bruce), auth.UserMergePlan{Source: alfred.ID, Target: dick.ID}), auth.ErrBadInput)
	wantErr(t, "into the system user", f.stageMerge(f.signedIn(bruce), auth.UserMergePlan{Source: jason.ID, Target: alfred.ID}), auth.ErrBadInput)
	wantErr(t, "the sole admin merging itself away",
		f.stageMerge(f.signedIn(bruce), auth.UserMergePlan{Source: bruce.ID, Target: dick.ID}), auth.ErrLastAdmin)

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
			dick := f.user("dick-grayson", "gotham.detective")
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
			err = f.stageMerge(f.session(bruce), auth.UserMergePlan{Source: jason.ID, Target: dick.ID})
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("an admin's merge = %v, want %v", err, tc.want)
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
