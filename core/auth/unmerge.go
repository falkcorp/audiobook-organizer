// file: core/auth/unmerge.go
// version: 0.1.0
// guid: c41e7d28-9b05-4f63-a8d2-6e13b7f09a54
// last-edited: 2026-11-22

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The reversal of an admin user merge (F4-08 B10p): Admin.PlanUserUnmerge and
// Admin.StageUserUnmerge undo what StageUserMerge did, and nothing else. They
// are the inverse of the merge for the records the merge moved, with one rule
// above all: a reversal is whole or it is refused. Every reason it cannot be
// whole is a refusal with a code, decided before the first write.
//
// What the merge left, and so what the reversal acts on, comes from the caller:
// UnmergeRecord is the facts a merge journal holds. Core treats them as claims.
// It acts only on what the stored records confirm: the source is still marked
// merged into the target and is at the version the merge left it, and a key or
// sign-in moves back only while the target still owns it (a key also only at
// the version the merge left it at).
//
// What a reversal does NOT bring back: the open invites and the temp-login
// links the source created (the merge deleted them, DATA4-9, and they hold
// nothing to restore), and the allow-list row of the source's address (the
// merge only pinned it, it never moved or removed it).

// The reasons of a MergeRefusal an unmerge adds (Code). The merge's own,
// ErrLastAdmin and the dominance and scope errors stay as they are.
const (
	// RefusedNotMerged: the source is not marked merged into the target (never
	// merged, already unmerged, or merged into another account).
	RefusedNotMerged = "not_merged"
	// RefusedSourceChanged: the source changed after the merge (its version is
	// not the one the merge left), or is not disabled.
	RefusedSourceChanged = "source_changed"
	// RefusedSourceDeleted and RefusedTargetDeleted: the account is gone.
	RefusedSourceDeleted = "source_deleted"
	RefusedTargetDeleted = "target_deleted"
	// RefusedTargetInactive: the target is disabled or pending.
	RefusedTargetInactive = "target_inactive"
	// RefusedTargetMerged: the target was itself merged into another account.
	RefusedTargetMerged = "target_merged"
	// RefusedEmailClaimed: the source's verified address is held verified by
	// another account now. Nobody is displaced to give it back.
	RefusedEmailClaimed = "email_claimed"
	// RefusedKeyMoved: an API key the merge moved is no longer the target's,
	// or has changed since the merge (status, expiry, name).
	RefusedKeyMoved = "key_moved"
	// RefusedSignInMoved: a sign-in the merge moved is no longer the target's.
	RefusedSignInMoved = "signin_moved"
)

const (
	actionUserUnmerge = "core.user.unmerge"
	reasonUnmerged    = "unmerged"
)

// UnmergeRecord is what the caller knows of the merge it asks core to undo: the
// facts a merge journal holds. Every field is an id, a code or a version; none
// is a credential.
type UnmergeRecord struct {
	// Source is the account the merge folded away, Target the one it folded
	// into.
	Source, Target UserID
	// SourceVersionAfter is the version the merge left Source at (its version
	// before the merge, plus one). A source at any other version changed since.
	SourceVersionAfter int64
	// SourceStatusBefore, SourceEmailBefore and SourceEmailVerifiedBefore are
	// Source as it was before the merge.
	SourceStatusBefore        UserStatus
	SourceEmailBefore         string
	SourceEmailVerifiedBefore bool
	// TargetEmailBefore and TargetEmailVerifiedBefore are Target's address
	// before the merge.
	TargetEmailBefore         string
	TargetEmailVerifiedBefore bool
	// EmailOutcome is what MergeSummary.Email said the merge did.
	EmailOutcome MergeEmailOutcome
	// RolesAdded are the roles the merge gave Target (MergeSummary.RolesTransferred).
	RolesAdded []rbac.RoleName
	// APIKeys is every key the merge moved, by id, with the version it has
	// once moved (MergeReceipt.APIKeyVersions).
	APIKeys map[string]int64
	// Identities is every sign-in the merge moved, as "<provider>/<subject>"
	// (MergeReceipt.IdentityIDs).
	Identities []string
}

