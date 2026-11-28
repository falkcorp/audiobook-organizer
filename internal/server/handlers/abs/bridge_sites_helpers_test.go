// file: internal/server/handlers/abs/bridge_sites_helpers_test.go
// version: 0.1.0
// guid: 372d4949-1cff-4ae1-b835-899dc1f1928c
// last-edited: 2026-11-21

package abs_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
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

// bridgeScope is the native scope the table maps atom to.
func bridgeScope(atom string) rbac.Permission { return rbac.Permission("test:" + atom) }

// bridgeSignedIn is the Compat of a server whose ABS routes are admitted by
// core (F4-08 B10b1: ABSRequireAuth under an Identity asks core, never books'
// own rows). It answers PrincipalFor with the principal and checker the request
// already carries (the stage 12 stand-in bindCore binds), naming the user the
// surface resolved, and admits every credential CheckCredentialFor.
type bridgeSignedIn struct{ coreauth.Compat }

func (bridgeSignedIn) PrincipalFor(ctx context.Context, u *coreauth.User, _ []rbac.Permission, _ int64, _ coreauth.ClientInfo) (*coreauth.Principal, error) {
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok {
		return nil, coreauth.ErrInvalidCredentials
	}
	out := *p
	out.User = u
	return &out, nil
}

func (bridgeSignedIn) CheckCredentialFor(*coreauth.Principal, rbac.Requirement) error { return nil }

// bridgeIdentityFor is bridgeIdentity for a request that also passes the ABS
// surface's own auth: the user lookup reads h's store and the Compat is
// bridgeSignedIn.
func bridgeIdentityFor(t testing.TB, h *harness, atoms ...string) *legacyauth.Identity {
	t.Helper()
	b, err := legacyauth.New(bridgeTable(atoms), func(_ context.Context, id coreauth.UserID) (*database.User, error) {
		return h.store.GetUserByID(string(id))
	}, nil)
	require.NoError(t, err)
	return &legacyauth.Identity{
		Bridge:  b,
		Compat:  bridgeSignedIn{},
		Client:  func(*http.Request) coreauth.ClientInfo { return coreauth.ClientInfo{} },
		Refresh: func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil },
	}
}

// bindCore gives c a core principal of method m holding scopes, as stage 12
// binds it, and the Identity when id is non-nil. A nil id leaves the request as
// a LegacyAuthGin server serves it: a principal in the context changes nothing.
func bindCore(c *gin.Context, id *legacyauth.Identity, m coreauth.Method, scopes ...rbac.Permission) {
	chk := bridgeChecker(scopes)
	p := &coreauth.Principal{Method: m, Perms: chk}
	ctx := coreauth.WithPrincipal(c.Request.Context(), p)
	ctx = rbac.WithChecker(ctx, chk)
	if id != nil {
		ctx = legacyauth.WithIdentity(ctx, id)
	}
	c.Request = c.Request.WithContext(ctx)
}
