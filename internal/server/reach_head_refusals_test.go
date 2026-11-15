// file: internal/server/reach_head_refusals_test.go
// version: 1.0.0
// guid: ba9283a8-0b01-4f78-9c59-08653ba87539
// last-edited: 2026-11-14

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// The reach oracle (internal/books/cutover, F4-07) compares what a principal may
// reach on R0 with what the migrated corpus gives it under books' route
// requirements. Eight of its cells are gains of the auth-disabled principal that
// are not a change of policy: the route is AnyAuthenticated, which the
// auth-disabled principal holds, and R0's HANDLER refuses it because the handler
// needs a user and auth off has none. The router-level gain would turn into a real
// one the day a handler port resolved a fallback user, so each refusal is proven
// here on HEAD through the real router, with auth off and no credentials, and
// each is an obligation on F4-08 to prove again on the registered path
// (docs/agent-tasks/platform-core/seams.d/F4-07.md).
//
// The eight cells (reachHandlerRefusals in internal/books/cutover):
//
//	GET    /api/v1/auth/me
//	GET    /api/v1/auth/sessions
//	DELETE /api/v1/auth/sessions/:id
//	GET    /api/v1/auth/api-keys
//	GET    /api/v1/auth/api-keys/:id
//	POST   /api/v1/auth/api-keys         (key minting: first)
//	DELETE /api/v1/auth/api-keys/:id
//	POST   /api/v3/ops/runs/:id/approve

type authOffRefusal struct {
	name   string
	method string
	path   func(f *credGuardFixture) string
	body   any
	want   int    // the status HEAD's handler answers
	msg    string // its message, which names the handler's own check (handlers/auth.go, handlers/apikeys.go, handlers/opsv3/runs.go)
}

func authOffRefusals() []authOffRefusal {
	return []authOffRefusal{
		{"POST /auth/api-keys mints no key", http.MethodPost,
			func(*credGuardFixture) string { return "/api/v1/auth/api-keys" },
			map[string]any{"name": "minted with auth off"}, http.StatusUnauthorized, "authentication required"},
		{"GET /auth/me", http.MethodGet, func(*credGuardFixture) string { return "/api/v1/auth/me" }, nil, http.StatusUnauthorized, "not authenticated"},
		{"GET /auth/sessions", http.MethodGet, func(*credGuardFixture) string { return "/api/v1/auth/sessions" }, nil, http.StatusUnauthorized, "not authenticated"},
		{"DELETE /auth/sessions/:id", http.MethodDelete, func(f *credGuardFixture) string { return "/api/v1/auth/sessions/" + f.sessionToken }, nil, http.StatusUnauthorized, "not authenticated"},
		{"GET /auth/api-keys", http.MethodGet, func(*credGuardFixture) string { return "/api/v1/auth/api-keys" }, nil, http.StatusUnauthorized, "authentication required"},
		{"GET /auth/api-keys/:id", http.MethodGet, func(f *credGuardFixture) string { return "/api/v1/auth/api-keys/" + f.targetKeyID }, nil, http.StatusUnauthorized, "authentication required"},
		{"DELETE /auth/api-keys/:id", http.MethodDelete, func(f *credGuardFixture) string { return "/api/v1/auth/api-keys/" + f.targetKeyID }, nil, http.StatusUnauthorized, "authentication required"},
		{"POST /ops/runs/:id/approve", http.MethodPost, func(*credGuardFixture) string { return "/api/v3/ops/runs/01M7R2REACHORACLE0000000000/approve" },
			map[string]any{"decision": "approve", "method": "password"}, http.StatusForbidden, "approving needs a signed-in session"},
	}
}

// doAnonymous sends a request with no credentials at all, as the auth-disabled
// principal arrives.
func (f *credGuardFixture) doAnonymous(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	return w
}

func TestReachHeadHandlers_RefuseTheAuthDisabledPrincipal(t *testing.T) {
	f := setupCredGuardServerWith(t, func(c *config.Config) { c.EnableAuth = false })

	keysBefore, err := f.store.ListAllAPIKeys()
	require.NoError(t, err)

	for _, rt := range authOffRefusals() {
		t.Run(rt.name, func(t *testing.T) {
			w := f.doAnonymous(t, rt.method, rt.path(f), rt.body)
			assert.Equal(t, rt.want, w.Code, "the handler must refuse the auth-disabled principal (no user); body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), rt.msg, "the refusal must be the handler's own")
		})
	}

	// Nothing the refused requests named was touched.
	keysAfter, err := f.store.ListAllAPIKeys()
	require.NoError(t, err)
	assert.Len(t, keysAfter, len(keysBefore), "a refused POST /auth/api-keys must mint nothing, a refused DELETE must revoke nothing")
	for _, k := range keysAfter {
		assert.Equal(t, "active", k.Status, "key %s must still be active", k.ID)
	}
	sessions, err := f.store.ListUserSessions(f.adminID)
	require.NoError(t, err)
	assert.NotEmpty(t, sessions, "the refused DELETE /auth/sessions/:id must revoke nothing")
}
