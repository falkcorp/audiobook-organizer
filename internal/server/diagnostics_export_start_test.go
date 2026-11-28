// file: internal/server/diagnostics_export_start_test.go
// version: 1.1.0
// guid: dc0c0ef5-7f71-4b17-a738-f09827298455
// last-edited: 2026-11-28

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

const exportParams = `{"category":"general"}`

// exportStartRoute is one way of starting the diagnostics export: the request
// it makes, given the id of a finished export a retry can re-run.
type exportStartRoute struct {
	name string
	req  func(retryID string) *http.Request
}

func jsonPost(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// exportStartRoutes is every route that reaches EnqueueOp for a def id the
// caller names (or, for the export's own route, the export): the refusal lives
// in the registry so that each of them makes it, and a route missing from this
// list is a route nobody has shown does.
func exportStartRoutes() []exportStartRoute {
	return []exportStartRoute{
		{"POST /api/v1/diagnostics/export", func(string) *http.Request {
			return jsonPost("/api/v1/diagnostics/export", exportParams)
		}},
		{"POST /api/v1/operations/v2", func(string) *http.Request {
			return jsonPost("/api/v1/operations/v2", `{"def_id":"`+handlers.DiagnosticsExportDefID+`","params":`+exportParams+`}`)
		}},
		{"POST /api/v1/operations/v2/:id/retry", func(id string) *http.Request {
			return jsonPost("/api/v1/operations/v2/"+id+"/retry", ``)
		}},
		{"POST /api/v3/ops/defs/:id/runs", func(string) *http.Request {
			return jsonPost("/api/v3/ops/defs/"+handlers.DiagnosticsExportDefID+"/runs", `{"params":`+exportParams+`}`)
		}},
		{"POST /api/v3/ops/runs", func(string) *http.Request {
			return jsonPost("/api/v3/ops/runs", `{"def":"`+handlers.DiagnosticsExportDefID+`","params":`+exportParams+`}`)
		}},
	}
}

// exportRows is how many operation rows the store holds, any status: a refused
// start must leave this unchanged.
func exportRows(t *testing.T, store database.Store) int {
	t.Helper()
	rows, err := store.ListOperationsV2Since(time.Time{}, 10000)
	require.NoError(t, err)
	return len(rows)
}

// TestDiagnosticsExportStart_EveryRouteRefusesWithoutAnAccountUnderCoreIdentity:
// with core's auth disabled a request binds no user, so no actor is stamped, and
// an export started then could only be queued and fail with errNoExportActor.
// Every route that can start it answers 409 before it enqueues, saying why, and
// nothing is queued: the refusal is the definition's start precondition, made by
// EnqueueOp, so it holds on the export's own route, the generic start, a retry of
// a finished export, and both v3 run routes. A signed-in account's start on each
// is accepted, so the refusal is not a closed route. An export is not widened to
// the no-account case (AUTH-OFF-EXPORT is the owner's call).
func TestDiagnosticsExportStart_EveryRouteRefusesWithoutAnAccountUnderCoreIdentity(t *testing.T) {
	w := newSiteWorld(t, false, true, "settings.manage", "scan.trigger")
	require.NotNil(t, w.core.opRegistry, "the fixture builds the operations registry")
	w.core.opRegistry.Start(context.Background())
	t.Cleanup(func() { _ = w.core.opRegistry.Shutdown(context.Background()) })

	const finished = "01JBATMANEXPORT0000000000A"
	seedCompletedExport(t, w.store, finished, handlers.DiagnosticsExportDefID, nil, nil)

	off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil, "test:settings.manage", "test:scan.trigger")
	for _, route := range exportStartRoutes() {
		t.Run("auth off/"+route.name, func(t *testing.T) {
			before := exportRows(t, w.store)
			rec := identityServe(w.core.router, route.req(finished), off, offC)
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "core authentication is disabled")
			for _, minted := range []string{"operation_id", "run_id", "op_id"} {
				assert.NotContains(t, rec.Body.String(), minted, "an operation was started for a request that has no account")
			}
			assert.Equal(t, before, exportRows(t, w.store), "a refused start left an operation row behind")
		})
	}

	// The kind this export is renamed to at the cutover is not a second way in:
	// no definition answers to it at runtime, so a start by that name queues
	// nothing, whoever asks.
	for _, p := range []struct {
		name, path, body string
	}{
		{"v1 start", "/api/v1/operations/v2", `{"def_id":"core.diagnostics.export","params":` + exportParams + `}`},
		{"v3 start", "/api/v3/ops/defs/core.diagnostics.export/runs", `{"params":` + exportParams + `}`},
	} {
		t.Run("renamed kind/"+p.name, func(t *testing.T) {
			before := exportRows(t, w.store)
			rec := identityServe(w.core.router, jsonPost(p.path, p.body), off, offC)
			assert.NotEqual(t, http.StatusAccepted, rec.Code, rec.Body.String())
			assert.Equal(t, before, exportRows(t, w.store), "a start by the renamed kind queued an operation")
		})
	}

	user, userC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:settings.manage", "test:scan.trigger")
	for _, route := range exportStartRoutes() {
		t.Run("signed in/"+route.name, func(t *testing.T) {
			rec := identityServe(w.core.router, route.req(finished), user, userC)
			require.Equal(t, http.StatusAccepted, rec.Code, "a signed-in account's start was refused: "+rec.Body.String())
		})
	}
}

// TestDiagnosticsExportStartPrecondition_NamesWhyItRefuses: the precondition
// refuses a start that stamps no actor in both of its shapes, naming core's auth
// being disabled only when that is the reason, and allows one that stamps an
// account. Off core identity there is no refusal (HEAD's export runs with auth
// off).
func TestDiagnosticsExportStartPrecondition_NamesWhyItRefuses(t *testing.T) {
	w := newSiteWorld(t, false, true, "settings.manage")
	pre := w.core.diagnosticsStartPrecondition()
	require.NotNil(t, pre)
	reason := func(err error) string {
		var refused *opsregistry.StartRefusedError
		require.ErrorAs(t, err, &refused)
		return refused.Reason
	}

	off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
	assert.Contains(t, reason(pre(w.ctxOf(true, nil, off, offC), opsregistry.EnqueueOptions{})), "core authentication is disabled")

	assert.NoError(t, pre(w.ctxOf(true, nil, off, offC), opsregistry.EnqueueOptions{ActorUserID: w.bruce.ID}),
		"a start that stamps an account was refused")

	anon := reason(pre(w.ctxOf(true, nil, nil, nil), opsregistry.EnqueueOptions{}))
	assert.NotContains(t, anon, "core authentication is disabled", "an unauthenticated request was told core's auth is off")

	headPre := w.head.diagnosticsStartPrecondition()
	require.NotNil(t, headPre, "the precondition is always wired; it reads the identity when asked")
	assert.NoError(t, headPre(context.Background(), opsregistry.EnqueueOptions{}), "HEAD's export start is unchanged")
}
