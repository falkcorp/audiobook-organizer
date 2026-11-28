// file: internal/books/identity_integrations_legacy_reach_boot_test.go
// version: 1.0.0
// guid: b2624956-00b9-4474-b578-0f77594b3570
// last-edited: 2026-11-28

package books

import (
	"net/http"
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
// "Reaches" is judged on the answer's own words: stage 13's refusal is the
// permission_denied problem, and a handler may still answer 400 or a 403 of its
// own (deluge discovery disabled, a credential guard); those are not the
// route permission.

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

// refusedByRoutePermission reports stage 13's 403 permission_denied, as opposed
// to any answer the handler gives.
func refusedByRoutePermission(code int, body string) bool {
	return code == http.StatusForbidden && strings.Contains(body, "permission_denied")
}

func TestLegacyIntegrationsReachBoot_IntegrationsAtomHolderReachesEveryRoute(t *testing.T) {
	r := bootAccess(t, nil, nil)
	admin := newCoreAdmin(t, r)
	holder, _ := admin.mintKey("batman-bat-signal", []string{"core:integrations.manage", "books:integrations.manage"})
	other, _ := admin.mintKey("batman-gotham-gazette", []string{"books:view"})

	// The cell's own check that it can tell the two apart: the manifest's
	// twelve rows are exactly these, and a key without the scopes is refused
	// on each by the route permission.
	for i, c := range legacyIntegrationsRoutes {
		body := ""
		if c.method == http.MethodPost {
			body = "{}"
		}
		addr := "192.0.2." + strconv.Itoa(40+i) + ":41000"
		rec := invitesReq(t, r, c.method, c.path, body, other, nil, addr)
		t.Logf("cell %s %s key:books:view: %d", c.method, c.path, rec.Code)
		if !refusedByRoutePermission(rec.Code, rec.Body.String()) {
			t.Errorf("%s %s with a key holding no integrations scope: %d %s, want 403 permission_denied", c.method, c.path, rec.Code, credExcerpt(rec))
		}

		rec = invitesReq(t, r, c.method, c.path, body, holder, nil, addr)
		t.Logf("cell %s %s key:integrations.manage atom: %d", c.method, c.path, rec.Code)
		if rec.Code == http.StatusUnauthorized || refusedByRoutePermission(rec.Code, rec.Body.String()) {
			t.Errorf("%s %s with a key holding HEAD's integrations.manage atom: %d %s; HEAD admitted it", c.method, c.path, rec.Code, credExcerpt(rec))
		}
	}
	require.Len(t, legacyIntegrationsRoutes, 12)
}
