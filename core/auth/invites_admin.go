// file: core/auth/invites_admin.go
// version: 0.1.0
// guid: 4491f338-0bc4-4d88-a52c-edd06d063272
// last-edited: 2026-11-22

package auth

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The audit actions of the invite writes of InviteAdmin.
const (
	actionInviteMarkDelivered = "core.invite.mark_delivered"
	actionInviteRotate        = "core.invite.rotate"
)

// InviteRotation is what RotateInvite did besides minting the link.
type InviteRotation struct {
	// Invite is the new invite. It is born unflagged (BindEmailOnAccept
	// false) whatever the old one was: the new link has not been mailed yet,
	// and only MarkInviteEmailDelivered, after the mailer succeeded, flags it.
	Invite InviteView
	// WasEmailBound is the OLD invite's flag. A caller that re-mails the new
	// link marks it delivered only when this is true, so a resend never makes
	// an unflagged invite bind an address.
	WasEmailBound bool
}

// inviteByID is the open or expired invite with public ID id (ErrNotFound when
// there is none), read from the store. An undecodable record is skipped, as
// usernameFree does: it cannot be named.
func (s *Service) inviteByID(ctx context.Context, id string) (*OneTimeToken, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: invite %q", ErrNotFound, id)
	}
	for t, err := range s.d.Store.ScanOneTime(ctx, OneTimeInvite) {
		if errors.Is(err, ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("%w: invite %q", ErrNotFound, id)
}

// Invite is InviteAdmin.Invite: the open invite with public ID id, never its
// token. An expired invite is not open (ErrNotFound), as the list leaves it
// out.
func (s *Service) Invite(ctx context.Context, id string) (InviteView, error) {
	if _, err := s.ctxManager(ctx, "read an invite"); err != nil {
		return InviteView{}, err
	}
	t, err := s.inviteByID(ctx, id)
	if err != nil {
		return InviteView{}, err
	}
	if !s.now().Before(t.ExpiresAt) {
		return InviteView{}, fmt.Errorf("%w: invite %q", ErrNotFound, id)
	}
	return inviteView(t), nil
}

// AdminInvites is InviteAdmin.AdminInvites: Invites behind the legacy
// surface's check (mayAdministerInvites). The check runs when the sequence is
// ranged over, and a refusal is its one element.
func (s *Service) AdminInvites(ctx context.Context) iter.Seq2[InviteView, error] {
	return func(yield func(InviteView, error) bool) {
		if _, err := ctxInviteAdministrator(ctx, "list invites"); err != nil {
			yield(InviteView{}, err)
			return
		}
		for v, err := range s.Invites(ctx) {
			if !yield(v, err) {
				return
			}
		}
	}
}

// AdminDeleteInvite is InviteAdmin.AdminDeleteInvite: the delete
// Service.DeleteInvite makes, for the principal bound on ctx under the legacy
// surface's check (mayAdministerInvites). CoreActions names no invite
// deletion, so it is not audited (seams.d/F4-02.md), as DeleteInvite is not.
func (s *Service) AdminDeleteInvite(ctx context.Context, id string) error {
	if _, err := ctxInviteAdministrator(ctx, "delete an invite"); err != nil {
		return err
	}
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		return s.d.Store.DeleteOneTime(b, OneTimeInvite, id)
	})
}

// ctxInviteAdministrator is the principal bound on ctx when
// mayAdministerInvites admits it.
func ctxInviteAdministrator(ctx context.Context, what string) (*Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: no principal is bound on the request", ErrDenied)
	}
	if err := mayAdministerInvites(p, what); err != nil {
		return nil, err
	}
	return p, nil
}

// mayAdministerInvites is the legacy surface's rule for listing and deleting
// invites (F4-08 B10y, item 4 cause B): HEAD's GET and DELETE
// /api/v1/users/invites were s.perm(users.manage) and nothing more, no
// credential guard and no user, so an API key holding the atom and the
// auth-off request both reached them. The rule is mayManageUsers without its
// two legs that HEAD did not have, the interactive method and the user:
// core:users.manage held through any method by a principal with a user, or by
// the auth-disabled principal, whose checker holds core:users.manage (it is
// neither AuthRequired nor Sensitive, 05-rbac §4.11). Neither call site checks
// anything else (no dominance, no role bound: a delete names no user, and a
// list reveals no credential). It is reachable only through AdminInvites and
// AdminDeleteInvite, which no core-native route calls
// (archtest.InviteRuleLegacyOnly); creating and resending keep mayManageUsers.
func mayAdministerInvites(p *Principal, what string) error {
	if p == nil || p.Perms == nil {
		return fmt.Errorf("%w: no authenticated actor", ErrDenied)
	}
	switch {
	case p.User != nil && p.User.ID != "":
	case p.User == nil && p.Method == MethodAuthDisabled:
	default:
		return fmt.Errorf("%w: no authenticated actor", ErrDenied)
	}
	if !p.Perms.Has(permUsersManage) {
		return fmt.Errorf("%w: %s is needed to %s", ErrDenied, permUsersManage, what)
	}
	return nil
}

