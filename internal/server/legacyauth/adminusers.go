// file: internal/server/legacyauth/adminusers.go
// version: 0.1.0
// guid: 8d41c7e2-5a93-4f16-b0e8-3c72a9d5f164
// last-edited: 2026-11-22

package legacyauth

import (
	"context"
	"iter"

	coreauth "github.com/falkcorp/mediacore/auth"
)

// AdminUsers is the actor-checked read of core's user records that books'
// admin routes use under core identity (F4-08 B10s, shared with B10t). It is
// auth.Admin's AdminUser and AdminUsers, which B10t-core publishes: each call
// re-checks the principal of ctx (core:users.manage and an interactive
// sign-in, as auth.Admin.PlanUserMerge does), so holding the handle grants
// nothing, and the records carry no credential material.
//
// It is deliberately NOT a member of Identity and NOT the read-only
// auth.Identities view: that view answers any caller, and a books route that
// lists or reads other accounts must be judged against the caller it serves.
// A request reads its OWN record through the principal it already carries
// (Bridge.FromCorePrincipal), never through this.
type AdminUsers interface {
	// AdminUser is one account; coreauth.ErrNotFound when there is none.
	AdminUser(ctx context.Context, id coreauth.UserID) (*coreauth.User, error)
	// AdminUsers lists accounts, EmailVerified included.
	AdminUsers(ctx context.Context, f coreauth.UserFilter) iter.Seq2[*coreauth.User, error]
}
