// file: core/internal/authhttp/errors.go
// version: 1.0.2
// guid: 4e91b6c3-0d27-4a58-9f3e-b8c71d5a2e04
// last-edited: 2026-11-11

package authhttp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The refusals this package makes itself, before a Service method runs.

// forbidden is a 403 naming the requirement the caller does not satisfy.
func forbidden(need rbac.Requirement) *httpapi.Error {
	e := &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied"}
	if ps := need.Permissions(); len(ps) > 0 {
		return httpapi.Forbidden(ps[0])
	}
	return e
}

// notFound is the one answer for a user, key, session or invite that is
// missing, and for one the caller may not see: the same answer, so a probe
// cannot tell them apart.
func notFound() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such resource"}
}

// fail maps a Service error to the response of 05-authn §6. No detail is the
// error's own text: the Service's messages name internals (a username, a key
// ID), and a response must carry only what the route chose to say. An error
// that is not one of the Service's sentinels is a 500 whose cause is logged
// and withheld. s and target identify the dominance refusal for its audit
// line (target is the user acted on, "" when not known).
func (a *api) fail(ctx context.Context, s spec, p *auth.Principal, target string, err error) error {
	if he, ok := errors.AsType[*httpapi.Error](err); ok {
		return he
	}
	var thr *auth.ThrottledError
	if he := authedCredentialFailure(s, err); he != nil {
		return he
	}
	// The Service wraps a bad bootstrap token's error with
	// ErrInvalidCredentials too: the more specific sentinel is matched first.
	switch {
	case errors.As(err, &thr):
		return httpapi.Throttled(time.Duration(thr.RetryAfter) * time.Second)
	case errors.Is(err, auth.ErrThrottled):
		return httpapi.Throttled(time.Duration(auth.LoginRetryAfter) * time.Second)
	case errors.Is(err, auth.ErrBusy):
		return (&httpapi.Error{Status: http.StatusServiceUnavailable, Code: "busy", Detail: "the server is busy hashing passwords; try again"}).
			WithRetryAfter(2 * time.Second)
	case errors.Is(err, auth.ErrStoreUnavailable):
		return &httpapi.Error{Status: http.StatusServiceUnavailable, Code: "auth_unavailable", Detail: "the credential store is unavailable"}
	case errors.Is(err, auth.ErrBootstrapInvalid):
		return &httpapi.Error{Status: http.StatusUnauthorized, Code: "bootstrap_invalid", Detail: "the bootstrap token is wrong, expired or spent"}
	case errors.Is(err, auth.ErrInvalidCredentials):
		return &httpapi.Error{Status: http.StatusUnauthorized, Code: "invalid_credentials", Detail: "the username or password is wrong"}
	case errors.Is(err, auth.ErrResetRequired):
		return &httpapi.Error{Status: http.StatusUnauthorized, Code: "password_reset_required", Detail: "this account must reset its password with a reset link"}
	case errors.Is(err, auth.ErrInactiveUser):
		return &httpapi.Error{Status: http.StatusUnauthorized, Code: "inactive_user", Detail: "this account is not active"}
	case errors.Is(err, auth.ErrSetupClosed):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "setup_closed", Detail: "setup is not available"}
	case errors.Is(err, auth.ErrInviteInvalid):
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "invite_invalid", Detail: "the invite is not valid"}
	case errors.Is(err, auth.ErrDominance):
		a.denyDominance(ctx, s, p, target)
		return &httpapi.Error{Status: http.StatusForbidden, Code: "dominance_required", Detail: "you may not act on a user whose roles you do not hold"}
	case errors.Is(err, auth.ErrScopeEscalation):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "scope_escalation", Detail: "the key would hold a scope its creator does not"}
	case errors.Is(err, auth.ErrDenied):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "you may not do this"}
	case errors.Is(err, auth.ErrLastAdmin):
		return &httpapi.Error{Status: http.StatusConflict, Code: "last_admin", Detail: "this would leave no active administrator"}
	case errors.Is(err, auth.ErrKeyRevoked):
		return &httpapi.Error{Status: http.StatusConflict, Code: "key_revoked", Detail: "a revoked key stays revoked"}
	case errors.Is(err, auth.ErrUserExists):
		return &httpapi.Error{Status: http.StatusConflict, Code: "user_exists", Detail: "that username or e-mail address is taken"}
	case errors.Is(err, auth.ErrStaleEpoch):
		return &httpapi.Error{Status: http.StatusConflict, Code: "stale_account", Detail: "the account changed while the request was running; try again"}
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, auth.ErrRevoked), errors.Is(err, auth.ErrExpired):
		return notFound()
	case errors.Is(err, auth.ErrBadInput):
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "bad_request", Detail: "the request is malformed or incomplete"}
	}
	a.log.ErrorContext(ctx, "authhttp: unmapped error", "route", s.method+" "+s.path, "error", err)
	return err
}

