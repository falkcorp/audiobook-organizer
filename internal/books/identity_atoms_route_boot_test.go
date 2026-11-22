// file: internal/books/identity_atoms_route_boot_test.go
// version: 0.1.0
// guid: 3e9b6a12-d4c7-4f50-8b21-a6f0c93e7d58
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// The checks HEAD wrote as "holds every atom" under core identity (F4-08 B10y,
// item 4 cause A). The bridge resolves an atom through the atom table's Route;
// playlists.create's was zero (no HEAD route enforces it), so nobody held it,
// and every all-atoms check refused everyone: APIKeyMayActAsAdmin (an admin's
// key on the RequireAdmin routes) and the Repairs unknown-fixer scope (the
// admin session on a run whose fixer this build does not register). Each cell
// below fails when the Route goes back to zero.

// TestAtomBoot_AdminKeyActsAsAdminOnTheRequireAdminRoutes: an API key of the
// admin holding core:admin (what the cutover writes for HEAD's key:admin:every
// and key:admin:empty, m3 keyScopes) is admitted on RequireAdmin routes, as
// HEAD's APIKeyMayActAsAdmin admitted it (origin/main
// middleware/auth.go:537-567).
func TestAtomBoot_AdminKeyActsAsAdminOnTheRequireAdminRoutes(t *testing.T) {
	r := bootAccess(t, nil, nil)
	admin := newCoreAdmin(t, r)
	raw, _ := admin.mintKey("batman-utility-belt", []string{"core:admin"})
	for _, path := range []string{"/api/v1/cache/stats/keys", "/api/v1/admin/debug/edits"} {
		rec := withKey(r, http.MethodGet, path, raw)
		if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Errorf("GET %s with the admin's key: %d %s; HEAD's APIKeyMayActAsAdmin admitted it", path, rec.Code, credExcerpt(rec))
		}
		t.Logf("cell GET %s key:admin(core:admin): %d", path, rec.Code)
	}
}

// TestAtomBoot_AdminSessionMayApproveARunOfAnUnregisteredFixer: a Repairs run
// whose fixer this build does not register needs every permission there is
// (opaccess fixerPermitted, auth.All()); the admin holds them all, so the
// approve passes the run's access check, as at HEAD, and reaches the handler's
// own answer. Refused, it is 403 "this run belongs to a Repairs fixer you may
// not use".
func TestAtomBoot_AdminSessionMayApproveARunOfAnUnregisteredFixer(t *testing.T) {
	r := bootAccess(t, nil, nil)
	admin := newCoreAdmin(t, r)
	require.NoError(t, r.store.InsertRunV3(database.RunRecord{
		SchemaVersion: 3, ID: "run-batcomputer-plan", DefID: repairs.PlanOpID,
		Kind: "op", Mode: "live", Trigger: "manual", Params: json.RawMessage(`{"fixer_id":"batman-retired-gadget"}`),
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/runs/run-batcomputer-plan/approve", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	req.AddCookie(admin.cookie)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	t.Logf("cell POST /api/v3/ops/runs/:id/approve session:admin: %d %s", rec.Code, credExcerpt(rec))
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Errorf("the admin session approving a run of an unregistered fixer: %d %s; HEAD's admin held every permission", rec.Code, credExcerpt(rec))
	}
}

// atomSetChecker holds exactly its scopes.
type atomSetChecker []rbac.Permission

func (s atomSetChecker) Has(p rbac.Permission) bool { return slices.Contains(s, p) }
func (s atomSetChecker) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	if r.Kind() != rbac.KindAllOf || len(ps) == 0 {
		return false
	}
	for _, p := range ps {
		if !s.Has(p) {
			return false
		}
	}
	return true
}
func (atomSetChecker) Confined() bool                { return false }
func (s atomSetChecker) Concrete() []rbac.Permission { return s }

// TestAtomResolver_EditorCanCreatePlaylists: through books' own atom table, the
// in-handler bridge answers Can(playlists.create) for a session that holds
// books:playlists.create (HEAD's editor role held the atom, internal/auth
// seed.go), and refuses one that does not.
func TestAtomResolver_EditorCanCreatePlaylists(t *testing.T) {
	user := &database.User{ID: "u-robin", Username: "robin", Status: "active"}
	b, err := legacyauth.New(newAtomResolver(), func(context.Context, coreauth.UserID) (*database.User, error) { return user, nil }, nil)
	require.NoError(t, err)
	ctxWith := func(c rbac.Checker) context.Context {
		p := &coreauth.Principal{Method: coreauth.MethodSession, Perms: c,
			User: &coreauth.User{ID: coreauth.UserID(user.ID), Username: user.Username, Status: coreauth.StatusActive}}
		return rbac.WithChecker(coreauth.WithPrincipal(context.Background(), p), c)
	}
	editor := atomSetChecker{"books:view", "core:tasks.view", "books:edit", "books:playlists.create"}
	if !b.Can(ctxWith(editor), "playlists.create") {
		t.Error("an editor holding books:playlists.create: Can(playlists.create) = false; HEAD's editor held the atom")
	}
	if b.Can(ctxWith(atomSetChecker{"books:view", "core:tasks.view"}), "playlists.create") {
		t.Error("a viewer without books:playlists.create: Can(playlists.create) = true")
	}
}
