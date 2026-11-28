// file: core/auth/merge_plan_test.go
// version: 0.1.0
// guid: 3c1f6a52-8d0e-4b7a-9a41-5e2b7c90d4f1
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Admin.PlanUserMerge and the rules StageUserMerge now runs for the core half
// of F4-08 B10l: the roles transfer under the principal's dominance, HEAD's
// e-mail rule on core's one EmailVerified flag, the no-sign-in refusal, and the
// plan's pinned versions and rows. internal/usermerge.planAccounts is the HEAD
// rule; the books op slice turns the plan into one batch with the books rows.

const (
	oracleMail  = "oracle@gotham.example"
	batgirlMail = "batgirl@gotham.example"
)

// refusalCode is the MergeRefusal code err carries, "" when it carries none.
func refusalCode(err error) string {
	if r, ok := errors.AsType[*auth.MergeRefusal](err); ok {
		return r.Code
	}
	return ""
}

// wantRefusal fails unless err is a MergeRefusal with code.
func wantRefusal(t *testing.T, what string, err error, code string) {
	t.Helper()
	if !errors.Is(err, auth.ErrMergeRefused) || refusalCode(err) != code {
		t.Fatalf("%s: err = %v, want a merge refusal with code %q", what, err, code)
	}
}

// preview is PlanUserMerge as actor, error and all.
func (f *fx) preview(actor *auth.Principal, source, target auth.UserID) (auth.UserMergePlan, auth.MergeSummary, error) {
	f.t.Helper()
	return f.svc.PlanUserMerge(auth.WithPrincipal(context.Background(), actor), source, target)
}

// setMail gives u the address email, verified or not.
func (f *fx) setMail(u *auth.User, email string, verified bool) *auth.User {
	f.t.Helper()
	return f.edit(u, func(c *auth.User) { c.Email, c.EmailVerified = email, verified })
}

// verifiedHolders counts the users holding email verified.
func (f *fx) verifiedHolders(email string) int {
	f.t.Helper()
	n := 0
	for u, err := range f.st.ScanUsers(context.Background()) {
		if err != nil {
			f.t.Fatal(err)
		}
		if u.EmailVerified && auth.SameEmail(u.Email, email) {
			n++
		}
	}
	return n
}

// assertUntouched fails unless source is still active and unmarked at version
// srcV, and target is at version tgtV: a refused merge landed nothing.
func (f *fx) assertUntouched(source, target *auth.User) {
	f.t.Helper()
	s, tg := f.reload(source.ID), f.reload(target.ID)
	if s.Version != source.Version || s.Status != auth.StatusActive || s.MergedInto != "" || !s.MergedAt.IsZero() {
		f.t.Fatalf("a refused merge changed the source: %+v", s)
	}
	if tg.Version != target.Version {
		f.t.Fatalf("a refused merge changed the target: %+v", tg)
	}
}

// TestPlanUserMerge_SummaryHoldsNoCredential: the summary is ids, counts and
// outcome codes. Not one byte of a password hash, an API key secret, its
// stored hash or a session token reaches it, nor the plan's printed form; the
// plan itself is opaque to its holder.
func TestPlanUserMerge_SummaryHoldsNoCredential(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.withPassword(f.user("jason-todd", "gotham.detective"), "red-hood-1988-secret")
	dick := f.signable(f.user("dick-grayson", roleCommissioner))
	raw, k := f.mint(f.session(jason), jason.ID, "gotham:patrol")
	f.linkIdentity(jason, "wayne-sso", "sub-red-hood")
	f.setMail(jason, oracleMail, true)
	hash, err := f.st.PasswordHash(context.Background(), jason.ID)
	if err != nil {
		t.Fatal(err)
	}

	plan, sum, err := f.preview(f.signedIn(bruce), jason.ID, dick.ID)
	if err != nil {
		t.Fatalf("PlanUserMerge: %v", err)
	}
	if plan.Source() != jason.ID || plan.Target() != dick.ID {
		t.Fatalf("plan names %s into %s", plan.Source(), plan.Target())
	}
	if sum.Source != jason.ID || sum.Target != dick.ID || sum.Email != auth.MergeEmailMoved || sum.TargetHoldsAdmin ||
		!slices.Equal(sum.RolesTransferred, []rbac.RoleName{"gotham.detective"}) ||
		!slices.Equal(sum.APIKeys, []string{k.ID}) || !slices.Equal(sum.IdentityProviders, []string{"wayne-sso"}) {
		t.Fatalf("summary = %+v", sum)
	}
	printed, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	for what, text := range map[string]string{
		"json summary": string(printed), "printed summary": fmt.Sprintf("%+v %#v", sum, sum), "printed plan": fmt.Sprintf("%+v %#v", plan, plan),
	} {
		for secret, needle := range map[string]string{
			"password hash": hash, "password": "red-hood-1988-secret", "key secret": raw, "key hash prefix": k.HashPrefix, "subject": "sub-red-hood",
		} {
			if needle != "" && strings.Contains(text, needle) {
				t.Errorf("the %s holds the %s", what, secret)
			}
		}
	}
	f.assertUntouched(f.reload(jason.ID), dick)
}

