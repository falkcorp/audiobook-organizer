// file: internal/server/handlers/repairs/bridge_sites_helpers_test.go
// version: 0.1.0
// guid: fdec1ce5-2fed-4b1d-9fb6-f25023c09c71
// last-edited: 2026-11-21

package repairs

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// The F4-08 B7 adopted sites ask legacyauth.Held (and its siblings) of the
// request context. These helpers build the two worlds those sites run in: a
// context with no Identity (a LegacyAuthGin server, where HEAD's own code
// decides) and one that carries an Identity and the principal and checker stage
// 12 binds (LegacyAuthCore). The adopted sites sit in five handler packages and
// a helper compiled into production code would be dead code, so each package's
// tests carry their own copy of this file.

// bridgeChecker holds exactly its scopes, the rule of core's compiled checker
// for an AllOf/AnyOf requirement.
type bridgeChecker []rbac.Permission

func (s bridgeChecker) Has(p rbac.Permission) bool { return slices.Contains(s, p) }
func (s bridgeChecker) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindAllOf:
		for _, p := range ps {
			if !s.Has(p) {
				return false
			}
		}
		return len(ps) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, s.Has)
	}
	return false
}
func (s bridgeChecker) Confined() bool              { return false }
func (s bridgeChecker) Concrete() []rbac.Permission { return slices.Clone(s) }

// bridgeTable maps each atom it lists to one native scope "test:<atom>".
type bridgeTable []string

func (t bridgeTable) Requirement(atom string, _ bool) (rbac.Requirement, bool) {
	if !slices.Contains(t, atom) {
		return rbac.Requirement{}, false
	}
	return rbac.Need(rbac.Permission("test:" + atom)), true
}
func (t bridgeTable) Atoms() []string { return slices.Clone(t) }

// bridgeCompat is the Compat an Identity needs and these sites never call.
type bridgeCompat struct{ coreauth.Compat }

// bridgeIdentity is an Identity over a table that knows atoms.
func bridgeIdentity(t testing.TB, atoms ...string) *legacyauth.Identity {
	t.Helper()
	b, err := legacyauth.New(bridgeTable(atoms), func(context.Context, coreauth.UserID) (*database.User, error) {
		return nil, errors.New("no user lookup in this test")
	}, nil)
	require.NoError(t, err)
	return &legacyauth.Identity{
		Bridge:  b,
		Compat:  bridgeCompat{},
		Client:  func(*http.Request) coreauth.ClientInfo { return coreauth.ClientInfo{} },
		Refresh: func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil },
	}
}
