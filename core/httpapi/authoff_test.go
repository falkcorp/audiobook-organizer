// file: core/httpapi/authoff_test.go
// version: 0.1.0
// guid: 0c8a4b71-6d2e-4a93-b5f1-7e3d9a1c2f48
// last-edited: 2026-11-22

package httpapi

import (
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/auth"
)

// sessionEndingCodes is the set of problem codes that make the SPA treat a 401
// as "signed out": core/web/packages/api-client/src/client.ts:26
// SESSION_ENDING_CODES. A copy, so a drift in either place fails a test.
var sessionEndingCodes = map[string]bool{
	"authentication_required": true,
	"expired":                 true,
	"revoked":                 true,
	"inactive_user":           true,
	"invalid_credential":      true,
}

// TestUserRequired_AuthOffIsNeverSessionEnding pins F4-08 ruling G5(a): a route
// that needs a user and finds the auth-disabled principal answers 403
// interactive_required, a code outside the session-ending set, while a request
// with no principal keeps 401 authentication_required. Reverting the auth-off
// branch of UserRequired fails the first two rows.
func TestUserRequired_AuthOffIsNeverSessionEnding(t *testing.T) {
	cat := s13Catalog(t)
	for _, c := range []struct {
		name   string
		p      *auth.Principal
		status int
		code   string
	}{
		{"auth-disabled principal", s13AuthOff(cat), http.StatusForbidden, "interactive_required"},
		{"auth-disabled principal with no checker", &auth.Principal{Method: auth.MethodAuthDisabled}, http.StatusForbidden, "interactive_required"},
		{"no principal", nil, http.StatusUnauthorized, "authentication_required"},
		{"a method that is not auth-disabled, with no user", &auth.Principal{Method: auth.MethodSession}, http.StatusUnauthorized, "authentication_required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := UserRequired(c.p)
			if e == nil {
				t.Fatal("UserRequired returned nil: a nil refusal admits")
			}
			if e.Status != c.status || e.Code != c.code {
				t.Fatalf("got %d %s, want %d %s", e.Status, e.Code, c.status, c.code)
			}
			if c.p != nil && c.p.Method == auth.MethodAuthDisabled && (e.Status == http.StatusUnauthorized || sessionEndingCodes[e.Code]) {
				t.Fatalf("the auth-disabled refusal %d %s ends a no-auth install's session", e.Status, e.Code)
			}
		})
	}
}

// TestAuthOffRefusal_Shape pins the status, the code and that the detail names
// the remedy.
func TestAuthOffRefusal_Shape(t *testing.T) {
	e := AuthOffRefusal()
	if e.Status != http.StatusForbidden || e.Code != "interactive_required" || e.Detail != "turn authentication on to use this route" {
		t.Fatalf("AuthOffRefusal = %d %s %q", e.Status, e.Code, e.Detail)
	}
}
