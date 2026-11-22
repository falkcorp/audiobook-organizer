// file: internal/books/identity_apikey_routes_boot_test.go
// version: 0.1.0
// guid: 2a9d6e14-7b3c-4f85-a6e1-0c8f5b2d94e7
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
)

// HEAD's /api/v1/auth/api-keys on the flipped build (F4-08 B10z). M3 drains
// books' apikey: rows into core and retires the claim, so until B10z a key
// created from the SPA's key page was a 500 and the list was empty. These
// cells hold the routes to HEAD: imported keys list, a new key works on
// /api/v1 and on ABS, its scopes come from M3's own rule (ambient included),
// every request still clamps a key to its owner's current roles, and the
// read, status and revoke routes answer as HEAD did.

// v1Key is one entry of GET /api/v1/auth/api-keys.
type v1Key struct {
	ID         string   `json:"id"`
	UserID     string   `json:"user_id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	Status     string   `json:"status"`
	Identifier string   `json:"identifier"`
	Username   string   `json:"username"`
	ExpiresAt  *string  `json:"expires_at"`
}

// v1Keys is GET /api/v1/auth/api-keys (path carries any query) as cookie.
func v1Keys(t *testing.T, r *runningApp, path string, a *coreAdmin) map[string]v1Key {
	t.Helper()
	rec := coreDo(t, r, http.MethodGet, path, nil, a.cookie, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			Keys       []v1Key `json:"api_keys"`
			Count      int     `json:"count"`
			Unreadable int     `json:"unreadable"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.Equal(t, len(body.Data.Keys), body.Data.Count, rec.Body.String())
	out := map[string]v1Key{}
	for _, k := range body.Data.Keys {
		out[k.ID] = k
	}
	return out
}

// v1Created is POST /api/v1/auth/api-keys's 201 body.
type v1Created struct {
	ID        string   `json:"id"`
	Token     string   `json:"token"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
	Note      string   `json:"note"`
}

// v1Create creates a key through HEAD's route, as a session (cookie) or a key
// (bearer), and returns the response.
func v1Create(t *testing.T, r *runningApp, body map[string]any, a *coreAdmin, bearer string) (int, v1Created, string) {
	t.Helper()
	var cookie *http.Cookie
	if a != nil {
		cookie = a.cookie
	}
	rec := coreDo(t, r, http.MethodPost, "/api/v1/auth/api-keys", body, cookie, bearer)
	var env struct {
		Data v1Created `json:"data"`
	}
	if rec.Code == http.StatusCreated {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	}
	return rec.Code, env.Data, rec.Body.String()
}

// mustCreate is v1Create that requires a 201.
func mustCreate(t *testing.T, r *runningApp, body map[string]any, a *coreAdmin, bearer string) v1Created {
	t.Helper()
	code, res, raw := v1Create(t, r, body, a, bearer)
	require.Equal(t, http.StatusCreated, code, raw)
	require.True(t, strings.HasPrefix(res.Token, "mck_"), "a key created after the flip is core's")
	return res
}

// keyCoreMe is GET /api/core/v1/me with the key raw: its status and method.
func keyCoreMe(t *testing.T, r *runningApp, raw string) (int, string) {
	t.Helper()
	rec := coreDo(t, r, http.MethodGet, "/api/core/v1/me", nil, nil, raw)
	var body struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Method
}

// coreMePerms is GET /api/core/v1/me's permission list (the effective
// scopes, ambient ones included) for a key (bearer) or a session (cookie).
func coreMePerms(t *testing.T, r *runningApp, cookie *http.Cookie, bearer string) []string {
	t.Helper()
	rec := coreDo(t, r, http.MethodGet, "/api/core/v1/me", nil, cookie, bearer)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Permissions []string `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Permissions
}

// coreScopes is the stored core scopes of key id, read through core's admin
// listing as admin.
func coreScopes(t *testing.T, admin *coreAdmin, id string) []string {
	t.Helper()
	for _, k := range admin.keys() {
		if k.ID == id {
			return k.Scopes
		}
	}
	t.Fatalf("core lists no key %s", id)
	return nil
}

