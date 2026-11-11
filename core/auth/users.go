// file: core/auth/users.go
// version: 1.0.1
// guid: 41d0b787-5ce8-4a3f-9d8e-8c7b8380e2f0
// last-edited: 2026-11-11

package auth

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// User management (05-authn §3.2 "Users"; 05-rbac §4.6 last admin, §4.7
// rules 2 and 5). Every write re-reads the user inside its batch and decides
// on that read, so a concurrent writer fails the commit (storage.ErrConflict)
// and commitRetrying runs the decision again on the new state.
//
// The audit actions are 05-rbac §8's core.user.* names. A self-service
// password change is recorded as core.user.reset_password with "self" in its
// details: the table names no separate action for it.
const (
	actionUserCreate   = "core.user.create"
	actionUserDisable  = "core.user.disable"
	actionUserEnable   = "core.user.enable"
	actionUserDelete   = "core.user.delete"
	actionUserPassword = "core.user.reset_password"
	actionUserSetRoles = "core.user.set_roles"
	refKindRole        = "role"
)

// mayManageUsers checks that actor may administer accounts at all:
// core:users.manage through an interactive method. An API key never
// administers accounts, whatever its scopes (§4.13).
func (s *Service) mayManageUsers(actor *Principal, what string) error {
	if _, err := actorUser(actor); err != nil {
		return err
	}
	if !actor.Perms.Has(permUsersManage) {
		return fmt.Errorf("%w: %s is needed to %s", ErrDenied, permUsersManage, what)
	}
	if !s.interactive(actor.Method) {
		return fmt.Errorf("%w: %s needs an interactive sign-in", ErrDenied, what)
	}
	return nil
}

// dominates is 05-rbac §4.7 rule 5 against target as just read: actor holds
// every scope of target's current roles, or core:admin. It applies to the
// actor's own account too, where a session (role permissions, nothing
// narrower) always passes.
func (s *Service) dominates(actor *Principal, target *User) error {
	if err := s.d.RBAC.Dominates(actor.Perms, target.Roles); err != nil {
		return fmt.Errorf("%w: %w", ErrDominance, err)
	}
	return nil
}

// mutable refuses the accounts no administrator changes: _system, which
// attributes core's own writes and never signs in.
func mutable(u *User) error {
	if u.Status == StatusSystem {
		return fmt.Errorf("%w: the system user %s cannot be changed", ErrBadInput, u.ID)
	}
	return nil
}

// checkNewPassword applies core.auth.password.min_length. Length is counted
// in bytes, HEAD's rule (len(new_password) < 8 at handlers/auth.go), so an
// account created before the cutover meets the same bound after it.
func (s *Service) checkNewPassword(plain string) error {
	if n := s.settings().MinPasswordLength; len(plain) < n {
		return fmt.Errorf("%w: a password must be at least %d characters", ErrBadInput, n)
	}
	return nil
}

// hashNew derives the stored hash of a new password within the hashing wait
// bound (ErrBusy when no permit frees up). It runs before any batch opens, so
// a commit retry never derives twice.
func (s *Service) hashNew(ctx context.Context, plain string) (string, error) {
	_, hash, err := s.hasher.HashWithin(ctx, loginHashWait, plain)
	if errors.Is(err, password.ErrBusy) {
		return "", ErrBusy
	}
	if err != nil {
		return "", err
	}
	return hash, nil
}

