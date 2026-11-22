// file: internal/server/handlers/apikeys_identity_test.go
// version: 0.1.0
// guid: 0d7e3b52-c1f9-4a86-9b4e-6f28a5c3d917
// last-edited: 2026-11-22

package handlers_test

import (
	"context"
	"iter"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// The key routes under core identity at handler level (F4-08 B10z); the
// end-to-end cells are internal/books' identity_apikey_routes_boot_test.go.

// fakeKeyAdmin is core's auth.Admin with the key methods these tests reach;
// any other method is the nil embedded interface and would panic.
type fakeKeyAdmin struct {
	coreauth.Admin
	all     *bool
	created []coreauth.KeySpec
}

func (f *fakeKeyAdmin) AdminAPIKeys(_ context.Context, _ *coreauth.Principal, all bool) iter.Seq2[*coreauth.APIKey, error] {
	f.all = &all
	return func(func(*coreauth.APIKey, error) bool) {}
}

func (f *fakeKeyAdmin) KeyOwnerScopes(context.Context, *coreauth.Principal, coreauth.UserID) ([]rbac.Permission, error) {
	return []rbac.Permission{"test:library.view"}, nil
}

func (f *fakeKeyAdmin) AdminCreateAPIKey(_ context.Context, _ *coreauth.Principal, spec coreauth.KeySpec) (string, *coreauth.APIKey, error) {
	f.created = append(f.created, spec)
	return "mck_" + "fixture-not-a-key", &coreauth.APIKey{ID: "k-1", Owner: spec.Owner, Name: spec.Name, Scopes: spec.Scopes, ExpiresAt: spec.ExpiresAt}, nil
}

// fakeKeyScoper maps every atom to its test scope.
type fakeKeyScoper struct{}

func (fakeKeyScoper) Scopes(atoms []string, _ []rbac.Permission) ([]rbac.Permission, []string, error) {
	out := []rbac.Permission{"core:self"}
	for _, a := range atoms {
		out = append(out, bridgeScope(a))
	}
	return out, nil, nil
}

func (fakeKeyScoper) Atoms([]rbac.Permission) []string { return []string{} }

func TestAPIKeysIdentity_AServerWithoutTheKeyRuleRefuses503(t *testing.T) {
	for name, id := range map[string]*legacyauth.Identity{
		"no key rule": withKeys(bridgeIdentity(t, string(auth.PermLibraryView)), &fakeKeyAdmin{}, nil),
		"no admin":    withKeys(bridgeIdentity(t, string(auth.PermLibraryView)), nil, fakeKeyScoper{}),
	} {
		t.Run(name, func(t *testing.T) {
			store := handlersmocks.NewMockAPIKeyHandlerStore(t)
			h := handlers.NewAPIKeyHandler(store)

			c, rec := newAuthCtx(http.MethodPost, "/auth/api-keys", map[string]any{"name": "batarang", "scopes": []string{"library.view"}})
			setAuthUser(c, &database.User{ID: "u-bruce"})
			c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), []auth.Permission{auth.PermLibraryView}))
			bindCoreUser(c, id, coreauth.MethodSession, bridgeScope(string(auth.PermLibraryView)))
			h.Create(c)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())

			c, rec = newAuthCtx(http.MethodGet, "/auth/api-keys", nil)
			setAuthUser(c, &database.User{ID: "u-bruce"})
			bindCoreUser(c, id, coreauth.MethodSession)
			h.List(c)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		})
	}
}

// HEAD's clampToCallingKey refused a caller whose method may not change
// credentials and that is not a key (nothing to clamp to). Under identity the
// same caller is refused before core is asked for anything.
func TestAPIKeysIdentity_AMethodWithNothingToClampToIsRefused(t *testing.T) {
	admin := &fakeKeyAdmin{}
	id := withKeys(bridgeIdentity(t, string(auth.PermLibraryView)), admin, fakeKeyScoper{})
	h := handlers.NewAPIKeyHandler(handlersmocks.NewMockAPIKeyHandlerStore(t))
	create := func(m auth.Method, cm coreauth.Method) (int, string) {
		c, rec := newAuthCtx(http.MethodPost, "/auth/api-keys", map[string]any{"name": "batarang", "scopes": []string{"library.view"}})
		c.Set("auth_user", &database.User{ID: "u-bruce"})
		ctx := auth.WithPermissions(c.Request.Context(), []auth.Permission{auth.PermLibraryView})
		c.Request = c.Request.WithContext(auth.WithMethod(ctx, m))
		bindCoreUser(c, id, cm, bridgeScope(string(auth.PermLibraryView)))
		h.Create(c)
		return rec.Code, rec.Body.String()
	}
	code, body := create(auth.MethodABS, legacyauth.MethodBooksABS)
	require.Equal(t, http.StatusForbidden, code, body)
	require.Contains(t, body, "no expiry to limit a new key to")
	require.Empty(t, admin.created, "core was asked to mint for a method HEAD refused")

	code, body = create(auth.MethodSession, coreauth.MethodSession)
	require.Equal(t, http.StatusCreated, code, body)
	require.Len(t, admin.created, 1)
	require.Equal(t, []rbac.Permission{"core:self", "test:library.view"}, admin.created[0].Scopes, "the key rule's scopes are what core mints")
}

// withKeys returns id with Admin and Keys set.
func withKeys(id *legacyauth.Identity, admin coreauth.Admin, keys legacyauth.KeyScoper) *legacyauth.Identity {
	id.Keys = keys
	if admin != nil {
		id.Admin = admin
	}
	return id
}

// bindCoreUser is bindCore with a user on the principal, as stage 12 binds a
// signed-in caller.
func bindCoreUser(c *gin.Context, id *legacyauth.Identity, m coreauth.Method, scopes ...rbac.Permission) {
	bindCore(c, id, m, scopes...)
	p, _ := coreauth.PrincipalFrom(c.Request.Context())
	p.User = &coreauth.User{ID: "u-bruce", Username: "bruce", Status: coreauth.StatusActive}
}
