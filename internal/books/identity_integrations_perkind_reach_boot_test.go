// file: internal/books/identity_integrations_perkind_reach_boot_test.go
// version: 0.1.0
// guid: 8d41a7c2-5e96-4b13-a0f8-93c2d6e71b54
// last-edited: 2026-11-28

package books

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The legacy routes that stay alive after core's integration routes mount keep
// HEAD's reach under core identity (F7-09 round 2, per-kind table: docs/plans/
// platform-core/f7-09-integrations-per-kind-reach.md, table 5). Core cannot
// express them per kind yet, so each is the only route through which its
// principals reach what it does:
//
//   - GET /deluge/status and POST /deluge/test-connection: the integrations.manage
//     atom. Core serves no download-kind health or test (404, naming these routes)
//     until the W6 per-kind co-requirement lands, and the settings atom must not
//     gain them here either.
//   - GET /metadata/providers/throttles: library.view ({books:view, core:tasks.view}). Core's
//     metadata health asks the settings atom's seven scopes.
//   - POST /ai/test-connection: library.edit_metadata (books:edit). Core's ai test
//     asks the settings atom's seven scopes.
//
// The existing legacy cell (identity_integrations_legacy_reach_boot_test.go) already
// sends the atom's holder to /deluge/status among its twelve routes; this one names
// the four per-kind predecessors, adds the principals that must NOT reach them (the
// settings atom does not gain /deluge/*, and the atoms of the other routes stay out),
// and fails if a route is dropped from the booted app, which is how a flip PR would
// retire it. internal/server's TestKeptIntegrationPredecessors_* is the same guard
// against the 410 retirement lists.
//
// The atom rows are the ones books_test.go:199-202 pins for the cutover.

// settingsAtomScopes is HEAD's settings.manage atom after the cutover, as
// internal/books/atoms.go lists it.
var settingsAtomScopes = []string{"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"}

func TestLegacyReach_PerKindPredecessorsStayAlive(t *testing.T) {
	r := bootAccess(t, nil, nil)
	admin := newCoreAdmin(t, r)
	integrationsAtom, _ := admin.mintKey("batman-bat-signal", []string{"core:integrations.manage", "books:integrations.manage"})
	settingsAtom, _ := admin.mintKey("batman-gotham-gazette", settingsAtomScopes)
	// library.view is the atom {books:view, core:tasks.view} (books_test.go:194).
	viewer, _ := admin.mintKey("batman-night-owl", []string{"books:view", "core:tasks.view"})
	editor, _ := admin.mintKey("batman-red-hood", []string{"books:edit"})

	cases := []struct {
		method, path, body string
		// perm is a scope the booted route's requirement must name: the route is in
		// the booted app with the requirement HEAD's atom resolves to.
		perm string
		// reach and refuse are the keys HEAD admits and refuses (verdicts.json: the
		// atom-only rows).
		reach, refuse map[string]string
	}{
		{http.MethodGet, "/api/v1/deluge/status", "", "books:integrations.manage",
			map[string]string{"integrations.manage atom": integrationsAtom},
			map[string]string{"settings.manage atom": settingsAtom, "books:view": viewer, "books:edit": editor}},
		{http.MethodPost, "/api/v1/deluge/test-connection", "{}", "books:integrations.manage",
			map[string]string{"integrations.manage atom": integrationsAtom},
			map[string]string{"settings.manage atom": settingsAtom, "books:view": viewer, "books:edit": editor}},
		{http.MethodGet, "/api/v1/metadata/providers/throttles", "", "core:tasks.view",
			map[string]string{"library.view atom": viewer},
			map[string]string{"library.edit_metadata (books:edit)": editor, "integrations.manage atom": integrationsAtom, "settings.manage atom": settingsAtom}},
		{http.MethodPost, "/api/v1/ai/test-connection", "{}", "books:edit",
			map[string]string{"library.edit_metadata (books:edit)": editor},
			map[string]string{"integrations.manage atom": integrationsAtom, "settings.manage atom": settingsAtom, "books:view": viewer}},
	}

	// The retirement guard: the booted app serves each route with a requirement
	// that names its scope. A flip PR that deletes a row from legacy_manifest.go
	// and its wire_*_routes.go registration fails here.
	booted := map[string]string{}
	for ri := range r.app.Routes() {
		if ri.Owner == "books" {
			booted[ri.Method+" "+ri.Pattern] = ri.Perm.String()
		}
	}
	for _, c := range cases {
		perm, ok := booted[c.method+" "+c.path]
		require.True(t, ok, "%s %s is not in the booted app: retiring it needs a core route that expresses its requirement (todo.d f7-09-retire-throttles-route, f7-09-enable-core-download-health)", c.method, c.path)
		require.Contains(t, perm, c.perm, "%s %s requirement", c.method, c.path)
	}

	n := 0
	for _, c := range cases {
		for who, key := range c.reach {
			n++
			addr := "192.0.2." + strconv.Itoa(60+n) + ":41000"
			rec := invitesReq(t, r, c.method, c.path, c.body, key, nil, addr)
			t.Logf("cell %s %s as %s: %d", c.method, c.path, who, rec.Code)
			if refusedByRoutePermission(c.path, rec.Code, rec.Body.String()) {
				t.Errorf("%s %s as %s: %d %s; HEAD admitted it", c.method, c.path, who, rec.Code, credExcerpt(rec))
			}
		}
		for who, key := range c.refuse {
			n++
			addr := "192.0.2." + strconv.Itoa(60+n) + ":41000"
			rec := invitesReq(t, r, c.method, c.path, c.body, key, nil, addr)
			t.Logf("cell %s %s as %s: %d", c.method, c.path, who, rec.Code)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "permission_denied") {
				t.Errorf("%s %s as %s: %d %s; HEAD refused it (403 permission_denied)", c.method, c.path, who, rec.Code, credExcerpt(rec))
			}
		}
	}
}
