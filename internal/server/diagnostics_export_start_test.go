// file: internal/server/diagnostics_export_start_test.go
// version: 1.0.0
// guid: dc0c0ef5-7f71-4b17-a738-f09827298455
// last-edited: 2026-11-28

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

func startExportRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/diagnostics/export", strings.NewReader(`{"category":"general"}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestDiagnosticsExportStart_RefusedWithoutAnAccountUnderCoreIdentity: with
// core's auth disabled a request binds no user, and an export started then
// could only be queued and fail with errNoExportActor. POST
// /diagnostics/export answers 409 before it enqueues, saying why; a signed-in
// account's start is accepted, so the refusal is not a closed route. An export
// is not widened to the no-account case (AUTH-OFF-EXPORT is the owner's call).
func TestDiagnosticsExportStart_RefusedWithoutAnAccountUnderCoreIdentity(t *testing.T) {
	w := newSiteWorld(t, false, true, "settings.manage")
	require.NotNil(t, w.core.opRegistry, "the fixture builds the operations registry")
	w.core.opRegistry.Start(context.Background())
	t.Cleanup(func() { _ = w.core.opRegistry.Shutdown(context.Background()) })

	off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil, "test:settings.manage")
	rec := identityServe(w.core.router, startExportRequest(), off, offC)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "core authentication is disabled")
	assert.NotContains(t, rec.Body.String(), "operation_id", "an operation was enqueued for a request that has no account")

	user, userC := adoptPrincipal(coreauth.MethodSession, w.bruce, "test:settings.manage")
	rec = identityServe(w.core.router, startExportRequest(), user, userC)
	require.Equal(t, http.StatusAccepted, rec.Code, "a signed-in account's start was refused: "+rec.Body.String())
	assert.Contains(t, rec.Body.String(), "operation_id")
}

// TestDiagnosticsExportStart_CheckNamesWhyItRefuses: the check refuses a
// request with no bound user in both of its shapes, naming core's auth being
// disabled only when that is the reason, and passes one with a user. Off core
// identity there is no check (HEAD's export runs with auth off).
func TestDiagnosticsExportStart_CheckNamesWhyItRefuses(t *testing.T) {
	w := newSiteWorld(t, false, true, "settings.manage")
	check := w.core.diagnosticsStartCheck()
	require.NotNil(t, check)
	require.Nil(t, w.head.diagnosticsStartCheck(), "HEAD's export start is unchanged")

	off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
	assert.Contains(t, check(ginOf(w.ctxOf(true, nil, off, offC))), "core authentication is disabled")

	assert.Empty(t, check(ginOf(w.ctxOf(true, []auth.Permission{}, nil, nil))), "a request with a bound user was refused")

	anon := ginOf(w.ctxOf(true, nil, nil, nil))
	refusal := check(anon)
	assert.NotEmpty(t, refusal)
	assert.NotContains(t, refusal, "core authentication is disabled", "an unauthenticated request was told core's auth is off")
}
