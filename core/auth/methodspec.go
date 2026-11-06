// file: core/auth/methodspec.go
// version: 1.0.0
// guid: 683a3d5a-6b87-4cc1-b187-b1c61724fff9
// last-edited: 2026-11-06

package auth

import "context"

type MethodSpec struct {
	Method               Method // "ext:cfaccess", "books.abs"
	Interactive          bool
	MayChangeCredentials bool
	OwnerVerifier        bool // the extension may assert AssuranceOwner
	// AppHandoff is the native-app hand-off of the OIDC callback (§4.7,
	// SEC-2): set only on a module compat method (ABO: books.abs, whose
	// openid callback redeems the code), at most one per app; nil on an
	// extension. The kernel refuses it elsewhere (ErrMethodSpec).
	AppHandoff OIDCAppHandoff
}

// OIDCAppHandoff hands a sign-in started by a native app back to that app
// instead of issuing a web session (HEAD: oauth_login.go appDeepLinkParams
// and abs.MintAppAuthorizationCode). Books implements it over its own code
// store.
type OIDCAppHandoff interface {
	// Allowed is the exact-match allowlist of app redirect URIs (HEAD's
	// abs.AppRedirectURIAllowed, the same list the ABS openid route uses).
	Allowed(redirectURI string) bool
	// Mint issues a single-use authorization code bound to the app's PKCE
	// challenge, the user and the user's credential epoch, and returns the
	// app-scheme location carrying the code and the app's state (HEAD's
	// MintAppAuthorizationCode and AppRedirectURL).
	Mint(ctx context.Context, u *User, email, challenge, redirectURI, appState string) (location string, err error)
}