// Format prints a record as its two ids, for every verb: Identities holds
// sign-in subjects, which %v, %+v, %#v and a log line must not show.
func (r UnmergeRecord) Format(f fmt.State, _ rune) {
	if _, err := fmt.Fprintf(f, "auth.UnmergeRecord{%s from %s, %d keys, %d sign-ins}", r.Source, r.Target, len(r.APIKeys), len(r.Identities)); err != nil {
		return
	}
}

// LogValue is the same redaction for slog.
func (r UnmergeRecord) LogValue() slog.Value {
	return slog.GroupValue(slog.String("source", string(r.Source)), slog.String("target", string(r.Target)),
		slog.Int("api_keys", len(r.APIKeys)), slog.Int("identities", len(r.Identities)))
}

var _ slog.LogValuer = UnmergeRecord{}

// UserUnmergePlan is the auth half of an unmerge, built by
// Admin.PlanUserUnmerge and handed back to Admin.StageUserUnmerge. Like
// UserMergePlan it is opaque and redacted when printed: the rows hold stored
// bytes of credential records. It pins both users' versions and the rows it
// read, so StageUserUnmerge refuses (ErrMergeConflict) when any of them moved
// since the preview. The zero value is no plan and is refused.
type UserUnmergePlan struct {
	rec        UnmergeRecord
	srcVersion int64
	tgtVersion int64
	rows       []MergeRow
}

// Source is the account the unmerge brings back.
func (p UserUnmergePlan) Source() UserID { return p.rec.Source }

// Target is the account the unmerge takes it back from.
func (p UserUnmergePlan) Target() UserID { return p.rec.Target }

// Format prints a plan as its two ids and how many records it holds, for every
// verb (see UserMergePlan.Format).
func (p UserUnmergePlan) Format(f fmt.State, _ rune) {
	if _, err := fmt.Fprintf(f, "auth.UserUnmergePlan{%s from %s, %d records}", p.rec.Source, p.rec.Target, len(p.rows)); err != nil {
		return
	}
}

// UnmergeSummary is what an unmerge preview may show an admin: ids, counts and
// outcome codes, never a credential.
type UnmergeSummary struct {
	Source UserID
	Target UserID
	// SourceStatus is the status the source gets back.
	SourceStatus UserStatus
	// RolesRemoved are the merge-added roles Target still holds and loses.
	RolesRemoved []rbac.RoleName
	// SourceEmailRestored is whether the source gets its address back;
	// TargetEmailReverted whether the target's address goes back to what it was
	// before the merge (false when the target changed it since).
	SourceEmailRestored bool
	TargetEmailReverted bool
	// APIKeys are the ids of the keys that move back; IdentityProviders the
	// provider of each sign-in that moves back.
	APIKeys           []string
	IdentityProviders []string
	// TargetSessionsEnded is whether the target's credential epoch is bumped
	// (a sign-in moved back, so a session opened through it must not outlive
	// it).
	TargetSessionsEnded bool
}

// MergeReceipt is what a merge plan knows of the records the merge moves and
// an unmerge needs: it is how a journal comes to say which sign-ins moved and
// what version each key has once it has moved. It is NOT part of MergeSummary:
// a sign-in's subject is an identifier the provider issued, and a preview
// (which the HTTP response carries) holds none. Read it with
// UserMergePlan.Receipt, write it only to the merge journal, and never print
// it.
type MergeReceipt struct {
	// IdentityIDs are the "<provider>/<subject>" ids of the sign-ins that move.
	IdentityIDs []string
	// APIKeyVersions is the version each moved key (by id) has once it has
	// moved: the version it had when the plan read it, plus the one StageMerge
	// writes it at. It is the version actually committed, because the plan's
	// Expect on each key's stored bytes fails the commit of a merge whose key
	// changed after the plan read it.
	APIKeyVersions map[string]int64
}