// authedCredentialFailure is the answer to a credential or account failure on a
// route whose caller is already signed in (any route that is not Public). A 401
// there reads as a dead session to the SPA, which signs the user out, when the
// session is fine and the Service refused something else: the account behind
// the request was deactivated or restricted, or a password the request
// supplied is wrong. So these are 409 and 422, never 401. Sign-in routes
// (Public) keep 401: there it is the answer to the credential presented.
// nil means err is not one of these, or the route is a sign-in route.
func authedCredentialFailure(s spec, err error) *httpapi.Error {
	if s.perm.Kind() == rbac.KindPublic {
		return nil
	}
	switch {
	case errors.Is(err, auth.ErrResetRequired):
		return &httpapi.Error{Status: http.StatusConflict, Code: "password_reset_required", Detail: "this account must reset its password with a reset link"}
	case errors.Is(err, auth.ErrInactiveUser):
		return &httpapi.Error{Status: http.StatusConflict, Code: "inactive_user", Detail: "this account is not active"}
	case errors.Is(err, auth.ErrInvalidCredentials):
		return &httpapi.Error{Status: http.StatusUnprocessableEntity, Code: "invalid_credentials", Detail: "the password or credential supplied is wrong"}
	}
	return nil
}

// linkFail is fail for a call that spends a one-time token (a temp-login or
// reset link, an invite): every way the token can be bad, spent, expired, stale
// or its user gone or restricted is the one answer, so a holder of a token
// cannot learn which, and nobody can learn whether an account exists.
func (a *api) linkFail(ctx context.Context, s spec, err error) error {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrExpired), errors.Is(err, auth.ErrRevoked),
		errors.Is(err, auth.ErrStaleEpoch), errors.Is(err, auth.ErrInactiveUser), errors.Is(err, auth.ErrResetRequired),
		errors.Is(err, auth.ErrInviteInvalid), errors.Is(err, auth.ErrNotFound), errors.Is(err, auth.ErrUserExists):
		return &httpapi.Error{Status: http.StatusUnauthorized, Code: "link_invalid", Detail: "the link is not valid"}
	}
	return a.fail(ctx, s, nil, "", err)
}

// oidcInline maps an OIDC error that is answered inline (not by a redirect to
// the sign-in page). The second result is false for an error the callback
// answers with a redirect.
func oidcInline(err error) (*httpapi.Error, bool) {
	switch {
	case errors.Is(err, oidc.ErrUnknownProvider), errors.Is(err, oidc.ErrNotConfigured):
		return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such provider"}, true
	case errors.Is(err, oidc.ErrBadRedirect):
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "bad_redirect_uri", Detail: "redirect_uri is not registered"}, true
	case errors.Is(err, oidc.ErrBadChallenge):
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "bad_code_challenge", Detail: "code_challenge_method must be S256 and the challenge a PKCE S256 value"}, true
	case errors.Is(err, oidc.ErrAppStateTooLong):
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "app_state_too_long", Detail: "the state parameter is too long"}, true
	case errors.Is(err, oidc.ErrStateInvalid):
		return &httpapi.Error{Status: http.StatusBadRequest, Code: "oauth_state_invalid", Detail: "the sign-in state is invalid or expired; start again"}, true
	case errors.Is(err, auth.ErrEmailInUse):
		return &httpapi.Error{Status: http.StatusConflict, Code: "email_in_use", Detail: "sign in and link this provider from your profile"}, true
	case errors.Is(err, auth.ErrStoreUnavailable):
		return &httpapi.Error{Status: http.StatusServiceUnavailable, Code: "auth_unavailable", Detail: "the credential store is unavailable"}, true
	}
	return nil, false
}

// oidcRedirectCode is the sign-in page's error code for a callback failure
// that is answered by a redirect to it, as HEAD's callback did (the page shows
// a short message for the code): nothing in it names an address, a token or a
// reason an operator would not tell the user.
func oidcRedirectCode(err error) string {
	switch {
	case errors.Is(err, oidc.ErrDeniedByProvider):
		return "oauth_denied"
	case errors.Is(err, oidc.ErrNoCode):
		return "oauth_no_code"
	case errors.Is(err, oidc.ErrExchange):
		return "oauth_exchange_failed"
	case errors.Is(err, oidc.ErrMintFailed):
		return "oauth_code_mint_failed"
	case errors.Is(err, oidc.ErrEmailNotVerified), errors.Is(err, oidc.ErrEmailNotPlain), errors.Is(err, oidc.ErrEmailNotAllowed),
		errors.Is(err, auth.ErrInactiveUser), errors.Is(err, auth.ErrResetRequired), errors.Is(err, oidc.ErrSetupRequired):
		return "oauth_not_authorized"
	}
	return codeOAuthFailed
}

// codeOAuthFailed is oidcRedirectCode's answer for an error nobody classified:
// the callback answers it as a server error instead of a redirect.
const codeOAuthFailed = "oauth_failed"

func errUnauthenticated() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "sign in to use this route"}
}

func errInteractive() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusForbidden, Code: "interactive_required", Detail: "this route needs an interactive sign-in, not an API key"}
}

func errOwner() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusForbidden, Code: "owner_required", Detail: "this route needs an owner-verified sign-in"}
}
