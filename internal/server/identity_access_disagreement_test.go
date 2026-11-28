// file: internal/server/identity_access_disagreement_test.go
// version: 1.0.0
// guid: 20846c75-9758-4cb6-bf97-757c163c315b
// last-edited: 2026-11-28

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// Under core identity a Cloudflare Access assertion is judged twice until
// ACCESS-ONE-VERIFIER lands: by core's Access extension at stage 12 (admission)
// and by books' verify-only gin stage (the verified marker trustedClientIP
// reads). The two hold separate keysets and separate allow-lists, so they can
// disagree. These cells pin that every disagreement lands on the stricter
// side: books' verifier never admits anyone, and a verification only books
// failed never loosens a client key.

const (
	disagreeVisitor = "192.0.2.77"
	disagreePeer    = "127.0.0.1"
	// disagreeCoreOnlyJWT is an assertion core admitted but books' verifier
	// does not accept (a keyset books has not fetched, say).
	disagreeCoreOnlyJWT = "jwt-signed-with-a-key-books-never-fetched"
	disagreeRobinJWT    = "jwt-robin-from-the-belfry"
	disagreeRobinEmail  = "robin@example.test"
)

// disagreeRequest is method path from this host (a cloudflared on the same
// machine), naming disagreeVisitor in CF-Connecting-IP under assertion.
func disagreeRequest(method, path, assertion string) *http.Request {
	var body *bytes.Reader
	if method == http.MethodPost {
		body = bytes.NewReader([]byte(`{"username":"two-face","password":"heads-or-tails"}`))
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = disagreePeer + ":4100"
	req.Header.Set("CF-Connecting-IP", disagreeVisitor)
	if assertion != "" {
		req.Header.Set(cfaccess.CFAccessHeader, assertion)
	}
	return req
}

// (a) Books' verifier accepts the assertion, core admitted no one (no
// principal): 401. The marker is set and changes nothing about admission.
func TestIdentityAccessDisagreement_BooksVerifiesCoreRefusesIsRefused(t *testing.T) {
	fx := newAccessBinderFixture(t)
	rec := identityServe(fx.srv.router, disagreeRequest(http.MethodGet, "/api/v1/auth/me", binderAlfredJWT), nil, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	require.True(t, fx.marked, "books' verifier accepted the assertion and marked the request")
	require.Empty(t, fx.store.seen(), "and resolved no one")
}

// (b) Core admitted the caller (a principal is bound) but books' verifier
// rejects the assertion: the login throttle key is the peer's own address, the
// stricter bucket, never the visitor CF-Connecting-IP names. The control is
// the same request with an assertion books accepts, whose key is the visitor,
// so the cell can tell the two apart.
func TestIdentityAccessDisagreement_CoreAdmitsBooksFailsKeysOnThePeer(t *testing.T) {
	fx := newAccessBinderFixture(t)
	p, c := adoptPrincipal(legacyauth.MethodExtCFAccess, fx.alfred)

	identityServe(fx.srv.router, disagreeRequest(http.MethodPost, "/api/v1/auth/login", disagreeCoreOnlyJWT), p, c)
	require.False(t, fx.marked, "books' verifier rejected the assertion")
	require.Equal(t, disagreePeer, fx.throttleKey,
		"an assertion only core accepted keys the login throttle on the peer, not on the header's visitor")

	identityServe(fx.srv.router, disagreeRequest(http.MethodPost, "/api/v1/auth/login", binderAlfredJWT), p, c)
	require.True(t, fx.marked)
	require.Equal(t, disagreeVisitor, fx.throttleKey, "control: an assertion books verifies keys on the visitor")
	require.Empty(t, fx.store.seen())
}

// An address on books' snapshot allow-list (OAuthAllowedEmails) that core's
// list (modules.books.cfaccess.allowed_emails) does not hold, so core admits
// no one for it, gets exactly what HEAD's exemption gave it, the login-ceiling
// exemption (newAccessExemption reads books' list), and no admission: 401 on
// /api/v1. The store would resolve and admit anyone, so the 401 is the bridge's
// principal check, not a refused write.
func TestIdentityAccessDisagreement_BooksListOnlyGetsTheExemptionNotAdmission(t *testing.T) {
	fx := newAccessBinderFixtureOpts(t, accessBinderOpts{
		admit:   true,
		allowed: disagreeRobinEmail,
		extra: fakeAccessVerifier{
			disagreeRobinJWT: {Provider: legacyoauth.ProviderCFAccess, Subject: "cf|robin", Email: disagreeRobinEmail, EmailVerified: true},
		},
	})
	rec := identityServe(fx.srv.router, disagreeRequest(http.MethodGet, "/api/v1/auth/me", disagreeRobinJWT), nil, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "books' list alone admits no one: %s", rec.Body.String())
	require.Empty(t, fx.store.seen(), "books' store resolved, linked or created no one")

	exempt := func(assertion string) bool {
		require.NotNil(t, fx.srv.loginCeilingExempt, "the exemption is built whenever Access is configured")
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = disagreeRequest(http.MethodPost, "/api/v1/auth/login", assertion)
		return fx.srv.loginCeilingExempt(c)
	}
	require.True(t, exempt(disagreeRobinJWT), "HEAD's login-ceiling exemption for a listed, verified address")
	require.False(t, exempt(binderAlfredJWT), "an address not on books' list gets no exemption")
}