// Receipt is the plan's MergeReceipt (a copy). The zero plan has an empty one.
func (p UserMergePlan) Receipt() MergeReceipt {
	return MergeReceipt{
		IdentityIDs:    slices.Clone(p.receipt.IdentityIDs),
		APIKeyVersions: maps.Clone(p.receipt.APIKeyVersions),
	}
}

// mergeReceipt reads the receipt of the rows a merge plan holds.
func (s *Service) mergeReceipt(ctx context.Context, rows []MergeRow) (MergeReceipt, error) {
	r := MergeReceipt{IdentityIDs: []string{}, APIKeyVersions: map[string]int64{}}
	for _, row := range rows {
		switch row.Kind {
		case MergeIdentity:
			r.IdentityIDs = append(r.IdentityIDs, row.ID)
		case MergeAPIKey:
			k, err := s.d.Store.APIKeyByID(ctx, row.ID)
			if err != nil {
				return MergeReceipt{}, err
			}
			r.APIKeyVersions[row.ID] = k.Version + 1
		}
	}
	return r, nil
}

// PlanUserUnmerge is Admin.PlanUserUnmerge: it runs every refusal
// StageUserUnmerge runs, reads the records the reversal moves back, and returns
// the opaque plan StageUserUnmerge takes with a summary that holds no
// credential. It stages and writes nothing; the guards the evaluation stages
// go into a scratch batch that is discarded.
func (s *Service) PlanUserUnmerge(ctx context.Context, rec UnmergeRecord) (plan UserUnmergePlan, sum UnmergeSummary, err error) {
	scratch := s.d.NewBatch()
	defer func() {
		if cerr := scratch.Close(); cerr != nil && err == nil {
			plan, sum, err = UserUnmergePlan{}, UnmergeSummary{}, cerr
		}
	}()
	ev, err := s.evalUnmerge(ctx, scratch, rec, nil)
	if err != nil {
		return UserUnmergePlan{}, UnmergeSummary{}, err
	}
	plan = UserUnmergePlan{rec: ev.rec, srcVersion: ev.src.Version, tgtVersion: ev.tgt.Version, rows: ev.rows}
	sum = UnmergeSummary{
		Source: ev.src.ID, Target: ev.tgt.ID, SourceStatus: ev.srcAfter.Status, RolesRemoved: slices.Clone(ev.removed),
		SourceEmailRestored: ev.srcAfter.Email != "", TargetEmailReverted: ev.revertTarget,
		APIKeys: []string{}, IdentityProviders: []string{}, TargetSessionsEnded: ev.endTargetSessions,
	}
	for _, r := range ev.rows {
		switch r.Kind {
		case MergeAPIKey:
			sum.APIKeys = append(sum.APIKeys, r.ID)
		case MergeIdentity:
			provider, _, _ := strings.Cut(r.ID, "/")
			sum.IdentityProviders = append(sum.IdentityProviders, provider)
		}
	}
	return plan, sum, nil
}

