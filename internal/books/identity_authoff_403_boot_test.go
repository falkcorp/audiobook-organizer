// file: internal/books/identity_authoff_403_boot_test.go
// version: 0.1.0
// guid: 7d4e2a95-1b3c-4f68-a0d7-5c9e8b1f3a26
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
)

// F4-08 ruling G5(a) (B10sr): with authentication off the server never answers
// the auth-disabled principal a session-ending 401. The SPA shell treats a 401
// whose problem code is in SESSION_ENDING_CODES as "signed out" and drops a
// no-auth install to a sign-in screen it cannot use, so every refusal of that
// principal is a 403 whose code is outside the set: core's routes answer 403
// interactive_required, books' gin handlers 403 FORBIDDEN. The actions are
// still refused. These cells boot the real composition and ask through the
// root handler, one RemoteAddr per cell (127.0.0.N auth off, 192.0.2.N auth
// on) so the per-address limiter never answers, and fail any 429 outright.

// authOffSessionEnding is a copy of the problem codes whose 401 ends the SPA
// session: core/web/packages/api-client/src/client.ts:26 SESSION_ENDING_CODES.
var authOffSessionEnding = map[string]bool{
	"authentication_required": true,
	"expired":                 true,
	"revoked":                 true,
	"inactive_user":           true,
	"invalid_credential":      true,
}

// authOffCell is one probe: a request and the refusal it must meet with auth
// off.
type authOffCell struct {
	name, method, path, body string
	// code is the problem code of the 403.
	code string
}

const (
	offBatmanUser = "01BATMANNOSUCHUSER000000"
	offBatmanItem = "01BATMANNOSUCHITEM000000"
)

// authOffCoreCells are core routes the sweep moved: the four the SPA's Users
// page calls first, then every other needUser, audit and AuthRequired-scope
// route reachable without a guard of its own.
var authOffCoreCells = []authOffCell{
	{"users page: disable", http.MethodPost, "/api/core/v1/users/" + offBatmanUser + "/disable", "{}", "interactive_required"},
	{"users page: enable", http.MethodPost, "/api/core/v1/users/" + offBatmanUser + "/enable", "{}", "interactive_required"},
	{"users page: reset password", http.MethodPost, "/api/core/v1/users/" + offBatmanUser + "/reset-password", "{}", "interactive_required"},
	{"users page: temp login", http.MethodPost, "/api/core/v1/users/" + offBatmanUser + "/temp-login", "{}", "interactive_required"},
	{"get me", http.MethodGet, "/api/core/v1/me", "", "interactive_required"},
	{"my sessions", http.MethodGet, "/api/core/v1/me/sessions", "", "interactive_required"},
	{"my keys", http.MethodGet, "/api/core/v1/me/api-keys", "", "interactive_required"},
	{"mint my key", http.MethodPost, "/api/core/v1/me/api-keys", `{"name":"batarang","scopes":["books:view"]}`, "interactive_required"},
	{"every key", http.MethodGet, "/api/core/v1/api-keys", "", "interactive_required"},
	{"one key", http.MethodGet, "/api/core/v1/api-keys/" + offBatmanItem, "", "interactive_required"},
	{"a user's keys", http.MethodGet, "/api/core/v1/users/" + offBatmanUser + "/api-keys", "", "interactive_required"},
	{"a user's sessions", http.MethodGet, "/api/core/v1/users/" + offBatmanUser + "/sessions", "", "interactive_required"},
	{"core invite list", http.MethodGet, "/api/core/v1/users/invites", "", "interactive_required"},
	{"audit query", http.MethodGet, "/api/core/v1/audit", "", "interactive_required"},
	{"audit export", http.MethodGet, "/api/core/v1/audit/export", "", "interactive_required"},
}

// authOffBooksCells are books' own gin routes whose handler reads a user the
// auth-disabled principal does not have: HEAD's 401 UNAUTHORIZED is now 403
// FORBIDDEN.
var authOffBooksCells = []authOffCell{
	{"books me", http.MethodGet, "/api/v1/auth/me", "", "FORBIDDEN"},
	{"books sessions", http.MethodGet, "/api/v1/auth/sessions", "", "FORBIDDEN"},
	{"books keys", http.MethodGet, "/api/v1/auth/api-keys", "", "FORBIDDEN"},
}

