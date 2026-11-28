// file: internal/books/identity_integrations_legacy_reach_boot_test.go
// version: 1.0.0
// guid: b2624956-00b9-4474-b578-0f77594b3570
// last-edited: 2026-11-28

package books

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The legacy integrations routes keep HEAD's reach under core identity (F7-09
// W1/W4/W5). Core's own instance, health and browse routes now ask the
// settings.manage atom's scopes besides their own (core/internal/integrationhttp,
// core/internal/libraryhttp); the books mount is a different surface and is
// unchanged: /api/v1/deluge/*, GET /api/v1/itunes/library/download and
// /api/v1/openlibrary/* are s.perm("integrations.manage") rows (internal/server
// legacy_manifest.go), which the atom table resolves to AllOf(core:integrations.manage,
// books:integrations.manage). A principal holding only HEAD's integrations.manage
// atom reaches every one of them, and a key holding no integrations scope does not.
//
// "Reaches" means the handler ran: no 401 and no 403, except the one 403 two
// handlers give on their own, deluge discovery being off (its text is pinned
// below). Any other 403, whatever stage 13's code for it (permission_denied,
// interactive_required, owner_required), is a refusal.

// legacyIntegrationsRoutes are the twelve rows of the manifest whose atom is
// integrations.manage, as the manifest lists them.
var legacyIntegrationsRoutes = []struct{ method, path string }{
	{http.MethodGet, "/api/v1/deluge/discover"},
	{http.MethodPost, "/api/v1/deluge/discover/import"},
	{http.MethodGet, "/api/v1/deluge/labels"},
	{http.MethodGet, "/api/v1/deluge/status"},
	{http.MethodPost, "/api/v1/deluge/test-connection"},
	{http.MethodGet, "/api/v1/deluge/torrents"},
	{http.MethodGet, "/api/v1/itunes/library/download"},
	{http.MethodDelete, "/api/v1/openlibrary/data"},
	{http.MethodPost, "/api/v1/openlibrary/download"},
	{http.MethodPost, "/api/v1/openlibrary/import"},
	{http.MethodGet, "/api/v1/openlibrary/status"},
	{http.MethodPost, "/api/v1/openlibrary/upload"},
}

// handlerOwn403 is what deluge discover and its import answer 403 from inside
// the handler while deluge_discovery_enabled is off (internal/server/deluge_discovery.go).
const handlerOwn403 = "deluge discovery is disabled"

// refusedByRoutePermission reports stage 13's refusal of a request that
// the holder's key must have passed: 401, or a 403 that is not the handler's own.
func refusedByRoutePermission(path string, code int, body string) bool {
	if code == http.StatusUnauthorized {
		return true
	}
	if code != http.StatusForbidden {
		return false
	}
	own := strings.HasPrefix(path, "/api/v1/deluge/discover") && strings.Contains(body, handlerOwn403)
	return !own
}

func TestLegacyIntegrationsReachBoot_IntegrationsAtomHolderReachesEveryRoute(t *testing.T) {
	r := bootAccess(t, nil, nil)
	admin := newCoreAdmin(t, r)
	holder, _ := admin.mintKey("batman-bat-signal", []string{"core:integrations.manage", "books:integrations.manage"})
	other, _ := admin.mintKey("batman-gotham-gazette", []string{"books:view"})

	// The list above is the booted app's: every books route that asks the
	// integrations.manage atom's scopes is in it, and nothing else is.
	var served []string
	for ri := range r.app.Routes() {
		if ri.Owner == "books" && ri.Perm.String() == "all(core:integrations.manage,books:integrations.manage)" {
			served = append(served, ri.Method+" "+ri.Pattern)
		}
	}
	var listed []string
	for _, c := range legacyIntegrationsRoutes {
		listed = append(listed, c.method+" "+c.path)
	}
	slices.Sort(served)
	slices.Sort(listed)
	require.Equal(t, listed, served, "the routes that ask HEAD's integrations.manage atom changed: add or drop a cell")

	// Each route is tried twice: a key without the scopes must be refused by
	// the route permission (so the cell can tell the two apart), and the atom's
	// holder must not.
	for i, c := range legacyIntegrationsRoutes {
		body := ""
		if c.method == http.MethodPost {
			body = "{}"
		}
		addr := "192.0.2." + strconv.Itoa(40+i) + ":41000"
		rec := invitesReq(t, r, c.method, c.path, body, other, nil, addr)
		t.Logf("cell %s %s key:books:view: %d", c.method, c.path, rec.Code)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "permission_denied") {
			t.Errorf("%s %s with a key holding no integrations scope: %d %s, want 403 permission_denied", c.method, c.path, rec.Code, credExcerpt(rec))
		}

		rec = invitesReq(t, r, c.method, c.path, body, holder, nil, addr)
		t.Logf("cell %s %s key:integrations.manage atom: %d", c.method, c.path, rec.Code)
		if refusedByRoutePermission(c.path, rec.Code, rec.Body.String()) {
			t.Errorf("%s %s with a key holding HEAD's integrations.manage atom: %d %s; HEAD admitted it", c.method, c.path, rec.Code, credExcerpt(rec))
		}
	}
	require.Len(t, legacyIntegrationsRoutes, 12)
}
