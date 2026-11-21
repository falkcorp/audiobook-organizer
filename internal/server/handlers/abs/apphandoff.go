// file: internal/server/handlers/abs/apphandoff.go
// version: 0.1.0
// guid: 7d2a94e1-5b3c-4f60-8a17-c9e0b3d62f85
// last-edited: 2026-11-20

package abs

import (
	"context"
	"errors"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// appHandoff is books' native-app hand-off for core's OIDC callback
// (05-authn §4.7, SEC-2): core resolves the user of a sign-in that a native ABS
// app started, then asks this value for an app authorization code instead of
// issuing a web session. It is a thin adapter over the three exported seam
// functions of openid.go and holds no state of its own: the code store, the
// redirect allowlist and the redemption at /auth/openid/callback stay HEAD's,
// so there is exactly one place that decides what a valid app callback is.
//
// What is enforced where (each control once):
//
//   - The redirect allowlist is AppRedirectURIAllowed (exact match, the same
//     list /auth/openid enforces). Core's Start asks Allowed before it parks
//     the target in the signed state, and again before it calls Mint; Mint
//     re-checks inside MintAppAuthorizationCode, so a code is never bound to a
//     target the allowlist did not approve.
//   - The PKCE method is S256 only. Mint is never told the method, so the only
//     place that can enforce it is core's oidc.Flow.Start (ErrBadChallenge).
//     This file does not add a second, weaker check of it.
//   - The challenge's shape (43 base64url characters) is checked by
//     MintAppAuthorizationCode. Core's own check admits 43 to 128 characters,
//     so a longer challenge passes Start and is refused here, after the
//     identity-provider round trip (fail closed, reported by core as
//     oauth_code_mint_failed).
//   - The code is single-use, expires in oidcCodeTTL and carries the user's
//     credential epoch; OpenIDCallback enforces all three at redemption.
//
// Whether OpenIDCallback sees core's epoch is the flip's question, not this
// file's: it compares the code's epoch with h.store.GetUserByID's row.
type appHandoff struct{}

// NewAppHandoff returns books' hand-off, the value the flip registers as
// auth.MethodSpec{Method: "books.abs", AppHandoff: abs.NewAppHandoff()}.
func NewAppHandoff() coreauth.OIDCAppHandoff { return appHandoff{} }

var _ coreauth.OIDCAppHandoff = appHandoff{}

// Allowed is HEAD's exact-match redirect allowlist.
func (appHandoff) Allowed(redirectURI string) bool { return AppRedirectURIAllowed(redirectURI) }

// Mint issues HEAD's single-use authorization code for the signed-in user and
// returns the app-scheme location that carries it and the app's state.
//
// The minter needs only the user's ID and credential epoch, and both are
// copied from the record core resolved for this sign-in, never re-read, so the
// code is stamped with the epoch the sign-in was admitted at. A user that is
// not active, or that core marked SignInRestricted, gets no code: core already
// refuses both before it calls here, and a hand-off that issued one anyway
// would be a bearer credential for an account that may not sign in.
func (appHandoff) Mint(_ context.Context, u *coreauth.User, email, challenge, redirectURI, appState string) (string, error) {
	// An empty ID is refused by MintAppAuthorizationCode itself.
	if u == nil {
		return "", errors.New("abs: cannot mint an authorization code with no user")
	}
	if u.Status != coreauth.StatusActive || u.SignInRestricted {
		return "", errors.New("abs: cannot mint an authorization code for a user who may not sign in")
	}
	code, err := MintAppAuthorizationCode(&database.User{ID: string(u.ID), CredentialEpoch: u.CredentialEpoch}, email, challenge, redirectURI)
	if err != nil {
		return "", err
	}
	return AppRedirectURL(redirectURI, code, appState), nil
}
