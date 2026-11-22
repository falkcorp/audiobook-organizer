// file: core/auth/merge.go
// version: 1.2.0
// guid: 2bd811e8-1e8d-440b-990a-c71b06517f58
// last-edited: 2026-11-11

package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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
// Every method re-checks the principal bound on ctx, so holding it grants
// nothing. PlanUserMerge and StageUserMerge stage into the caller's batch and
// never commit; the account methods of InviteAdmin and AccountAdmin (invites,
// set-email, the admin user reads), which Admin embeds, are self-contained
// writes that commit in their own batch with their audit record in it (F4-08
// B10t).
type Admin interface {
	InviteAdmin
	AccountAdmin

	// PlanUserMerge previews folding source into target: it re-checks the
	// principal exactly as StageUserMerge does, reads the records the merge
	// moves, and returns the opaque plan StageUserMerge takes with a summary
	// that holds no credential. It stages and writes nothing.
	PlanUserMerge(ctx context.Context, source, target UserID) (UserMergePlan, MergeSummary, error)
	StageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) (AfterCommit, error)
	// PlanUserUnmerge and StageUserUnmerge undo a merge (unmerge.go); the same
	// split of preview and staging, the same re-check of the ctx principal.
	PlanUserUnmerge(ctx context.Context, rec UnmergeRecord) (UserUnmergePlan, UnmergeSummary, error)
	StageUserUnmerge(ctx context.Context, b *storage.Batch, plan UserUnmergePlan) (AfterCommit, error)
}

// AfterCommit is what a staging method hands back for the caller to run once
// the caller's batch has committed: auth cannot see that commit, so it cannot
// publish core.principal.invalidated.v1 for it on its own (§4.12). Calling it
// after a commit that failed publishes nothing: it re-reads what the batch was
// to write and publishes only what the store holds. It never fails and never
// blocks; calling it twice publishes twice.
type AfterCommit func(ctx context.Context)

var _ Admin = (*Service)(nil)

// PlanUserMerge is Admin.PlanUserMerge. It runs every refusal StageUserMerge
// runs (mergeEval), so a merge that would be refused is refused here, and a
// plan that comes back is one StageUserMerge accepts while nothing it read
// changes. The guards the evaluation stages (the role-set guard, the last-admin
// holder's guard) go into a scratch batch that is discarded.
// A scratch batch that fails to close fails the preview: no plan comes back
// whose evaluation did not end cleanly.
func (s *Service) PlanUserMerge(ctx context.Context, source, target UserID) (plan UserMergePlan, sum MergeSummary, err error) {
	scratch := s.d.NewBatch()
	defer func() {
		if cerr := scratch.Close(); cerr != nil && err == nil {
			plan, sum, err = UserMergePlan{}, MergeSummary{}, cerr
		}
	}()
	ev, err := s.evalMerge(ctx, scratch, source, target, nil)
	if err != nil {
		return UserMergePlan{}, MergeSummary{}, err
	}
	plan = UserMergePlan{
		source: ev.src.ID, target: ev.tgt.ID, srcVersion: ev.src.Version, tgtVersion: ev.tgt.Version, rows: ev.rows,
	}
	sum = MergeSummary{
		Source: ev.src.ID, Target: ev.tgt.ID, RolesTransferred: slices.Clone(ev.added),
		TargetHoldsAdmin: ev.tgtAdmin, Email: ev.email, APIKeys: []string{}, IdentityProviders: []string{},
	}
	if plan.receipt, err = s.mergeReceipt(ctx, ev.rows); err != nil {
		return UserMergePlan{}, MergeSummary{}, err
	}
	for _, r := range ev.rows {
		switch r.Kind {
		case MergeAPIKey:
			sum.APIKeys = append(sum.APIKeys, r.ID)
		case MergeIdentity:
			provider, _, _ := strings.Cut(r.ID, "/")
			sum.IdentityProviders = append(sum.IdentityProviders, provider)
		case MergeInviteAllow:
			sum.AllowListRows++
		}
	}
	return plan, sum, nil
}