// TestUserMerge_RolesTransfer: the roles the source holds and the target does
// not move to the target in the merge's batch (the target keeps its own); the
// merge says so in the summary, audits it, and its AfterCommit tells rbac the
// assignments changed. A merge that moves no role does not.
func TestUserMerge_RolesTransfer(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.signable(f.user("dick-grayson", roleCommissioner))
	tim := f.signable(f.user("tim-drake", "gotham.detective"))
	actor := f.signedIn(bruce)
	ctx := auth.WithPrincipal(context.Background(), actor)
	f.genLog.take()

	// Same roles: nothing moves, nothing is reported.
	plan := f.planMerge(actor, jason.ID, tim.ID)
	b := f.st.NewBatch()
	after, err := f.svc.StageUserMerge(ctx, b, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	after(ctx)
	if got := f.genLog.take(); len(got) != 0 {
		t.Fatalf("a merge that moved no role reported %v", got)
	}
	if got := f.reload(tim.ID); !slices.Equal(got.Roles, []rbac.RoleName{"gotham.detective"}) {
		t.Fatalf("target roles after a merge that moves none = %v", got.Roles)
	}

	harvey := f.user("harvey-dent", "gotham.detective")
	_, sum, err := f.preview(actor, harvey.ID, dick.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sum.RolesTransferred, []rbac.RoleName{"gotham.detective"}) {
		t.Fatalf("summary roles = %v", sum.RolesTransferred)
	}
	plan = f.planMerge(actor, harvey.ID, dick.ID)
	b = f.st.NewBatch()
	defer func() { _ = b.Close() }()
	after, err = f.svc.StageUserMerge(ctx, b, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.reload(dick.ID); len(got.Roles) != 1 {
		t.Fatalf("staging wrote the target's roles before the commit: %v", got.Roles)
	}
	after(ctx)
	if got := f.genLog.take(); len(got) != 0 {
		t.Fatalf("AfterCommit before the commit reported %v", got)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	after(ctx)
	got := f.reload(dick.ID)
	if !slices.Equal(got.Roles, []rbac.RoleName{roleCommissioner, "gotham.detective"}) || got.Version != dick.Version+1 {
		t.Fatalf("target after the merge: roles %v version %d, want both roles at %d", got.Roles, got.Version, dick.Version+1)
	}
	if gens := f.genLog.take(); len(gens) != 1 || gens[0] != f.rb.Generation() {
		t.Fatalf("AfterCommit reported %v, want exactly the current generation %d", gens, f.rb.Generation())
	}
	f.audit.mu.Lock()
	var setRoles *map[string]any
	for _, e := range f.audit.recorded {
		if e.Action == "core.user.set_roles" && e.Reason == "merged" {
			d := e.Details
			setRoles = &d
		}
	}
	f.audit.mu.Unlock()
	if setRoles == nil || (*setRoles)["from"] != string(harvey.ID) {
		t.Fatalf("no core.user.set_roles audit for the transfer: %v", setRoles)
	}
}

// TestUserMerge_RolesNeedDominance: the roles move only under the principal's
// dominance. A user manager below admin who does not hold a scope of one of the
// source's roles is refused for the whole merge, the roles it could have given
// included: nothing of the merge lands.
func TestUserMerge_RolesNeedDominance(t *testing.T) {
	f := newFx(t)
	f.user("bruce-wayne", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	owner := func(p *auth.Principal) *auth.Principal { p.Assurance |= auth.AssuranceOwner; return p }
	// A holds gordon's scopes, B carries gotham:relay.worker, which gordon lacks.
	jason := f.user("jason-todd", "gotham.detective", "relay-worker")
	harvey := f.user("harvey-dent", "gotham.detective")
	dick := f.signable(f.user("dick-grayson", roleCommissioner))
	actor := owner(f.signedIn(gordon))

	_, _, err := f.preview(actor, jason.ID, dick.ID)
	wantErr(t, "a preview of a source holding a role the principal lacks", err, auth.ErrDominance)
	wantErr(t, "a stage of the same", f.stageMerge(actor, f.rawPlan(jason.ID, dick.ID)), auth.ErrDominance)
	f.assertUntouched(jason, dick)
	if got := f.reload(dick.ID); slices.Contains(got.Roles, "gotham.detective") {
		t.Fatalf("the roles the principal could assign moved although the merge was refused: %v", got.Roles)
	}

	// The same principal, a source whose every role it holds: the merge lands.
	if err := f.stageMerge(actor, f.planMerge(actor, harvey.ID, dick.ID)); err != nil {
		t.Fatalf("the owner, dominating a source whose roles it holds: %v", err)
	}
	if got := f.reload(dick.ID); !slices.Contains(got.Roles, "gotham.detective") {
		t.Fatalf("the roles did not move: %v", got.Roles)
	}
}

// TestUserMerge_RoleSetMovesBeforeTheCommit: the roles that move are claimed
// against the stored role set, and a role edited between staging and the commit
// fails the commit (storage.ErrConflict), so a role never lands on the target
// after the principal stopped being allowed to give it.
func TestUserMerge_RoleSetMovesBeforeTheCommit(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	checker := f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	if _, err := f.rb.CreateRole(ctx, checker, rbac.RoleInput{Name: roleRobin, Title: "Robin", Permissions: []rbac.Permission{"gotham:patrol"}}); err != nil {
		t.Fatal(err)
	}
	jason := f.setRoles(f.user("jason-todd").ID, roleRobin)
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))
	actor := f.signedIn(bruce)
	plan := f.planMerge(actor, jason.ID, dick.ID)

	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	if _, err := f.svc.StageUserMerge(auth.WithPrincipal(ctx, actor), b, plan); err != nil {
		t.Fatalf("StageUserMerge: %v", err)
	}
	if _, err := f.rb.CreateRole(ctx, checker, rbac.RoleInput{Name: "custom.oracle", Title: "Oracle", Permissions: []rbac.Permission{"gotham:patrol"}}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a commit after the role set moved", b.Commit(ctx, storage.Sync), storage.ErrConflict)
	f.assertUntouched(jason, dick)
	if got := f.reload(dick.ID); slices.Contains(got.Roles, roleRobin) {
		t.Fatalf("the role landed although the commit failed: %v", got.Roles)
	}
	if err := f.stageMerge(actor, f.planMerge(actor, jason.ID, dick.ID)); err != nil {
		t.Fatalf("the retry over the new role set: %v", err)
	}
}

// TestUserMerge_Email is HEAD's e-mail rule (internal/usermerge planAccounts)
// on core's one EmailVerified flag, case by case: the source's address is
// always cleared; the target keeps its own; the source's moves to a target with
// none; a same-address merge whose only verified side is the source leaves the
// target verified; a different verified address of the source is released. The
// address is never verified on two accounts after the merge, which the OIDC
// resolver's address-based link depends on, and a verified address another
// account holds is refused before anything is written.
func TestUserMerge_Email(t *testing.T) {
	cases := []struct {
		name                     string
		src, tgt                 string
		srcVerified, tgtVerified bool
		third                    string // another account holding third verified
		thirdUnverified          bool
		wantOutcome              auth.MergeEmailOutcome
		wantEmail                string
		wantVerified             bool
		wantErr                  error
	}{
		{name: "neither has one", wantOutcome: auth.MergeEmailNone},
		{name: "the source's verified address moves to a target with none", src: oracleMail, srcVerified: true,
			wantOutcome: auth.MergeEmailMoved, wantEmail: oracleMail, wantVerified: true},
		{name: "the source's unverified address moves unverified", src: oracleMail,
			wantOutcome: auth.MergeEmailMoved, wantEmail: oracleMail},
		{name: "same address, only the source verified: the target ends verified", src: oracleMail, srcVerified: true,
			tgt: " Oracle@Gotham.Example ", wantOutcome: auth.MergeEmailVerifiedTransferred, wantEmail: " Oracle@Gotham.Example ", wantVerified: true},
		{name: "same address, both verified", src: oracleMail, srcVerified: true, tgt: oracleMail, tgtVerified: true,
			wantOutcome: auth.MergeEmailKept, wantEmail: oracleMail, wantVerified: true},
		{name: "same address, only the target verified", src: oracleMail, tgt: oracleMail, tgtVerified: true,
			wantOutcome: auth.MergeEmailKept, wantEmail: oracleMail, wantVerified: true},
		{name: "same address, neither verified", src: oracleMail, tgt: oracleMail,
			wantOutcome: auth.MergeEmailKept, wantEmail: oracleMail},
		{name: "a different verified address of the source is released", src: oracleMail, srcVerified: true,
			tgt: batgirlMail, tgtVerified: true, wantOutcome: auth.MergeEmailReleasedVerified, wantEmail: batgirlMail, wantVerified: true},
		{name: "a different unverified address of the source is dropped", src: oracleMail, tgt: batgirlMail, tgtVerified: true,
			wantOutcome: auth.MergeEmailKept, wantEmail: batgirlMail, wantVerified: true},
		{name: "a second admin holds the address verified: the moved address is refused", src: oracleMail, srcVerified: true,
			third: oracleMail, wantErr: auth.ErrEmailInUse},
		{name: "a second admin holds the address verified: the transferred verification is refused", src: oracleMail, srcVerified: true,
			tgt: oracleMail, third: oracleMail, wantErr: auth.ErrEmailInUse},
		{name: "another account holds the address unverified: no claim", src: oracleMail, srcVerified: true,
			third: oracleMail, thirdUnverified: true, wantOutcome: auth.MergeEmailMoved, wantEmail: oracleMail, wantVerified: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			bruce := f.user("bruce-wayne", rbac.RoleAdmin)
			jason := f.user("jason-todd", "gotham.detective")
			dick := f.signable(f.user("dick-grayson", "gotham.detective"))
			if tc.third != "" {
				f.setMail(f.user("barbara-gordon", rbac.RoleAdmin), tc.third, !tc.thirdUnverified)
			}
			if tc.src != "" {
				jason = f.setMail(jason, tc.src, tc.srcVerified)
			}
			if tc.tgt != "" {
				dick = f.setMail(dick, tc.tgt, tc.tgtVerified)
			}
			actor := f.signedIn(bruce)

			plan, sum, err := f.preview(actor, jason.ID, dick.ID)
			if tc.wantErr != nil {
				wantErr(t, "the preview", err, tc.wantErr)
				wantErr(t, "a stage over a hand-made plan", f.stageMerge(actor, f.rawPlan(jason.ID, dick.ID)), tc.wantErr)
				f.assertUntouched(jason, dick)
				if got := f.reload(jason.ID); got.Email != tc.src || got.EmailVerified != tc.srcVerified {
					t.Fatalf("a refused merge changed the source's address: %q verified %v", got.Email, got.EmailVerified)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanUserMerge: %v", err)
			}
			if sum.Email != tc.wantOutcome {
				t.Fatalf("summary e-mail outcome = %q, want %q", sum.Email, tc.wantOutcome)
			}
			if err := f.stageMerge(actor, plan); err != nil {
				t.Fatalf("StageUserMerge: %v", err)
			}
			src, tgt := f.reload(jason.ID), f.reload(dick.ID)
			if src.Email != "" || src.EmailVerified || src.MergedInto != dick.ID || src.Status != auth.StatusDisabled {
				t.Fatalf("source after the merge: %+v", src)
			}
			if tgt.Email != tc.wantEmail || tgt.EmailVerified != tc.wantVerified {
				t.Fatalf("target after the merge: %q verified %v, want %q verified %v", tgt.Email, tgt.EmailVerified, tc.wantEmail, tc.wantVerified)
			}
			for _, addr := range []string{oracleMail, batgirlMail} {
				if n := f.verifiedHolders(addr); n > 1 {
					t.Fatalf("%d accounts hold %s verified after the merge", n, addr)
				}
			}
			f.audit.mu.Lock()
			last := f.audit.recorded[len(f.audit.recorded)-1]
			f.audit.mu.Unlock()
			if last.Details["email"] != string(tc.wantOutcome) {
				t.Fatalf("merge audit e-mail = %v, want %q", last.Details["email"], tc.wantOutcome)
			}
		})
	}
}

// TestUserMerge_EmailLinkable: a merge never makes an address linkable that was
// not. oidc.Resolver.linkByEmail links a sign-in to the single verified,
// non-admin, passwordless, active holder of its address; HEAD's plan cannot
// give one such an address (it requires an admin survivor), and core refuses
// the merge that would instead of trusting its caller to.
func TestUserMerge_EmailLinkable(t *testing.T) {
	setup := func(t *testing.T, srcVerified bool, tgtRoles ...rbac.RoleName) (*fx, *auth.Principal, *auth.User, *auth.User) {
		f := newFx(t)
		bruce := f.user("bruce-wayne", rbac.RoleAdmin)
		jason := f.setMail(f.user("jason-todd", "gotham.detective"), oracleMail, srcVerified)
		dick := f.user("dick-grayson", tgtRoles...)
		f.linkIdentity(dick, "wayne-sso", "sub-nightwing") // a sign-in, so no_sign_in is not what refuses
		return f, f.signedIn(bruce), jason, dick
	}

	t.Run("a passwordless non-admin target gaining a verified address", func(t *testing.T) {
		f, actor, jason, dick := setup(t, true, "gotham.detective")
		_, _, err := f.preview(actor, jason.ID, dick.ID)
		wantRefusal(t, "the preview", err, auth.RefusedEmailLinkable)
		wantRefusal(t, "a stage over a hand-made plan", f.stageMerge(actor, f.rawPlan(jason.ID, dick.ID)), auth.RefusedEmailLinkable)
		f.assertUntouched(jason, dick)
	})
	t.Run("the same merge when the address stays unverified", func(t *testing.T) {
		f, actor, jason, dick := setup(t, false, "gotham.detective")
		if err := f.stageMerge(actor, f.planMerge(actor, jason.ID, dick.ID)); err != nil {
			t.Fatalf("an unverified address: %v", err)
		}
		if got := f.reload(dick.ID); got.Email != oracleMail || got.EmailVerified {
			t.Fatalf("target: %q verified %v", got.Email, got.EmailVerified)
		}
	})
	t.Run("a target with a password", func(t *testing.T) {
		f, actor, jason, dick := setup(t, true, "gotham.detective")
		f.signable(dick)
		if err := f.stageMerge(actor, f.planMerge(actor, jason.ID, dick.ID)); err != nil {
			t.Fatalf("a target with a password: %v", err)
		}
		if got := f.reload(dick.ID); got.Email != oracleMail || !got.EmailVerified {
			t.Fatalf("target: %q verified %v", got.Email, got.EmailVerified)
		}
	})
	t.Run("an admin target", func(t *testing.T) {
		f, actor, jason, dick := setup(t, true, rbac.RoleAdmin)
		_, sum, err := f.preview(actor, jason.ID, dick.ID)
		if err != nil {
			t.Fatalf("an admin target: %v", err)
		}
		if !sum.TargetHoldsAdmin {
			t.Fatal("the summary does not say the target is an admin")
		}
	})
	t.Run("a target that already held it verified", func(t *testing.T) {
		f, actor, jason, dick := setup(t, true, "gotham.detective")
		f.setMail(dick, oracleMail, true)
		if err := f.stageMerge(actor, f.planMerge(actor, jason.ID, dick.ID)); err != nil {
			t.Fatalf("a target that already held the address verified: %v", err)
		}
	})
}

// TestUserMerge_NoSignIn: the merge never leaves the target with no way to sign
// in. A password, a sign-in the target already has and a sign-in the merge moves
// to it each suffice; with none of them the merge is refused before it writes.
func TestUserMerge_NoSignIn(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	actor := f.signedIn(bruce)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.user("dick-grayson", "gotham.detective")

	_, _, err := f.preview(actor, jason.ID, dick.ID)
	wantRefusal(t, "no password, no sign-in anywhere", err, auth.RefusedNoSignIn)
	wantRefusal(t, "the same over a hand-made plan", f.stageMerge(actor, f.rawPlan(jason.ID, dick.ID)), auth.RefusedNoSignIn)
	f.assertUntouched(jason, dick)

	f.linkIdentity(jason, "wayne-sso", "sub-red-hood")
	plan, sum, err := f.preview(actor, jason.ID, dick.ID)
	if err != nil {
		t.Fatalf("a sign-in the merge moves to the target: %v", err)
	}
	if !slices.Equal(sum.IdentityProviders, []string{"wayne-sso"}) {
		t.Fatalf("summary providers = %v", sum.IdentityProviders)
	}
	if err := f.stageMerge(actor, plan); err != nil {
		t.Fatalf("StageUserMerge: %v", err)
	}
	if !f.hasIdentity("wayne-sso", "sub-red-hood") {
		t.Fatal("the moved sign-in is gone")
	}

	tim := f.user("tim-drake", "gotham.detective")
	f.linkIdentity(tim, "wayne-sso", "sub-robin")
	steph := f.user("stephanie-brown", "gotham.detective")
	if err := f.stageMerge(actor, f.planMerge(actor, steph.ID, tim.ID)); err != nil {
		t.Fatalf("a target with a sign-in of its own: %v", err)
	}
}

// TestUserMerge_AlreadyMergedAway: an account that was merged away is not merged
// again; the refusal names the account it went into.
func TestUserMerge_AlreadyMergedAway(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	actor := f.signedIn(bruce)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))
	tim := f.signable(f.user("tim-drake", "gotham.detective"))
	if err := f.stageMerge(actor, f.planMerge(actor, jason.ID, dick.ID)); err != nil {
		t.Fatal(err)
	}
	gone := f.reload(jason.ID)

	_, _, err := f.preview(actor, jason.ID, tim.ID)
	wantRefusal(t, "the preview of a merged-away source", err, auth.RefusedAlreadyMerged)
	if !strings.Contains(err.Error(), string(dick.ID)) {
		t.Fatalf("the refusal does not name the account it went into: %v", err)
	}
	wantRefusal(t, "a stage over a hand-made plan", f.stageMerge(actor, f.rawPlan(jason.ID, tim.ID)), auth.RefusedAlreadyMerged)
	if got := f.reload(jason.ID); got.Version != gone.Version || got.MergedInto != dick.ID {
		t.Fatalf("a refused second merge changed the source: %+v", got)
	}
	if got := f.reload(tim.ID); got.Version != tim.Version {
		t.Fatalf("a refused second merge changed the target: %+v", got)
	}
}

// TestUserMerge_PlanPins: the plan pins both users' versions and every row the
// preview read. Whatever changed since (either user, a key added or revoked, a
// linked sign-in), the stage is refused with ErrMergeConflict and writes nothing;
// a fresh plan lands.
func TestUserMerge_PlanPins(t *testing.T) {
	changes := map[string]func(f *fx, jason, dick *auth.User){
		"the target": func(f *fx, _, dick *auth.User) {
			f.edit(dick, func(u *auth.User) { u.DisplayName = "Nightwing" })
		},
		"the source": func(f *fx, jason, _ *auth.User) {
			f.edit(jason, func(u *auth.User) { u.DisplayName = "Red Hood" })
		},
		"a key the source gained": func(f *fx, jason, _ *auth.User) {
			f.mint(f.session(f.reload(jason.ID)), jason.ID, "gotham:patrol")
		},
		"a sign-in the source gained": func(f *fx, jason, _ *auth.User) {
			f.linkIdentity(jason, "wayne-sso", "sub-red-hood")
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			bruce := f.user("bruce-wayne", rbac.RoleAdmin)
			actor := f.signedIn(bruce)
			jason := f.user("jason-todd", roleCommissioner)
			dick := f.signable(f.user("dick-grayson", "gotham.detective"))
			f.mint(f.session(jason), jason.ID, "gotham:patrol")
			plan := f.planMerge(actor, jason.ID, dick.ID)
			change(f, jason, dick)
			src, tgt := f.reload(jason.ID), f.reload(dick.ID)

			wantErr(t, "a stage over a plan read before "+name+" changed", f.stageMerge(actor, plan), auth.ErrMergeConflict)
			if got := f.reload(jason.ID); got.Version != src.Version || got.Status != auth.StatusActive || got.MergedInto != "" {
				t.Fatalf("a refused stage changed the source: %+v", got)
			}
			if got := f.reload(dick.ID); got.Version != tgt.Version {
				t.Fatalf("a refused stage changed the target: %+v", got)
			}
			if err := f.stageMerge(actor, f.planMerge(actor, jason.ID, dick.ID)); err != nil {
				t.Fatalf("a fresh plan: %v", err)
			}
		})
	}
}