// StageUserUnmerge is Admin.StageUserUnmerge: it stages into the caller's batch
// b the auth half of undoing an admin user merge, and never commits. The ctx
// principal is re-checked as StageUserMerge does (core:users.manage through an
// interactive method, the owner rule, dominance over both users), and every
// refusal runs before the first write (evalUnmerge). Then, in b:
//
//   - the keys and sign-ins the record names move back to Source
//     (Store.StageMerge in the other direction, each with an Expect on the bytes
//     the plan read);
//   - Target loses the roles the merge gave it (never one it held before), takes
//     back its own address when the merge changed it and it is as the merge left
//     it, and has its credential epoch bumped when a sign-in moved back;
//   - Source gets its status back, the merged-into marker cleared, its address
//     restored when nobody else holds it verified, and a fresh credential epoch
//     (user.enabled when it comes back active, credential_epoch.bumped, and a
//     role.unassigned line per removed role, journaled), and a core.user.unmerge
//     audit record.
//
// The caller commits; a record changed since the plan fails the commit with
// storage.ErrConflict, which the caller reports as ErrMergeConflict. The
// returned AfterCommit publishes what a commit that landed revokes (Target's
// sessions after an epoch bump; a moved key publishes nothing, as the merge
// published nothing for it: 05-authn's list names only what a write revoked, and
// a stream open on the key ends at its next revalidation), tells rbac
// when roles moved, and fires Lifecycle.StatusChanged for Source when its
// status really changed. It re-reads Source first and does nothing for a batch
// that did not commit. The step is nil whenever the error is not.
func (s *Service) StageUserUnmerge(ctx context.Context, b *storage.Batch, plan UserUnmergePlan) (AfterCommit, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: an unmerge stages into the caller's batch", ErrBadInput)
	}
	ev, err := s.evalUnmerge(ctx, b, plan.rec, &plan)
	if err != nil {
		return nil, err
	}
	epoch, err := s.stageUserUnmerge(ctx, b, ev)
	if err != nil {
		return nil, err
	}
	return s.unmergeCommitted(ev, epoch), nil
}

// unmergeEval is what an unmerge reads and decides before it writes anything.
type unmergeEval struct {
	rec               UnmergeRecord
	src, tgt          *User
	srcAfter          User
	tgtAfter          User
	rows              []MergeRow // the target's rows that move back, in the store's order
	removed           []rbac.RoleName
	revertTarget      bool // the target's address goes back to what it was
	endTargetSessions bool
}

func (s *Service) unmergeRefusalOf(err error, code, format string, args ...any) error {
	if errors.Is(err, ErrNotFound) {
		return mergeRefusal(code, format, args...)
	}
	return err
}

// evalUnmerge reads both users and the rows the unmerge moves back, runs every
// refusal and works out both users' end state. b receives only the guards the
// last-admin and role rules stage (never a data write); plan is nil for a
// preview and the plan being staged otherwise. The refusals, in order: the
// principal (manage, interactive, owner), the ids, an account that is gone,
// dominance over both users, the plan's pins, a source that is not marked
// merged into the target or changed since, a target that is merged away or not
// active, the keys and sign-ins that are no longer the target's, the roles the
// principal may not take back, the last-admin rule, and a verified address
// another account holds now.
func (s *Service) evalUnmerge(ctx context.Context, b *storage.Batch, rec UnmergeRecord, plan *UserUnmergePlan) (*unmergeEval, error) {
	actor, _ := PrincipalFrom(ctx)
	if err := s.mayManageUsers(actor, "unmerge users"); err != nil {
		return nil, err
	}
	if !s.ownerSatisfied(actor) {
		return nil, fmt.Errorf("%w: unmerging users needs the deployment owner", ErrDenied)
	}
	if rec.Source == "" || rec.Target == "" || rec.Source == rec.Target {
		return nil, fmt.Errorf("%w: an unmerge needs two distinct users", ErrBadInput)
	}
	switch rec.SourceStatusBefore {
	case StatusActive, StatusDisabled, StatusPending:
	default:
		return nil, fmt.Errorf("%w: status %q is not one an unmerge restores", ErrBadInput, rec.SourceStatusBefore)
	}
	src, err := s.d.Store.GetUser(ctx, rec.Source)
	if err != nil {
		return nil, s.unmergeRefusalOf(err, RefusedSourceDeleted, "the account the merge folded away no longer exists")
	}
	tgt, err := s.d.Store.GetUser(ctx, rec.Target)
	if err != nil {
		return nil, s.unmergeRefusalOf(err, RefusedTargetDeleted, "the account the merge folded into no longer exists")
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
		return nil, fmt.Errorf("%w: %s or %s changed since the unmerge preview", ErrMergeConflict, src.ID, tgt.ID)
	}
	ev, err := s.evalUnmergePinned(ctx, b, actor, rec, src, tgt)
	if err == nil && plan != nil && !(&UserMergePlan{rows: plan.rows}).rowsMatch(ev.rows) {
		return nil, fmt.Errorf("%w: a record of %s changed since the unmerge preview", ErrMergeConflict, tgt.ID)
	}
	return ev, err
}

