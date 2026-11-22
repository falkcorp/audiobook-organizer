// file: core/auth/unmerge_test.go
// version: 0.1.0
// guid: 5a27c9e4-0d3b-4f81-b6a2-e18d7c4f3b90
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Admin.PlanUserUnmerge and Admin.StageUserUnmerge (F4-08 B10p): the reversal
// of StageUserMerge. Batman names only; addresses on example.test.

const (
	umProvider = "wayne-sso"
	umSubject  = "jason-todd-subject"
	umIdentity = umProvider + "/" + umSubject
)

// umScene is a finished merge: Jason (the source) folded into Dick (the
// target) by Bruce, with the record a merge journal would hold.
type umScene struct {
	f                    *fx
	bruce, jason, dick   *auth.User
	key                  *auth.APIKey
	rec                  auth.UnmergeRecord
	srcBefore, tgtBefore *auth.User
	sum                  auth.MergeSummary
	receipt              auth.MergeReceipt
	// afterMerge is the source as the merge left it.
	afterMerge *auth.User
}

type umOpts struct {
	srcRoles  []rbac.RoleName
	srcStatus auth.UserStatus
	// noIdentity leaves the sign-in out, so only a key moves.
	noIdentity bool
}

func newUmScene(t *testing.T, o umOpts) *umScene {
	t.Helper()
	f := newFx(t)
	s := &umScene{f: f}
	s.bruce = f.user("bruce-wayne", rbac.RoleAdmin)
	roles := o.srcRoles
	if roles == nil {
		roles = []rbac.RoleName{roleCommissioner, "gotham.detective"}
	}
	s.jason = f.userWithEmail("jason-todd", "jason.todd@example.test", true, roles...)
	s.dick = f.signable(f.user("dick-grayson", "gotham.detective"))
	_, s.key = f.mint(f.session(s.jason), s.jason.ID, "gotham:patrol")
	if !o.noIdentity {
		f.linkIdentity(s.jason, umProvider, umSubject)
	}
	if o.srcStatus != "" && o.srcStatus != auth.StatusActive {
		s.jason = f.edit(s.jason, func(u *auth.User) { u.Status = o.srcStatus })
	}
	s.srcBefore, s.tgtBefore = f.reload(s.jason.ID), f.reload(s.dick.ID)

	ctx := auth.WithPrincipal(context.Background(), f.signedIn(s.bruce))
	plan, sum, err := f.svc.PlanUserMerge(ctx, s.jason.ID, s.dick.ID)
	if err != nil {
		t.Fatalf("PlanUserMerge: %v", err)
	}
	s.sum, s.receipt = sum, plan.Receipt()
	if err := f.stageMerge(f.signedIn(s.bruce), plan); err != nil {
		t.Fatalf("StageUserMerge: %v", err)
	}
	s.afterMerge = f.reload(s.jason.ID)
	s.rec = auth.UnmergeRecord{
		Source: s.jason.ID, Target: s.dick.ID,
		SourceVersionAfter:        s.srcBefore.Version + 1,
		SourceStatusBefore:        s.srcBefore.Status,
		SourceEmailBefore:         s.srcBefore.Email,
		SourceEmailVerifiedBefore: s.srcBefore.EmailVerified,
		TargetEmailBefore:         s.tgtBefore.Email,
		TargetEmailVerifiedBefore: s.tgtBefore.EmailVerified,
		EmailOutcome:              sum.Email,
		RolesAdded:                sum.RolesTransferred,
		APIKeys:                   s.receipt.APIKeyVersions,
		Identities:                s.receipt.IdentityIDs,
	}
	return s
}

func (s *umScene) ctx(p *auth.Principal) context.Context {
	return auth.WithPrincipal(context.Background(), p)
}

// plan is PlanUserUnmerge as p over rec.
func (s *umScene) plan(p *auth.Principal, rec auth.UnmergeRecord) (auth.UserUnmergePlan, auth.UnmergeSummary, error) {
	return s.f.svc.PlanUserUnmerge(s.ctx(p), rec)
}

// stage stages plan as p into a fresh batch, commits it when staging
// succeeded, and runs the AfterCommit as a caller must.
func (s *umScene) stage(p *auth.Principal, plan auth.UserUnmergePlan) error {
	s.f.t.Helper()
	ctx := context.Background()
	if p != nil {
		ctx = auth.WithPrincipal(ctx, p)
	}
	b := s.f.st.NewBatch()
	defer func() { _ = b.Close() }()
	after, err := s.f.svc.StageUserUnmerge(ctx, b, plan)
	if err != nil {
		if after != nil {
			s.f.t.Errorf("StageUserUnmerge returned an AfterCommit with its error %v", err)
		}
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return err
	}
	after(ctx)
	return nil
}