// TestUserMerge_AdminSourceIntoAdminTarget: a second admin merged into an admin
// that gains the source's other roles. The last-admin rule holds because the
// target is an admin who stays, the summary says the target holds admin, and
// the target ends with the union of the roles.
func TestUserMerge_AdminSourceIntoAdminTarget(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	alfred := f.user("alfred-pennyworth", rbac.RoleAdmin, "gotham.detective")
	dick := f.signable(f.user("dick-grayson", rbac.RoleAdmin))
	actor := f.signedIn(bruce)

	plan, sum, err := f.preview(actor, alfred.ID, dick.ID)
	if err != nil {
		t.Fatalf("PlanUserMerge: %v", err)
	}
	if !sum.TargetHoldsAdmin || !slices.Equal(sum.RolesTransferred, []rbac.RoleName{"gotham.detective"}) {
		t.Fatalf("summary = %+v", sum)
	}
	if err := f.stageMerge(actor, plan); err != nil {
		t.Fatalf("StageUserMerge: %v", err)
	}
	if got := f.reload(dick.ID); !slices.Equal(got.Roles, []rbac.RoleName{rbac.RoleAdmin, "gotham.detective"}) {
		t.Fatalf("target roles = %v", got.Roles)
	}
	if got := f.reload(alfred.ID); got.Status != auth.StatusDisabled || got.MergedInto != dick.ID {
		t.Fatalf("source = %+v", got)
	}
}