func (s *Service) evalUnmergePinned(ctx context.Context, b *storage.Batch, actor *Principal, rec UnmergeRecord, src, tgt *User) (*unmergeEval, error) {
	if src.MergedInto != tgt.ID {
		return nil, mergeRefusal(RefusedNotMerged, "%s is not marked as merged into %s", src.ID, tgt.ID)
	}
	if src.Status != StatusDisabled || src.Version != rec.SourceVersionAfter {
		return nil, mergeRefusal(RefusedSourceChanged, "%s changed after the merge", src.ID)
	}
	if tgt.MergedInto != "" {
		return nil, mergeRefusal(RefusedTargetMerged, "%s was itself merged into another account", tgt.ID)
	}
	if tgt.Status != StatusActive {
		return nil, mergeRefusal(RefusedTargetInactive, "%s is %s, not active", tgt.ID, tgt.Status)
	}
	ev := &unmergeEval{rec: rec, src: src, tgt: tgt, srcAfter: *src, tgtAfter: *tgt}
	var err error
	if ev.rows, err = s.unmergeRows(ctx, rec, tgt); err != nil {
		return nil, err
	}
	ev.endTargetSessions = slices.ContainsFunc(ev.rows, func(r MergeRow) bool { return r.Kind == MergeIdentity })

	// The roles the merge gave the target come off it, only those it still
	// holds, and never one it held before: RolesAdded is by construction the
	// roles the target did not have.
	ev.removed = slices.DeleteFunc(slices.Clone(normalizeRoles(rec.RolesAdded)), func(r rbac.RoleName) bool {
		return !slices.Contains(tgt.Roles, r)
	})
	if len(ev.removed) > 0 {
		ev.tgtAfter.Roles = normalizeRoles(slices.DeleteFunc(slices.Clone(tgt.Roles), func(r rbac.RoleName) bool {
			return slices.Contains(ev.removed, r)
		}))
		if err := s.d.RBAC.CanAssign(actor.Perms, ev.removed); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrScopeEscalation, err)
		}
	}
	// The last-admin rule, with the source's own return counted: keepAnAdmin
	// scans stored users, where the source is still disabled.
	isAdmin := s.adminHolds(ctx)
	srcHoldsAdmin, err := isAdmin(src.Roles, rec.SourceStatusBefore)
	if err != nil {
		return nil, err
	}
	if srcHoldsAdmin {
		gen, err := s.roleSetGeneration(ctx)
		if err != nil {
			return nil, err
		}
		if err := s.d.RBAC.GuardRoleSet(b, gen); err != nil {
			return nil, err
		}
	} else if err := s.keepAnAdmin(ctx, b, tgt, ev.tgtAfter.Roles, tgt.Status); err != nil {
		return nil, err
	}

	if err := s.unmergeEmail(ctx, ev); err != nil {
		return nil, err
	}
	ev.srcAfter.Status = rec.SourceStatusBefore
	ev.srcAfter.MergedInto, ev.srcAfter.MergedAt = "", time.Time{}
	return ev, nil
}

