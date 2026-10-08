// file: internal/server/owner_routes_test.go
// version: 1.4.0
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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

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
//
// It mutates process-wide state (config.AppConfig and
// cfAccessVerifierOverride) and restores both in t.Cleanup. That is safe
// only because no test in package server runs in parallel (see the
// prohibition in server_more_test.go); setupOwnerRouteServerAuth relies on
// the same rule.
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
	return setupOwnerRouteServerAuth(t, true)
}

// setupOwnerRouteServerAuth is setupOwnerRouteServer with local auth on or
// off. The Access middleware runs either way.
func setupOwnerRouteServerAuth(t *testing.T, authOn bool) *ownerRouteFixture {
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
		c.EnableAuth = authOn
	})
	// The owner's local account: an admin, so only the owner proof differs
	// between it and the password-session admin.
	_, err := f.store.CreateUser("synthetic-owner", ownerRouteEmail, "bcrypt", "x", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	return &ownerRouteFixture{f}
}

// request sends method path with the given headers and no body.
func (f *ownerRouteFixture) request(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	return f.requestBody(method, path, "{}", hdr)
}

// requestBody is request with a JSON body.
func (f *ownerRouteFixture) requestBody(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
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

// putOwnerEmail sends PUT /api/v1/config {"owner_email": email}.
func (f *ownerRouteFixture) putOwnerEmail(email string, hdr map[string]string) *httptest.ResponseRecorder {
	return f.requestBody(http.MethodPut, "/api/v1/config", `{"owner_email":"`+email+`"}`, hdr)
}

// TestOwnerTrustRoot_ConfigThroughTheRouter is the second review's BLOCKER
// end to end: once an owner is set, only the owner may change owner_email (or
// cf_access_*, enable_auth, oauth_allowed_emails); while none is set nobody
// may through the API, the would-be owner included (set OWNER_EMAIL on the
// host). Auth on and off.
func TestOwnerTrustRoot_ConfigThroughTheRouter(t *testing.T) {
	for _, authOn := range []bool{true, false} {
		name := "auth on"
		if !authOn {
			name = "auth off"
		}
		t.Run(name, func(t *testing.T) {
			f := setupOwnerRouteServerAuth(t, authOn)
			refused := map[string]map[string]string{
				"another admin's Access sign-in": {oauth.CFAccessHeader: "jwt-other"},
				"a look-alike Access sign-in":    {oauth.CFAccessHeader: "jwt-lookalike"},
				"password session":               {"Authorization": "Bearer " + f.sessionToken},
				"API key":                        {"Authorization": "Bearer " + f.apiKey},
			}
			if !authOn {
				refused["no sign-in"] = nil
			}
			for caller, hdr := range refused {
				w := f.putOwnerEmail(otherAdminEmail, hdr)
				assert.Equal(t, http.StatusForbidden, w.Code, "%s: %s", caller, w.Body.String())
				assert.Contains(t, w.Body.String(), "Only the owner may change", caller)
				assert.Equal(t, ownerRouteEmail, config.Snapshot().OwnerEmail, "%s changed owner_email", caller)
				w = f.requestBody(http.MethodPut, "/api/v1/config", `{"cf_access_team_domain":"attacker.example.test"}`, hdr)
				assert.Equal(t, http.StatusForbidden, w.Code, "%s cf_access_team_domain: %s", caller, w.Body.String())
				assert.Contains(t, w.Body.String(), "Only the owner may change", caller)
			}
			// The owner may hand ownership on.
			w := f.putOwnerEmail(otherAdminEmail, map[string]string{oauth.CFAccessHeader: "jwt-owner"})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, otherAdminEmail, config.Snapshot().OwnerEmail)

			// No owner: nobody sets the trust root through the API, the
			// would-be owner's own Access sign-in included (host only).
			config.AppConfig.OwnerEmail = ""
			noOwner := map[string]map[string]string{
				"that person's Access sign-in":    {oauth.CFAccessHeader: "jwt-owner"},
				"another person's Access sign-in": {oauth.CFAccessHeader: "jwt-other"},
				"password session":                {"Authorization": "Bearer " + f.sessionToken},
				"API key":                         {"Authorization": "Bearer " + f.apiKey},
			}
			if !authOn {
				noOwner["no sign-in"] = nil
			}
			for caller, hdr := range noOwner {
				w := f.putOwnerEmail(ownerRouteEmail, hdr)
				assert.Equal(t, http.StatusForbidden, w.Code, "set owner_email by %s: %s", caller, w.Body.String())
				assert.Contains(t, w.Body.String(), "OWNER_EMAIL", caller)
				assert.Empty(t, config.Snapshot().OwnerEmail)
				w = f.requestBody(http.MethodPut, "/api/v1/config", `{"cf_access_team_domain":"attacker.example.test"}`, hdr)
				assert.Equal(t, http.StatusForbidden, w.Code, "repoint cf_access by %s: %s", caller, w.Body.String())
				assert.Contains(t, w.Body.String(), "OWNER_EMAIL", caller)
			}
		})
	}
}

// Backup restore (and system/factory reset, behind the same gate) can bring
// owner_email back to unset, after which the first Access user could claim
// ownership; while an owner is set only the owner reaches them.
func TestOwnerTrustRoot_RestoreNeedsTheOwnerWhileOneIsSet(t *testing.T) {
	f := setupOwnerRouteServer(t)
	body := `{"backup_filename":"no-such-backup.tar.gz"}`
	for caller, hdr := range map[string]map[string]string{
		"password session":               {"Authorization": "Bearer " + f.sessionToken},
		"another admin's Access sign-in": {oauth.CFAccessHeader: "jwt-other"},
	} {
		w := f.requestBody(http.MethodPost, "/api/v1/backup/restore", body, hdr)
		assert.True(t, ownerRefused(w), "%s got %d: %s", caller, w.Code, w.Body.String())
		w = f.requestBody(http.MethodPost, "/api/v1/system/reset", `{"confirm":"RESET"}`, hdr)
		assert.True(t, ownerRefused(w), "%s reset got %d: %s", caller, w.Code, w.Body.String())
		w = f.requestBody(http.MethodPost, "/api/v1/system/factory-reset", `{"confirm":"RESET"}`, hdr)
		assert.True(t, ownerRefused(w), "%s factory reset got %d: %s", caller, w.Code, w.Body.String())
	}
	w := f.requestBody(http.MethodPost, "/api/v1/backup/restore", body, map[string]string{oauth.CFAccessHeader: "jwt-owner"})
	assert.False(t, ownerRefused(w), "the owner was refused: %s", w.Body.String())

	// No owner yet: the ordinary guards only (a signed-in session reaches
	// the handler, which then fails on the missing backup).
	config.AppConfig.OwnerEmail = ""
	w = f.requestBody(http.MethodPost, "/api/v1/backup/restore", body, map[string]string{"Authorization": "Bearer " + f.sessionToken})
	assert.False(t, ownerRefused(w), "refused with no owner set: %s", w.Body.String())
}
