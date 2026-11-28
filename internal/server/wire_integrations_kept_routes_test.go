// file: internal/server/wire_integrations_kept_routes_test.go
// version: 0.1.0
// guid: 2f8c6b14-d7a3-4e90-b5c1-6a9e03d4f782
// last-edited: 2026-11-28

package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keptIntegrationPredecessors are the legacy routes that serve what core's
// integration routes cannot express per kind yet (F7-09 round 2, per-kind table:
// docs/plans/platform-core/f7-09-integrations-per-kind-reach.md, table 5). Each
// stays alive after the F7-09 mount:
//
//   - GET /metadata/providers/throttles asks library.view; core's metadata health
//     asks the settings atom's seven scopes, and core cannot express books:view
//     yet, so library.view holders would lose the read.
//   - POST /ai/test-connection asks library.edit_metadata, the same for ai test.
//   - GET /deluge/status and POST /deluge/test-connection ask the
//     integrations.manage atom, which core serves for no download-kind route until
//     the W6 per-kind co-requirement lands (core answers 404 and names these).
//
// This is the retirement guard: the cells fail if one of them is registered as a
// 410 stub (the retired*Routes lists of the *_gone_test.go files are what retire a
// route), is dropped from the router, or is added to a retired list.
var keptIntegrationPredecessors = []struct{ method, pattern, path string }{
	{http.MethodGet, "/api/v1/metadata/providers/throttles", "/api/v1/metadata/providers/throttles"},
	{http.MethodPost, "/api/v1/ai/test-connection", "/api/v1/ai/test-connection"},
	{http.MethodGet, "/api/v1/deluge/status", "/api/v1/deluge/status"},
	{http.MethodPost, "/api/v1/deluge/test-connection", "/api/v1/deluge/test-connection"},
}

// TestKeptIntegrationPredecessors_AreRegisteredAndNotGone: each route is on the
// router and answers for real, not with the shared 410 stub.
func TestKeptIntegrationPredecessors_AreRegisteredAndNotGone(t *testing.T) {
	f := setupCredGuardServer(t)
	registered := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, k := range keptIntegrationPredecessors {
		t.Run(k.method+" "+k.pattern, func(t *testing.T) {
			require.True(t, registered[k.method+" "+k.pattern], "route not registered: retiring it needs a core route that can express its requirement")
			w := f.do(t, f.sessionToken, k.method, k.path, nil)
			assert.NotEqual(t, http.StatusGone, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestKeptIntegrationPredecessors_AreInNoRetiredList: no retired*Routes list
// names a kept route, so adding GET throttles (or another) to one fails by name
// before the 410 stub is even wired. The two lists that name neighbours of these
// routes are checked: the provider-throttle resets (DELETE only) and the
// catalog-review diagnostics.
func TestKeptIntegrationPredecessors_AreInNoRetiredList(t *testing.T) {
	retired := map[string]bool{}
	for _, r := range retiredMetadataRoutes {
		retired[r.method+" "+r.pattern] = true
	}
	for _, r := range retiredCatalogReviewRoutes {
		retired[r.method+" "+r.pattern] = true
	}
	for _, k := range keptIntegrationPredecessors {
		assert.False(t, retired[k.method+" "+k.pattern], "%s %s is kept for the per-kind reach and must not be retired", k.method, k.pattern)
	}
}
