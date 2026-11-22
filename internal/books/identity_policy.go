// file: internal/books/identity_policy.go
// version: 0.1.0
// guid: 3e8a6d52-1c94-4b07-9f3d-a5b2c7e08164
// last-edited: 2026-11-21

package books

import (
	"context"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	"github.com/falkcorp/mediacore/rbac"
)

// cfaccessProvider is the provider name core's policy and the extension carry.
const cfaccessProvider = "cfaccess"

// identityPolicy is the admission policy of a Cloudflare Access sign-in under
// core identity, built from exactly the inputs HEAD's gate read: the allow-list
// (OAUTH_ALLOWED_EMAILS, normalized by legacyoauth.New, the same function that
// built HEAD's) and the role of a newly created user (OAUTH_DEFAULT_ROLE, "viewer"
// when empty, again legacyoauth.New's rule). There is no domain rule: HEAD's
// IsEmailAllowed matched an exact address and nothing else, so AllowedDomains
// stays empty and an address that is not on the list is refused, as at HEAD (an
// empty list admits nobody). The invite allow-list HEAD's ExtraAllowed read is
// core's own (the Store's InviteAllowed), not an input here. Accounts are linked
// by a verified e-mail as HEAD's ResolveUser step 4 did.
func identityPolicy(allowedEmails, defaultRole string) oidc.Policy {
	c := legacyoauth.New(legacyoauth.Config{
		AllowedEmails: legacyoauth.ParseAllowedEmails(allowedEmails),
		DefaultRole:   defaultRole,
	})
	return oidc.Policy{
		Provider:            cfaccessProvider,
		AllowedEmails:       c.AllowedEmails,
		DefaultRole:         rbac.RoleName(c.DefaultRole),
		LinkExistingByEmail: true,
	}
}

// identityPolicyFrom reads the two inputs from a config snapshot.
func identityPolicyFrom(c config.Config) oidc.Policy {
	return identityPolicy(c.OAuthAllowedEmails, c.OAuthDefaultRole)
}

// policyResolver is the account resolver the Access extension holds. The
// extension is built before the config boot, but the allow-list is read from
// the settings that boot loads, as HEAD read it when it built its server (after
// the settings boot), so the policy is bound by Register at that point: until
// then the resolver refuses every claim (oidc.ErrResolverUnbound), so an
// extension that holds it admits nobody. The policy the extension was built with
// (a placeholder carrying only the provider) is never used.
type policyResolver struct {
	inner cfaccess.AccountResolver
	pol   atomic.Pointer[oidc.Policy]
}

func newPolicyResolver(inner cfaccess.AccountResolver) *policyResolver {
	return &policyResolver{inner: inner}
}

// bind sets the policy every later Resolve runs under.
func (p *policyResolver) bind(pol oidc.Policy) { p.pol.Store(&pol) }

// Resolve implements cfaccess.AccountResolver.
func (p *policyResolver) Resolve(ctx context.Context, _ oidc.Policy, c oidc.Claims) (*auth.User, error) {
	pol := p.pol.Load()
	if pol == nil {
		return nil, oidc.ErrResolverUnbound
	}
	return p.inner.Resolve(ctx, *pol, c)
}
