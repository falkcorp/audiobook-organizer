// file: internal/server/legacyauth/gin_test.go
// version: 0.1.0
// guid: 5a3f8d27-4e19-4c6b-b2d0-9f7e1a4c8d36
// last-edited: 2026-11-20

package legacyauth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

type ginCase struct {
	ctx    context.Context
	status int
	body   string // a substring the body must hold ("" checks none)
}

func runGin(t *testing.T, mw gin.HandlerFunc, cases map[string]ginCase) {
	t.Helper()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := serve(tc.ctx, mw)
			if rec.Code != tc.status {
				t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), tc.status)
			}
			reached := rec.Body.String() == "reached"
			if reached != (tc.status == http.StatusOK) {
				t.Errorf("handler reached = %v with status %d", reached, rec.Code)
			}
			if tc.body != "" && !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("body %q lacks %q", rec.Body.String(), tc.body)
			}
		})
	}
}

// TestRequirePerm (05-http-api §8.3 s.perm row, 05-rbac §4.11): the atom's
// route requirement through rbac; the auth-disabled principal is judged by
// its checker and answered 401 when refused, as HEAD answered a caller
// without a user.
func TestRequirePerm(t *testing.T) {
	b := newBridge(t, nil)
	runGin(t, b.RequirePerm("library.view"), map[string]ginCase{
		"no principal":                       {bound(nil, laxChecker{}), 401, "authentication required"},
		"no checker":                         {bound(principal(coreauth.MethodSession, laxChecker{}), nil), 403, "permission denied: library.view"},
		"a lacking scope":                    {as(coreauth.MethodSession, setChecker{"books:view"}), 403, "permission denied: library.view"},
		"the full AllOf":                     {as(coreauth.MethodSession, setChecker{"books:view", "core:tasks.view"}), 200, ""},
		"an API key with the full AllOf":     {as(coreauth.MethodAPIKey, setChecker{"books:view", "core:tasks.view"}), 200, ""},
		"auth-disabled, its checker admits":  {as(coreauth.MethodAuthDisabled, laxChecker{}), 200, ""},
		"auth-disabled, its checker refuses": {as(coreauth.MethodAuthDisabled, setChecker{}), 401, "authentication required"},
	})
	for atom, name := range map[string]string{"batcave.flood": "an unknown atom", "oracle.public": "a Public entry", "oracle.legacy": "a LegacyAtom entry"} {
		runGin(t, b.RequirePerm(atom), map[string]ginCase{
			name + " under a checker that satisfies everything": {as(coreauth.MethodSession, laxChecker{}), 403, "permission denied: " + atom},
		})
	}
}

// TestRequireAdmin (05-rbac §6.2): the admin kind's permission through rbac.
func TestRequireAdmin(t *testing.T) {
	b := newBridge(t, nil)
	debug := rbac.Need("books:admin.debug")
	runGin(t, b.RequireAdmin(debug), map[string]ginCase{
		"no principal":                          {bound(nil, laxChecker{}), 401, "authentication required"},
		"a lacking scope":                       {as(coreauth.MethodSession, setChecker{"books:admin.view"}), 403, "admin role required"},
		"no checker":                            {bound(principal(coreauth.MethodSession, laxChecker{}), nil), 403, "admin role required"},
		"the scope":                             {as(coreauth.MethodSession, setChecker{"books:admin.debug"}), 200, ""},
		"auth-disabled, refused (AuthRequired)": {as(coreauth.MethodAuthDisabled, setChecker{}), 401, "authentication required"},
	})
	for name, perm := range map[string]rbac.Requirement{
		"Public": rbac.Public, "AnyAuthenticated": rbac.AnyAuthenticated, "zero": {}, "LegacyAtom": rbac.LegacyAtom("users.manage"),
	} {
		runGin(t, b.RequireAdmin(perm), map[string]ginCase{
			"a " + name + " permission under a checker that satisfies everything": {as(coreauth.MethodSession, laxChecker{}), 403, "admin role required"},
		})
	}
}

// TestRequireCredentialChange (05-authn §4.11, 05-rbac §4.11 (a), SEC5-8):
// only a method that may change credentials passes; an API key and the
// auth-disabled principal are refused with HEAD's message.
func TestRequireCredentialChange(t *testing.T) {
	runGin(t, RequireCredentialChange(), map[string]ginCase{
		"no principal":                {bound(nil, laxChecker{}), 401, "authentication required"},
		"an API key":                  {as(coreauth.MethodAPIKey, laxChecker{}), 403, auth.CredentialChangeRefusedMessage},
		"the auth-disabled principal": {as(coreauth.MethodAuthDisabled, laxChecker{}), 403, auth.CredentialChangeRefusedMessage},
		"an extension method":         {as("ext:cfaccess", laxChecker{}), 403, auth.CredentialChangeRefusedMessage},
		"a session":                   {as(coreauth.MethodSession, laxChecker{}), 200, ""},
		"a delegated session":         {as(coreauth.MethodSessionDelegated, laxChecker{}), 200, ""},
	})
}

// owned is p with owner assurance.
func owned(m coreauth.Method, c rbac.Checker) context.Context {
	p := principal(m, c)
	p.Assurance = coreauth.AssuranceOwner
	return bound(p, c)
}

func constOwner(v bool) OwnerRequired { return func() bool { return v } }

// TestRequireOwner (HEAD's ownerGateWhenOwnerSet): owner assurance passes;
// without it everyone is refused while the owner is required, and everyone
// with a principal passes while it is not (the route's own guards decide). A
// missing OwnerRequired reads as required.
func TestRequireOwner(t *testing.T) {
	admin := setChecker{rbac.CoreAdmin}
	for name, owner := range map[string]OwnerRequired{"not injected": nil, "required": constOwner(true)} {
		runGin(t, newBridge(t, owner).RequireOwner(), map[string]ginCase{
			"no principal (" + name + ")":                           {bound(nil, admin), 401, "authentication required"},
			"owner assurance (" + name + ")":                        {owned(coreauth.MethodSession, setChecker{}), 200, ""},
			"an interactive admin without assurance (" + name + ")": {as(coreauth.MethodSession, admin), 403, msgOwnerRequired},
		})
	}
	// Not required: a pass-through, as HEAD's gate with owner_email unset. The
	// credential guard (RequireCredentialChange) and the route's permission
	// are separate handlers in the chain and refuse the key and the
	// auth-disabled principal there.
	runGin(t, newBridge(t, constOwner(false)).RequireOwner(), map[string]ginCase{
		"no principal":                 {bound(nil, admin), 401, "authentication required"},
		"an interactive admin":         {as(coreauth.MethodSession, admin), 200, ""},
		"a delegated session's admin":  {as(coreauth.MethodSessionDelegated, admin), 200, ""},
		"an admin's API key":           {as(coreauth.MethodAPIKey, admin), 200, ""},
		"a session lacking core:admin": {as(coreauth.MethodSession, setChecker{"books:view"}), 200, ""},
		"a session with no checker":    {bound(principal(coreauth.MethodSession, admin), nil), 200, ""},
		"the auth-disabled principal":  {as(coreauth.MethodAuthDisabled, laxChecker{}), 200, ""},
	})
}
