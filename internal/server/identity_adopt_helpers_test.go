// file: internal/server/identity_adopt_helpers_test.go
// version: 0.1.0
// guid: 0f5c8a21-7d34-4e96-b1a8-3c9e2d6f4b70
// last-edited: 2026-11-21

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// The B7 adoption tests run a real Server twice: built the way books builds it
// today (no identity, LegacyAuthGin) and built under core identity
// (WithIdentity, LegacyAuthCore). The core side has no kernel: a request is
// handed a principal and checker as stage 12 binds them, by identityServe.

// adoptChecker holds exactly its scopes (the same rule as core's compiled
// checker for an AllOf/AnyOf requirement).
type adoptChecker []rbac.Permission

func (s adoptChecker) Has(p rbac.Permission) bool { return slices.Contains(s, p) }
func (s adoptChecker) Satisfies(r rbac.Requirement) bool {
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
func (s adoptChecker) Confined() bool              { return false }
func (s adoptChecker) Concrete() []rbac.Permission { return slices.Clone(s) }

// adoptTable maps each legacy atom the adoption tests use to one native scope
// "test:<atom>", so a principal holding that scope holds the atom.
type adoptTable []string

func (t adoptTable) Requirement(atom string, _ bool) (rbac.Requirement, bool) {
	if !slices.Contains(t, atom) {
		return rbac.Requirement{}, false
	}
	return rbac.Need(rbac.Permission("test:" + atom)), true
}
func (t adoptTable) Atoms() []string { return slices.Clone(t) }

// adoptCompat is a coreauth.Compat that answers as configured and records
// what it was asked.
type adoptCompat struct {
	p       *coreauth.Principal
	err     error
	users   []coreauth.UserID
	epochs  []int64
	clients []coreauth.ClientInfo
}

func (*adoptCompat) VerifyPassword(context.Context, string, string, coreauth.ClientInfo) (*coreauth.User, error) {
	return nil, errors.New("not used")
}
func (*adoptCompat) AuthenticateKey(context.Context, string, coreauth.ClientInfo) (*coreauth.Principal, error) {
	return nil, errors.New("not used")
}
func (a *adoptCompat) PrincipalFor(_ context.Context, u *coreauth.User, _ []rbac.Permission, epoch int64, c coreauth.ClientInfo) (*coreauth.Principal, error) {
	a.users = append(a.users, u.ID)
	a.epochs = append(a.epochs, epoch)
	a.clients = append(a.clients, c)
	return a.p, a.err
}
func (*adoptCompat) CheckCredentialFor(*coreauth.Principal, rbac.Requirement) error { return nil }

// adoptAdmin stands for core's auth.Admin where a test needs the handle present
// and never calls it: every method is the embedded nil interface's.
type adoptAdmin struct{ coreauth.Admin }

// adoptIdentity builds an Identity over a bridge whose user lookup resolves
// users, and compat.
func adoptIdentity(t testing.TB, atoms []string, owner legacyauth.OwnerRequired, compat coreauth.Compat, users ...*database.User) *legacyauth.Identity {
	t.Helper()
	b, err := legacyauth.New(adoptTable(atoms), func(_ context.Context, id coreauth.UserID) (*database.User, error) {
		for _, u := range users {
			if u.ID == string(id) {
				return u, nil
			}
		}
		return nil, errors.New("no such user")
	}, owner)
	require.NoError(t, err)
	if compat == nil {
		compat = &adoptCompat{}
	}
	return &legacyauth.Identity{
		Bridge: b,
		Compat: compat,
		Admin:  adoptAdmin{},
		Client: func(*http.Request) coreauth.ClientInfo { return coreauth.ClientInfo{UserAgent: "batarang"} },
		// Refresh stands for core's Service.Refresh: the principal unchanged.
		Refresh: func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil },
		// Access stands for core's account resolver under the Access policy: no
		// person is admitted (the ABS Access sign-in is not what these tests drive).
		Access: func(context.Context, legacyauth.AccessClaims) (*coreauth.User, error) {
			return nil, errors.New("adoptIdentity: no Access sign-in in this fixture")
		},
	}
}

// adoptPrincipal is a principal of method m for the user, holding scopes.
func adoptPrincipal(m coreauth.Method, u *database.User, scopes ...rbac.Permission) (*coreauth.Principal, rbac.Checker) {
	c := adoptChecker(scopes)
	p := &coreauth.Principal{Method: m, Perms: c}
	if u != nil {
		p.User = &coreauth.User{ID: coreauth.UserID(u.ID), Username: u.Username, Status: coreauth.StatusActive}
	}
	return p, c
}

// identityServe serves req on h with p and c bound as stage 12 binds them (a
// nil p binds neither).
func identityServe(h http.Handler, req *http.Request, p *coreauth.Principal, c rbac.Checker) *httptest.ResponseRecorder {
	if p != nil {
		ctx := coreauth.WithPrincipal(req.Context(), p)
		if c != nil {
			ctx = rbac.WithChecker(ctx, c)
		}
		req = req.WithContext(ctx)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// newTestServerWith is newTestServer with options.
func newTestServerWith(t testing.TB, store database.Store, opts ...ServerOption) *Server {
	t.Helper()
	installServerConfigFacade(t)
	s, err := NewServer(store, opts...)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(s.releaseUnstarted)
	attachTestMetrics(t, s)
	return s
}