// authOffProbe sends one cell from its own address and returns the status and
// problem code of the answer. A 429 fails outright.
func authOffProbe(t *testing.T, r *runningApp, c authOffCell, addr string, headers map[string]string, ck *http.Cookie) (int, string, *httptest.ResponseRecorder) {
	t.Helper()
	rec := authOffSend(r, c.method, c.path, c.body, addr, headers, ck)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatalf("%s %s from %s answered 429: the cell proves nothing", c.method, c.path, addr)
	}
	var pb struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pb)
	return rec.Code, pb.Code, rec
}

func authOffSend(r *runningApp, method, path, body, addr string, headers map[string]string, ck *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = addr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if ck != nil {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

func bootAuthOff(t *testing.T) *runningApp {
	t.Helper()
	return runComposedWith(t, nil, nil, func(o *app.Options) { o.ConfigFlags["core.auth.mode"] = "disabled" }, nil)
}

// TestAuthOffBoot_RefusalsAreNeverSessionEnding: every cell is refused 403
// with its code, never a 401 and never a code of the session-ending set,
// with no credential, and again with a stale session cookie and a bogus
// Bearer (the install that turned auth off while a browser still held a
// session: stage 12 does not look at presented credentials with auth off).
func TestAuthOffBoot_RefusalsAreNeverSessionEnding(t *testing.T) {
	r := bootAuthOff(t)
	cells := append(append([]authOffCell{}, authOffCoreCells...), authOffBooksCells...)
	for v, variant := range []struct {
		name    string
		headers map[string]string
		cookie  *http.Cookie
	}{
		{"no credential", nil, nil},
		{"stale session and bogus bearer", map[string]string{"Authorization": "Bearer batman-expired-token"}, &http.Cookie{Name: sessionCookieName, Value: "gotham-expired-session"}},
	} {
		for i, c := range cells {
			// One address per cell and variant: 127.0.<variant>.<cell>.
			addr := "127.0." + strconv.Itoa(v+1) + "." + strconv.Itoa(i+1) + ":41000"
			status, code, rec := authOffProbe(t, r, c, addr, variant.headers, variant.cookie)
			t.Logf("cell %-28s %-6s %s (%s): %d %s", c.name, c.method, c.path, variant.name, status, code)
			if status != http.StatusForbidden || code != c.code {
				t.Errorf("auth off, %s, %s: %d %s (%s); want 403 %s", c.name, variant.name, status, code, credExcerpt(rec), c.code)
			}
			if status == http.StatusUnauthorized || authOffSessionEnding[code] {
				t.Errorf("auth off, %s, %s: %d %s ends a no-auth install's session", c.name, variant.name, status, code)
			}
		}
	}
}

// TestAuthOffBoot_ControlAuthOnNoCredentialStill401: the same core routes with
// authentication on and no credential keep 401 authentication_required, the
// control that proves the 403 above is the auth-disabled rule and not a route
// that always refuses.
func TestAuthOffBoot_ControlAuthOnNoCredentialStill401(t *testing.T) {
	r := bootAccess(t, nil, nil)
	for i, c := range authOffCoreCells {
		addr := "192.0.2." + strconv.Itoa(60+i) + ":41000"
		status, code, rec := authOffProbe(t, r, c, addr, nil, nil)
		t.Logf("cell %-28s %-6s %s (auth on, no credential): %d %s", c.name, c.method, c.path, status, code)
		if status != http.StatusUnauthorized || code != "authentication_required" {
			t.Errorf("auth on, %s: %d %s (%s); want 401 authentication_required", c.name, status, code, credExcerpt(rec))
		}
	}
	for i, c := range authOffBooksCells {
		addr := "192.0.2." + strconv.Itoa(100+i) + ":41000"
		status, code, rec := authOffProbe(t, r, c, addr, nil, nil)
		t.Logf("cell %-28s %-6s %s (auth on, no credential): %d %s", c.name, c.method, c.path, status, code)
		if status != http.StatusUnauthorized {
			t.Errorf("auth on, %s: %d %s (%s); want 401", c.name, status, code, credExcerpt(rec))
		}
	}
}