// TestUserMerge_ConcurrentMergesOfOneSource: two merges of the same source into
// two different targets, planned before either runs, race to commit. Exactly one
// lands; the other is refused as a conflict, and the source's verified address
// is on exactly one target afterwards.
func TestUserMerge_ConcurrentMergesOfOneSource(t *testing.T) {
	for range 8 {
		f := newFx(t)
		bruce := f.user("bruce-wayne", rbac.RoleAdmin)
		actor := f.signedIn(bruce)
		jason := f.setMail(f.user("jason-todd", "gotham.detective"), oracleMail, true)
		dick := f.signable(f.user("dick-grayson", "gotham.detective"))
		tim := f.signable(f.user("tim-drake", "gotham.detective"))
		plans := []auth.UserMergePlan{f.planMerge(actor, jason.ID, dick.ID), f.planMerge(actor, jason.ID, tim.ID)}

		errs := make([]error, len(plans))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range plans {
			wg.Go(func() {
				<-start
				errs[i] = f.stageMerge(actor, plans[i])
			})
		}
		close(start)
		wg.Wait()

		landed := 0
		for _, err := range errs {
			switch {
			case err == nil:
				landed++
			case errors.Is(err, storage.ErrConflict), errors.Is(err, auth.ErrMergeConflict), errors.Is(err, auth.ErrMergeRefused):
			default:
				t.Fatalf("a losing merge failed for the wrong reason: %v", err)
			}
		}
		if landed != 1 {
			t.Fatalf("%d of two concurrent merges of one source landed (%v), want exactly 1", landed, errs)
		}
		if n := f.verifiedHolders(oracleMail); n != 1 {
			t.Fatalf("%d accounts hold the address verified after two concurrent merges, want 1", n)
		}
		src := f.reload(jason.ID)
		if src.Status != auth.StatusDisabled || (src.MergedInto != dick.ID && src.MergedInto != tim.ID) {
			t.Fatalf("source = %+v", src)
		}
	}
}

