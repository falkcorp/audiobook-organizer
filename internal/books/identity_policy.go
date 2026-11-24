// file: internal/books/identity_policy.go
// version: 0.1.0
// guid: 3e8a6d52-1c94-4b07-9f3d-a5b2c7e08164
// last-edited: 2026-11-21

package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// cfaccessProvider is the provider name core's policy and the extension carry.
const cfaccessProvider = "cfaccess"

// cfaccessAllowedEmailsKey is where the Cloudflare Access allow-list lives under
// the flip (modules.books.cfaccess.allowed_emails, env
// ABO_BOOKS_CFACCESS_ALLOWED_EMAILS, owner-protected): the retired
// OAUTH_ALLOWED_EMAILS, which M2 writes here as well as into the OIDC provider
// entries it builds. It is read straight from the key, never through the
// snapshot's OAuthAllowedEmails: that field is the OIDC provider fold's
// projection (empty or another provider's list on an install with none or
// other entries), and an Access-only install has no provider entry at all.
const cfaccessAllowedEmailsKey coreconfig.Path = "modules.books.cfaccess.allowed_emails"

// identityPolicy is the admission policy of a Cloudflare Access sign-in under
// core identity, built from exactly the inputs HEAD's gate read: the allow-list
// (the books key above, which was OAUTH_ALLOWED_EMAILS; normalized by
// legacyoauth.New, the same function that built HEAD's) and the role of a newly
// created user (OAUTH_DEFAULT_ROLE, "viewer" when empty, again legacyoauth.New's
// rule). There is no domain rule: HEAD's
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

// errAllowListUnreadable is the refusal of a boot that cannot read the Access
// allow-list: guessing an empty one would lock the owner out silently, and a
// guessed full one would admit strangers, so the boot stops instead.
var errAllowListUnreadable = errors.New("books: the Cloudflare Access allow-list " + string(cfaccessAllowedEmailsKey) + " cannot be read")

// cfaccessAllowedEmails is the allow-list string as the key holds it (the
// effective value, whatever layer set it). An empty or unset key is the empty
// list, which admits nobody. An absent config reader, an unregistered key or a
// value that is not a string is errAllowListUnreadable.
func cfaccessAllowedEmails(acc module.ConfigReads) (string, error) {
	if acc == nil {
		return "", fmt.Errorf("%w: the module was handed no config access", errAllowListUnreadable)
	}
	ks, ok := acc.Lookup(cfaccessAllowedEmailsKey)
	if !ok {
		return "", fmt.Errorf("%w: the key is not registered", errAllowListUnreadable)
	}
	if len(ks.Effective) == 0 {
		return "", nil
	}
	var v string
	if err := json.Unmarshal(ks.Effective, &v); err != nil {
		return "", fmt.Errorf("%w: %v", errAllowListUnreadable, err)
	}
	return v, nil
}

// identityPolicyFrom builds the Access policy: the allow-list from the books key
// (cfaccessAllowedEmails) and the new-user role from the config snapshot. The
// role keeps the snapshot's OAuthDefaultRole, which is the OIDC fold's
// default_role: an Access-only install has no provider entry, so it reads the
// legacyoauth default ("viewer"), the narrowest role.
func identityPolicyFrom(acc module.ConfigReads, c config.Config) (oidc.Policy, error) {
	emails, err := cfaccessAllowedEmails(acc)
	if err != nil {
		return oidc.Policy{}, err
	}
	return identityPolicy(emails, c.OAuthDefaultRole), nil
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
