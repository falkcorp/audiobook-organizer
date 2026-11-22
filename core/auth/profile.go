// file: core/auth/profile.go
// version: 0.1.0
// guid: 9e2d4b60-37fa-4c81-b5a3-6d1c80f47e92
// last-edited: 2026-11-20

package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/storage"
)

// ProfileUpdate is a PATCH /me: the fields the caller set. A nil field is left
// as it is.
type ProfileUpdate struct {
	DisplayName *string
	Email       *string
}

// UpdateProfile changes the actor's own display name and e-mail (§3.2,
// 05-authn §4.13 PATCH /me; the writer F4-05 left to F4-08, HEAD's UpdateMe).
// It needs a user and an interactive method: the auth-disabled principal has
// no user (§4.11) and a key may not edit the account it signs in for, both
// ErrDenied before anything is read. An empty patch, or an empty e-mail, is
// ErrBadInput (HEAD: "email is required"); an e-mail that is not plain ASCII
// (PlainEmail) is ErrEmailNotPlain, because NormalizeEmail would fold a
// look-alike onto another person's address.
//
// An e-mail that is a different address from the stored one (SameEmail) is an
// identity change: an address is what an SSO sign-in links an account on. The
// new address is never verified (a self-typed address that counted as proven
// would let anyone take over another person's first SSO sign-in by typing it
// into a profile), so EmailVerified is cleared; the old address stops being
// this account's, so nothing resolves to the account through it any more. It
// is refused with ErrEmailInUse when another account holds the new address
// verified. That check and the write run under the account e-mail lock
// (emailMu), the same one CreateUser, AcceptInvite and the OIDC resolver take
// (LockEmails), so no verified claim to the address can land between them.
// A change of spelling only (the same address by SameEmail, say a different
// case) keeps EmailVerified: it is the address that was verified.
//
// The credential epoch does not move and no journal line is written: 05-authn
// §4.14 lists what bumps it (password change or reset, disable, enable, sign
// out everywhere) and an e-mail change is not one, so the account's sessions
// and keys stay valid, as under HEAD.
//
// The record is re-read inside every attempt of the commit, so a concurrent
// write to another field of the same user is kept, not overwritten, and a
// change that finds nothing to do writes nothing.
func (s *Service) UpdateProfile(ctx context.Context, actor *Principal, in ProfileUpdate) (*User, error) {
	self, err := actorUser(actor)
	if err != nil {
		return nil, err
	}
	if !s.interactive(actor.Method) {
		return nil, fmt.Errorf("%w: editing a profile needs an interactive sign-in", ErrDenied)
	}
	if in.DisplayName == nil && in.Email == nil {
		return nil, fmt.Errorf("%w: nothing to change", ErrBadInput)
	}
	name := ""
	if in.DisplayName != nil {
		name = strings.TrimSpace(*in.DisplayName)
	}
	email := ""
	unlock := func() {} // releases the e-mail lock, once, before the callbacks run
	if in.Email != nil {
		email = strings.TrimSpace(*in.Email)
		if email == "" {
			return nil, fmt.Errorf("%w: an e-mail address is required", ErrBadInput)
		}
		if _, plain := PlainEmail(email); !plain {
			return nil, ErrEmailNotPlain
		}
		// Held across the check and the commit, never across a network call.
		s.emailMu.Lock()
		var once sync.Once
		unlock = func() { once.Do(s.emailMu.Unlock) }
		defer unlock()
	}
	now := s.now().UTC()
	var out *User
	post, err := s.commitQueueingN(ctx, conflictRetries, func(b *storage.Batch, inv *invalidations) error {
		cur, err := s.d.Store.GetUser(ctx, self)
		if err != nil {
			return err
		}
		if err := mutable(cur); err != nil {
			return err
		}
		up := *cur
		changed := false
		if in.DisplayName != nil && name != cur.DisplayName {
			up.DisplayName, changed = name, true
		}
		if in.Email != nil && email != cur.Email {
			if !SameEmail(cur.Email, email) {
				taken, err := s.emailVerifiedElsewhere(ctx, email, self)
				if err != nil {
					return err
				}
				if taken {
					return fmt.Errorf("%w: another account holds that address", ErrEmailInUse)
				}
				up.EmailVerified = false
			}
			up.Email, changed = email, true
		}
		if !changed {
			out = cur
			return nil
		}
		up.Version, up.UpdatedAt = cur.Version+1, now
		if err := s.d.Store.PutUser(b, &up); err != nil {
			return err
		}
		out = &up
		if !SameEmail(cur.Email, up.Email) {
			s.queueEmailChanged(inv, EmailChanged{UserID: self, From: cur.Email, To: up.Email})
		}
		return nil
	})
	// The lifecycle callbacks run with the e-mail lock released: one that
	// reaches LockEmails, CreateUser or another profile write must not wait
	// on this call.
	unlock()
	post.run(ctx)
	if err != nil {
		return nil, err
	}
	return out, nil
}
