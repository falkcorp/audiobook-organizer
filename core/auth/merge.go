// file: core/auth/merge.go
// version: 1.1.0
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
	StageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) error
}

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
// (DATA4-9), the count listed in the core.user.disable audit record. An
// invite Source creates after the scan escapes the deletion, but it is
// refused on accept: its creator is disabled and its epoch moved
// (inviteRefusal). The caller commits; a record changed since the plan was
// built fails the commit with storage.ErrConflict, which the caller reports
// as ErrMergeConflict.
func (s *Service) StageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) error {
	if b == nil {
		return fmt.Errorf("%w: a merge stages into the caller's batch", ErrBadInput)
	}
	actor, _ := PrincipalFrom(ctx)
	if err := s.mayManageUsers(actor, "merge users"); err != nil {
		return err
	}
	if !s.ownerSatisfied(actor) {
		return fmt.Errorf("%w: merging users needs the deployment owner", ErrDenied)
	}
	if plan.Source == "" || plan.Target == "" || plan.Source == plan.Target {
		return fmt.Errorf("%w: a merge needs two distinct users", ErrBadInput)
	}
	src, err := s.d.Store.GetUser(ctx, plan.Source)
	if err != nil {
		return err
	}
	tgt, err := s.d.Store.GetUser(ctx, plan.Target)
	if err != nil {
		return err
	}
	for _, u := range []*User{src, tgt} {
		if err := mutable(u); err != nil {
			return err
		}
		if err := s.dominates(actor, u); err != nil {
			return err
		}
	}
	if err := s.keepAnAdmin(ctx, b, src, src.Roles, StatusDisabled); err != nil {
		return err
	}
	if err := s.d.Store.StageMerge(b, plan); err != nil {
		return err
	}
	up := *src
	up.Status = StatusDisabled
	up.Version = src.Version + 1
	up.CredentialEpoch = src.CredentialEpoch + 1
	up.UpdatedAt = s.now().UTC()
	if err := s.d.Store.PutUser(b, &up); err != nil {
		return err
	}
	deleted := 0
	for t, err := range s.d.Store.ScanOneTime(ctx, OneTimeInvite) {
		if err != nil {
			if s.leftUndecodable("onetime", src.ID, err) {
				continue
			}
			return err
		}
		if t.CreatedBy != src.ID {
			continue
		}
		if err := s.d.Store.DeleteOneTime(b, OneTimeInvite, t.ID); err != nil {
			return err
		}
		deleted++
	}
	links, err := s.deleteCreatedLinks(ctx, b, src.ID)
	if err != nil {
		return err
	}
	subject := ref(refKindUser, string(src.ID))
	s.journalLine(ctx, b, journal.Revocation{Kind: journal.UserDisabled, Subject: subject, Epoch: up.CredentialEpoch})
	s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: subject, Epoch: up.CredentialEpoch})
	return s.d.Audit.RecordIn(ctx, b, audit.Event{
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