// absBook is a book with a sync id, for the ABS progress route.
func absBook(t *testing.T, r *runningApp, title string) string {
	t.Helper()
	b, err := r.store.CreateBook(&database.Book{Title: title, FilePath: filepath.Join(t.TempDir(), title+".m4b")})
	require.NoError(t, err)
	syncID, err := r.store.MintOrGetSyncID(b.ID)
	require.NoError(t, err)
	return syncID
}

// absProgress is PATCH /api/me/progress/:id (own listening progress, HEAD's
// any-signed-in-user route, books:reading under core) with the key raw.
func absProgress(t *testing.T, r *runningApp, syncID, raw string) (int, string) {
	t.Helper()
	rec := coreDo(t, r, http.MethodPatch, "/api/me/progress/"+syncID, map[string]any{"currentTime": 42.0, "duration": 600.0, "progress": 0.07}, nil, raw)
	return rec.Code, rec.Body.String()
}

func TestAPIKeyRoutes_UnderIdentity(t *testing.T) {
	var h headKeys
	fx := newFlipFixture(t, seedHEAD(t, &h), "")
	r := bootOverHEADStore(t, fx)
	bruce := loginCoreAdmin(t, r, wayne, h.password)
	lucius := loginCoreAdmin(t, r, "lucius.fox", h.password)
	luciusID := userIDByEmail(t, bruce, "lucius.fox@wayne-enterprises.example.test")
	book := absBook(t, r, "Year One")

	t.Run("GET lists the imported keys in HEAD's shape", func(t *testing.T) {
		own := v1Keys(t, r, "/api/v1/auth/api-keys", bruce)
		k, ok := own[h.limited.id]
		require.True(t, ok, "bruce's imported key is not listed: %v", own)
		require.Equal(t, []string{"library.view"}, k.Scopes)
		require.Equal(t, "active", k.Status)
		require.True(t, strings.HasPrefix(k.Identifier, "abk_") && len(k.Identifier) == 12, k.Identifier)
		require.Empty(t, k.Username, "HEAD names the owner only on ?all=true")
		_, leaked := own[h.full.id]
		require.False(t, leaked, "the caller's own list carries another user's key")

		all := v1Keys(t, r, "/api/v1/auth/api-keys?all=true", bruce)
		require.Contains(t, all, h.full.id, "?all=true leaves out lucius's key")
		require.Equal(t, "lucius.fox", all[h.full.id].Username)
		require.Equal(t, luciusID, all[h.full.id].UserID)

		// ?all=true without users.manage is the caller's own list, as on HEAD.
		mine := v1Keys(t, r, "/api/v1/auth/api-keys?all=true", lucius)
		require.Contains(t, mine, h.full.id)
		require.NotContains(t, mine, h.limited.id, "a non-admin's ?all=true listed another user's key")
	})

	t.Run("a key created after the flip holds what an imported key with the same HEAD row holds", func(t *testing.T) {
		// One rule (m3.KeyScoper) for both: M3 fed it the owner's role
		// patterns at the cutover, the route feeds it the owner's compiled set.
		empty := mustCreate(t, r, map[string]any{"name": "lucius-twin"}, lucius, "")
		require.ElementsMatch(t, coreScopes(t, bruce, h.full.id), coreScopes(t, bruce, empty.ID),
			"lucius's empty-scope key: imported vs created after the flip")
		view := mustCreate(t, r, map[string]any{"name": "bruce-twin", "scopes": []string{"library.view"}}, bruce, "")
		require.ElementsMatch(t, coreScopes(t, bruce, h.limited.id), coreScopes(t, bruce, view.ID),
			"bruce's library.view key: imported vs created after the flip")
	})

	t.Run("an empty-scope key created on HEAD's route works on core and on ABS", func(t *testing.T) {
		res := mustCreate(t, r, map[string]any{"name": "lucius-grapnel"}, lucius, "")
		require.Equal(t, []string{}, res.Scopes, "HEAD answers the scopes asked for")
		code, method := keyCoreMe(t, r, res.Token)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "api_key", method)
		code, body := absProgress(t, r, book, res.Token)
		require.Equal(t, http.StatusOK, code, body)
		perms := v1Permissions(t, r, res.Token)
		require.Contains(t, perms, "library.edit_metadata", "the editor's empty-scope key holds the editor's set: %v", perms)
		listed := v1Keys(t, r, "/api/v1/auth/api-keys", lucius)
		require.Contains(t, listed, res.ID, "the new key is not listed")
		require.True(t, strings.HasPrefix(listed[res.ID].Identifier, "mck_"))
	})

	t.Run("an explicit library.view key reaches ABS progress through the ambient grant", func(t *testing.T) {
		res := mustCreate(t, r, map[string]any{"name": "batarang-view", "scopes": []string{"library.view"}}, lucius, "")
		require.Equal(t, []string{"library.view"}, res.Scopes)
		code, body := absProgress(t, r, book, res.Token)
		require.Equal(t, http.StatusOK, code, body)
		perms := v1Permissions(t, r, res.Token)
		require.Contains(t, perms, "library.view")
		require.NotContains(t, perms, "library.edit_metadata", "the key holds more than it was given: %v", perms)
		require.Contains(t, coreScopes(t, bruce, res.ID), "books:reading", "the new key lacks M3's ambient scope")
	})

	t.Run("the expiry rules are HEAD's", func(t *testing.T) {
		code, _, raw := v1Create(t, r, map[string]any{"name": "x", "expires_in_days": 1097}, lucius, "")
		require.Equal(t, http.StatusBadRequest, code, raw)
		code, _, raw = v1Create(t, r, map[string]any{"name": "x", "expires_in_days": 2, "expires_at": "2099-01-01T00:00:00Z"}, lucius, "")
		require.Equal(t, http.StatusBadRequest, code, raw)
		res := mustCreate(t, r, map[string]any{"name": "long-batrope", "scopes": []string{"library.view"}, "expires_in_days": 1096}, lucius, "")
		exp, err := time.Parse(time.RFC3339, res.ExpiresAt)
		require.NoError(t, err)
		require.WithinDuration(t, time.Now().Add(1096*24*time.Hour), exp, time.Minute)
		require.Empty(t, res.Note, "a session is not clamped")
		def := mustCreate(t, r, map[string]any{"name": "default-batrope", "scopes": []string{"library.view"}}, lucius, "")
		exp, err = time.Parse(time.RFC3339, def.ExpiresAt)
		require.NoError(t, err)
		require.WithinDuration(t, time.Now().Add(30*24*time.Hour), exp, time.Minute)
	})

	t.Run("a key caller names scopes, holds them, and is clamped to its own expiry", func(t *testing.T) {
		parent := mustCreate(t, r, map[string]any{"name": "short-batrope", "scopes": []string{"library.view"}, "expires_in_days": 2}, lucius, "")
		code, _, raw := v1Create(t, r, map[string]any{"name": "child"}, nil, parent.Token)
		require.Equal(t, http.StatusForbidden, code, raw)
		require.Contains(t, raw, "must name the scopes")
		code, _, raw = v1Create(t, r, map[string]any{"name": "child", "scopes": []string{"library.edit_metadata"}}, nil, parent.Token)
		require.Equal(t, http.StatusForbidden, code, raw)
		require.Contains(t, raw, "cannot grant scope you don't have: library.edit_metadata")
		child := mustCreate(t, r, map[string]any{"name": "child", "scopes": []string{"library.view"}, "expires_in_days": 30}, nil, parent.Token)
		require.Equal(t, clampedNoteText, child.Note)
		require.Equal(t, parent.ExpiresAt, child.ExpiresAt, "the child outlives the key that made it")
		// A key may not mint for another user: refused at the route.
		code, _, raw = v1Create(t, r, map[string]any{"name": "child", "scopes": []string{"library.view"}, "user_id": userIDByEmail(t, bruce, "bruce.wayne@wayne-enterprises.example.test")}, nil, parent.Token)
		require.Equal(t, http.StatusForbidden, code, raw)
	})

	t.Run("an admin creates a key for another user; a non-admin may not", func(t *testing.T) {
		res := mustCreate(t, r, map[string]any{"name": "for-lucius", "scopes": []string{"library.view"}, "user_id": luciusID}, bruce, "")
		listed := v1Keys(t, r, "/api/v1/auth/api-keys", lucius)
		require.Contains(t, listed, res.ID, "the key is not lucius's")
		bruceID := userIDByEmail(t, bruce, "bruce.wayne@wayne-enterprises.example.test")
		code, _, raw := v1Create(t, r, map[string]any{"name": "for-bruce", "scopes": []string{"library.view"}, "user_id": bruceID}, lucius, "")
		require.Equal(t, http.StatusForbidden, code, raw)
		require.Contains(t, raw, "only admins can create keys for other users")
		// The target's roles bound the key: lucius (editor) holds no
		// users.manage. HEAD answered 201 here too (it checked the caller's
		// set) and its request-time intersection left the key without it; M3's
		// rule cuts it at creation, so the key never holds it.
		over := mustCreate(t, r, map[string]any{"name": "for-lucius-admin", "scopes": []string{"users.manage"}, "user_id": luciusID}, bruce, "")
		require.Equal(t, []string{"users.manage"}, over.Scopes, "HEAD answers the scopes asked for")
		me := coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, over.Token)
		require.Equal(t, http.StatusOK, me.Code, me.Body.String())
		require.NotContains(t, me.Body.String(), "users.manage", "a key holds what its owner does not")
		require.NotContains(t, me.Body.String(), `"permissions":[`, "HEAD's intersection left this key no atom; so does M3's rule")
		for _, s := range coreScopes(t, bruce, over.ID) {
			require.NotContains(t, []string{"core:users.manage", "core:admin"}, s)
		}
	})

	t.Run("GET /:id, PATCH and DELETE answer as HEAD", func(t *testing.T) {
		res := mustCreate(t, r, map[string]any{"name": "batcuffs", "scopes": []string{"library.view"}}, lucius, "")
		path := "/api/v1/auth/api-keys/" + res.ID

		rec := coreDo(t, r, http.MethodGet, path, nil, lucius.cookie, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"scopes":["library.view"]`)
		rec = coreDo(t, r, http.MethodGet, "/api/v1/auth/api-keys/"+h.limited.id, nil, lucius.cookie, "")
		require.Equal(t, http.StatusForbidden, rec.Code, "another user's key, not an admin: HEAD's 403: %s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "access denied")
		rec = coreDo(t, r, http.MethodGet, "/api/v1/auth/api-keys/01JBATCAVE0000000000000000", nil, lucius.cookie, "")
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		rec = coreDo(t, r, http.MethodGet, path, nil, bruce.cookie, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"username":"lucius.fox"`, "HEAD names the owner to an admin")

		rec = coreDo(t, r, http.MethodPatch, path, map[string]any{"status": "paused"}, lucius.cookie, "")
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		rec = coreDo(t, r, http.MethodPatch, path, map[string]any{"status": "revoked"}, lucius.cookie, "")
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "use DELETE")
		rec = coreDo(t, r, http.MethodPatch, path, map[string]any{"status": "inactive"}, lucius.cookie, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"status":"inactive"`)
		require.Equal(t, http.StatusUnauthorized, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, res.Token).Code, "an inactive key still authenticates")
		rec = coreDo(t, r, http.MethodPatch, path, map[string]any{"status": "active"}, lucius.cookie, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, http.StatusOK, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, res.Token).Code)

		rec = coreDo(t, r, http.MethodDelete, "/api/v1/auth/api-keys/"+h.limited.id, nil, lucius.cookie, "")
		require.Equal(t, http.StatusForbidden, rec.Code, "a non-admin revoked another user's key: %s", rec.Body.String())
		rec = coreDo(t, r, http.MethodDelete, path, nil, lucius.cookie, "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		require.Equal(t, http.StatusUnauthorized, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, res.Token).Code)
		rec = coreDo(t, r, http.MethodPatch, path, map[string]any{"status": "active"}, lucius.cookie, "")
		require.Equal(t, http.StatusConflict, rec.Code, "a revoked key came back: %s", rec.Body.String())
		listed := v1Keys(t, r, "/api/v1/auth/api-keys", lucius)
		require.Equal(t, "revoked", listed[res.ID].Status)
	})

	t.Run("a key lists, reads and revokes its own user's keys, as on HEAD", func(t *testing.T) {
		caller := mustCreate(t, r, map[string]any{"name": "utility-belt", "scopes": []string{"library.view"}}, lucius, "")
		other := mustCreate(t, r, map[string]any{"name": "spare-belt", "scopes": []string{"library.view"}}, lucius, "")
		rec := coreDo(t, r, http.MethodGet, "/api/v1/auth/api-keys", nil, nil, caller.Token)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), other.ID)
		require.NotContains(t, rec.Body.String(), h.limited.id, "a key listed another user's key")
		rec = coreDo(t, r, http.MethodGet, "/api/v1/auth/api-keys/"+other.ID, nil, nil, caller.Token)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = coreDo(t, r, http.MethodGet, "/api/v1/auth/api-keys/"+h.limited.id, nil, nil, caller.Token)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		rec = coreDo(t, r, http.MethodDelete, "/api/v1/auth/api-keys/"+h.limited.id, nil, nil, caller.Token)
		require.Equal(t, http.StatusForbidden, rec.Code, "a key revoked another user's key: %s", rec.Body.String())
		rec = coreDo(t, r, http.MethodDelete, "/api/v1/auth/api-keys/"+other.ID, nil, nil, caller.Token)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		require.Equal(t, http.StatusUnauthorized, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, other.Token).Code)
		// A second DELETE of the revoked key: HEAD's store revoke is a no-op
		// on a revoked key and the route answers 204 again.
		rec = coreDo(t, r, http.MethodDelete, "/api/v1/auth/api-keys/"+other.ID, nil, nil, caller.Token)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	})

	t.Run("POST /api/v1/auth/bootstrap redeems no token under identity", func(t *testing.T) {
		// Books writes no bootstrap token under identity (server_lifecycle:
		// core's file and core's route, POST /api/core/v1/auth/bootstrap), so
		// HEAD's route answers 401 before it would mint into books' retired
		// key store.
		rec := coreDo(t, r, http.MethodPost, "/api/v1/auth/bootstrap", map[string]any{"token": "batcomputer-override"}, nil, "")
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "invalid bootstrap token")
	})

	t.Run("recorded difference: a key in the plain session_id cookie is not read", func(t *testing.T) {
		// HEAD's RequireAuth read a key from the session_id cookie too; core
		// reads a key only from the Authorization header or the __Host-
		// cookie (core/auth/middleware.go), so the plain cookie no longer
		// carries one.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req.RemoteAddr = "192.0.2.250:41000"
		req.AddCookie(&http.Cookie{Name: "session_id", Value: h.limited.raw})
		rec := httptest.NewRecorder()
		r.app.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
		require.Equal(t, http.StatusOK, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, h.limited.raw).Code, "the same key by Bearer")
	})

	t.Run("rotate answers 410 for a key's own key and refuses another user's", func(t *testing.T) {
		own := mustCreate(t, r, map[string]any{"name": "rotor", "scopes": []string{"library.view"}}, lucius, "")
		rec := coreDo(t, r, http.MethodPost, "/api/v1/auth/api-keys/"+own.ID+"/rotate", nil, nil, own.Token)
		require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
		rec = coreDo(t, r, http.MethodPost, "/api/v1/auth/api-keys/"+h.limited.id+"/rotate", nil, nil, own.Token)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		rec = coreDo(t, r, http.MethodPost, "/api/v1/auth/api-keys/"+own.ID+"/rotate", nil, lucius.cookie, "")
		require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
	})
}

// clampedNoteText is handlers.clampedNote, HEAD's create-response note.
const clampedNoteText = "expiry shortened to the expiry of the API key that made this request: a key cannot mint a key that outlives it (sign in to create a longer-lived one)"

// TestAPIKeyRoutes_OwnerSubset holds a key created on HEAD's route to its
// owner's CURRENT roles: a demotion narrows it on the next request to exactly
// what the owner still holds, a disabled owner leaves it nothing, a confined
// key gets no ambient scope, and (a recorded difference) a promotion does not
// widen it.
func TestAPIKeyRoutes_OwnerSubset(t *testing.T) {
	var h headKeys
	fx := newFlipFixture(t, seedHEAD(t, &h), "")
	r := bootOverHEADStore(t, fx)
	bruce := loginCoreAdmin(t, r, wayne, h.password)
	lucius := loginCoreAdmin(t, r, "lucius.fox", h.password)
	luciusID := userIDByEmail(t, bruce, "lucius.fox@wayne-enterprises.example.test")
	setRoles := func(roles ...string) {
		t.Helper()
		rec := coreDo(t, r, http.MethodPut, "/api/core/v1/users/"+luciusID+"/roles", map[string]any{"roles": roles}, bruce.cookie, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	sessionPerms := func() []string {
		t.Helper()
		rec := coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, lucius.cookie, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Data struct {
				User struct {
					Permissions []string `json:"permissions"`
				} `json:"user"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Data.User.Permissions
	}

	empty := mustCreate(t, r, map[string]any{"name": "grapnel-all"}, lucius, "")
	named := mustCreate(t, r, map[string]any{"name": "grapnel-edit", "scopes": []string{"library.view", "library.edit_metadata"}}, lucius, "")
	for _, k := range []v1Created{empty, named} {
		require.Contains(t, v1Permissions(t, r, k.Token), "library.edit_metadata")
	}

	t.Run("a demotion takes the permission on the next request, down to exactly the owner's set", func(t *testing.T) {
		setRoles("viewer")
		owner := sessionPerms()
		for name, k := range map[string]v1Created{"empty-scope": empty, "named": named} {
			perms := v1Permissions(t, r, k.Token)
			require.NotContainsf(t, perms, "library.edit_metadata", "%s: the demoted owner's key still edits", name)
			for _, p := range perms {
				require.Containsf(t, owner, p, "%s: the key holds %s, which its owner no longer holds (%v)", name, p, owner)
			}
		}
		require.ElementsMatch(t, owner, v1Permissions(t, r, empty.Token), "the empty-scope key is not the owner's set after the demotion")
		// The same, in core's scopes, where the ambient ones show: no key
		// scope, ambient included, outside the demoted owner's own set.
		ownerScopes := coreMePerms(t, r, lucius.cookie, "")
		for name, k := range map[string]v1Created{"empty-scope": empty, "named": named} {
			for _, s := range coreMePerms(t, r, nil, k.Token) {
				require.Containsf(t, ownerScopes, s, "%s: the key holds core scope %s outside its demoted owner's set %v", name, s, ownerScopes)
			}
		}
	})

	t.Run("a promotion does not widen a key made before it (recorded difference: HEAD's empty-scope key followed the owner up)", func(t *testing.T) {
		setRoles("viewer")
		before := mustCreate(t, r, map[string]any{"name": "pre-promotion"}, lucius, "")
		setRoles("editor")
		require.NotContains(t, v1Permissions(t, r, before.Token), "library.edit_metadata",
			"a key's scopes are fixed at creation (05-rbac §6.3); HEAD's dynamic empty-scope key is not reproduced")
	})

	t.Run("a confined key gets no ambient scope", func(t *testing.T) {
		res := mustCreate(t, r, map[string]any{"name": "riddler-worker", "scopes": []string{"fingerprint.worker"}}, bruce, "")
		scopes := coreScopes(t, bruce, res.ID)
		require.NotEmpty(t, scopes)
		for _, s := range ambientScopes() {
			require.NotContains(t, scopes, string(s), "a confined key was given ambient %s: %v", s, scopes)
		}
	})

	t.Run("a disabled owner leaves the key nothing", func(t *testing.T) {
		setRoles("editor")
		require.Equal(t, http.StatusOK, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, named.Token).Code)
		rec := coreDo(t, r, http.MethodPost, "/api/core/v1/users/"+luciusID+"/disable", nil, bruce.cookie, "")
		require.Less(t, rec.Code, 300, rec.Body.String())
		for name, k := range map[string]v1Created{"empty-scope": empty, "named": named} {
			require.Equalf(t, http.StatusUnauthorized, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, k.Token).Code, "%s on /api/v1", name)
			code, _ := keyCoreMe(t, r, k.Token)
			require.Equalf(t, http.StatusUnauthorized, code, "%s on core", name)
		}
	})
}