// normalizeRoles returns roles sorted, without duplicates or empty names, as
// a fresh slice (nil for none).
func normalizeRoles(roles []rbac.RoleName) []rbac.RoleName {
	var out []rbac.RoleName
	for _, r := range roles {
		if r = rbac.RoleName(strings.TrimSpace(string(r))); r != "" {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// roleDiff is what changes between two normalized role lists.
func roleDiff(from, to []rbac.RoleName) (added, removed []rbac.RoleName) {
	for _, r := range to {
		if !slices.Contains(from, r) {
			added = append(added, r)
		}
	}
	for _, r := range from {
		if !slices.Contains(to, r) {
			removed = append(removed, r)
		}
	}
	return added, removed
}

func roleStrings(rs []rbac.RoleName) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}

// CreateUser creates an active user (§3.2): core:users.manage through an
// interactive method, and CanAssign over every role given (05-rbac §4.7 rule
// 2), so a users.manage holder cannot hand out a role it does not hold. The
// username is trimmed and must be free under FoldUsername (the store refuses
// a taken one, ErrUserExists). An e-mail another account holds verified is
// refused (ErrUserExists), HEAD's rule; the check and the commit run under
// the service's e-mail lock, so two creations in this process cannot both
// pass it. The password is optional (an OIDC-only account) and otherwise at
// least core.auth.password.min_length.
func (s *Service) CreateUser(ctx context.Context, actor *Principal, in NewUser) (*User, error) {
	if err := s.mayManageUsers(actor, "create a user"); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Username)
	if name == "" {
		return nil, fmt.Errorf("%w: a username is required", ErrBadInput)
	}
	roles := normalizeRoles(in.Roles)
	if err := s.d.RBAC.CanAssign(actor.Perms, roles); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrScopeEscalation, err)
	}
	hash := ""
	if in.Password != "" {
		if err := s.checkNewPassword(in.Password); err != nil {
			return nil, err
		}
		var err error
		if hash, err = s.hashNew(ctx, in.Password); err != nil {
			return nil, err
		}
	}
	now := s.now().UTC()
	id, err := newULID(now, rand.Reader)
	if err != nil {
		return nil, err
	}
	email := strings.TrimSpace(in.Email)
	u := &User{
		ID: UserID(id), Username: name, Email: email, EmailVerified: in.EmailVerified && email != "",
		HasPassword: hash != "", DisplayName: strings.TrimSpace(in.DisplayName), Roles: roles,
		Status: StatusActive, CreatedAt: now, UpdatedAt: now, Version: 1,
	}

	s.emailMu.Lock()
	defer s.emailMu.Unlock()
	if email != "" {
		taken, err := s.emailVerifiedElsewhere(ctx, email, u.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, fmt.Errorf("%w: e-mail %q is verified on another account", ErrUserExists, email)
		}
	}
	err = s.commitRetrying(ctx, func(b *storage.Batch) error {
		rec := *u
		rec.Roles = slices.Clone(roles)
		if err := s.d.Store.SetPassword(b, &rec, hash); err != nil {
			return err
		}
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserCreate, Outcome: audit.Success, Target: ref(refKindUser, id),
			Details: map[string]any{"username": name, "roles": roleStrings(roles)},
		})
	})
	if err != nil {
		return nil, err
	}
	s.hasUsers.Store(true)
	return u, nil
}

// emailVerifiedElsewhere reports whether an account other than except holds
// email verified. An undecodable record cannot be ruled out, so it counts as
// one (HEAD's ErrUserEmailTaken rule).
func (s *Service) emailVerifiedElsewhere(ctx context.Context, email string, except UserID) (bool, error) {
	for u, err := range s.d.Store.ScanUsers(ctx) {
		if errors.Is(err, ErrUndecodableRecord) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if u.ID != except && u.EmailVerified && SameEmail(u.Email, email) {
			return true, nil
		}
	}
	return false, nil
}

// User reads one user (ErrNotFound when absent).
func (s *Service) User(ctx context.Context, id UserID) (*User, error) {
	return s.d.Store.GetUser(ctx, id)
}

// UserByUsername reads the user whose username folds to name's
// (ErrNotFound when none).
func (s *Service) UserByUsername(ctx context.Context, name string) (*User, error) {
	return s.d.Store.UserByUsername(ctx, FoldUsername(strings.TrimSpace(name)))
}

// Users lists the users f admits, in the store's order. An undecodable
// record is yielded as ErrUndecodableRecord for that record only.
func (s *Service) Users(ctx context.Context, f UserFilter) iter.Seq2[*User, error] {
	return func(yield func(*User, error) bool) {
		for u, err := range s.d.Store.ScanUsers(ctx) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if f.Status != "" && u.Status != f.Status {
				continue
			}
			if f.Role != "" && !slices.Contains(u.Roles, f.Role) {
				continue
			}
			if !yield(u, nil) {
				return
			}
		}
	}
}