// StageUserMerge is Admin.StageUserMerge (05-authn §3.1): it stages into the
// caller's batch b the auth half of an admin user merge. The ctx principal
// is re-checked, so holding the handle grants nothing: core:users.manage
// through an interactive method, dominance over both users, and §4.8's
// owner rule (ownerSatisfied). Every refusal runs before the first write
// (mergeEval): both users and every row the plan read must be as the plan
// read them (ErrMergeConflict), the last-admin rule for disabling Source
// (05-rbac §4.6), the roles Source holds and Target does not must be ones the
// principal may assign (dominance over Source, then the stored role set), the
// e-mail rule and the no-sign-in rule. Then, in b:
// Store.StageMerge, which re-owns every record of the plan with an Expect on
// the bytes the plan read; Target with the transferred roles and its e-mail
// outcome (or a guard on it when nothing changes); Source disabled, marked
// merged into Target, its address cleared and its credential epoch bumped
// (user.disabled and credential_epoch.bumped journaled); and every open
// invite Source created deleted, not re-owned (DATA4-9), and every temp-login
// link it created (deleteCreatedLinks), the counts listed in the
// core.user.disable audit record. Target must be active: merging into a
// disabled or pending account would hand its records to a user who cannot sign
// in. An invite or link Source creates after the scan escapes the deletion,
// but it is refused on use: its creator is disabled and its epoch moved
// (inviteRefusal, linkCreatorRefusal). The caller commits; a record changed
// since the plan was built fails the commit with storage.ErrConflict, which
// the caller reports as ErrMergeConflict. After its commit the caller runs the
// returned AfterCommit, which publishes Source's invalidation (every
// credential: Source is disabled) and, when roles moved, tells rbac that
// assignments changed; the step is nil whenever the error is not.
func (s *Service) StageUserMerge(ctx context.Context, b *storage.Batch, plan UserMergePlan) (AfterCommit, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: a merge stages into the caller's batch", ErrBadInput)
	}
	ev, err := s.evalMerge(ctx, b, plan.source, plan.target, &plan)
	if err != nil {
		return nil, err
	}
	epoch, err := s.stageUserMerge(ctx, b, ev)
	if err != nil {
		return nil, err
	}
	return s.mergeCommitted(plan.source, plan.target, ev.src.Email, epoch, len(ev.added) > 0), nil
}

// mergeCommitted is StageUserMerge's AfterCommit: it publishes Source's
// user-wide invalidation, and tells rbac that assignments changed when the
// merge moved roles, only when the store shows the merge's write, Source
// disabled at the bumped epoch or later and marked merged into Target. A
// caller whose commit failed and who runs it anyway publishes nothing.
//
// It also fires the registered Lifecycle.UserMergedAway for Source, on that
// same condition. srcEmail is the address Source held when the merge was
// evaluated, because the merge clears it from the committed row.
func (s *Service) mergeCommitted(src, tgt UserID, srcEmail string, epoch int64, rolesMoved bool) AfterCommit {
	return func(ctx context.Context) {
		u, err := s.d.Store.GetUser(ctx, src)
		if err != nil || u.Status != StatusDisabled || u.CredentialEpoch < epoch || u.MergedInto != tgt {
			return
		}
		inv := &invalidations{}
		inv.user(src, journal.UserDisabled)
		s.publishInvalidations(ctx, inv)
		if rolesMoved {
			s.d.RBAC.AssignmentsChanged()
		}
		s.queueUserMergedAway(inv, UserMergedAway{UserID: src, Email: srcEmail, MergedInto: tgt})
		afterCommit(inv.post).run(ctx)
	}
}

// mergeEval is what a merge reads and decides before it writes anything.
type mergeEval struct {
	src, tgt *User
	tgtAfter User // the target as the merge leaves it
	added    []rbac.RoleName
	email    MergeEmailOutcome
	rows     []MergeRow
	tgtAdmin bool // the target holds core:admin before the merge
}