// TestUserMerge_AfterCommitReportsOnlyTheLandedMerge: the merge's AfterCommit
// publishes the source's invalidation, and tells rbac about the moved roles,
// only when the store shows the merge; staged and never committed, it does
// neither.
func TestUserMerge_AfterCommitReportsOnlyTheLandedMerge(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	actor := f.signedIn(bruce)
	ctx := auth.WithPrincipal(context.Background(), actor)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.signable(f.user("dick-grayson", roleCommissioner))
	f.inval.take()
	f.genLog.take()

	b := f.st.NewBatch()
	after, err := f.svc.StageUserMerge(ctx, b, f.planMerge(actor, jason.ID, dick.ID))
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	after(ctx)
	if got, gens := f.inval.take(), f.genLog.take(); len(got) != 0 || len(gens) != 0 {
		t.Fatalf("a merge that never committed published %v and reported %v", got, gens)
	}

	if err := f.stageMerge(actor, f.planMerge(actor, jason.ID, dick.ID)); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, "the landed merge", f.inval.take(), invEvent{string(jason.ID), "", "user.disabled"})
	if gens := f.genLog.take(); len(gens) != 1 {
		t.Fatalf("the landed merge reported %v, want one generation", gens)
	}
}

// TestUserMerge_TargetChangedBeforeTheCommit: the target is written or guarded
// in the merge's batch, so a change to it between staging and the commit (it is
// disabled, its record edited) fails the commit with storage.ErrConflict and
// nothing lands, whether the merge rewrites the target (it gains a role) or
// only guards it.
func TestUserMerge_TargetChangedBeforeTheCommit(t *testing.T) {
	for name, tgtRoles := range map[string][]rbac.RoleName{
		"the target is rewritten": {roleCommissioner}, "the target is only guarded": {"gotham.detective"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			ctx := context.Background()
			bruce := f.user("bruce-wayne", rbac.RoleAdmin)
			actor := f.signedIn(bruce)
			jason := f.user("jason-todd", "gotham.detective")
			dick := f.signable(f.user("dick-grayson", tgtRoles...))
			plan := f.planMerge(actor, jason.ID, dick.ID)

			b := f.st.NewBatch()
			defer func() { _ = b.Close() }()
			if _, err := f.svc.StageUserMerge(auth.WithPrincipal(ctx, actor), b, plan); err != nil {
				t.Fatal(err)
			}
			f.edit(dick, func(u *auth.User) { u.Status = auth.StatusDisabled })
			wantErr(t, "a commit after the target was disabled", b.Commit(ctx, storage.Sync), storage.ErrConflict)
			if got := f.reload(jason.ID); got.Status != auth.StatusActive || got.MergedInto != "" {
				t.Fatalf("the source after a failed commit: %+v", got)
			}
		})
	}
}

