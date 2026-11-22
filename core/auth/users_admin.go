// file: core/auth/users_admin.go
// version: 0.1.0
// guid: ff1a867f-0e16-4f87-9398-5d705442dc9d
// last-edited: 2026-11-22

package auth

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"sync"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/storage"
)

// actionUserSetEmail is the audit action of SetUserEmail.
const actionUserSetEmail = "core.user.set_email"

// SetUserEmail is AccountAdmin.SetUserEmail, HEAD's PUT /users/:id/email
// (handlers.SetUserEmail over PebbleStore.SetUserEmailAsAdmin): an admin sets
// or, with "", clears another user's address, and a set address is VERIFIED
// (HEAD's EmailSource admin), which is why users.manage is not enough: a
// verified address is what an SSO sign-in links to, so the principal must also
// hold core:admin (ctxAdmin) and dominate the target (rbac §4.7 rule 5, which
// HEAD did not ask and core does; the principal's own account passes). The
// address is validated as an invite's is (inviteEmail: one plain address,
// normalised).
//
// An address another account holds verified is refused (ErrEmailInUse), even
// when it is already this user's own unverified copy: setting it verifies it,
// and two verified holders must not exist (HEAD checks the same way,
// "even when the address is unchanged"). Accounts that only typed the address
// in keep their unverified copy, as at HEAD: this is not an invite's bind and
// displaces nobody.
//
// The credential epoch is NOT bumped, as at HEAD: SetUserEmailAsAdmin
// (internal/books/store/pebble_store_auth.go) rewrites the row in its own
// batch and does not go through mutateUserCredentials, the one HEAD writer
// that bumps CredentialEpoch, so sessions, keys and tokens of the user survive
// an address change, as they do a self-service one (UpdateProfile). The
// version is bumped, and the write is audited.
//
// When the address changes by SameEmail the account's EmailChanged hook
// fires, after the commit with the e-mail lock released; setting the address
// the user already holds (verifying a typed-in copy, or a change of spelling)
// fires nothing.
func (s *Service) SetUserEmail(ctx context.Context, id UserID, email string) error {
	actor, err := s.ctxAdmin(ctx, "set another user's e-mail")
	if err != nil {
		return err
	}
	addr, err := inviteEmail(email)
	if err != nil {
		return err
	}
	// Held across the check and the commit, never across a callback.
	s.emailMu.Lock()
	var once sync.Once
	unlock := func() { once.Do(s.emailMu.Unlock) }
	defer unlock()
	now := s.now().UTC()
	post, err := s.commitQueueingN(ctx, conflictRetries, func(b *storage.Batch, inv *invalidations) error {
		cur, err := s.d.Store.GetUser(ctx, id)
		if err != nil {
			return err
		}
		if err := mutable(cur); err != nil {
			return err
		}
		if err := s.dominates(actor, cur); err != nil {
			return err
		}
		if addr != "" {
			taken, err := s.emailVerifiedElsewhere(ctx, addr, cur.ID)
			if err != nil {
				return err
			}
			if taken {
				return fmt.Errorf("%w: another account holds that address verified", ErrEmailInUse)
			}
		}
		up := *cur
		up.Email, up.EmailVerified = addr, addr != ""
		if up.Email == cur.Email && up.EmailVerified == cur.EmailVerified {
			return nil
		}
		up.Version, up.UpdatedAt = cur.Version+1, now
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return err
		}
		if !SameEmail(cur.Email, up.Email) {
			s.queueEmailChanged(inv, EmailChanged{UserID: cur.ID, From: cur.Email, To: up.Email})
		}
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionUserSetEmail, Outcome: audit.Success, Target: ref(refKindUser, string(cur.ID)),
			Details: map[string]any{"cleared": addr == "", "was_verified": cur.EmailVerified},
		})
	})
	// The lifecycle callbacks run with the e-mail lock released (see
	// UpdateProfile).
	unlock()
	post.run(ctx)
	return err
}

// AdminUser is AccountAdmin.AdminUser: one user's record for a principal that
// holds core:users.manage through an interactive method; ErrNotFound when
// there is none. The record is User, which holds no credential: the password
// hash is stored beside the record and passed to Store.SetPassword apart from
// it, User.HasPassword is only a flag, and TOTP and API-key secrets are
// records of their own. cleanUser copies the record, so a caller that edits
// what it was given edits no one else's copy.
func (s *Service) AdminUser(ctx context.Context, id UserID) (*User, error) {
	if _, err := s.ctxManager(ctx, "read a user"); err != nil {
		return nil, err
	}
	u, err := s.d.Store.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return cleanUser(u), nil
}

// AdminUsers is AccountAdmin.AdminUsers: Users behind the principal check, with
// the same records AdminUser returns (EmailVerified included). The check runs
// when the sequence is ranged over, and a refusal is its one element; an
// undecodable record is yielded as ErrUndecodableRecord for that record only,
// as Users does.
func (s *Service) AdminUsers(ctx context.Context, f UserFilter) iter.Seq2[*User, error] {
	return func(yield func(*User, error) bool) {
		if _, err := s.ctxManager(ctx, "list users"); err != nil {
			yield(nil, err)
			return
		}
		for u, err := range s.Users(ctx, f) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if !yield(cleanUser(u), nil) {
				return
			}
		}
	}
}

// cleanUser is a copy of u that shares nothing with it (the role slice is
// cloned). It is the one place an admin read builds its result, so that what
// such a read may return is the fields of User and nothing else:
// TestAdminUser_CarriesNoCredential pins both the field set and the values.
func cleanUser(u *User) *User {
	c := *u
	c.Roles = slices.Clone(u.Roles)
	return &c
}
