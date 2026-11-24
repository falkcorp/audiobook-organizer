// file: internal/books/identity_key_clamp_boot_test.go
// version: 0.1.0
// guid: 7c2e9a41-3f58-4b6d-a1e0-5d84c9b27f13
// last-edited: 2026-11-22

package books

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// The owner clamp on a snapshot key (F4-08 B10z). HEAD's empty-scope key is
// dynamic: it holds "all the owner's role permissions" as they are on each
// request. Under the flip a key's scopes are a list fixed when it is created
// (M3's keyScopes for an imported empty-scope key; the request's scopes for a
// new one). That is a narrowing only if every request still intersects the
// key's scopes with the owner's CURRENT role permissions, so that demoting the
// owner takes the permission away from the key at once, as HEAD did. These
// cells demote the owner between two requests of the same key and read the
// effective set the handlers see (GET /api/v1/auth/me's permissions, the set
// auth.Can reads). The clamp is core's: resolveKey reads the owner afresh and
// authz.Compile intersects the key's scopes with the owner's role set.

// coreDoClients numbers coreDo's requests: each comes from its own address in
// 192.0.2.0/24 (TEST-NET-1), so core's stage-10 rate limiter, which buckets by
// client address, never throttles a test that makes many requests at once.
var coreDoClients atomic.Uint32

// coreDo is one request to the composed app with a JSON body and, when
// cookie is set, a signed-in session.
func coreDo(t *testing.T, r *runningApp, method, path string, body any, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = fmt.Sprintf("192.0.2.%d:41000", 1+coreDoClients.Add(1)%254)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// v1Permissions is GET /api/v1/auth/me's permission list for the key raw.
func v1Permissions(t *testing.T, r *runningApp, raw string) []string {
	t.Helper()
	rec := coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, raw)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			User struct {
				Permissions []string `json:"permissions"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.NotEmpty(t, body.Data.User.Permissions, "GET /api/v1/auth/me named no permission: %s", rec.Body.String())
	return body.Data.User.Permissions
}

// userIDByEmail is core's id of the user holding email.
func userIDByEmail(t *testing.T, admin *coreAdmin, email string) string {
	t.Helper()
	for id, e := range admin.users() {
		if e == email {
			return id
		}
	}
	t.Fatalf("core holds no user %s", email)
	return ""
}

// TestKeyClamp_DemotedOwnerTakesThePermissionFromEveryKey: lucius (editor)
// holds an M3-imported empty-scope key (HEAD's "all my role permissions") and a
// key minted after the flip with the editor's books:edit. bruce moves lucius
// to viewer; on the very next request neither key carries library.edit_metadata
// (HEAD's atom behind books:edit), while the viewer's library.view stays.
func TestKeyClamp_DemotedOwnerTakesThePermissionFromEveryKey(t *testing.T) {
	var h headKeys
	fx := newFlipFixture(t, seedHEAD(t, &h), "")
	r := bootOverHEADStore(t, fx)
	bruce := loginCoreAdmin(t, r, wayne, h.password)
	lucius := loginCoreAdmin(t, r, "lucius.fox", h.password)
	luciusID := userIDByEmail(t, bruce, "lucius.fox@wayne-enterprises.example.test")

	// A key lucius mints after the flip, naming the editor's books scopes: the
	// same snapshot shape M3 gives his imported key.
	minted, _ := lucius.mintKey("lucius-grapnel", []string{"books:edit", "books:view", "core:tasks.view"})

	keys := map[string]string{"imported empty-scope abk_": h.full.raw, "minted after the flip mck_": minted}
	for name, raw := range keys {
		perms := v1Permissions(t, r, raw)
		require.Containsf(t, perms, "library.edit_metadata", "%s: before the demotion the editor's key edits: %v", name, perms)
		require.Containsf(t, perms, "library.view", "%s: %v", name, perms)
	}

	rec := coreDo(t, r, http.MethodPut, "/api/core/v1/users/"+luciusID+"/roles", map[string]any{"roles": []string{"viewer"}}, bruce.cookie, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for name, raw := range keys {
		t.Run(name, func(t *testing.T) {
			perms := v1Permissions(t, r, raw)
			require.Falsef(t, slices.Contains(perms, "library.edit_metadata"),
				"the demoted owner's key still edits on its next request (a snapshot that outlived the role): %v", perms)
			require.Contains(t, perms, "library.view", "the viewer's permission stays: %v", perms)
		})
	}
}