// unmergeRows reads the target's records and keeps the ones the record names,
// each only while it is still as the merge left it: a key the target still
// owns at the version the merge left it at, a sign-in the target still owns.
// One record of the set that is not (moved again, deleted, changed) refuses
// the whole unmerge. The rows come in the store's order, so two reads of
// unchanged records are equal.
func (s *Service) unmergeRows(ctx context.Context, rec UnmergeRecord, tgt *User) ([]MergeRow, error) {
	all, err := s.d.Store.MergeRows(ctx, tgt.ID)
	if err != nil {
		return nil, err
	}
	signIns := map[string]bool{}
	for _, id := range rec.Identities {
		signIns[id] = true
	}
	var rows []MergeRow
	keys, found := 0, 0
	for _, r := range all {
		switch r.Kind {
		case MergeAPIKey:
			want, ok := rec.APIKeys[r.ID]
			if !ok {
				continue
			}
			k, err := s.d.Store.APIKeyByID(ctx, r.ID)
			if err != nil {
				return nil, s.unmergeRefusalOf(err, RefusedKeyMoved, "an API key the merge moved is no longer the target's")
			}
			if k.Owner != tgt.ID || k.Version != want {
				return nil, mergeRefusal(RefusedKeyMoved, "an API key the merge moved is no longer as the merge left it")
			}
			keys++
			rows = append(rows, r)
		case MergeIdentity:
			if signIns[r.ID] {
				found++
				rows = append(rows, r)
			}
		}
	}
	if keys != len(rec.APIKeys) {
		return nil, mergeRefusal(RefusedKeyMoved, "an API key the merge moved is no longer the target's")
	}
	if found != len(signIns) {
		return nil, mergeRefusal(RefusedSignInMoved, "a sign-in the merge moved is no longer the target's")
	}
	return rows, nil
}

// unmergeEmail decides the two addresses. The source's address is given back
// only while no other account holds it verified (an address is never taken from
// anyone), and the target's address goes back to what it was only while the
// merge's change to it still stands: when the merge moved or verified the
// source's address on the target and the target still holds it exactly so. A
// target that changed its address since keeps what it has.
func (s *Service) unmergeEmail(ctx context.Context, ev *unmergeEval) error {
	rec, tgt := ev.rec, ev.tgt
	if rec.SourceEmailBefore == "" {
		return nil
	}
	switch rec.EmailOutcome {
	case MergeEmailMoved, MergeEmailVerifiedTransferred:
		// The verification the merge left on the target's address: the source's
		// own for a moved address, set for a transferred one.
		left := rec.SourceEmailVerifiedBefore
		if rec.EmailOutcome == MergeEmailVerifiedTransferred {
			left = true
		}
		if SameEmail(tgt.Email, rec.SourceEmailBefore) && tgt.EmailVerified == left {
			ev.revertTarget = true
			ev.tgtAfter.Email, ev.tgtAfter.EmailVerified = rec.TargetEmailBefore, rec.TargetEmailVerifiedBefore
		}
	}
	if rec.SourceEmailVerifiedBefore {
		except := []UserID{ev.src.ID}
		if ev.revertTarget {
			except = append(except, tgt.ID)
		}
		taken, err := s.emailVerifiedElsewhere(ctx, rec.SourceEmailBefore, except...)
		if err != nil {
			return err
		}
		if taken {
			return mergeRefusal(RefusedEmailClaimed, "another account holds the address %s held verified before the merge", ev.src.ID)
		}
	}
	ev.srcAfter.Email, ev.srcAfter.EmailVerified = rec.SourceEmailBefore, rec.SourceEmailVerifiedBefore
	return nil
}