// ChangeOwnPassword replaces the actor's own password (§3.2, §4.12): an
// interactive method only, because API keys are not epoch-bound and a leaked
// key must not be able to take the account. current is verified within the
// hashing wait bound (ErrBusy, ErrInvalidCredentials; an account without a
// password has nothing to verify and is refused the same way). A wrong
// current password is not charged to the sign-in throttle: the caller already
// holds an interactive session, HEAD's rule.
//
// The write bumps the credential epoch, so every other session and one-time
// token of the account dies; the caller's own session is re-stamped with the
// new epoch in the same batch, so the person who changed the password stays
// signed in. It lands only if the epoch is still the one the verification
// read (ErrStaleEpoch otherwise: the password, or the account, changed in
// between); a transparent rehash, which keeps the epoch, does not refuse it.
// PasswordResetRequired and SignInRestricted are left as they are: those are
// cleared by a reset link (§4.14), never by a password the caller verified
// against a hash that may be stale.
func (s *Service) ChangeOwnPassword(ctx context.Context, actor *Principal, current, next string) error {
	self, err := actorUser(actor)
	if err != nil {
		return err
	}
	if !s.interactive(actor.Method) {
		return fmt.Errorf("%w: changing a password needs an interactive sign-in", ErrDenied)
	}
	if current == "" {
		return fmt.Errorf("%w: the current password is required", ErrBadInput)
	}
	if err := s.checkNewPassword(next); err != nil {
		return err
	}
	u, err := s.d.Store.GetUser(ctx, self)
	if err != nil {
		return err
	}
	stored, err := s.d.Store.PasswordHash(ctx, self)
	if err != nil {
		return err
	}
	if stored == "" {
		return fmt.Errorf("%w: the account has no password to verify", ErrInvalidCredentials)
	}
	ok, _, err := s.hasher.VerifyWithin(ctx, loginHashWait, password.AlgoOf(stored), stored, current)
	if errors.Is(err, password.ErrBusy) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	hash, err := s.hashNew(ctx, next)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		cur, err := s.d.Store.GetUser(ctx, self)
		if err != nil {
			return err
		}
		if cur.CredentialEpoch != u.CredentialEpoch {
			return fmt.Errorf("%w: the account's credentials changed while the password was verified", ErrStaleEpoch)
		}
		up := *cur
		up.Version = cur.Version + 1
		up.CredentialEpoch = cur.CredentialEpoch + 1
		up.HasPassword = true
		up.UpdatedAt = now
		if err := s.d.Store.SetPassword(b, &up, hash); err != nil {
			return err
		}
		if err := s.restampOwnSession(ctx, b, actor, up.CredentialEpoch); err != nil {
			return err
		}
		subject := ref(refKindUser, string(self))
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.PasswordChanged, Subject: subject, Epoch: up.CredentialEpoch})
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: subject, Epoch: up.CredentialEpoch})
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserPassword, Outcome: audit.Success, Target: subject,
			Details: map[string]any{"self": true},
		})
	})
}

// restampOwnSession re-stamps the session actor presented with epoch in b
// (HEAD's keepSessionID). A principal that did not come from a session of its
// own user keeps nothing; a session already gone keeps nothing either.
func (s *Service) restampOwnSession(ctx context.Context, b *storage.Batch, actor *Principal, epoch int64) error {
	if !actor.Method.sessionMethod() || actor.CredentialID == "" {
		return nil
	}
	rec, err := s.d.Store.SessionByPublicID(ctx, actor.CredentialID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.UserID != actor.User.ID {
		return nil
	}
	return s.d.Store.RestampSession(b, rec, epoch)
}

// SetStatus disables or re-enables a user (§3.2): core:users.manage through
// an interactive method and dominance over the user's current roles. Only
// active and disabled are settable; _system and a pending invitee are
// refused (ErrBadInput). A change bumps the credential epoch, so every
// session and one-time token issued before it stays dead after a later
// re-enable; API keys are refused while the owner is disabled and work again
// after. Disabling the last active core:admin holder is refused
// (ErrLastAdmin). Setting the status a user already has writes nothing.
func (s *Service) SetStatus(ctx context.Context, actor *Principal, id UserID, st UserStatus) error {
	if st != StatusActive && st != StatusDisabled {
		return fmt.Errorf("%w: status %q is not settable (active or disabled)", ErrBadInput, st)
	}
	if err := s.mayManageUsers(actor, "change a user's status"); err != nil {
		return err
	}
	now := s.now().UTC()
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		u, err := s.d.Store.GetUser(ctx, id)
		if err != nil {
			return err
		}
		if err := mutable(u); err != nil {
			return err
		}
		if u.Status != StatusActive && u.Status != StatusDisabled {
			return fmt.Errorf("%w: user %s is %s, not active or disabled", ErrBadInput, u.ID, u.Status)
		}
		if err := s.dominates(actor, u); err != nil {
			return err
		}
		if u.Status == st {
			return nil
		}
		if err := s.keepAnAdmin(ctx, b, u, u.Roles, st); err != nil {
			return err
		}
		up := *u
		up.Status = st
		up.Version = u.Version + 1
		up.CredentialEpoch = u.CredentialEpoch + 1
		up.UpdatedAt = now
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return err
		}
		subject := ref(refKindUser, string(id))
		action := actionUserEnable
		if st == StatusDisabled {
			action = actionUserDisable
			s.journalLine(ctx, b, journal.Revocation{Kind: journal.UserDisabled, Subject: subject, Epoch: up.CredentialEpoch})
		}
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, Subject: subject, Epoch: up.CredentialEpoch})
		return s.d.Audit.RecordIn(ctx, b, audit.Event{Action: action, Outcome: audit.Success, Target: subject})
	})
}

