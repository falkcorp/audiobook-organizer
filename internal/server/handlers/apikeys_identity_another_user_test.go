// file: internal/server/handlers/apikeys_identity_another_user_test.go
// version: 0.1.0
// guid: ca613ed3-ea85-4d96-a063-c40c57fd0c79
// last-edited: 2026-11-22

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// POST /api/v1/auth/api-keys under core identity with the route's credential
// guard out of the way (F4-08 B10rn, STOP 2 ruling, option 2). The handler is
// called directly, so server.credRouteWhen's refusal of a key minting for
// another user never runs. The handler must refuse it itself, with its own
// message, before core is asked for anything. A key minting for itself and an
// admin session minting for another user are unchanged.

const keyForAnotherUser = "API keys cannot create keys for other users; sign in to do that"

func TestAPIKeysIdentity_AKeyCannotCreateAKeyForAnotherUser(t *testing.T) {
	atoms := []string{string(auth.PermLibraryView), string(auth.PermUsersManage)}
	admin := &fakeKeyAdmin{}
	id := withKeys(bridgeIdentity(t, atoms...), admin, fakeKeyScoper{})
	h := handlers.NewAPIKeyHandler(handlersmocks.NewMockAPIKeyHandlerStore(t))
	// create sends the request as u-bruce holding library.view and users.manage
	// (so the handler's own admin check for another user passes) through m.
	create := func(m auth.Method, cm coreauth.Method, body map[string]any) (int, string) {
		c, rec := newAuthCtx(http.MethodPost, "/auth/api-keys", body)
		c.Set("auth_user", &database.User{ID: "u-bruce"})
		ctx := auth.WithPermissions(c.Request.Context(), []auth.Permission{auth.PermLibraryView, auth.PermUsersManage})
		c.Request = c.Request.WithContext(auth.WithMethod(ctx, m))
		bindCoreUser(c, id, cm, bridgeScope(string(auth.PermLibraryView)), bridgeScope(string(auth.PermUsersManage)))
		h.Create(c)
		return rec.Code, rec.Body.String()
	}
	forDick := map[string]any{"name": "batarang", "scopes": []string{"library.view"}, "user_id": "u-dick"}

	t.Run("a key for another user is refused by the handler", func(t *testing.T) {
		for name, cm := range map[string]coreauth.Method{"core key": coreauth.MethodAPIKey, "ABS-surface key": legacyauth.MethodBooksABS} {
			admin.created = nil
			code, body := create(auth.MethodAPIKey, cm, forDick)
			require.Equal(t, http.StatusForbidden, code, "%s: %s", name, body)
			require.Contains(t, body, keyForAnotherUser, name)
			require.Empty(t, admin.created, "%s: core was asked to mint a key for another user", name)
		}
	})

	t.Run("a key for itself is unchanged: core mints it", func(t *testing.T) {
		admin.created = nil
		code, body := create(auth.MethodAPIKey, coreauth.MethodAPIKey, map[string]any{"name": "grapnel", "scopes": []string{"library.view"}})
		require.Equal(t, http.StatusCreated, code, body)
		require.Len(t, admin.created, 1)
		require.Equal(t, coreauth.UserID("u-bruce"), admin.created[0].Owner)
	})

	t.Run("a key naming its own user is not another user", func(t *testing.T) {
		admin.created = nil
		code, body := create(auth.MethodAPIKey, coreauth.MethodAPIKey, map[string]any{"name": "grapnel", "scopes": []string{"library.view"}, "user_id": "u-bruce"})
		require.Equal(t, http.StatusCreated, code, body)
		require.Len(t, admin.created, 1)
	})

	t.Run("an admin session for another user is unchanged: core mints it", func(t *testing.T) {
		admin.created = nil
		code, body := create(auth.MethodSession, coreauth.MethodSession, forDick)
		require.Equal(t, http.StatusCreated, code, body)
		require.Len(t, admin.created, 1)
		require.Equal(t, coreauth.UserID("u-dick"), admin.created[0].Owner)
		require.Equal(t, []rbac.Permission{"core:self", "test:library.view"}, admin.created[0].Scopes)
	})
}
