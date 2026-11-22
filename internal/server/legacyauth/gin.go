// file: internal/server/legacyauth/gin.go
// version: 0.1.0
// guid: 8c5e2a19-7d43-4f06-b1a8-2e9f4d6c3b75
// last-edited: 2026-11-20

package legacyauth

import (
	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// The gin adapters (05-http-api §8.3). They are in-handler checks over the
// principal and checker stage 12 bound, enforced through rbac so a denial is
// audited (SEC-17), and they answer with HEAD's legacy bodies (the gin mount
// keeps its own bodies, 05-http-api §4.3). The route-level decision is stage
// 13's (core/httpapi); these run again inside gin so a gin route that ever
// disagreed with its mux pattern is still checked (SEC2-22).

// Messages of the refusals, HEAD's where HEAD had one.
const (
	msgAuthRequired  = "authentication required"
	msgAdminRequired = "admin role required"
	msgOwnerRequired = "this action needs the deployment owner's verified sign-in"
)

// unauthenticated answers 401 and stops the chain.
func unauthenticated(c *gin.Context) {
	httputil.RespondWithUnauthorized(c, msgAuthRequired)
	c.Abort()
}

// forbidden answers 403 with msg and stops the chain.
func forbidden(c *gin.Context, msg string) {
	httputil.RespondWithForbidden(c, msg)
	c.Abort()
}

// enforce runs rbac.RequireRequirement on r and answers a refusal as HEAD's
// RespondPermissionDenied did: 401 when the caller has no user (the
// auth-disabled principal, refused only an all-AuthRequired requirement,
// 05-rbac §4.11), 403 with msg otherwise.
func enforce(c *gin.Context, p *coreauth.Principal, r rbac.Requirement, msg string) {
	if holds(c, p, r, msg) {
		c.Next()
	}
}

// holds is enforce's check without continuing the chain: it reports whether
// the caller satisfies r and, when not, writes the refusal and stops the chain.
func holds(c *gin.Context, p *coreauth.Principal, r rbac.Requirement, msg string) bool {
	if err := rbac.RequireRequirement(c.Request.Context(), r); err != nil {
		if p.User == nil {
			unauthenticated(c)
			return false
		}
		forbidden(c, msg)
		return false
	}
	return true
}

// RequirePerm is the adapter of HEAD's s.perm(atom): the caller must satisfy
// the atom's table route requirement, the same requirement as the route's
// manifest Perm, so stage 13 and the adapter agree (COR5-4). The
// auth-disabled principal is judged by its own checker, under which every
// s.perm route passes except one whose requirement is all AuthRequired
// (05-rbac §4.11). No principal is 401; an unknown atom is 403.
func (b *Bridge) RequirePerm(atom string) gin.HandlerFunc {
	msg := "permission denied: " + atom
	return func(c *gin.Context) {
		p, ok := coreauth.PrincipalFrom(c.Request.Context())
		if !ok {
			unauthenticated(c)
			return
		}
		r, err := b.resolve(atom, false)
		if err != nil {
			forbidden(c, msg)
			return
		}
		enforce(c, p, r, msg)
	}
}

// RequireAdmin is the adapter of HEAD's role-name RequireAdmin and adminRole
// gates: the caller must satisfy perm, which is 05-rbac §6.2's permission for
// the route's admin kind (books:admin.view, AllOf(core:users.manage,
// books:admin.view), books:admin.debug or core:admin; the route's manifest
// Perm). Auth-off reach follows from the auth-disabled checker. perm must be
// an AllOf or AnyOf; any other value refuses every request.
func (b *Bridge) RequireAdmin(perm rbac.Requirement) gin.HandlerFunc {
	return func(c *gin.Context) {
		if b.CheckAdmin(c, perm) {
			c.Next()
		}
	}
}

// CheckAdmin is RequireAdmin's decision without continuing the chain, for a
// gate that adds a further check of its own after it (F4-08 B7b). It reports
// whether the caller satisfies perm; when not it has written the refusal (401
// without a principal or a user, 403 otherwise, and 403 for a perm that is not
// an AllOf or AnyOf) and stopped the chain.
func (b *Bridge) CheckAdmin(c *gin.Context, perm rbac.Requirement) bool {
	p, ok := coreauth.PrincipalFrom(c.Request.Context())
	if !ok {
		unauthenticated(c)
		return false
	}
	if perm.Kind() != rbac.KindAllOf && perm.Kind() != rbac.KindAnyOf {
		forbidden(c, msgAdminRequired)
		return false
	}
	return holds(c, p, perm, msgAdminRequired)
}

// RequireCredentialChange is the adapter of HEAD's credGuard (Guards.Interactive
// at stage 13): the principal's method must be one that may change
// credentials (coreauth.Principal.MayChangeCredentials, the check stage 13 and
// authhttp make, which reads the registered MethodSpec of an extension). The auth-disabled principal is refused: HEAD's guard was a
// pass-through with auth off, and core tightens it on purpose (05-authn
// §4.11, 05-rbac §4.11 (a), SEC5-8). The body is HEAD's.
func RequireCredentialChange() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := coreauth.PrincipalFrom(c.Request.Context())
		if !ok {
			unauthenticated(c)
			return
		}
		if !p.MayChangeCredentials() {
			forbidden(c, auth.CredentialChangeRefusedMessage)
			return
		}
		c.Next()
	}
}

// RequireOwner is the adapter of HEAD's RequireOwner (Guards.Owner at stage
// 13, 05-authn §4.8): the principal must carry AssuranceOwner, which only an
// OwnerVerifier extension sets. Only when core.auth.owner.required is false
// (the injected OwnerRequired; nil reads as true) does it fall back to an
// interactive principal holding core:admin, judged through rbac.Require so
// the denial is audited. The auth-disabled principal has neither and is
// refused.
func (b *Bridge) RequireOwner() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		p, ok := coreauth.PrincipalFrom(ctx)
		if !ok {
			unauthenticated(c)
			return
		}
		if p.Assurance&coreauth.AssuranceOwner != 0 {
			c.Next()
			return
		}
		if b.ownerRequired == nil || b.ownerRequired() || !p.Interactive() {
			forbidden(c, msgOwnerRequired)
			return
		}
		if rbac.Require(ctx, rbac.CoreAdmin) != nil {
			forbidden(c, msgOwnerRequired)
			return
		}
		c.Next()
	}
}