// DeleteUser deletes a user and everything that authenticates as it, in one
// batch (§3.2): core:users.manage through an interactive method and
// dominance over the user's roles. Its sessions, API keys, external-identity
// links and the one-time tokens it is the target or the creator of go with
// it (an invite whose creator is gone could never pass its CanAssign
// re-check, SEC3-2). _system is refused, and so is deleting the last active
// core:admin holder (ErrLastAdmin). One user.deleted line is journaled; a
// restore re-applying it deletes the same records (§4.14).
//
// A record of the user the store cannot decode cannot be named for deletion
// and is left; auth already refuses it as restricted (§6), and its owner is
// gone.
func (s *Service) DeleteUser(ctx context.Context, actor *Principal, id UserID) error {
	if err := s.mayManageUsers(actor, "delete a user"); err != nil {
		return err
	}
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		u, err := s.d.Store.GetUser(ctx, id)
		if err != nil {
			return err
		}
		if err := mutable(u); err != nil {
			return err
		}
		if err := s.dominates(actor, u); err != nil {
			return err
		}
		if err := s.keepAnAdmin(ctx, b, u, nil, ""); err != nil {
			return err
		}
		if err := s.deleteCredentials(ctx, b, id); err != nil {
			return err
		}
		if err := s.d.Store.DeleteUser(b, u); err != nil {
			return err
		}
		subject := ref(refKindUser, string(id))
		s.journalLine(ctx, b, journal.Revocation{Kind: journal.UserDeleted, Subject: subject})
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserDelete, Outcome: audit.Success, Target: subject,
			Details: map[string]any{"username": u.Username},
		})
	})
}

// deleteCredentials stages into b the deletion of every session, API key,
// external identity and one-time token of id.
func (s *Service) deleteCredentials(ctx context.Context, b *storage.Batch, id UserID) error {
	for rec, err := range s.d.Store.ScanUserSessions(ctx, id) {
		if err != nil {
			if s.leftUndecodable("session", id, err) {
				continue
			}
			return err
		}
		if err := s.d.Store.DeleteSession(b, rec); err != nil {
			return err
		}
	}
	for k, err := range s.d.Store.ScanAPIKeys(ctx, id) {
		if err != nil {
			if s.leftUndecodable("apikey", id, err) {
				continue
			}
			return err
		}
		if err := s.d.Store.DeleteAPIKey(b, k); err != nil {
			return err
		}
	}
	for ident, err := range s.d.Store.ScanIdentities(ctx, id) {
		if err != nil {
			if s.leftUndecodable("identity", id, err) {
				continue
			}
			return err
		}
		if err := s.d.Store.DeleteIdentity(b, ident); err != nil {
			return err
		}
	}
	for _, kind := range []OneTimeKind{OneTimeInvite, OneTimeTempLogin} {
		for t, err := range s.d.Store.ScanOneTime(ctx, kind) {
			if err != nil {
				if s.leftUndecodable("onetime", id, err) {
					continue
				}
				return err
			}
			if t.UserID != id && t.CreatedBy != id {
				continue
			}
			if err := s.d.Store.DeleteOneTime(b, kind, t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// leftUndecodable reports whether err is a scan's per-record
// ErrUndecodableRecord, which a user delete or a sign-out-everywhere leaves
// in place (logged): it cannot be named for deletion, and auth already
// refuses it.
func (s *Service) leftUndecodable(kind string, id UserID, err error) bool {
	if !errors.Is(err, ErrUndecodableRecord) {
		return false
	}
	s.log.Warn("auth: undecodable record left in place", "kind", kind, "user", string(id))
	return true
}

// SetRoles replaces a user's roles (§3.2; 05-rbac §4.7 rules 2 and 5):
// core:users.manage through an interactive method, dominance over the
// user's current roles, and CanAssign over every role added and every role
// removed, so a users.manage holder can neither hand out admin nor strip it
// from a co-admin. (Every removed role is one of the current roles, so
// dominance already covers it; CanAssign names it as rule 2 does.) Taking
// core:admin from its last active holder is refused (ErrLastAdmin). The
// epoch is not bumped: permissions are recomputed on the next request from
// the role list (§4.12). Each removed role is journaled role.unassigned.
// The same role set writes nothing.
func (s *Service) SetRoles(ctx context.Context, actor *Principal, id UserID, roles []rbac.RoleName) error {
	if err := s.mayManageUsers(actor, "change a user's roles"); err != nil {
		return err
	}
	next := normalizeRoles(roles)
	now := s.now().UTC()
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		u, err := s.d.Store.GetUser(ctx, id)
		if err != nil {
			return err
		}
		if err := mutable(u); err != nil {
			return err
		}
		if err := s.dominates(actor, u); err != nil {
			return err
		}
		added, removed := roleDiff(normalizeRoles(u.Roles), next)
		if len(added) == 0 && len(removed) == 0 {
			return nil
		}
		if err := s.d.RBAC.CanAssign(actor.Perms, slices.Concat(added, removed)); err != nil {
			return fmt.Errorf("%w: %w", ErrScopeEscalation, err)
		}
		if err := s.keepAnAdmin(ctx, b, u, next, u.Status); err != nil {
			return err
		}
		up := *u
		up.Roles = slices.Clone(next)
		up.Version = u.Version + 1
		up.UpdatedAt = now
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return err
		}
		subject := ref(refKindUser, string(id))
		for _, r := range removed {
			s.journalLine(ctx, b, journal.Revocation{Kind: journal.RoleUnassigned, Subject: subject, Object: ref(refKindRole, string(r))})
		}
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserSetRoles, Outcome: audit.Success, Target: subject,
			Details: map[string]any{"added": roleStrings(added), "removed": roleStrings(removed)},
		})
	})
}