// unmerge plans and stages rec as p.
func (s *umScene) unmerge(p *auth.Principal, rec auth.UnmergeRecord) error {
	s.f.t.Helper()
	plan, _, err := s.plan(p, rec)
	if err != nil {
		return err
	}
	return s.stage(p, plan)
}

// current is a plan over the users' current versions and no rows, for a
// refusal a real preview would stop before it could return a plan.
func (s *umScene) current(rec auth.UnmergeRecord) auth.UserUnmergePlan {
	version := func(id auth.UserID) int64 {
		if u, err := s.f.st.GetUser(context.Background(), id); err == nil {
			return u.Version
		}
		return 0
	}
	return auth.NewUnmergePlanForTest(rec, version(rec.Source), version(rec.Target))
}

// snapshot is everything an unmerge could write, as text: both users, every key
// row and every sign-in owner, and how many journal and audit lines exist.
func (s *umScene) snapshot() string {
	s.f.t.Helper()
	var out string
	for _, id := range []auth.UserID{s.jason.ID, s.dick.ID} {
		if u, err := s.f.st.GetUser(context.Background(), id); err == nil {
			out += fmt.Sprintf("user %+v\n", *u)
		} else {
			out += fmt.Sprintf("user %s gone\n", id)
		}
	}
	for id := range s.rec.APIKeys {
		seen, err := s.f.st.Keyspace().Get(context.Background(), keys.Join(keys.Str("apikey"), keys.Str(id)))
		out += fmt.Sprintf("key %s %q %v\n", id, seen, err)
	}
	for id, err := range s.f.st.ScanIdentities(context.Background(), "") {
		if err != nil {
			s.f.t.Fatal(err)
		}
		out += fmt.Sprintf("identity %s/%s -> %s\n", id.Provider, id.Subject, id.UserID)
	}
	s.f.jrnl.mu.Lock()
	out += fmt.Sprintf("journal %d\n", len(s.f.jrnl.lines))
	s.f.jrnl.mu.Unlock()
	s.f.audit.mu.Lock()
	out += fmt.Sprintf("audit %d\n", len(s.f.audit.recorded))
	s.f.audit.mu.Unlock()
	return out
}

// lines are the journal lines of kind about user written since the merge (the
// merge's own lines are at an epoch no later than afterMerge's).
func (s *umScene) lines(kind journal.Kind, user auth.UserID) []journal.Revocation {
	s.f.jrnl.mu.Lock()
	defer s.f.jrnl.mu.Unlock()
	var out []journal.Revocation
	for _, l := range s.f.jrnl.lines {
		if l.Kind == kind && l.Subject.Kind == "user" && l.Subject.ID == string(user) &&
			(kind == journal.RoleUnassigned || l.Epoch > s.afterMerge.CredentialEpoch || user == s.dick.ID) {
			out = append(out, l)
		}
	}
	return out
}

