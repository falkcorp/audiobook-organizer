// file: internal/database/iface_oauth.go
// version: 1.0.0
// guid: 4f8a1c62-7d09-4b53-8e14-6a2c9b0d3e57

package database

// OAuthIdentityStore covers linked external-identity (OAuth/OIDC/Cloudflare Access)
// CRUD. Lookups are by (provider, subject) — the provider's stable account id.
type OAuthIdentityStore interface {
	CreateOAuthIdentity(identity *OAuthIdentity) (*OAuthIdentity, error)
	GetOAuthIdentityByProviderSubject(provider, subject string) (*OAuthIdentity, error)
	GetOAuthIdentitiesForUser(userID string) ([]OAuthIdentity, error)
}
