// file: core/httpapi/legacy_signedin_test.go
// version: 1.0.0
// guid: 3c8e1f52-9b6d-4a07-8d2e-5f41a7c9b0e3
// last-edited: 2026-11-27

package httpapi

import (
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// A LegacyAuthCore mount with a non-Public route is refused until SetAuth wires
// stages 12 and 13 (checkMount, ErrNoAuthService), and the refusal stays. The
// fixtures in this package that mount a legacy manifest to test its dispatch,
// the Basic gate in front of it or its own-auth admission are not about who
// may call the routes, so they wire stages 12 and 13 the way production does
// and sign every request in as a core:admin session. The routes keep the
// Perm they declare; the authorization cells are TestRouterAuth_Legacy and
// the stage-13 tests, which name their principals.

// signedInAtoms resolves every legacy atom the dispatch fixtures declare
// ("cases.view", "cases.manage") to one scope the signed-in session holds
// (core:admin implies every scope in the catalog). It is the fixtures' own
// table: s13Atoms is the production table's mirror that a guard reads, so it
// takes no fixture atoms.
func signedInAtoms(atom string, readSide bool) (rbac.Requirement, bool) {
	if readSide || atom == "" {
		return rbac.Requirement{}, false
	}
	return rbac.AllOf("books:view"), true
}

// wireSignedIn calls SetAuth on r with a stage 12 that binds a core:admin
// session to every request and the real stage 13 behind it. It must be called
// before Build, once, like SetAuth.
func wireSignedIn(t testing.TB, r *Router) {
	t.Helper()
	cat := s13Catalog(t)
	err := r.SetAuth(AuthWiring{
		Service:       &raFakeAuthn{cat: cat, defaultAs: "admin"},
		OwnerRequired: func() bool { return true },
		ResolveAtom:   signedInAtoms,
		Catalog:       cat,
	})
	if err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
}
