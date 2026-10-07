// file: internal/server/owner_routes_test.go
// version: 1.1.0
// guid: 7b2d9e41-6c85-4f30-a1e7-4c9f2b8d6a15
// last-edited: 2026-10-07

// Router-level coverage for the owner gate on the iTunes library (2026-10-07):
// every route that removes, overwrites or repoints iTunes tracks, or turns off
// the library's safety checks, answers only a verified Cloudflare Access
// sign-in as owner_email. The real router is driven with a fake Access
// verifier (cfAccessVerifierOverride). Synthetic users and emails only.

package server

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// wantOwnerRoutes is the reviewed list (plan D14). A route dropped from
// ownerRoute, or a new one added without review, fails here.
var wantOwnerRoutes = map[string]ownerRouteKind{
	"POST /api/v1/itunes/rebuild":                ownerRouteApply,
	"POST /api/v1/itunes/rebuild-full":           ownerRouteApply,
	"POST /api/v1/itunes/relocate":               ownerRouteApply,
	"POST /api/v1/itunes/cleanup-merged":         ownerRouteApply,
	"POST /api/v1/itunes/adopt-base":             ownerRouteAlways,
	"POST /api/v1/itunes/write-back":             ownerRouteAlways,
	"POST /api/v1/itunes/write-back-all":         ownerRouteAlways,
	"POST /api/v1/itunes/writeback/held/release": ownerRouteAlways,
	"POST /api/v1/itunes/library/upload":         ownerRouteAlways,
	"POST /api/v1/itunes/library/restore":        ownerRouteAlways,
}

const (
	ownerRouteEmail = "kowner@example.test"
	// The owner's address with U+212A KELVIN SIGN: a different IdP account
	// that strings.EqualFold (and the sign-in allowlist's ToLower) folds
	// onto the owner.
	ownerLookalikeEmail = "Kowner@example.test"
	otherAdminEmail     = "target@example.test"
)

type fakeAccessVerifier map[string]*oauth.IdentityClaims

func (f fakeAccessVerifier) Verify(_ context.Context, raw string) (*oauth.IdentityClaims, error) {
	if c, ok := f[raw]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, errors.New("fake access verifier: unknown token")
}

// asTestOwner makes the next server built in this test (newTestServer /
// NewServer read it in setupRoutes) accept the returned header as the owner's
// verified Access sign-in: owner_email is set, the email is allowlisted, and
// an Access identity resolves to an admin. Call it BEFORE building the server.
// For tests of an owner route's handler behaviour, which must reach the
// handler through the owner gate rather than around it.
func asTestOwner(t *testing.T) map[string]string {
	t.Helper()
	const email = "test-owner@example.test"
	prevVerifier := cfAccessVerifierOverride
	cfAccessVerifierOverride = fakeAccessVerifier{
		"jwt-test-owner": {Provider: oauth.ProviderCFAccess, Subject: "sub-test-owner", Email: email, EmailVerified: true},
	}
	prev := config.AppConfig
	config.AppConfig.OwnerEmail = email
	config.AppConfig.OAuthAllowedEmails = email
	config.AppConfig.OAuthDefaultRole = auth.SeedRoleAdmin
	t.Cleanup(func() {
		cfAccessVerifierOverride = prevVerifier
		config.AppConfig.OwnerEmail = prev.OwnerEmail
		config.AppConfig.OAuthAllowedEmails = prev.OAuthAllowedEmails
		config.AppConfig.OAuthDefaultRole = prev.OAuthDefaultRole
	})
	return map[string]string{oauth.CFAccessHeader: "jwt-test-owner"}
}

type ownerRouteFixture struct {
	*credGuardFixture
}

func setupOwnerRouteServer(t *testing.T) *ownerRouteFixture {
	t.Helper()
	cfAccessVerifierOverride = fakeAccessVerifier{
		"jwt-owner":     {Provider: oauth.ProviderCFAccess, Subject: "sub-owner", Email: ownerRouteEmail, EmailVerified: true},
		"jwt-other":     {Provider: oauth.ProviderCFAccess, Subject: "sub-other", Email: otherAdminEmail, EmailVerified: true},
		"jwt-lookalike": {Provider: oauth.ProviderCFAccess, Subject: "sub-lookalike", Email: ownerLookalikeEmail, EmailVerified: true},
	}
	t.Cleanup(func() { cfAccessVerifierOverride = nil })
	f := setupCredGuardServerWith(t, func(c *config.Config) {
		c.OwnerEmail = ownerRouteEmail
		c.OAuthAllowedEmails = ownerRouteEmail + "," + otherAdminEmail
		c.OAuthDefaultRole = auth.SeedRoleAdmin
	})
	// The owner's local account: an admin, so only the owner proof differs
	// between it and the password-session admin.
	_, err := f.store.CreateUser("synthetic-owner", ownerRouteEmail, "bcrypt", "x", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	return &ownerRouteFixture{f}
}

// request sends method path with the given headers and no body.
func (f *ownerRouteFixture) request(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	return w
}

// ownerRefused reports whether w is the owner gate's refusal (every
// auth.OwnerProofWhyNot reason starts "Owner actions").
func ownerRefused(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "Owner actions")
}