// mergeRowsHook wraps the store a service reads through and runs onFirst after
// the first MergeRows read of an evaluation, the point after the plan's pins
// have been checked and before the e-mail scan: a writer that lands there is
// the interleaving evalMerge's reads (not one snapshot) cannot rule out.
type mergeRowsHook struct {
	auth.Store
	onFirst func(rows []auth.MergeRow, err error) ([]auth.MergeRow, error)
}

func (h *mergeRowsHook) MergeRows(ctx context.Context, id auth.UserID) ([]auth.MergeRow, error) {
	rows, err := h.Store.MergeRows(ctx, id)
	if fn := h.onFirst; fn != nil {
		h.onFirst = nil
		return fn(rows, err)
	}
	return rows, err
}

// TestUserMerge_PlanStaleDuringEvaluation: a refusal that follows the pin check
// is reported as ErrMergeConflict when the plan went stale while the evaluation
// ran, whatever the refusal, and as itself otherwise. The interleaving is forced
// with a hook on the store, not hoped for.
func TestUserMerge_PlanStaleDuringEvaluation(t *testing.T) {
	t.Run("the winner commits between the pin check and the e-mail scan", func(t *testing.T) {
		f := newFx(t)
		bruce := f.user("bruce-wayne", rbac.RoleAdmin)
		actor := f.signedIn(bruce)
		jason := f.setMail(f.user("jason-todd", "gotham.detective"), oracleMail, true)
		dick := f.signable(f.user("dick-grayson", "gotham.detective"))
		tim := f.signable(f.user("tim-drake", "gotham.detective"))
		winner := f.planMerge(actor, jason.ID, dick.ID)
		loser := f.planMerge(actor, jason.ID, tim.ID)

		hook := &mergeRowsHook{Store: f.st}
		hook.onFirst = func(rows []auth.MergeRow, err error) ([]auth.MergeRow, error) {
			if werr := f.stageMerge(actor, winner); werr != nil {
				t.Errorf("the winning merge: %v", werr)
			}
			return rows, err // the loser read its rows before the winner landed
		}
		ctx := auth.WithPrincipal(context.Background(), actor)
		b := f.st.NewBatch()
		defer func() { _ = b.Close() }()
		after, err := f.over(hook).StageUserMerge(ctx, b, loser)
		if !errors.Is(err, auth.ErrMergeConflict) {
			t.Fatalf("the losing merge = %v, want ErrMergeConflict (the plan is stale), not the e-mail refusal the winner's commit caused", err)
		}
		if errors.Is(err, auth.ErrEmailInUse) || after != nil {
			t.Fatalf("the losing merge = %v, after %v", err, after != nil)
		}
		if got := f.reload(jason.ID); got.MergedInto != dick.ID {
			t.Fatalf("source = %+v, want merged into the winner's target", got)
		}
		if got := f.reload(tim.ID); got.Version != tim.Version {
			t.Fatalf("the losing target was written: %+v", got)
		}
	})

	t.Run("a record changed while the refusal was decided", func(t *testing.T) {
		f := newFx(t)
		bruce := f.user("bruce-wayne", rbac.RoleAdmin)
		actor := f.signedIn(bruce)
		jason := f.user("jason-todd", "gotham.detective")
		dick := f.user("dick-grayson", "gotham.detective") // no password, no sign-in: the merge is refused
		f.mint(f.session(f.reload(jason.ID)), jason.ID, "gotham:patrol")
		plan := f.rawPlan(jason.ID, dick.ID) // read before the key existed: no rows

		hook := &mergeRowsHook{Store: f.st}
		hook.onFirst = func([]auth.MergeRow, error) ([]auth.MergeRow, error) { return nil, nil } // the stale read
		ctx := auth.WithPrincipal(context.Background(), actor)
		b := f.st.NewBatch()
		defer func() { _ = b.Close() }()
		_, err := f.over(hook).StageUserMerge(ctx, b, plan)
		if !errors.Is(err, auth.ErrMergeConflict) {
			t.Fatalf("a merge refused over rows that moved = %v, want ErrMergeConflict", err)
		}
	})

	t.Run("nothing changed: the refusal stands", func(t *testing.T) {
		f := newFx(t)
		bruce := f.user("bruce-wayne", rbac.RoleAdmin)
		actor := f.signedIn(bruce)
		jason := f.user("jason-todd", "gotham.detective")
		dick := f.user("dick-grayson", "gotham.detective")
		plan := f.rawPlan(jason.ID, dick.ID)
		err := f.stageMerge(actor, plan)
		if got := refusalCode(err); got != auth.RefusedNoSignIn {
			t.Fatalf("a refused merge over an unchanged plan = %v, want the %s refusal", err, auth.RefusedNoSignIn)
		}
		if errors.Is(err, auth.ErrMergeConflict) {
			t.Fatalf("an unchanged plan reported as stale: %v", err)
		}
	})
}