// TestUserUnmerge_RestoresTheSource: a merge, then its undo. The source is
// active again with its address, its key and its sign-in; the target loses the
// role the merge gave it and keeps the one it held before, gives the address
// back, and has its sessions ended; both credential epochs move, journaled; the
// action is audited and the lifecycle hears the source come back.
func TestUserUnmerge_RestoresTheSource(t *testing.T) {
	s := newUmScene(t, umOpts{})
	f := s.f
	rec := f.lifecycle()
	cave := f.signedIn(f.reload(s.dick.ID)) // a session of the target, opened after the merge
	f.inval.take()
	tgtMerged := f.reload(s.dick.ID)
	if !slices.Contains(tgtMerged.Roles, roleCommissioner) || tgtMerged.Email != s.srcBefore.Email {
		t.Fatalf("the merge did not give the target the role and the address: %+v", tgtMerged)
	}

	actor := f.signedIn(s.bruce)
	plan, sum, err := s.plan(actor, s.rec)
	if err != nil {
		t.Fatalf("PlanUserUnmerge: %v", err)
	}
	if sum.Source != s.jason.ID || sum.Target != s.dick.ID || sum.SourceStatus != auth.StatusActive ||
		!slices.Equal(sum.RolesRemoved, []rbac.RoleName{roleCommissioner}) || !sum.SourceEmailRestored || !sum.TargetEmailReverted ||
		!slices.Equal(sum.APIKeys, []string{s.key.ID}) || !slices.Equal(sum.IdentityProviders, []string{umProvider}) || !sum.TargetSessionsEnded {
		t.Fatalf("PlanUserUnmerge summary = %+v", sum)
	}
	if err := s.stage(actor, plan); err != nil {
		t.Fatalf("StageUserUnmerge: %v", err)
	}

	src := f.reload(s.jason.ID)
	if src.Status != auth.StatusActive || src.MergedInto != "" || !src.MergedAt.IsZero() {
		t.Fatalf("source after the unmerge: status %s merged_into %q merged_at %v", src.Status, src.MergedInto, src.MergedAt)
	}
	if src.Email != s.srcBefore.Email || !src.EmailVerified {
		t.Fatalf("source address after the unmerge = %q verified %v", src.Email, src.EmailVerified)
	}
	if src.Version != s.afterMerge.Version+1 {
		t.Fatalf("source version = %d, want %d", src.Version, s.afterMerge.Version+1)
	}
	// The epoch must move PAST the merge's own bump: the merge already
	// bumped it, so a session opened before the merge is dead anyway; what
	// the unmerge's bump kills is anything issued while the account was
	// disabled.
	if src.CredentialEpoch != s.afterMerge.CredentialEpoch+1 {
		t.Fatalf("source epoch = %d, want the merge's %d plus one", src.CredentialEpoch, s.afterMerge.CredentialEpoch)
	}
	if got := f.key(s.key.ID); got.Owner != s.jason.ID || got.Version != s.receipt.APIKeyVersions[s.key.ID]+1 {
		t.Fatalf("key after the unmerge: owner %s version %d", got.Owner, got.Version)
	}
	if id, err := f.st.IdentityBySubject(context.Background(), umProvider, umSubject); err != nil || id.UserID != s.jason.ID {
		t.Fatalf("sign-in after the unmerge: %+v, %v", id, err)
	}

	tgt := f.reload(s.dick.ID)
	if !slices.Equal(tgt.Roles, []rbac.RoleName{"gotham.detective"}) {
		t.Fatalf("target roles after the unmerge = %v, want only the role it held before the merge", tgt.Roles)
	}
	if tgt.Email != "" || tgt.EmailVerified {
		t.Fatalf("target address after the unmerge = %q verified %v, want what it was before the merge", tgt.Email, tgt.EmailVerified)
	}
	if tgt.CredentialEpoch != tgtMerged.CredentialEpoch+1 {
		t.Fatalf("target epoch = %d, want %d: a sign-in moved back", tgt.CredentialEpoch, tgtMerged.CredentialEpoch+1)
	}
	if got := f.sessionEpoch(cave.CredentialID); got >= tgt.CredentialEpoch {
		t.Fatalf("the target's session is at epoch %d, the target at %d: the session still stands", got, tgt.CredentialEpoch)
	}
	wantEvents(t, "the unmerge's invalidations", f.inval.take(), invEvent{string(s.dick.ID), cave.CredentialID, string(journal.CredentialEpochBumped)})

	if l := s.lines(journal.UserEnabled, s.jason.ID); len(l) != 1 || l[0].Epoch != src.CredentialEpoch {
		t.Fatalf("user.enabled lines for the source = %+v, want one at epoch %d", l, src.CredentialEpoch)
	}
	if l := s.lines(journal.CredentialEpochBumped, s.jason.ID); len(l) != 1 || l[0].Epoch != src.CredentialEpoch {
		t.Fatalf("credential_epoch.bumped lines for the source = %+v, want one at epoch %d", l, src.CredentialEpoch)
	}
	if l := s.lines(journal.CredentialEpochBumped, s.dick.ID); len(l) != 1 || l[0].Epoch != tgt.CredentialEpoch {
		t.Fatalf("credential_epoch.bumped lines for the target = %+v, want one at epoch %d", l, tgt.CredentialEpoch)
	}
	if l := s.lines(journal.RoleUnassigned, s.dick.ID); len(l) != 1 || l[0].Object.ID != string(roleCommissioner) {
		t.Fatalf("role.unassigned lines for the target = %+v, want the commissioner role", l)
	}

	acts := f.audit.recordedActions()
	if last := acts[len(acts)-1]; last != "core.user.unmerge" {
		t.Fatalf("last audit action = %s, want core.user.unmerge (all: %v)", last, acts)
	}
	f.audit.mu.Lock()
	e := f.audit.recorded[len(f.audit.recorded)-1]
	f.audit.mu.Unlock()
	if e.Outcome != audit.Success || e.Target.ID != string(s.jason.ID) || e.Details["from"] != string(s.dick.ID) {
		t.Fatalf("unmerge audit = %+v", e)
	}

	rec.mu.Lock()
	status := slices.Clone(rec.status)
	rec.mu.Unlock()
	if len(status) != 1 || status[0].UserID != s.jason.ID || status[0].From != auth.StatusDisabled || status[0].To != auth.StatusActive {
		t.Fatalf("StatusChanged calls = %+v, want the source disabled to active", status)
	}
}

