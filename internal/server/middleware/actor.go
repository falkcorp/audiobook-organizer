// file: internal/server/middleware/actor.go
// version: 1.2.0
// guid: 3c7e92a1-5d48-4b0f-8a63-d1f2e90b74c5
// last-edited: 2026-10-26

package middleware

import (
	"errors"
	"slices"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
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
	// Always recorded for an authenticated request, even when empty: a request
	// whose context carries no permission set holds nothing (auth.PermissionsFromContext),
	// and a job capped at that set must hold nothing too, not fall back to what
	// the account's roles hold.
	set := auth.PermissionsFromContext(c.Request.Context())
	perms := make([]string, 0, len(set))
	for p := range set {
		perms = append(perms, string(p))
	}
	slices.Sort(perms)
	opts = append(opts, opsregistry.WithActorPermissions(perms))
	return opts
}

// RespondEnqueueError answers a failed EnqueueOp the way every route that starts
// an operation must: a start the definition refuses (its StartPrecondition,
// opsregistry.StartRefusedError) is a 409 carrying the reason, and anything else
// is an internal error under internalMsg. One mapping for every start route, so
// a refusal the registry makes reads the same on the generic start, its retry,
// the v3 run routes and a definition's own route.
func RespondEnqueueError(c *gin.Context, internalMsg string, err error) {
	var refused *opsregistry.StartRefusedError
	if errors.As(err, &refused) {
		httputil.RespondWithConflict(c, refused.Reason)
		return
	}
	httputil.InternalError(c, internalMsg, err)
}
