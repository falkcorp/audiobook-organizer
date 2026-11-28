// file: internal/server/legacy_signedin_test.go
// version: 1.0.0
// guid: b7d24e90-5a1c-4f68-93e0-6c2a8f1d7b45
// last-edited: 2026-11-27

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// A LegacyAuthCore mount with a non-Public route is refused until the router
// has stages 12 and 13 (httpapi.ErrNoAuthService), and the refusal stays. The
// fixtures that mount books' real manifest to test the perimeter, the Basic
// gate or a redaction rule in front of it are not about who may call a route,
// so they wire stages 12 and 13 as the kernel does and sign every request in
// as an interactive owner session that holds every scope. The routes keep the
// Perm the manifest declares; what each permission admits is the reach
// oracle's (internal/books/cutover), which names its principals.

// signedInChecker holds every scope: stage 13's Perm step admits whatever a
// route asks for, so a fixture reaches the engine wherever the perimeter
// under test let it through.
type signedInChecker struct{}

func (signedInChecker) Has(rbac.Permission) bool        { return true }
func (signedInChecker) Satisfies(rbac.Requirement) bool { return true }
func (signedInChecker) Confined() bool                  { return false }
func (signedInChecker) Concrete() []rbac.Permission     { return nil }

// signedInAuthn is stage 12 for those fixtures: it binds the owner session to
// every request and records nothing else.
type signedInAuthn struct{}

func (signedInAuthn) Middleware() func(http.Handler) http.Handler {
	p := &coreauth.Principal{
		User:         &coreauth.User{ID: "u-batman", Username: "batman"},
		Method:       coreauth.MethodSession,
		CredentialID: "s-cave",
		Perms:        signedInChecker{},
		Assurance:    coreauth.AssuranceOwner,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(coreauth.WithPrincipal(r.Context(), p)))
		})
	}
}

func (signedInAuthn) SessionCookie(coreauth.ClientInfo, string, time.Time) *http.Cookie {
	return &http.Cookie{Name: "hello_session", Path: "/", MaxAge: -1}
}

// signedInAtoms resolves every legacy atom to core:admin, which the signed-in
// checker holds. The production table is books' (internal/books), which this
// package cannot import.
func signedInAtoms(atom string, readSide bool) (rbac.Requirement, bool) {
	if readSide || atom == "" {
		return rbac.Requirement{}, false
	}
	return rbac.Need(rbac.CoreAdmin), true
}

// wireSignedIn calls SetAuth on r with signedInAuthn and the real stage 13
// behind it. It must be called before Build, once, like SetAuth.
func wireSignedIn(t testing.TB, r *httpapi.Router) {
	t.Helper()
	require.NoError(t, r.SetAuth(httpapi.AuthWiring{
		Service:       signedInAuthn{},
		OwnerRequired: func() bool { return true },
		ResolveAtom:   signedInAtoms,
	}))
}