// TestUserUnmerge_Refusals: every reason an unmerge cannot be whole is a
// refusal with its code, from both PlanUserUnmerge and StageUserUnmerge, and
// either leaves every record, journal line and audit record as it was.
func TestUserUnmerge_Refusals(t *testing.T) {
	type tc struct {
		name string
		opts umOpts
		// change breaks the scene; it may return a changed record.
		change func(s *umScene, rec *auth.UnmergeRecord)
		actor  func(s *umScene) *auth.Principal
		code   string // the MergeRefusal code, when want is nil
		want   error
	}
	bruce := func(s *umScene) *auth.Principal { return s.f.signedIn(s.bruce) }
	third := func(s *umScene) *auth.User { return s.f.user("harper-row", "gotham.detective") }
	cases := []tc{
		{name: "never merged into that account", code: auth.RefusedNotMerged, change: func(s *umScene, rec *auth.UnmergeRecord) {
			rec.Target = third(s).ID
		}},
		{name: "already unmerged", code: auth.RefusedNotMerged, change: func(s *umScene, _ *auth.UnmergeRecord) {
			if err := s.unmerge(bruce(s), s.rec); err != nil {
				s.f.t.Fatalf("the first unmerge: %v", err)
			}
		}},
		{name: "the source changed after the merge", code: auth.RefusedSourceChanged, change: func(s *umScene, _ *auth.UnmergeRecord) {
			s.f.edit(s.jason, func(u *auth.User) { u.DisplayName = "Red Hood" })
		}},
		{name: "the source was enabled by hand", code: auth.RefusedSourceChanged, change: func(s *umScene, _ *auth.UnmergeRecord) {
			s.f.edit(s.jason, func(u *auth.User) { u.Status = auth.StatusActive })
		}},
		{name: "the source is gone", code: auth.RefusedSourceDeleted, change: func(s *umScene, _ *auth.UnmergeRecord) {
			u := s.f.reload(s.jason.ID)
			s.f.commit(func(b *storage.Batch) error { return s.f.st.DeleteUser(b, u) })
		}},
		{name: "the target is gone", code: auth.RefusedTargetDeleted, change: func(s *umScene, _ *auth.UnmergeRecord) {
			u := s.f.reload(s.dick.ID)
			s.f.commit(func(b *storage.Batch) error { return s.f.st.DeleteUser(b, u) })
		}},
		{name: "the target is disabled", code: auth.RefusedTargetInactive, change: func(s *umScene, _ *auth.UnmergeRecord) {
			s.f.edit(s.dick, func(u *auth.User) { u.Status = auth.StatusDisabled })
		}},
		{name: "the target was itself merged away", code: auth.RefusedTargetMerged, change: func(s *umScene, _ *auth.UnmergeRecord) {
			other := third(s)
			s.f.edit(s.dick, func(u *auth.User) { u.MergedInto = other.ID })
		}},
		{name: "a moved key was re-owned", code: auth.RefusedKeyMoved, change: func(s *umScene, _ *auth.UnmergeRecord) {
			other := third(s)
			k := s.f.key(s.key.ID)
			k.Owner, k.Version = other.ID, k.Version+1
			s.f.commit(func(b *storage.Batch) error { return s.f.st.PutAPIKey(b, k) })
		}},
		{name: "a moved key changed since", code: auth.RefusedKeyMoved, change: func(s *umScene, _ *auth.UnmergeRecord) {
			k := s.f.key(s.key.ID)
			k.Name, k.Version = "grappling hook", k.Version+1
			s.f.commit(func(b *storage.Batch) error { return s.f.st.PutAPIKey(b, k) })
		}},
		{name: "a moved key is gone", code: auth.RefusedKeyMoved, change: func(s *umScene, _ *auth.UnmergeRecord) {
			k := s.f.key(s.key.ID)
			s.f.commit(func(b *storage.Batch) error { return s.f.st.DeleteAPIKey(b, k) })
		}},
		{name: "a moved sign-in was unlinked", code: auth.RefusedSignInMoved, change: func(s *umScene, _ *auth.UnmergeRecord) {
			s.f.commit(func(b *storage.Batch) error {
				return s.f.st.DeleteIdentity(b, &auth.ExternalIdentity{Provider: umProvider, Subject: umSubject, UserID: s.dick.ID})
			})
		}},
		{name: "the address is held verified by another account now", code: auth.RefusedEmailClaimed, change: func(s *umScene, _ *auth.UnmergeRecord) {
			// The target gave the address up first, then another account took it.
			s.f.edit(s.dick, func(u *auth.User) { u.Email, u.EmailVerified = "", false })
			s.f.userWithEmail("harper-row", s.srcBefore.Email, true, "gotham.detective")
		}},
		{name: "the journal lists no sign-in id for a sign-in that moved", code: auth.RefusedSignInMoved, change: func(s *umScene, rec *auth.UnmergeRecord) {
			// An id the target never held.
			rec.Identities = []string{umProvider + "/never-linked"}
		}},
		{name: "no principal", want: auth.ErrDenied, actor: func(*umScene) *auth.Principal { return nil }},
		{name: "a user manager below the source's roles", opts: umOpts{srcRoles: []rbac.RoleName{rbac.RoleAdmin}}, want: auth.ErrDominance,
			actor: func(s *umScene) *auth.Principal {
				p := s.f.signedIn(s.f.user("jim-gordon", roleCommissioner))
				p.Assurance |= auth.AssuranceOwner
				return p
			}},
		{name: "a key principal", want: auth.ErrDenied, actor: func(s *umScene) *auth.Principal {
			_, k := s.f.mint(s.f.session(s.bruce), s.bruce.ID, "core:users.manage")
			return s.f.keyPrincipal(s.bruce, k)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newUmScene(t, c.opts)
			rec := s.rec
			if c.change != nil {
				c.change(s, &rec)
			}
			actor := bruce(s)
			if c.actor != nil {
				actor = c.actor(s)
			}
			before := s.snapshot()

			check := func(what string, err error) {
				t.Helper()
				switch {
				case c.want != nil:
					wantErr(t, what, err, c.want)
				default:
					wantRefusal(t, what, err, c.code)
				}
			}
			_, _, err := s.plan(actor, rec)
			check("PlanUserUnmerge", err)
			check("StageUserUnmerge", s.stage(actor, s.current(rec)))
			if after := s.snapshot(); after != before {
				t.Fatalf("a refused unmerge wrote:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestUserUnmerge_RefusalTextHoldsNoAddress: a refusal reaches an admin's
// screen and logs, and must not quote an address or a sign-in subject.
func TestUserUnmerge_RefusalTextHoldsNoAddress(t *testing.T) {
	s := newUmScene(t, umOpts{})
	s.f.edit(s.dick, func(u *auth.User) { u.Email, u.EmailVerified = "", false })
	s.f.userWithEmail("harper-row", s.srcBefore.Email, true, "gotham.detective")
	_, _, err := s.plan(s.f.signedIn(s.bruce), s.rec)
	wantRefusal(t, "an address claimed elsewhere", err, auth.RefusedEmailClaimed)
	for _, secret := range []string{s.srcBefore.Email, umSubject, "example.test"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the refusal quotes %q: %v", secret, err)
		}
	}
}

// TestUserUnmerge_KeepsWhatTheTargetHeldBeforeTheMerge: the role the target
// held before the merge stays when the source held it too, and an address the
// target changed since the merge stays; neither is reverted over a change
// made after the merge.
func TestUserUnmerge_KeepsWhatTheTargetHeldBeforeTheMerge(t *testing.T) {
	s := newUmScene(t, umOpts{})
	s.f.edit(s.dick, func(u *auth.User) { u.Email, u.EmailVerified = "dick.grayson@example.test", true })
	if err := s.unmerge(s.f.signedIn(s.bruce), s.rec); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	tgt := s.f.reload(s.dick.ID)
	if !slices.Contains(tgt.Roles, "gotham.detective") {
		t.Fatalf("target lost the role it held before the merge: %v", tgt.Roles)
	}
	if slices.Contains(tgt.Roles, roleCommissioner) {
		t.Fatalf("target kept the role the merge gave it: %v", tgt.Roles)
	}
	if tgt.Email != "dick.grayson@example.test" || !tgt.EmailVerified {
		t.Fatalf("the target's own change of address was reverted: %q verified %v", tgt.Email, tgt.EmailVerified)
	}
	if src := s.f.reload(s.jason.ID); src.Email != s.srcBefore.Email || !src.EmailVerified {
		t.Fatalf("source address = %q verified %v, want the one it had", src.Email, src.EmailVerified)
	}
}

// TestUserUnmerge_VerifiedTransferReverts: when the merge verified an address
// the target held unverified, the unmerge makes it unverified again and gives
// the source its verified one back.
func TestUserUnmerge_VerifiedTransferReverts(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.userWithEmail("jason-todd", "jason.todd@example.test", true, "gotham.detective")
	dick := f.signable(f.userWithEmail("dick-grayson", "jason.todd@example.test", false, "gotham.detective"))
	ctx := auth.WithPrincipal(context.Background(), f.signedIn(bruce))
	plan, sum, err := f.svc.PlanUserMerge(ctx, jason.ID, dick.ID)
	if err != nil || sum.Email != auth.MergeEmailVerifiedTransferred {
		t.Fatalf("PlanUserMerge: outcome %q, %v", sum.Email, err)
	}
	if err := f.stageMerge(f.signedIn(bruce), plan); err != nil {
		t.Fatal(err)
	}
	s := &umScene{f: f, bruce: bruce, jason: jason, dick: dick}
	rec := auth.UnmergeRecord{
		Source: jason.ID, Target: dick.ID, SourceVersionAfter: jason.Version + 1, SourceStatusBefore: auth.StatusActive,
		SourceEmailBefore: "jason.todd@example.test", SourceEmailVerifiedBefore: true,
		TargetEmailBefore: "jason.todd@example.test", TargetEmailVerifiedBefore: false,
		EmailOutcome: sum.Email, APIKeys: plan.Receipt().APIKeyVersions, Identities: plan.Receipt().IdentityIDs,
	}
	if err := s.unmerge(f.signedIn(bruce), rec); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	if tgt := f.reload(dick.ID); tgt.Email != "jason.todd@example.test" || tgt.EmailVerified {
		t.Fatalf("target address = %q verified %v, want the unverified one it held before", tgt.Email, tgt.EmailVerified)
	}
	if src := f.reload(jason.ID); src.Email != "jason.todd@example.test" || !src.EmailVerified {
		t.Fatalf("source address = %q verified %v, want its verified one", src.Email, src.EmailVerified)
	}
}

// TestUserUnmerge_LastAdmin: the unmerge counts the source's own return. The
// target, the only active admin after the merge, loses the admin role the merge
// gave it; that is refused when the source comes back disabled (nobody would
// hold admin) and allowed when it comes back active (it does).
func TestUserUnmerge_LastAdmin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status auth.UserStatus
		want   error
	}{
		{"the source returns active", auth.StatusActive, nil},
		{"the source returns disabled", auth.StatusDisabled, auth.ErrLastAdmin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newUmScene(t, umOpts{srcRoles: []rbac.RoleName{rbac.RoleAdmin}, srcStatus: tc.status})
			if !slices.Contains(s.f.reload(s.dick.ID).Roles, rbac.RoleAdmin) {
				t.Fatal("the merge did not give the target the admin role")
			}
			// Bruce steps down: Dick is the only active admin the merge left.
			s.f.edit(s.bruce, func(u *auth.User) { u.Status = auth.StatusDisabled })
			actor := s.f.signedIn(s.f.reload(s.dick.ID))
			before := s.snapshot()
			err := s.unmerge(actor, s.rec)
			if tc.want != nil {
				wantErr(t, "the unmerge", err, tc.want)
				if after := s.snapshot(); after != before {
					t.Fatalf("a refused unmerge wrote:\n%s\n%s", before, after)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmerge: %v", err)
			}
			if src := s.f.reload(s.jason.ID); src.Status != auth.StatusActive || !slices.Contains(src.Roles, rbac.RoleAdmin) {
				t.Fatalf("source after the unmerge: %+v", src)
			}
		})
	}
}

// TestUserUnmerge_SourceBackAsItWas: a source that was disabled before the
// merge comes back disabled, with no user.enabled line and no StatusChanged.
func TestUserUnmerge_SourceBackAsItWas(t *testing.T) {
	s := newUmScene(t, umOpts{srcStatus: auth.StatusDisabled})
	rec := s.f.lifecycle()
	if err := s.unmerge(s.f.signedIn(s.bruce), s.rec); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	src := s.f.reload(s.jason.ID)
	if src.Status != auth.StatusDisabled || src.MergedInto != "" {
		t.Fatalf("source after the unmerge: status %s merged_into %q", src.Status, src.MergedInto)
	}
	if l := s.lines(journal.UserEnabled, s.jason.ID); len(l) != 0 {
		t.Fatalf("user.enabled lines for a source that stays disabled = %+v", l)
	}
	if l := s.lines(journal.CredentialEpochBumped, s.jason.ID); len(l) != 1 {
		t.Fatalf("credential_epoch.bumped lines = %+v, want one", l)
	}
	rec.wantNone(t, "a source that stays disabled")
}

// TestUserUnmerge_OnlyAKeyMoved: with no sign-in to move back, the target's
// credential epoch and sessions are left alone.
func TestUserUnmerge_OnlyAKeyMoved(t *testing.T) {
	s := newUmScene(t, umOpts{noIdentity: true})
	cave := s.f.signedIn(s.f.reload(s.dick.ID))
	epoch := s.f.reload(s.dick.ID).CredentialEpoch
	plan, sum, err := s.plan(s.f.signedIn(s.bruce), s.rec)
	if err != nil || sum.TargetSessionsEnded {
		t.Fatalf("PlanUserUnmerge: summary %+v, %v", sum, err)
	}
	if err := s.stage(s.f.signedIn(s.bruce), plan); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	if got := s.f.reload(s.dick.ID).CredentialEpoch; got != epoch {
		t.Fatalf("target epoch = %d, want %d: no sign-in moved back", got, epoch)
	}
	if got := s.f.sessionEpoch(cave.CredentialID); got != epoch {
		t.Fatalf("the target's session is at epoch %d, the target at %d: no sign-in moved back", got, epoch)
	}
	if l := s.lines(journal.CredentialEpochBumped, s.dick.ID); len(l) != 0 {
		t.Fatalf("credential_epoch.bumped lines for the target = %+v, want none", l)
	}
	if got := s.f.key(s.key.ID); got.Owner != s.jason.ID {
		t.Fatalf("key owner = %s, want the source", got.Owner)
	}
}

// TestUserUnmerge_PlanWritesNothing: a preview stages and writes nothing and
// fires no callback.
func TestUserUnmerge_PlanWritesNothing(t *testing.T) {
	s := newUmScene(t, umOpts{})
	rec := s.f.lifecycle()
	before := s.snapshot()
	if _, _, err := s.plan(s.f.signedIn(s.bruce), s.rec); err != nil {
		t.Fatalf("PlanUserUnmerge: %v", err)
	}
	if after := s.snapshot(); after != before {
		t.Fatalf("a preview wrote:\n%s\n%s", before, after)
	}
	rec.wantNone(t, "a preview")
}

// TestUserUnmerge_StalePlanFails: a record changed after the preview is
// ErrMergeConflict at staging when it changed before StageUserUnmerge, and
// storage.ErrConflict at the commit when it changed between the two. Nothing
// of the unmerge lands, and an AfterCommit run over the failed batch fires
// nothing.
func TestUserUnmerge_StalePlanFails(t *testing.T) {
	s := newUmScene(t, umOpts{})
	f := s.f
	rec := f.lifecycle()
	actor := f.signedIn(s.bruce)

	plan, _, err := s.plan(actor, s.rec)
	if err != nil {
		t.Fatal(err)
	}
	f.edit(s.dick, func(u *auth.User) { u.DisplayName = "Nightwing" })
	wantErr(t, "a plan over a changed target", s.stage(actor, plan), auth.ErrMergeConflict)

	plan, _, err = s.plan(actor, s.rec)
	if err != nil {
		t.Fatal(err)
	}
	ctx := s.ctx(actor)
	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	after, err := f.svc.StageUserUnmerge(ctx, b, plan)
	if err != nil {
		t.Fatalf("StageUserUnmerge: %v", err)
	}
	k := f.key(s.key.ID)
	k.Name, k.Version = "batarang", k.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, k) })
	wantErr(t, "a commit over a key changed after staging", b.Commit(ctx, storage.Sync), storage.ErrConflict)
	after(ctx)

	if src := f.reload(s.jason.ID); src.Status != auth.StatusDisabled || src.MergedInto != s.dick.ID {
		t.Fatalf("source after a failed unmerge: %+v", src)
	}
	if got := f.key(s.key.ID); got.Owner != s.dick.ID {
		t.Fatalf("key owner after a failed unmerge = %s", got.Owner)
	}
	rec.wantNone(t, "an AfterCommit over a batch that did not commit")
}