// mergeRefusal is a MergeRefusal as an error.
func mergeRefusal(code, format string, args ...any) error {
	return &MergeRefusal{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// evalMerge reads both users and the rows the merge moves, runs every refusal
// and works out the target's end state. b receives the guards the last-admin
// and role-claim rules stage (never a data write); plan is nil for a preview
// and the plan being staged otherwise, whose pinned versions and rows must
// still hold. The refusals, in order: the principal (manage, interactive,
// owner), the two ids, dominance over both users (which covers every role
// that moves), the plan's pins, a source already merged away, a target that is
// not active, the last-admin rule for disabling the source, the roles the
// stored role set no longer lets the principal assign, the e-mail rule (a verified address another account holds, a target an OIDC
// sign-in with that address would auto-link) and the no-sign-in rule.
func (s *Service) evalMerge(ctx context.Context, b *storage.Batch, source, target UserID, plan *UserMergePlan) (*mergeEval, error) {
	actor, _ := PrincipalFrom(ctx)
	if err := s.mayManageUsers(actor, "merge users"); err != nil {
		return nil, err
	}
	if !s.ownerSatisfied(actor) {
		return nil, fmt.Errorf("%w: merging users needs the deployment owner", ErrDenied)
	}
	if source == "" || target == "" || source == target {
		return nil, fmt.Errorf("%w: a merge needs two distinct users", ErrBadInput)
	}
	src, err := s.d.Store.GetUser(ctx, source)
	if err != nil {
		return nil, err
	}
	tgt, err := s.d.Store.GetUser(ctx, target)
	if err != nil {
		return nil, err
	}
	for _, u := range []*User{src, tgt} {
		if err := mutable(u); err != nil {
			return nil, err
		}
		if err := s.dominates(actor, u); err != nil {
			return nil, err
		}
	}
	if plan != nil && (src.Version != plan.srcVersion || tgt.Version != plan.tgtVersion) {
		return nil, fmt.Errorf("%w: %s or %s changed since the merge preview", ErrMergeConflict, src.ID, tgt.ID)
	}
	ev, err := s.evalPinned(ctx, b, actor, src, tgt, plan)
	if err != nil && plan != nil {
		err = s.stalePlanOr(ctx, plan, err)
	}
	return ev, err
}

// stalePlanOr is err, unless the plan went stale while the evaluation ran: a
// refusal that follows the pin check can be the effect of a merge that landed
// after src and tgt were read (the reads are not one snapshot), and the true
// cause is then the stale plan, ErrMergeConflict, not the refusal (a verified
// address "held elsewhere" by the winner of a race). The users and the rows are
// read again; a version or a row that moved since the plan means the plan is
// stale. A read that fails leaves err as it was.
func (s *Service) stalePlanOr(ctx context.Context, plan *UserMergePlan, err error) error {
	if errors.Is(err, ErrMergeConflict) {
		return err
	}
	src, serr := s.d.Store.GetUser(ctx, plan.source)
	if serr != nil {
		return err
	}
	tgt, terr := s.d.Store.GetUser(ctx, plan.target)
	if terr != nil {
		return err
	}
	if src.Version != plan.srcVersion || tgt.Version != plan.tgtVersion {
		return fmt.Errorf("%w: %s or %s changed since the merge preview (refused meanwhile: %v)", ErrMergeConflict, src.ID, tgt.ID, err)
	}
	rows, rerr := s.d.Store.MergeRows(ctx, src.ID)
	if rerr != nil {
		return err
	}
	if !plan.rowsMatch(rows) {
		return fmt.Errorf("%w: a record of %s changed since the merge preview (refused meanwhile: %v)", ErrMergeConflict, src.ID, err)
	}
	return err
}

// rowsMatch reports whether rows are the records the plan read, byte for byte.
func (p *UserMergePlan) rowsMatch(rows []MergeRow) bool {
	return slices.EqualFunc(rows, p.rows, func(a, b MergeRow) bool {
		return a.Kind == b.Kind && a.ID == b.ID && bytes.Equal(a.Seen, b.Seen)
	})
}

// evalPinned is evalMerge from the source-already-merged refusal on: src and
// tgt are read, authorised and (for a plan) at the versions the plan pinned.
func (s *Service) evalPinned(ctx context.Context, b *storage.Batch, actor *Principal, src, tgt *User, plan *UserMergePlan) (*mergeEval, error) {
	if src.MergedInto != "" {
		return nil, mergeRefusal(RefusedAlreadyMerged, "%s was already merged into %s", src.ID, src.MergedInto)
	}
	if tgt.Status != StatusActive {
		return nil, fmt.Errorf("%w: the merge target %s is %s, not active", ErrInactiveUser, tgt.ID, tgt.Status)
	}
	if err := s.keepAnAdmin(ctx, b, src, src.Roles, StatusDisabled); err != nil {
		return nil, err
	}
	rows, err := s.d.Store.MergeRows(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if plan != nil && !plan.rowsMatch(rows) {
		return nil, fmt.Errorf("%w: a record of %s changed since the merge preview", ErrMergeConflict, src.ID)
	}
	ev := &mergeEval{src: src, tgt: tgt, tgtAfter: *tgt, rows: rows}
	ev.added, _ = roleDiff(normalizeRoles(tgt.Roles), normalizeRoles(src.Roles))
	if len(ev.added) > 0 {
		// The roles move only under the principal's dominance: it dominates
		// Source above, so it holds every scope of every role Source holds, the
		// added ones included (rbac.Dominates and CanAssign are one check). The
		// same check against the stored roles, and the guard that fails the
		// commit when the role set moves, run in claimRoles below.
		ev.tgtAfter.Roles = normalizeRoles(slices.Concat(tgt.Roles, ev.added))
	}
	isAdmin := s.adminHolds(ctx)
	if ev.tgtAdmin, err = isAdmin(tgt.Roles, tgt.Status); err != nil {
		return nil, err
	}
	adminAfter, err := isAdmin(ev.tgtAfter.Roles, tgt.Status)
	if err != nil {
		return nil, err
	}
	ev.tgtAfter.Email, ev.tgtAfter.EmailVerified, ev.email = mergedEmail(src, tgt)
	if newlyVerified := ev.tgtAfter.EmailVerified && !(tgt.EmailVerified && SameEmail(tgt.Email, ev.tgtAfter.Email)); newlyVerified {
		taken, err := s.emailVerifiedElsewhere(ctx, ev.tgtAfter.Email, src.ID, tgt.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, fmt.Errorf("%w: another account holds the address the merge would give %s verified", ErrEmailInUse, tgt.ID)
		}
		if !adminAfter && !tgt.HasPassword {
			return nil, mergeRefusal(RefusedEmailLinkable,
				"%s is not an admin and has no password, so a sign-in with the address the merge would verify for it would auto-link to it", tgt.ID)
		}
	}
	signIns := 0
	for _, r := range rows {
		if r.Kind == MergeIdentity {
			signIns++
		}
	}
	if !tgt.HasPassword && signIns == 0 {
		for _, err := range s.d.Store.ScanIdentities(ctx, tgt.ID) {
			if err != nil {
				return nil, err
			}
			signIns++
			break
		}
	}
	if !tgt.HasPassword && signIns == 0 {
		return nil, mergeRefusal(RefusedNoSignIn, "%s would have no way to sign in: no password and no linked sign-in", tgt.ID)
	}
	if err := s.claimRoles(ctx, b, actor.Perms, ev.added); err != nil {
		return nil, scopeRefusal(err)
	}
	return ev, nil
}

// mergedEmail is HEAD's e-mail rule for a merge (internal/usermerge
// planAccounts), on core's one EmailVerified flag: the source's address is
// always cleared; the target keeps its own; when the target has none it takes
// the source's with the source's verification; and when both hold the same
// address and only the source had it verified, the target now holds it
// verified (otherwise nobody would, and the owner's next SSO sign-in with it
// would create a new account). A different verified address of the source is
// released.
func mergedEmail(src, tgt *User) (email string, verified bool, outcome MergeEmailOutcome) {
	switch {
	case tgt.Email != "":
		email, verified, outcome = tgt.Email, tgt.EmailVerified, MergeEmailKept
		same := src.Email != "" && SameEmail(src.Email, tgt.Email)
		switch {
		case same && src.EmailVerified && !tgt.EmailVerified:
			verified, outcome = true, MergeEmailVerifiedTransferred
		case src.Email != "" && !same && src.EmailVerified:
			outcome = MergeEmailReleasedVerified
		}
	case src.Email != "":
		email, verified, outcome = src.Email, src.EmailVerified, MergeEmailMoved
	default:
		outcome = MergeEmailNone
	}
	return email, verified, outcome
}

// stageUserMerge stages the writes of an evaluated merge into b and returns
// Source's bumped epoch.
func (s *Service) stageUserMerge(ctx context.Context, b *storage.Batch, ev *mergeEval) (int64, error) {
	src, tgt := ev.src, ev.tgt
	now := s.now().UTC()
	if err := s.d.Store.StageMerge(b, src.ID, tgt.ID, ev.rows); err != nil {
		return 0, err
	}
	if len(ev.added) > 0 || ev.tgtAfter.Email != tgt.Email || ev.tgtAfter.EmailVerified != tgt.EmailVerified {
		up := ev.tgtAfter
		up.Version = tgt.Version + 1
		up.UpdatedAt = now
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return 0, err
		}
	} else if err := s.d.Store.GuardUser(b, tgt); err != nil {
		return 0, err
	}
	up := *src
	up.Status = StatusDisabled
	up.Version = src.Version + 1
	up.CredentialEpoch = src.CredentialEpoch + 1
	up.UpdatedAt = now
	up.MergedInto, up.MergedAt = tgt.ID, now
	up.Email, up.EmailVerified = "", false
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
	if len(ev.added) > 0 {
		if err := s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserSetRoles, Outcome: audit.Success, Target: ref(refKindUser, string(tgt.ID)), Reason: reasonMerged,
			Details: map[string]any{"added": roleStrings(ev.added), "removed": []string{}, "from": string(src.ID)},
		}); err != nil {
			return 0, err
		}
	}
	return up.CredentialEpoch, s.d.Audit.RecordIn(ctx, b, audit.Event{
		Action: actionUserDisable, Outcome: audit.Success, Target: subject, Reason: reasonMerged,
		Details: map[string]any{
			"into": string(tgt.ID), "invites_deleted": deleted, "links_deleted": links, "rows": len(ev.rows),
			"roles_transferred": roleStrings(ev.added), "email": string(ev.email),
		},
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

// OwnerRequired is core.auth.owner.required as the service reads it per call:
// the one function stage 13's Owner guard, the owner-guarded routes' own
// handlers (storagehttp, backuphttp) and the merge rule all take, so no two of
// them can disagree about whether the owner gate is on.
func (s *Service) OwnerRequired() bool { return s.ownerRequired() }

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