// MarkInviteEmailDelivered is InviteAdmin.MarkInviteEmailDelivered: it sets
// the invite's BindEmailOnAccept, HEAD's "the invitation was emailed to exactly
// the invite's address" (inviteEmailDelivered). It is the only writer of the
// flag. The caller calls it only after the mailer returned success: core
// cannot see the mail, and a flag set without it would let a link that was
// never sent bind an address.
//
// The principal must hold core:users.manage and core:admin through an
// interactive method (ctxAdmin), as the admin whose vouching HEAD's binding
// rested on. It refuses an invite that names no address (ErrBadInput), one
// that is gone, accepted or deleted (ErrNotFound) and one past its expiry
// (ErrInviteInvalid). It is idempotent: an invite already flagged and live
// returns nil, and writes and audits nothing. The accept re-checks the
// creator (inviteBindsEmail), so flagging gives nothing to an invite whose
// creator has since lost core:admin.
func (s *Service) MarkInviteEmailDelivered(ctx context.Context, inviteID string) error {
	if _, err := s.ctxAdmin(ctx, "mark an invite's e-mail delivered"); err != nil {
		return err
	}
	now := s.now().UTC()
	return s.commitRetrying(ctx, func(b *storage.Batch) error {
		t, err := s.inviteByID(ctx, inviteID)
		if err != nil {
			return err
		}
		if !now.Before(t.ExpiresAt) {
			return fmt.Errorf("%w: the invite expired", ErrInviteInvalid)
		}
		if t.Email == "" {
			return fmt.Errorf("%w: the invite names no e-mail address", ErrBadInput)
		}
		if t.BindEmailOnAccept {
			return nil
		}
		up := *t
		up.BindEmailOnAccept = true
		if err := s.d.Store.PutOneTime(b, &up); err != nil {
			return err
		}
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionInviteMarkDelivered, Outcome: audit.Success, Target: ref(refKindInvite, t.ID),
			Details: map[string]any{"username": t.Username, "role": string(t.Role), "created_by": string(t.CreatedBy)},
		})
	})
}

// RotateInvite is InviteAdmin.RotateInvite: a resend that kills the old link.
// In ONE batch it deletes the invite with public ID id and issues a new one
// with the same username, e-mail and role and EXACTLY the old ExpiresAt (a
// resend never extends the invite's life), under a new public ID. The caller
// becomes the new invite's creator and carries its own credential epoch, so
// the principal needs core:users.manage through an interactive method and
// CanAssign over the role, as CreateInvite asks. The new invite is born
// unflagged; InviteRotation.WasEmailBound says whether the old one was.
//
// An expired invite is refused (ErrInviteInvalid) and never rotated, an absent
// one is ErrNotFound, and the username and address are held to CreateInvite's
// rules again (ErrUserExists, ErrEmailInUse): no link is minted that could
// not be accepted. A batch that fails writes neither half: the old invite
// stands.
func (s *Service) RotateInvite(ctx context.Context, id string) (string, InviteRotation, error) {
	actor, err := s.ctxManager(ctx, "rotate an invite")
	if err != nil {
		return "", InviteRotation{}, err
	}
	now := s.now().UTC()
	s.inviteMu.Lock()
	defer s.inviteMu.Unlock()
	var token string
	var out InviteRotation
	err = s.commitRetrying(ctx, func(b *storage.Batch) error {
		old, err := s.inviteByID(ctx, id)
		if err != nil {
			return err
		}
		if !now.Before(old.ExpiresAt) {
			return fmt.Errorf("%w: the invite expired", ErrInviteInvalid)
		}
		if err := s.d.RBAC.CanAssign(actor.Perms, []rbac.RoleName{old.Role}); err != nil {
			return fmt.Errorf("%w: %w", ErrScopeEscalation, err)
		}
		if err := s.inviteEmailFree(ctx, old.Email); err != nil {
			return err
		}
		if _, err := s.d.Store.UserByUsername(ctx, FoldUsername(old.Username)); err == nil {
			return fmt.Errorf("%w: username %q is taken", ErrUserExists, old.Username)
		} else if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		creator, err := s.d.Store.GetUser(ctx, actor.User.ID)
		if err != nil {
			return err
		}
		if err := s.d.Store.DeleteOneTime(b, OneTimeInvite, old.ID); err != nil {
			return err
		}
		t := &OneTimeToken{
			Kind: OneTimeInvite, CreatedBy: creator.ID, Username: old.Username, Email: old.Email, Role: old.Role,
			CredentialEpoch: creator.CredentialEpoch, CreatedAt: now, ExpiresAt: old.ExpiresAt,
		}
		tok, err := s.d.Store.IssueOneTime(b, t)
		if err != nil {
			return err
		}
		token, out = string(tok), InviteRotation{Invite: inviteView(t), WasEmailBound: old.BindEmailOnAccept}
		return s.d.Audit.RecordIn(ctx, b, audit.Event{
			Action: actionInviteRotate, Outcome: audit.Success, Target: ref(refKindInvite, t.ID),
			Details: map[string]any{"from": old.ID, "username": old.Username, "role": string(old.Role), "was_email_bound": old.BindEmailOnAccept},
		})
	})
	if err != nil {
		return "", InviteRotation{}, err
	}
	return inviteLink(token), out, nil
}