// TestUserMerge_ReceiptHoldsWhatWasCommitted: the versions a merge plan's
// receipt reports for the moved keys are the versions the commit wrote, and a key
// edited between the preview and the stage conflicts the merge instead of
// letting it journal a version the key does not have.
func TestUserMerge_ReceiptHoldsWhatWasCommitted(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", roleCommissioner)
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))
	_, k := f.mint(f.session(jason), jason.ID, "gotham:patrol")
	f.linkIdentity(jason, umProvider, umSubject)
	ctx := auth.WithPrincipal(context.Background(), f.signedIn(bruce))

	plan, sum, err := f.svc.PlanUserMerge(ctx, jason.ID, dick.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Receipt().IdentityIDs; !slices.Equal(got, []string{umIdentity}) {
		t.Fatalf("receipt IdentityIDs = %v, want [%s]", got, umIdentity)
	}
	// The preview holds no subject: the receipt is journal-only.
	if text := fmt.Sprintf("%+v %#v", sum, sum); strings.Contains(text, umSubject) {
		t.Fatalf("the merge summary holds the sign-in subject: %s", text)
	}
	// An edit between the preview and the stage: the plan's Expect on the key's
	// bytes fails the merge, so the receipt above is never journaled.
	edited := f.key(k.ID)
	edited.Name, edited.Version = "grappling hook", edited.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutAPIKey(b, edited) })
	wantErr(t, "a merge staged over an edited key", f.stageMerge(f.signedIn(bruce), plan), auth.ErrMergeConflict)

	plan, _, err = f.svc.PlanUserMerge(ctx, jason.ID, dick.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.stageMerge(f.signedIn(bruce), plan); err != nil {
		t.Fatal(err)
	}
	if want, got := plan.Receipt().APIKeyVersions[k.ID], f.key(k.ID).Version; want != got || want == 0 {
		t.Fatalf("the receipt says key version %d, the commit wrote %d", want, got)
	}
}

