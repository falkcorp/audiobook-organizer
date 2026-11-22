// file: core/auth/admin_accounts.go
// version: 0.1.0
// guid: af9d507f-d904-4446-98be-9cea63169b5f
// last-edited: 2026-11-22

package auth

import (
	"context"
	"fmt"
	"iter"

	"github.com/falkcorp/mediacore/rbac"
)

// InviteAdmin and AccountAdmin are the account half of the actor-checked
// identity participant a module is handed (F4-08 B10t): the invite and admin
// set-email writes books serves under core identity, and the two reads its
// admin routes share. Both are embedded in Admin; a caller takes the one it
// uses. Every method re-checks the principal bound on ctx, the way
// PlanUserMerge does, so holding the handle grants nothing, and none of them
// returns a token, a hash or any other credential: an invite link comes back
// once, from CreateInvite and RotateInvite, and nowhere else.
//
// Unlike StageUserMerge these methods COMMIT: each is one self-contained
// write, in its own batch, with its audit record in it.
//
// They were one nine-method interface until F4-08 B10y; the invite methods
// are their own interface, and AccountAdmin keeps the rest.

// InviteAdmin is the invite half: create, read, list, delete, resend and the
// delivery mark.
//
// AdminInvites and AdminDeleteInvite are the legacy surface's (books' HEAD
// routes GET and DELETE /api/v1/users/invites) and answer HEAD's reach for
// them (mayAdministerInvites): core:users.manage through any method, a key
// included, or the auth-disabled principal. No core-native route calls them
// (archtest.InviteRuleLegacyOnly); core's own list and delete routes keep
// their guards and Service.DeleteInvite's rule.
type InviteAdmin interface {
	// CreateInvite is the Service's explicit-actor method: actor must be the
	// principal bound on ctx (WithPrincipal), and the checks run on that
	// principal's own permissions.
	CreateInvite(ctx context.Context, actor *Principal, in InviteSpec) (string, InviteView, error)
	// AdminDeleteInvite deletes the open invite with public ID id for the
	// principal bound on ctx (mayAdministerInvites); ErrNotFound when there
	// is none.
	AdminDeleteInvite(ctx context.Context, id string) error
	// Invite is one open invite by its public ID, for core:users.manage
	// through an interactive method; ErrNotFound when there is none or it
	// expired.
	Invite(ctx context.Context, id string) (InviteView, error)
	// AdminInvites lists the open invites for the principal bound on ctx
	// (mayAdministerInvites). The check runs inside the returned sequence,
	// which yields the refusal as its only element. It is not
	// Service.Invites, which checks nothing.
	AdminInvites(ctx context.Context) iter.Seq2[InviteView, error]
	// RotateInvite replaces an open invite by a new one with the same
	// username, e-mail, role and exactly the same expiry, in one batch.
	RotateInvite(ctx context.Context, id string) (string, InviteRotation, error)
	// MarkInviteEmailDelivered records that an admin mailed the invite to its
	// address, so accepting it may bind the address verified.
	MarkInviteEmailDelivered(ctx context.Context, inviteID string) error
}

// AccountAdmin is the account-record half: the admin set-email write and the
// admin user reads.
type AccountAdmin interface {
	// SetUserEmail sets (or, with "", clears) another user's address, verified.
	SetUserEmail(ctx context.Context, id UserID, email string) error
	// AdminUser and AdminUsers read user records for core:users.manage through
	// an interactive method. A record carries no credential material.
	AdminUser(ctx context.Context, id UserID) (*User, error)
	AdminUsers(ctx context.Context, f UserFilter) iter.Seq2[*User, error]
}

var (
	_ InviteAdmin  = (*Service)(nil)
	_ AccountAdmin = (*Service)(nil)
)

// ctxActor is the principal an explicit-actor write (CreateInvite,
// DeleteInvite) is made as: the one bound on ctx, never the one passed. actor
// must be that principal (same user, method and credential): a ctx that binds
// none, or another, is ErrDenied. The caller then decides on the returned
// principal's Perms, so an actor value assembled elsewhere with wider
// permissions changes nothing.
func (s *Service) ctxActor(ctx context.Context, actor *Principal) (*Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: no principal is bound on the request", ErrDenied)
	}
	if actor == nil || actor.User == nil || p.User == nil ||
		p.User.ID != actor.User.ID || p.Method != actor.Method || p.CredentialID != actor.CredentialID {
		return nil, fmt.Errorf("%w: the actor is not the principal of the request", ErrDenied)
	}
	return p, nil
}

// ctxManager is the principal bound on ctx when it may administer accounts
// (mayManageUsers: core:users.manage through an interactive method); what
// names the action in the refusal.
func (s *Service) ctxManager(ctx context.Context, what string) (*Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: no principal is bound on the request", ErrDenied)
	}
	if err := s.mayManageUsers(p, what); err != nil {
		return nil, err
	}
	return p, nil
}

// ctxAdmin is ctxManager that also requires core:admin: the writes that vouch
// for an address (a mailed invite, a verified admin-set address) are an
// admin's, as HEAD's were (handlers.adminRole), because a verified address is
// what an SSO sign-in links to.
func (s *Service) ctxAdmin(ctx context.Context, what string) (*Principal, error) {
	p, err := s.ctxManager(ctx, what)
	if err != nil {
		return nil, err
	}
	if !p.Perms.Has(rbac.CoreAdmin) {
		return nil, fmt.Errorf("%w: %s needs %s", ErrDenied, what, rbac.CoreAdmin)
	}
	return p, nil
}
