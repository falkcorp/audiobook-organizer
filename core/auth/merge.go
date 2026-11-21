// file: core/auth/merge.go
// version: 1.2.0
// guid: 2bd811e8-1e8d-440b-990a-c71b06517f58
// last-edited: 2026-11-11

package auth

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// reasonMerged is the reason core.user.disable records when a merge disabled
// the source user.
const reasonMerged = "merged"

// Admin is the actor-checked identity participant handed to modules as
// module.Deps.AuthAdmin (05-authn §3.1, 06 §6.4.4). *Service implements it.
// Every method re-checks the ctx principal, so holding it grants nothing;
// each joins the caller's batch and never commits.
type Admin interface {
	StageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) (AfterCommit, error)
}

// AfterCommit is what a staging method hands back for the caller to run once
// the caller's batch has committed: auth cannot see that commit, so it cannot
// publish core.principal.invalidated.v1 for it on its own (§4.12). Calling it
// after a commit that failed publishes nothing: it re-reads what the batch was
// to write and publishes only what the store holds. It never fails and never
// blocks; calling it twice publishes twice.
type AfterCommit func(ctx context.Context)

var _ Admin = (*Service)(nil)

// StageUserMerge is Admin.StageUserMerge (05-authn §3.1): it stages into the
// caller's batch b the auth half of an admin user merge. The ctx principal
// is re-checked, so holding the handle grants nothing: core:users.manage
// through an interactive method, dominance over both users, and §4.8's
// owner rule (ownerSatisfied). Then, in b: the last-admin rule for disabling
// Source (05-rbac §4.6); Store.StageMerge, which re-owns every record of
// plan with an Expect on the bytes the plan read; Source disabled with its
// credential epoch bumped (user.disabled and credential_epoch.bumped
// journaled); and every open invite Source created deleted, not re-owned
// (DATA4-9), and every temp-login link it created (stageDeleteCreatedLinks),
// the counts listed in the core.user.disable audit record. Target must be
// active: merging into a disabled or pending account would hand its records
// to a user who cannot sign in. An invite or link Source creates after the
// scan escapes the deletion, but it is refused on use: its creator is
// disabled and its epoch moved (inviteRefusal, linkCreatorRefusal). The
// caller commits; a record changed since the plan was
// built fails the commit with storage.ErrConflict, which the caller reports
// as ErrMergeConflict. After its commit the caller runs the returned
// AfterCommit, which publishes Source's invalidation (every credential: Source
// is disabled); the step is nil whenever the error is not.
func (s *Service) StageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) (AfterCommit, error) {
	epoch, err := s.stageUserMerge(ctx, b, plan)
	if err != nil {
		return nil, err
	}
	return s.mergeCommitted(plan.Source, epoch), nil
}

// mergeCommitted is StageUserMerge's AfterCommit: it publishes Source's
// user-wide invalidation only when the store shows the merge's write, Source
// disabled at the bumped epoch or later, so a caller whose commit failed and
// who runs it anyway publishes nothing.
func (s *Service) mergeCommitted(src UserID, epoch int64) AfterCommit {
	return func(ctx context.Context) {
		u, err := s.d.Store.GetUser(ctx, src)
		if err != nil || u.Status != StatusDisabled || u.CredentialEpoch < epoch {
			return
		}
		inv := &invalidations{}
		inv.user(src, journal.UserDisabled)
		s.publishInvalidations(ctx, inv)
	}
}

// stageUserMerge stages the merge into b and returns Source's bumped epoch.
func (s *Service) stageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) (int64, error) {
	if b == nil {
		return 0, fmt.Errorf("%w: a merge stages into the caller's batch", ErrBadInput)
	}
	actor, _ := PrincipalFrom(ctx)
	if err := s.mayManageUsers(actor, "merge users"); err != nil {
		return 0, err
	}
	if !s.ownerSatisfied(actor) {
		return 0, fmt.Errorf("%w: merging users needs the deployment owner", ErrDenied)
	}
	if plan.Source == "" || plan.Target == "" || plan.Source == plan.Target {
		return 0, fmt.Errorf("%w: a merge needs two distinct users", ErrBadInput)
	}
	src, err := s.d.Store.GetUser(ctx, plan.Source)
	if err != nil {
		return 0, err
	}
	tgt, err := s.d.Store.GetUser(ctx, plan.Target)
	if err != nil {
		return 0, err
	}
	for _, u := range []*User{src, tgt} {
		if err := mutable(u); err != nil {
			return 0, err
		}
		if err := s.dominates(actor, u); err != nil {
			return 0, err
		}
	}
	if tgt.Status != StatusActive {
		return 0, fmt.Errorf("%w: the merge target %s is %s, not active", ErrInactiveUser, tgt.ID, tgt.Status)
	}
	if err := s.keepAnAdmin(ctx, b, src, src.Roles, StatusDisabled); err != nil {
		return 0, err
	}
	if err := s.d.Store.StageMerge(b, plan); err != nil {
		return 0, err
	}
	up := *src
	up.Status = StatusDisabled
	up.Version = src.Version + 1
	up.CredentialEpoch = src.CredentialEpoch + 1
	up.UpdatedAt = s.now().UTC()
	if err := s.d.Store.PutUser(b, &up); err != nil {
		return 0, err
	}
	deleted := 0
	for t, err := range s.d.Store.ScanOneTime(ctx, OneTimeInvite) {
		if err != nil {
			if s.leftUndecodable("onetime", src.ID, err) {
				continue
			}
			return 0, err
		}
		if t.CreatedBy != src.ID {
			continue
		}
		if err := s.d.Store.DeleteOneTime(b, OneTimeInvite, t.ID); err != nil {
			return 0, err
		}
		deleted++
	}
	links, err := s.deleteCreatedLinks(ctx, b, src.ID)
	if err != nil {
		return 0, err
	}
	subject := ref(refKindUser, string(src.ID))
	s.journalLine(ctx, b, journal.Revocation{Kind: journal.UserDisabled, Subject: subject, Epoch: up.CredentialEpoch})
	s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: subject, Epoch: up.CredentialEpoch})
	return up.CredentialEpoch, s.d.Audit.RecordIn(ctx, b, audit.Event{
		Action: actionUserDisable, Outcome: audit.Success, Target: subject, Reason: reasonMerged,
		Details: map[string]any{"into": string(tgt.ID), "invites_deleted": deleted, "links_deleted": links, "rows": len(plan.Rows)},
	})
}

// ownerSatisfied is §4.8's owner rule for an owner-guarded action (user
// merge): the principal carries AssuranceOwner, or, when the owner gate is
// not required, it holds core:admin through an interactive method.
func (s *Service) ownerSatisfied(p *Principal) bool {
	if p.Assurance&AssuranceOwner != 0 {
		return true
	}
	return !s.ownerRequired() && s.interactive(p.Method) && p.Perms.Has(rbac.CoreAdmin)
}

// ownerRequired is core.auth.owner.required: the configured value, else
// true exactly when an OwnerVerifier extension is configured and an owner
// identity is set (§4.8, SEC4-14: HEAD gates on "owner set").
func (s *Service) ownerRequired() bool {
	if v := s.settings().OwnerRequired; v != nil {
		return *v
	}
	for _, e := range s.extensions {
		if e.Spec.OwnerVerifier && e.OwnerSet() {
			return true
		}
	}
	return false
}
