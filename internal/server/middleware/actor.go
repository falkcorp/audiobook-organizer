// file: internal/server/middleware/actor.go
// version: 1.1.0
// guid: 3c7e92a1-5d48-4b0f-8a63-d1f2e90b74c5
// last-edited: 2026-10-26

package middleware

import (
	"slices"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ActorOptions records, on an operation about to be enqueued, who started it,
// how they were verified and what the request was allowed to do. Every route
// that enqueues an operation for a caller uses it, so no route leaves an op
// without an actor.
//
// The identity is the verified session or token only; a forwarded email header
// is never read. The permission set is the request's own (an API key's scopes
// already intersected with its account's roles), so a job that outlives the
// request, such as the diagnostics export, can be capped at what the credential
// could do rather than at everything its account holds. Nothing is recorded for
// an unauthenticated request.
//
// It never marks the op as an owner apply (opsregistry.WithOwnerApply). The
// auth method it records is "cf_access" for the owner's own Cloudflare Access
// session, the same value the Repairs owner-apply handler records, so on a route
// that takes caller params, such as the generic start and retry routes, the
// method is no proof that a request is an owner apply. Owner grants are redeemed
// only under that mark, which the handler alone sets.
func ActorOptions(c *gin.Context) []opsregistry.EnqueueOption {
	var opts []opsregistry.EnqueueOption
	if c == nil || c.Request == nil {
		return opts
	}
	u, ok := auth.UserFromContext(c.Request.Context())
	if !ok {
		return opts
	}
	opts = append(opts, opsregistry.WithActor(u.ID))
	if m := CurrentAuthMethod(c); m != auth.MethodNone {
		opts = append(opts, opsregistry.WithActorAuthMethod(string(m)))
	}
	if set := auth.PermissionsFromContext(c.Request.Context()); len(set) > 0 {
		perms := make([]string, 0, len(set))
		for p := range set {
			perms = append(perms, string(p))
		}
		slices.Sort(perms)
		opts = append(opts, opsregistry.WithActorPermissions(perms))
	}
	return opts
}
