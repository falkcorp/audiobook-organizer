// file: core/auth/oidc/errors.go
// version: 1.0.1
// guid: 21138cfb-2d4d-4e59-ab0f-fb39e104eb9f
// last-edited: 2026-11-11

package oidc

import "errors"

// Refusals of the sign-in flow. A caller maps them with errors.Is and never
// by text. None carries a token, a code or a secret.
var (
	// ErrUnknownProvider: no enabled provider has that id (404).
	ErrUnknownProvider = errors.New("oidc: unknown provider")
	// ErrBadRedirect: an app redirect_uri the hand-off does not allow exactly,
	// or one that is not a plain absolute URI. Answered inline with 400, never
	// by a redirect to it.
	ErrBadRedirect = errors.New("oidc: redirect_uri is not registered")
	// ErrBadChallenge: the app's code_challenge_method is not S256 (S256 only,
	// no plain, and no default), or the challenge is not a 43-128 character
	// base64url string. 400.
	ErrBadChallenge = errors.New("oidc: code_challenge_method must be S256 and the challenge a PKCE S256 value")
	// ErrAppStateTooLong: the native app's state is longer than MaxAppState, or
	// the signed binding it travels in would outgrow a cookie. 400.
	ErrAppStateTooLong = errors.New("oidc: the app state is too long")
	// ErrStateInvalid: the state is missing, malformed, forged, expired,
	// already used, for another provider, or does not match the callback's
	// state parameter. 400 oauth_state_invalid (§6).
	ErrStateInvalid = errors.New("oidc: sign-in state invalid")
	// ErrNoCode: the callback carried no authorization code.
	ErrNoCode = errors.New("oidc: no authorization code")
	// ErrDeniedByProvider: the provider reported an error (the user declined).
	ErrDeniedByProvider = errors.New("oidc: the provider refused the sign-in")
	// ErrExchange: the code exchange or the id_token check failed. The cause
	// is logged by class, never returned: it can quote a provider response.
	ErrExchange = errors.New("oidc: code exchange failed")
	// ErrEmailNotVerified: the provider did not vouch for the address.
	ErrEmailNotVerified = errors.New("oidc: the identity's e-mail is not verified")
	// ErrEmailNotPlain: the address is not printable ASCII, or differs from its
	// normalized form by more than case. NormalizeEmail folds compatibility
	// characters (the Kelvin sign, fullwidth letters) onto ASCII, so a
	// different identity at the provider could otherwise read as another
	// person's address: the same rule IsOwnerEmail applies to the owner.
	ErrEmailNotPlain = errors.New("oidc: the identity's e-mail is not a plain ASCII address")
	// ErrEmailNotAllowed: the address is neither on the provider's allowlist
	// nor invited.
	ErrEmailNotAllowed = errors.New("oidc: e-mail is not allowed to sign in")
	// ErrMintFailed: the app hand-off could not issue its code, or returned a
	// location off the app's redirect_uri. No web session was opened.
	ErrMintFailed = errors.New("oidc: the app authorization code could not be issued")
	// ErrSetupRequired: no user exists yet; the first account comes from setup.
	ErrSetupRequired = errors.New("oidc: no account exists yet; set the app up first")
	// ErrNotConfigured: redirect_base_url is missing or unusable.
	ErrNotConfigured = errors.New("oidc: sign-in is not configured")
)
