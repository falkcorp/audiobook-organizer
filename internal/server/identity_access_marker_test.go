// file: internal/server/identity_access_marker_test.go
// version: 1.0.0
// guid: 07c2345f-2bf2-4c06-94a8-b14832be29db
// last-edited: 2026-11-28

package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
)

// The verified marker under core identity, through the server's own Access
// stage (accessMiddleware, cfaccess.CloudflareAccessVerifyOnly) and the
// client-key reader after it (trustedClientIP, loginThrottleKey): when the
// stage marks a request, a request from this host is keyed on the visitor
// CF-Connecting-IP names; otherwise on the peer. The cells are the two the
// binder's rule decides: an API key never borrows an assertion riding along
// with it, and a service token, which names no person, still marks.

// accessServiceTokenJWT is a valid Access service-token assertion: it verifies
// and names no person.
const accessServiceTokenJWT = "jwt-service-token-for-the-batsignal-relay"

// An API key with a valid assertion riding along: the key authenticates the
// request, the assertion is not consulted, so the request is not marked and
// the login throttle keys on the peer, not on the visitor the header names.
// The control, the same request without the key, is marked and keys on the
// visitor, so the cell tells the two apart.
func TestIdentityAccessMarker_AnAPIKeyNeverBorrowsTheAssertion(t *testing.T) {
	fx := newAccessBinderFixture(t)
	p, c := adoptPrincipal(coreauth.MethodAPIKey, fx.alfred)

	keyed := disagreeRequest(http.MethodGet, "/api/v1/auth/me", binderAlfredJWT)
	keyed.Header.Set("Authorization", "Bearer abk_gcpd-rooftop-relay-key")
	identityServe(fx.srv.router, keyed, p, c)
	require.False(t, fx.marked, "a request presenting an API key is not marked by the assertion riding along")
	require.Equal(t, disagreePeer, fx.throttleKey, "an API key request keys the login throttle on the peer")

	identityServe(fx.srv.router, disagreeRequest(http.MethodGet, "/api/v1/auth/me", binderAlfredJWT), p, c)
	require.True(t, fx.marked, "control: the same assertion without a key marks the request")
	require.Equal(t, disagreeVisitor, fx.throttleKey, "control: a marked request from this host keys on the visitor")
	require.Empty(t, fx.store.seen())
}

// A service token (valid, no person): the request is marked and keys on the
// visitor, exactly as HEAD's binder treated it. Core admits no one for it, so
// it is still refused.
func TestIdentityAccessMarker_AServiceTokenMarks(t *testing.T) {
	fx := newAccessBinderFixtureOpts(t, accessBinderOpts{allowed: binderEmail, extra: fakeAccessVerifier{accessServiceTokenJWT: nil}})

	rec := identityServe(fx.srv.router, disagreeRequest(http.MethodGet, "/api/v1/auth/me", accessServiceTokenJWT), nil, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "a service token admits no one: %s", rec.Body.String())
	require.True(t, fx.marked, "a service token is proof the request passed Access: marked")
	require.Equal(t, disagreeVisitor, fx.throttleKey, "a marked request from this host keys on the visitor")

	fx.marked = false
	identityServe(fx.srv.router, disagreeRequest(http.MethodGet, "/api/v1/auth/me", binderForgedJWT), nil, nil)
	require.False(t, fx.marked, "control: an assertion that does not verify does not mark")
	require.Equal(t, disagreePeer, fx.throttleKey, "control: an unmarked request keys on the peer")
	require.Empty(t, fx.store.seen())
}
