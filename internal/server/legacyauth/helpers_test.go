// file: internal/server/legacyauth/helpers_test.go
// version: 0.1.0
// guid: 2e7c4a91-6b38-4d05-9f1e-8a3d5c2b7e64
// last-edited: 2026-11-20

package legacyauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func init() { gin.SetMode(gin.TestMode) }

// laxChecker satisfies and holds everything, so in a refusal test only the
// check under test can refuse.
type laxChecker struct{}

func (laxChecker) Has(rbac.Permission) bool        { return true }
func (laxChecker) Satisfies(rbac.Requirement) bool { return true }
func (laxChecker) Confined() bool                  { return false }
func (laxChecker) Concrete() []rbac.Permission     { return nil }

// setChecker holds exactly its scopes: Satisfies an AllOf of held scopes and
// an AnyOf with one held scope, and nothing else.
type setChecker []rbac.Permission

func (s setChecker) Has(p rbac.Permission) bool { return slices.Contains(s, p) }
func (s setChecker) Satisfies(r rbac.Requirement) bool {
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
func (s setChecker) Confined() bool              { return false }
func (s setChecker) Concrete() []rbac.Permission { return slices.Clone(s) }

// table is a stand-in for books' atom table (05-rbac §6.1 rows, plus a few
// rows of kinds no table may hold).
type table map[string][2]rbac.Requirement // atom -> {route, read side}

func (t table) Requirement(atom string, readSide bool) (rbac.Requirement, bool) {
	e, ok := t[atom]
	if !ok {
		return rbac.Requirement{}, false
	}
	if readSide {
		return e[1], true
	}
	return e[0], true
}

func (t table) Atoms() []string {
	out := make([]string, 0, len(t))
	for a := range t {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

func same(r rbac.Requirement) [2]rbac.Requirement { return [2]rbac.Requirement{r, r} }

func atomTable() table {
	return table{
		"library.view":          same(rbac.AllOf("books:view", "core:tasks.view")),
		"library.edit_metadata": same(rbac.Need("books:edit")),
		"users.manage":          {rbac.AllOf("core:users.manage", "core:roles.view"), rbac.Need("core:users.view")},
		"scan.trigger":          same(rbac.AnyOf("books:scan")),
		// Kinds a table entry may not have: each must read as unknown.
		"oracle.public":   same(rbac.Public),
		"oracle.anyone":   same(rbac.AnyAuthenticated),
		"oracle.legacy":   same(rbac.LegacyAtom("library.view")),
		"oracle.check":    same(rbac.LegacyCheck("library.view", false)),
		"oracle.zero":     same(rbac.Requirement{}),
		"oracle.delegate": same(rbac.Delegated("batcomputer")),
	}
}

const bruceID = "u-bruce"

func bruce() *database.User {
	return &database.User{ID: bruceID, Username: "bruce", Status: "active", Roles: []string{"admin"}}
}

// lookup returns the user found by id from users.
func lookup(users ...*database.User) UserLookup {
	return func(_ context.Context, id coreauth.UserID) (*database.User, error) {
		for _, u := range users {
			if u.ID == string(id) {
				return u, nil
			}
		}
		return nil, errors.New("no such user")
	}
}

func newBridge(t *testing.T, owner OwnerRequired) *Bridge {
	t.Helper()
	b, err := New(atomTable(), lookup(bruce()), owner)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// principal is a principal of method m for bruce (no user for the
// auth-disabled method) whose Perms is c.
func principal(m coreauth.Method, c rbac.Checker) *coreauth.Principal {
	p := &coreauth.Principal{Method: m, Perms: c}
	if m != coreauth.MethodAuthDisabled {
		p.User = &coreauth.User{ID: bruceID, Username: "bruce", Status: coreauth.StatusActive}
	}
	return p
}

// bound is ctx with p and checker c bound as stage 12 binds them (a nil p or
// c binds none).
func bound(p *coreauth.Principal, c rbac.Checker) context.Context {
	ctx := context.Background()
	if p != nil {
		ctx = coreauth.WithPrincipal(ctx, p)
	}
	if c != nil {
		ctx = rbac.WithChecker(ctx, c)
	}
	return ctx
}

// as binds p with its own Perms as the checker.
func as(m coreauth.Method, c rbac.Checker) context.Context { return bound(principal(m, c), c) }

// serve runs mw then a handler that answers 200 "reached", on a request
// carrying ctx, and returns the recorder.
func serve(ctx context.Context, mw gin.HandlerFunc) *httptest.ResponseRecorder {
	r := gin.New()
	r.GET("/gotham", mw, func(c *gin.Context) { c.String(http.StatusOK, "reached") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/gotham", nil).WithContext(ctx))
	return rec
}
