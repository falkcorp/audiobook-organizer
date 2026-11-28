// file: internal/cfaccess/owner_guard_core.go
// version: 0.1.0
// guid: 52809594-6279-4f92-b215-422487cf3a81
// last-edited: 2026-11-21

package cfaccess

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// The owner gate under core identity (F4-08 B10b2). RequireOwner above reads
// the legacy context (auth.MethodFromContext and the Access e-mail the gin
// binder recorded); under core identity nothing sets either, and the same
// proof is the principal stage 12 bound: it came from this package's identity
// extension (CoreMethod) and carries AssuranceOwner, which only an
// OwnerVerifier assertion sets and which Extension asserts only when the
// verified JWT's e-mail passes auth.IsOwnerEmail against owner_email, the one
// comparison HEAD's proof uses.
//
// This is deliberately not core's own Guards.Owner fallback (the legacyauth
// bridge's RequireOwner, 05-authn 4.8): with core.auth.owner.required false it
// lets an interactive principal holding core:admin through, which would
// admit a password-session admin where HEAD refuses everyone but the Access
// owner while owner_email is set. A user's own e-mail is never a proof
// either: two accounts can share the owner's address.

// CoreMethod is the method of the principals this package's identity
// extension binds (Extension): "ext:" + ExtensionName.
const CoreMethod = coreauth.Method("ext:" + ExtensionName)

// OwnerProofWhyNotCore is auth.OwnerProofWhyNot over a core principal: "" when
// p is the owner (CoreMethod with AssuranceOwner), otherwise why not, in the
// same words HEAD's refusal uses so the UI says the same thing. It fails
// closed: an unset owner e-mail refuses everyone, a missing principal and a
// principal of any other method refuse, and a CoreMethod principal without
// AssuranceOwner (a verified Access user who is not the owner) refuses.
// Assurance on any other method does not count: the proof is the Access
// extension's, whatever else might one day set the bit.
func OwnerProofWhyNotCore(p *coreauth.Principal, ownerEmail, host string) string {
	if strings.TrimSpace(ownerEmail) == "" {
		return "Owner actions are turned off: no owner email is configured (set OWNER_EMAIL in the server environment or config file and restart; it cannot be set through the API)"
	}
	if p == nil || p.Method != CoreMethod {
		hint := "Cloudflare Access"
		if host != "" {
			hint += " (" + host + ")"
		}
		return "Owner actions need you to sign in through " + hint + "; a password, SSO, temp-login or invite session, or an API key, is not enough"
	}
	if p.Assurance&coreauth.AssuranceOwner == 0 {
		return "Owner actions need the owner's Cloudflare Access sign-in; this request is signed in as an unknown identity"
	}
	return ""
}

// RequireOwnerCore is RequireOwner under core identity: the same gate (the
// owner proof, then the permission leg, then the audit line), reading core's
// principal. ownerEmail is read on every request; nil or "" refuses everyone.
//
// applies has RequireOwner's meaning: when non-nil it limits the gate to the
// requests it reports true for.
//
// perm "" skips the permission leg only, as in RequireOwner. A non-empty perm
// is judged by can (the bridge's check of the atom, the replacement of HEAD's
// auth.Can); a nil can fails closed, so a wiring that forgets it refuses
// rather than passes. The owner proof is never skipped.
func RequireOwnerCore(ownerEmail func() string, perm auth.Permission, can func(ctx context.Context, perm auth.Permission) bool, applies func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if applies != nil && !applies(c) {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		email := ""
		if ownerEmail != nil {
			email = ownerEmail()
		}
		p, _ := coreauth.PrincipalFrom(ctx)
		userID, method := "", ""
		if p != nil {
			method = string(p.Method)
			if p.User != nil {
				userID = string(p.User.ID)
			}
		}
		if why := OwnerProofWhyNotCore(p, email, OwnerPublicHost(c.Request)); why != "" {
			ownerGuardLog.Warn("owner action refused: route=%s http_method=%s method=%s user=%v why=%s",
				c.FullPath(), c.Request.Method, method, userID, why)
			httputil.RespondWithForbidden(c, why)
			c.Abort()
			return
		}
		if perm != "" && (can == nil || !can(ctx, perm)) {
			ownerGuardLog.Warn("owner action refused: route=%s http_method=%s user=%v why=%s",
				c.FullPath(), c.Request.Method, userID, "missing permission "+string(perm))
			httputil.RespondWithForbidden(c, "permission denied: "+string(perm))
			c.Abort()
			return
		}
		ownerGuardLog.Info("owner action allowed: route=%s http_method=%s user=%v method=%s query=%s",
			c.FullPath(), c.Request.Method, userID, method, c.Request.URL.RawQuery)
		c.Next()
	}
}