// stageUserUnmerge stages the writes of an evaluated unmerge into b and returns
// the source's bumped epoch.
func (s *Service) stageUserUnmerge(ctx context.Context, b *storage.Batch, ev *unmergeEval) (int64, error) {
	src, tgt := ev.src, ev.tgt
	now := s.now().UTC()
	if len(ev.rows) > 0 {
		// The merge's own row writer, the other way round: it re-owns each row
		// from its first user to its second, with an Expect on the bytes read.
		if err := s.d.Store.StageMerge(b, tgt.ID, src.ID, ev.rows); err != nil {
			return 0, err
		}
	}
	tgtEpoch := tgt.CredentialEpoch
	if len(ev.removed) > 0 || ev.revertTarget || ev.endTargetSessions {
		up := ev.tgtAfter
		up.Version = tgt.Version + 1
		up.UpdatedAt = now
		if ev.endTargetSessions {
			up.CredentialEpoch = tgt.CredentialEpoch + 1
			tgtEpoch = up.CredentialEpoch
		}
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return 0, err
		}
		if s.holdsUsersManage(tgt.Roles) && !s.holdsUsersManage(up.Roles) {
			// Target can no longer mint a link, so none it minted may be used.
			if _, err := s.deleteCreatedLinks(ctx, b, tgt.ID); err != nil {
				return 0, err
			}
		}
	} else if err := s.d.Store.GuardUser(b, tgt); err != nil {
		return 0, err
	}
	up := ev.srcAfter
	up.Version = src.Version + 1
	up.CredentialEpoch = src.CredentialEpoch + 1
	up.UpdatedAt = now
	if err := s.d.Store.PutUser(b, &up); err != nil {
		return 0, err
	}
	subject := ref(refKindUser, string(src.ID))
	if up.Status == StatusActive {
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.UserEnabled, Subject: subject, Epoch: up.CredentialEpoch})
	}
	s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: subject, Epoch: up.CredentialEpoch})
	tgtSubject := ref(refKindUser, string(tgt.ID))
	if ev.endTargetSessions {
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: tgtSubject, Epoch: tgtEpoch})
	}
	for _, r := range ev.removed {
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.RoleUnassigned, Subject: tgtSubject, Object: ref(refKindRole, string(r))})
	}
	if len(ev.removed) > 0 {
		if err := s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserSetRoles, Outcome: audit.Success, Target: tgtSubject, Reason: reasonUnmerged,
			Details: map[string]any{"added": []string{}, "removed": roleStrings(ev.removed), "from": string(src.ID)},
		}); err != nil {
			return 0, err
		}
	}
	keys, signIns := 0, 0
	for _, r := range ev.rows {
		if r.Kind == MergeAPIKey {
			keys++
		} else {
			signIns++
		}
	}
	return up.CredentialEpoch, s.d.Audit.RecordIn(ctx, b, audit.Event{
		Action: actionUserUnmerge, Outcome: audit.Success, Target: subject, Reason: reasonUnmerged,
		Details: map[string]any{
			"from": string(tgt.ID), "status": string(up.Status), "api_keys": keys, "sign_ins": signIns,
			"roles_removed": roleStrings(ev.removed), "email_restored": up.Email != "", "target_email_reverted": ev.revertTarget,
			"target_epoch_bumped": ev.endTargetSessions,
		},
	})
}

// unmergeCommitted is StageUserUnmerge's AfterCommit. Only when the store shows
// the unmerge's write (the source no longer marked merged, at the status it was
// given and the bumped epoch or later) does it publish: the target's sessions
// when its epoch moved, rbac's assignment change when roles came off, and the lifecycle's
// StatusChanged for the source when its status really changed. A caller whose
// commit failed and who runs it anyway fires nothing.
func (s *Service) unmergeCommitted(ev *unmergeEval, epoch int64) AfterCommit {
	return func(ctx context.Context) {
		u, err := s.d.Store.GetUser(ctx, ev.src.ID)
		if err != nil || u.MergedInto != "" || u.Status != ev.srcAfter.Status || u.CredentialEpoch < epoch {
			return
		}
		inv := &invalidations{}
		if ev.endTargetSessions {
			s.invalidateSessions(ctx, inv, ev.tgt.ID, "", journal.CredentialEpochBumped)
		}
		if ev.src.Status != ev.srcAfter.Status {
			s.queueStatusChanged(inv, StatusChanged{UserID: ev.src.ID, Email: ev.srcAfter.Email, From: ev.src.Status, To: ev.srcAfter.Status})
		}
		s.publishInvalidations(ctx, inv)
		afterCommit(inv.post).run(ctx)
		if len(ev.removed) > 0 {
			s.d.RBAC.AssignmentsChanged()
		}
	}
}