// TestAPIKeyRoutes_AuthOff pins the seven key routes with authentication off
// (no user yet). Every one refuses the auth-disabled principal with 403, never
// 401, so a no-auth install's SPA is not signed out (F4-08 ruling G5(a);
// 09-abo-on-core §9.2.6). The four reads and DELETE reach the handler, whose
// no-user refusal (legacyauth.RespondNoUser) answers 403 FORBIDDEN with
// "turn authentication on to use this route" byte for byte; HEAD answered 401
// there. POST /api-keys, PATCH /api-keys/:id and POST /api-keys/:id/rotate
// are refused with 403 before the handler: auth-off tightening, SEC5-8
// (05-rbac §4.11(a), 05-authn §4.11, the R2 release note) — HEAD answered 401,
// 401 and 410 there. None of the seven codes ends the shell's session.
func TestAPIKeyRoutes_AuthOff(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	r := runComposedWith(t, nil, nil, nil, nil)
	// G5(a): the handler's no-user refusal of the auth-disabled principal; HEAD
	// answered {"error":"authentication required","code":"UNAUTHORIZED","status":401}.
	const authOff403 = `{"error":"turn authentication on to use this route","code":"FORBIDDEN","status":403}`
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		exact, code  string
	}{
		{http.MethodGet, "/api/v1/auth/api-keys", nil, http.StatusForbidden, authOff403, ""},
		{http.MethodGet, "/api/v1/auth/api-keys?all=true", nil, http.StatusForbidden, authOff403, ""},
		{http.MethodGet, "/api/v1/auth/api-keys/01JBATCAVE0000000000000000", nil, http.StatusForbidden, authOff403, ""},
		{http.MethodDelete, "/api/v1/auth/api-keys/01JBATCAVE0000000000000000", nil, http.StatusForbidden, authOff403, ""},
		// auth-off tightening, SEC5-8: HEAD 401.
		{http.MethodPost, "/api/v1/auth/api-keys", map[string]any{"name": "x", "scopes": []string{"library.view"}}, http.StatusForbidden, "", "FORBIDDEN"},
		// auth-off tightening, SEC5-8: HEAD 401.
		{http.MethodPatch, "/api/v1/auth/api-keys/01JBATCAVE0000000000000000", map[string]any{"status": "inactive"}, http.StatusForbidden, "", "interactive_required"},
		// auth-off tightening, SEC5-8: HEAD 410.
		{http.MethodPost, "/api/v1/auth/api-keys/01JBATCAVE0000000000000000/rotate", nil, http.StatusForbidden, "", "FORBIDDEN"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := coreDo(t, r, c.method, c.path, c.body, nil, "")
			require.Equal(t, c.status, rec.Code, rec.Body.String())
			if c.exact != "" {
				require.JSONEq(t, c.exact, rec.Body.String())
			}
			if c.code != "" {
				require.Contains(t, rec.Body.String(), `"`+c.code+`"`)
			}
			for _, ending := range []string{"authentication_required", "expired", "revoked", "inactive_user", "invalid_credential"} {
				require.NotContains(t, rec.Body.String(), `"`+ending+`"`, "a session-ending code (api-client client.ts)")
			}
		})
	}
}

// TestAPIKeyRoutes_AnAccessSignedInPersonCreatesKeys: HEAD let a Cloudflare
// Access caller (a method that may change credentials) create its own keys
// unclamped; under identity it still does, through core.
func TestAPIKeyRoutes_AnAccessSignedInPersonCreatesKeys(t *testing.T) {
	r := bootAccess(t, newFakeAccessVerifier(), nil)
	code, _, body := absMe(t, r, accessAlfred)
	require.Equal(t, http.StatusOK, code, body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/api-keys", strings.NewReader(`{"name":"alfred-tray","scopes":["library.view"]}`))
	req.RemoteAddr = "192.0.2.11:41000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(cfaccess.CFAccessHeader, accessAlfred)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var env struct {
		Data v1Created `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.True(t, strings.HasPrefix(env.Data.Token, "mck_"))
	require.Empty(t, env.Data.Note, "an Access sign-in is not clamped")
	require.Equal(t, http.StatusOK, coreDo(t, r, http.MethodGet, "/api/v1/auth/me", nil, nil, env.Data.Token).Code)
}