// keepAnAdmin is the last-admin rule (05-rbac §4.6): when u is an active
// core:admin holder and, with roles after and status after, would not be
// one, some other active holder must exist. That holder is guarded in b
// (Store.GuardUser), so a concurrent change that takes core:admin from it
// fails this commit with storage.ErrConflict and the retry decides again: two
// concurrent demotions of the last two holders cannot both land (05-rbac §9).
// One guard is enough: of all the writes that would together leave no
// holder, the one committing last guarded a holder another of them changed,
// after it read it.
//
// A holder is a user with status active (rbac.Holder.Active) and a role
// whose permissions grant core:admin by rbac.GrantsAdmin, the predicate rbac's
// own rule uses; an unknown role grants nothing, and an undecodable user
// record holds nothing (auth refuses it, §6). SignInRestricted does not
// matter: a restore-restricted last admin is still the holder (05-rbac §7.1).
//
// The role definitions are read, not guarded: rbac.Store, frozen SDK, has no
// way to stage an Expect on a role record into this batch, so a role edit
// that takes core:admin from the other holder's role and commits between
// this read and this commit is not seen here. rbac's own rule reads holders
// through its batch, so the reverse order is caught there.
func (s *Service) keepAnAdmin(ctx context.Context, b *storage.Batch, u *User, after []rbac.RoleName, afterStatus UserStatus) error {
	grants := map[rbac.RoleName]bool{}
	isAdmin := func(roles []rbac.RoleName, st UserStatus) (bool, error) {
		if st != StatusActive {
			return false, nil
		}
		for _, name := range roles {
			g, seen := grants[name]
			if !seen {
				r, err := s.d.RBAC.Role(ctx, name)
				switch {
				case errors.Is(err, rbac.ErrUnknownRole):
					g = false
				case err != nil:
					return false, fmt.Errorf("auth: last-admin rule: read role %s: %w", name, err)
				default:
					g = rbac.GrantsAdmin(r.Permissions())
				}
				grants[name] = g
			}
			if g {
				return true, nil
			}
		}
		return false, nil
	}
	was, err := isAdmin(u.Roles, u.Status)
	if err != nil || !was {
		return err
	}
	still, err := isAdmin(after, afterStatus)
	if err != nil || still {
		return err
	}
	for other, err := range s.d.Store.ScanUsers(ctx) {
		if errors.Is(err, ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if other.ID == u.ID {
			continue
		}
		ok, err := isAdmin(other.Roles, other.Status)
		if err != nil {
			return err
		}
		if ok {
			return s.d.Store.GuardUser(b, other)
		}
	}
	return fmt.Errorf("%w: %s is the last active holder", ErrLastAdmin, u.ID)
}

// newULID is a ULID: 48 bits of t's Unix milliseconds, 80 bits read from r,
// as 26 characters of Crockford base32. It is a copy of authstore's,
// taskstore's and audit's: R4 keeps the packages from sharing one without a
// new L0 package.
func newULID(t time.Time, r io.Reader) (string, error) {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [16]byte
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", fmt.Errorf("auth: user ID entropy: %w", err)
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(t.UnixMilli()))
	copy(b[:6], ts[2:])
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}
