// file: internal/books/identity_access_credroutes_boot_test.go
// version: 0.1.0
// guid: 7c41e0a9-5b2d-4f83-96e7-d2a8b3c50f14
// last-edited: 2026-11-22

package books

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
)

// An Access sign-in on the credential routes under core identity (F4-08 B10y).
//
// HEAD's verdict, from origin/main: every credential route runs credGuard
// (internal/server/credential_routes.go credRoute/credRouteWhen), which asks
// internal/auth Method.MayChangeCredentials, and that allowlist names
// cf_access (internal/auth/context.go). The owner gate is skipped while
// owner_email is unset (owner_routes.go ownerGateWhenOwnerSet), as it is in
// this boot. So an Access-verified person holding the admin role clears every
// authorization step on every credential route: HEAD's verdict is admit on
// each. Under core identity the routes carry Guards.Interactive (and gin keeps
// the credential guard behind it), both answered by the principal's registered
// classification: the Access extension must declare MayChangeCredentials or
// every one of these cells is 403 interactive_required.

// credRouteDummy fills a route pattern's parameters.
var credRouteDummy = regexp.MustCompile(`\{[^}]+\}|[:*][A-Za-z_]+`)

// TestAccessBoot_CredentialRoutesAdmitTheAccessAdminAsHEADDid: one cell per
// route the boot registers with Guards.Interactive (books' credential routes
// and core's own), each answering the Access-verified admin past every
// authorization step. A request's handler may still refuse its empty body or
// dummy ID (400, 404, 409, 422); only an authorization refusal fails a cell.
func TestAccessBoot_CredentialRoutesAdmitTheAccessAdminAsHEADDid(t *testing.T) {
	fake := newFakeAccessVerifier()
	r := bootAccess(t, fake, nil)
	admin := newCoreAdmin(t, r)

	code, id, body := absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusOK, code, body)
	rec := credJSON(t, r, http.MethodPut, "/api/core/v1/users/"+id+"/roles", map[string]any{"roles": []string{"admin"}}, func(req *http.Request) { req.AddCookie(admin.cookie) })
	require.Equal(t, http.StatusOK, rec.Code, "granting the Access person the admin role: %s", rec.Body.String())

	type route struct{ method, pattern string }
	var routes []route
	for ri := range r.app.Routes() {
		if ri.Guards.Interactive {
			routes = append(routes, route{ri.Method, ri.Pattern})
		}
	}
	// The one route whose handler would act on an empty body goes last.
	slices.SortStableFunc(routes, func(a, b route) int {
		return credLast(a.pattern) - credLast(b.pattern)
	})
	// Books' credential routes, from legacy_manifest.go's fInteractive rows
	// (as the router reports them, "{param}"):
	// the cell set must cover them, or a route lost its guard flag and this
	// test would pass by not asking. HEAD's POST /api/v1/auth/temp-tokens is
	// deleted at the flip (spec step 3); core's temp-login mint replaces it
	// and is held here in its place.
	for _, want := range []string{
		"PUT /api/v1/auth/me/password", "PATCH /api/v1/auth/me", "PATCH /api/v1/auth/api-keys/{id}",
		"POST /api/v1/auth/phone-key/claim", "POST /api/core/v1/users/{id}/temp-login", "POST /api/v1/users/invite",
		"POST /api/v1/users/{id}/reset-password", "PUT /api/v1/users/{id}/email", "POST /api/v1/users/{id}/deactivate",
		"POST /api/v1/users/{id}/reactivate", "POST /api/v1/users/invites/{id}/resend", "POST /api/v1/import-paths",
		"PUT /api/v1/plugins/{id}/settings", "POST /api/v1/tools/{name}/install", "POST /api/v1/update/apply",
		"GET /api/v1/admin/guest-service-tokens", "POST /api/v1/backup/restore",
	} {
		method, pattern, _ := strings.Cut(want, " ")
		if !slices.Contains(routes, route{method, pattern}) {
			t.Errorf("%s is not registered with Guards.Interactive; registered: %v", want, routes)
		}
	}

	cells := 0
	for i, rt := range routes {
		path := credRouteDummy.ReplaceAllString(rt.pattern, "batman-dummy")
		// Each cell from an address of its own: core's per-source rate limit
		// would otherwise answer the later cells 429 before stage 13 runs.
		addr := fmt.Sprintf("192.0.2.%d:41000", 20+i)
		rec := credJSON(t, r, rt.method, path, nil, func(req *http.Request) {
			req.RemoteAddr = addr
			req.Header.Set(cfaccess.CFAccessHeader, accessAlfred)
		})
		cells++
		if rec.Code == http.StatusTooManyRequests {
			t.Errorf("%s %s: 429 before authorization; the cell asked nothing", rt.method, rt.pattern)
			continue
		}
		t.Logf("cell %s %s: %d %s", rt.method, rt.pattern, rec.Code, credExcerpt(rec))
		if why := credAuthzRefusal(rec); why != "" {
			t.Errorf("%s %s as the Access-verified admin: %d %s (%s); HEAD's credGuard admitted cf_access", rt.method, rt.pattern, rec.Code, why, credExcerpt(rec))
		}
	}
	require.GreaterOrEqual(t, cells, 17, "too few credential routes were asked")
	t.Logf("%d credential-route cells", cells)
}

func credLast(pattern string) int {
	if strings.HasSuffix(pattern, "/factory-reset") || strings.HasSuffix(pattern, "/backup/restore") {
		return 1
	}
	return 0
}

// credJSON sends one request through the root handler.
func credJSON(t *testing.T, r *runningApp, method, path string, body any, decorate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	if decorate != nil {
		decorate(req)
	}
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// credAuthzRefusal names an authorization refusal: any 401, a 403 carrying
// one of stage 13's codes, or a 403 with one of the gin adapters' refusal
// messages. "" is anything else (admitted, or refused by the handler for
// its input).
func credAuthzRefusal(rec *httptest.ResponseRecorder) string {
	if rec.Code == http.StatusUnauthorized {
		return "unauthenticated"
	}
	if rec.Code != http.StatusForbidden {
		return ""
	}
	b := rec.Body.String()
	for _, needle := range []string{"interactive_required", "owner_required", "permission_denied", auth.CredentialChangeRefusedMessage, "admin role required", "permission denied"} {
		if strings.Contains(b, needle) {
			return needle
		}
	}
	return ""
}

func credExcerpt(rec *httptest.ResponseRecorder) string {
	b := rec.Body.String()
	if len(b) > 240 {
		b = b[:240]
	}
	return b
}
