// file: internal/server/legacyauth/accounts.go
// version: 0.1.0
// guid: 3e8a5c16-9d27-4b40-a1f3-7c6b2d9e0f84
// last-edited: 2026-11-22

package legacyauth

import (
	"context"
	"iter"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// InviteRecord is an open core invite as books' routes read it. It is books'
// own type, not coreauth.InviteView, so that the books half of F4-08 B10t builds
// against the published shape before core's lands; the wiring's adapter fills
// it from core's view. It never holds a token.
type InviteRecord struct {
	ID        string
	Username  string
	Email     string
	Role      rbac.RoleName
	CreatedBy coreauth.UserID
	CreatedAt time.Time
	ExpiresAt time.Time
	// BindEmailOnAccept is core's flag (set only by MarkInviteEmailDelivered):
	// the invite's address was mailed and its creator held core:admin, so
	// accepting it binds the address verified. Books reads it to decide whether
	// a rotation may set it again; it is never a second gate of its own.
	BindEmailOnAccept bool
}

// AdminUsers is the one actor-checked reader of core's accounts that books'
// routes share (F4-08 B10t and B10s): auth.Admin.AdminUser and AdminUsers, which
// re-check users.manage and an interactive sign-in. Books never reads a core
// account through the unchecked auth.Identities view for an admin route.
type AdminUsers interface {
	// AdminUser is auth.Admin.AdminUser.
	AdminUser(ctx context.Context, id coreauth.UserID) (*coreauth.User, error)
	// AdminUsers is auth.Admin.AdminUsers.
	AdminUsers(ctx context.Context, f coreauth.UserFilter) iter.Seq2[*coreauth.User, error]
}

// AccountAdmin is the part of core's identity books' invite and admin set-email
// routes call under core identity (F4-08 B10t). It is books' narrow view: the
// handlers name no wider type, and the tests stand a fake in for it.
//
// Under core identity the account record, including the invite and the
// address, is core's, so the books routes that used to write books' store are
// wrappers over these calls. None of them is a grant: each re-checks the
// principal of the ctx (or the explicit actor the caller passes from
// coreauth.PrincipalFrom), so every core rule runs for the request's own
// caller.
type AccountAdmin interface {
	AdminUsers
	// CreateInvite is auth.Admin.CreateInvite: it returns the invite's fragment
	// link, which is the only place its token appears, and the invite.
	CreateInvite(ctx context.Context, actor *coreauth.Principal, in coreauth.InviteSpec) (string, InviteRecord, error)
	// DeleteInvite is auth.Admin.DeleteInvite (the invite's public ID).
	DeleteInvite(ctx context.Context, actor *coreauth.Principal, id string) error
	// Invite is auth.Admin.Invite: one open invite by its public ID, read under
	// users.manage; coreauth.ErrNotFound when there is none. Resend reads the old
	// invite's spec from it.
	Invite(ctx context.Context, id string) (InviteRecord, error)
	// Invites is the actor-checked list of open invites (users.manage), never a
	// token.
	Invites(ctx context.Context) ([]InviteRecord, error)
	// MarkInviteEmailDelivered is auth.Admin.MarkInviteEmailDelivered: the
	// invite's address was mailed, so accepting it may bind the address
	// verified. It re-checks the ctx principal (core:admin, interactive).
	MarkInviteEmailDelivered(ctx context.Context, inviteID string) error
	// SetUserEmail is auth.Admin.SetUserEmail. It re-checks the ctx principal
	// (users.manage, core:admin, interactive, dominance over the target).
	SetUserEmail(ctx context.Context, id coreauth.UserID, email string) error
}