func TestOwnerRoutes_ReviewedList(t *testing.T) {
	f := setupOwnerRouteServer(t)
	got := maps.Clone(f.srv.ownerRoutes)
	assert.Equal(t, wantOwnerRoutes, got)
}

// TestOwnerRoutes_OnlyTheAccessOwner is the table: every owner route refuses
// every caller but the owner's verified Access sign-in.
func TestOwnerRoutes_OnlyTheAccessOwner(t *testing.T) {
	f := setupOwnerRouteServer(t)
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	refused := []struct {
		name string
		hdr  map[string]string
	}{
		{"password session (admin)", bearer(f.sessionToken)},
		{"API key (admin, every scope)", bearer(f.apiKey)},
		{"API key with the owner's Access JWT riding along", map[string]string{
			"Authorization": "Bearer " + f.apiKey, oauth.CFAccessHeader: "jwt-owner"}},
		{"Access sign-in as another admin", map[string]string{oauth.CFAccessHeader: "jwt-other"}},
		{"Access sign-in as a Kelvin-sign lookalike of the owner", map[string]string{oauth.CFAccessHeader: "jwt-lookalike"}},
		{"password session plus the unsigned Access email header", map[string]string{
			"Authorization": "Bearer " + f.sessionToken, "Cf-Access-Authenticated-User-Email": ownerRouteEmail}},
	}
	// The reviewed list, not the registry: the table must fail on a route
	// that lost its gate, not shrink with it.
	routes := slices.Sorted(maps.Keys(wantOwnerRoutes))
	require.NotEmpty(t, routes)
	for _, key := range routes {
		method, path, _ := strings.Cut(key, " ")
		for _, c := range refused {
			t.Run(key+"/"+c.name, func(t *testing.T) {
				w := f.request(method, path, c.hdr)
				// The API-key and lookalike callers may stop earlier (a
				// 401 when the sign-in is not admitted at all); either way
				// the handler is never reached.
				if w.Code == http.StatusUnauthorized {
					return
				}
				assert.True(t, ownerRefused(w), "got %d: %s", w.Code, w.Body.String())
			})
		}
		t.Run(key+"/the owner's Access sign-in", func(t *testing.T) {
			w := f.request(method, path, map[string]string{oauth.CFAccessHeader: "jwt-owner"})
			assert.False(t, ownerRefused(w), "the owner was refused: %s", w.Body.String())
			assert.NotContains(t, w.Body.String(), "permission denied")
			assert.NotEqual(t, http.StatusUnauthorized, w.Code)
		})
	}

	// owner_email unset: the owner's own Access sign-in is refused too.
	config.AppConfig.OwnerEmail = ""
	for _, key := range routes {
		method, path, _ := strings.Cut(key, " ")
		t.Run(key+"/owner_email unset", func(t *testing.T) {
			w := f.request(method, path, map[string]string{oauth.CFAccessHeader: "jwt-owner"})
			assert.True(t, ownerRefused(w), "got %d: %s", w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "no owner email is configured")
		})
	}
}

// The preview half of an apply route stays open to a password session with
// the route's base permission; the gate and the handler read dry_run through
// the same helper, so "dry_run=TRUE" or a repeated parameter is an apply for
// both.
func TestOwnerRoutes_PreviewStaysOpen(t *testing.T) {
	f := setupOwnerRouteServer(t)
	sess := map[string]string{"Authorization": "Bearer " + f.sessionToken}
	for key, kind := range wantOwnerRoutes {
		method, path, _ := strings.Cut(key, " ")
		if kind != ownerRouteApply {
			continue
		}
		t.Run(key, func(t *testing.T) {
			w := f.request(method, path+"?dry_run=true", sess)
			assert.False(t, ownerRefused(w), "the preview was refused: %s", w.Body.String())
			for _, q := range []string{"?dry_run=TRUE", "?dry_run=1", "?dry_run=false&dry_run=true", "?dry_run=", ""} {
				w = f.request(method, path+q, sess)
				assert.True(t, ownerRefused(w), "%s%s got %d: %s", path, q, w.Code, w.Body.String())
			}
		})
	}
}

// The owner proven by Access still needs integrations.manage: the owner's
// account demoted to editor (library.edit_metadata only) is refused.
func TestOwnerRoutes_OwnerNeedsIntegrationsManage(t *testing.T) {
	f := setupOwnerRouteServer(t)
	u, err := f.store.GetUserByEmail(ownerRouteEmail)
	require.NoError(t, err)
	require.NotNil(t, u)
	u.Roles = []string{auth.SeedRoleEditor}
	require.NoError(t, f.store.UpdateUser(u))
	w := f.request(http.MethodPost, "/api/v1/itunes/writeback/held/release", map[string]string{oauth.CFAccessHeader: "jwt-owner"})
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "permission denied: "+string(auth.PermIntegrationsManage))
}

// cleanup-merged's apply is retired in the handler: even the owner gets the
// retirement refusal, never a write.
func TestOwnerRoutes_CleanupMergedApplyStaysRetired(t *testing.T) {
	f := setupOwnerRouteServer(t)
	w := f.request(http.MethodPost, "/api/v1/itunes/cleanup-merged", map[string]string{oauth.CFAccessHeader: "jwt-owner"})
	assert.Contains(t, w.Body.String(), cleanupMergedApplyRetiredMessage)
}
