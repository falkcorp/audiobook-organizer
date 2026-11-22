// file: internal/books/identity_access.go
// version: 0.1.0
// guid: 8e2c5a17-d4b9-4f63-a1e8-3c7f09b6d254
// last-edited: 2026-11-21

package books

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
)

// accessResolver is legacyauth.Identity.Access: the ABS surface's Cloudflare
// Access sign-in (single sign-on, Mode A, the A7 hand-off's hop 2) resolved by
// the same account resolver and the same admission policy the Access extension
// holds (policyResolver), so a person admitted on /api/v1 and a person admitted
// on the ABS surface are one decision.
//
// The provider is cfaccessProvider, as the extension names it, and the policy
// the extension passes is a placeholder policyResolver replaces with the one
// Register bound; before that bind, and while core has not bound its resolver,
// every call is refused (oidc.ErrResolverUnbound) and admits nobody.
func (w *identityWiring) accessResolver() legacyauth.AccessResolver {
	return func(ctx context.Context, c legacyauth.AccessClaims) (*coreauth.User, error) {
		return w.policy.Resolve(ctx, oidc.Policy{Provider: cfaccessProvider}, oidc.Claims{
			Provider: cfaccessProvider, Subject: c.Subject, Email: c.Email, EmailVerified: c.EmailVerified,
		})
	}
}