// TestUserUnmerge_PlanPrintsNoRows: a plan prints as ids and a count, never rows.
func TestUserUnmerge_PlanPrintsNoRows(t *testing.T) {
	s := newUmScene(t, umOpts{})
	plan, _, err := s.plan(s.f.signedIn(s.bruce), s.rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if got := fmt.Sprintf(verb, plan); strings.Contains(got, s.key.ID) || strings.Contains(got, umSubject) {
			t.Fatalf("Sprintf(%q) = %q, names a record", verb, got)
		}
	}
}

// TestUserUnmerge_AGuestComesBackActiveAndNothingIsReissued: a guest is an
// account that signs in only through an external identity and holds a key (no
// password, the viewer role). The merge disabled it; the unmerge brings it back
// active. Core fires StatusChanged once for that real change (disabled to
// active) and no other lifecycle callback: not UserMergedAway (nothing was
// merged), not InviteAccepted, not UserDeleted. A subscriber that adds a guest
// to an external allow-list or mints a phone key on StatusChanged would have
// exactly one event to act on, so a guest's Cloudflare access and phone key are
// NOT re-added by the unmerge itself; they must be reissued by an admin.
func TestUserUnmerge_AGuestComesBackActiveAndNothingIsReissued(t *testing.T) {
	s := newUmScene(t, umOpts{srcRoles: []rbac.RoleName{rbac.RoleViewer}})
	f := s.f
	rec := f.lifecycle()
	if got := f.reload(s.jason.ID); got.Status != auth.StatusDisabled {
		t.Fatalf("the merge left the guest %s, want disabled", got.Status)
	}
	if got := f.reload(s.jason.ID); got.HasPassword {
		t.Fatalf("the guest holds a password; this scene is not a guest")
	}
	if err := s.unmerge(f.signedIn(s.bruce), s.rec); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	if got := f.reload(s.jason.ID); got.Status != auth.StatusActive {
		t.Fatalf("the guest came back %s, want active", got.Status)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.status) != 1 || rec.status[0].UserID != s.jason.ID || rec.status[0].From != auth.StatusDisabled || rec.status[0].To != auth.StatusActive {
		t.Fatalf("StatusChanged calls = %+v, want exactly one: the guest, disabled to active", rec.status)
	}
	if n := len(rec.merged) + len(rec.invites) + len(rec.deleted) + len(rec.emails); n != 0 {
		t.Fatalf("other lifecycle callbacks ran: merged=%d invites=%d deleted=%d emails=%d, want none", len(rec.merged), len(rec.invites), len(rec.deleted), len(rec.emails))
	}
}
