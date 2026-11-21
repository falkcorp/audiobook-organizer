// file: internal/server/handlers/abs/collections_bridge_sites_test.go
// version: 0.1.0
// guid: 36170c05-abe7-4142-a798-dd93cf3ab687
// last-edited: 2026-11-21

package abs_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// F4-08 B7: canManageCollections asks the bridge under core identity and HEAD's
// auth.Can otherwise. The caller still signs in through HEAD's ABS session (the
// harness has no kernel), so the gin user is bound either way; what differs is
// which permission set judges the write.

// serveWithCore re-serves h's routes with the principal stage 12 would bind
// (holding scopes) and, when id is non-nil, the Identity of a server built
// under core identity.
func (h *harness) serveWithCore(t *testing.T, id *legacyauth.Identity, scopes ...rbac.Permission) {
	t.Helper()
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	if id != nil {
		r.Use(id.Bind())
	}
	r.Use(func(c *gin.Context) {
		bindCore(c, nil, coreauth.MethodSession, scopes...)
		c.Next()
	})
	h.handler.Register(r)
	h.router = r
}

func TestBridgeSites_CanManageCollections(t *testing.T) {
	scope := bridgeScope(string(auth.PermCollectionsManage))
	create := func(t *testing.T, h *harness, tok string) int {
		t.Helper()
		code, _ := h.doAny(t, request{
			method:  http.MethodPost,
			path:    "/api/collections",
			headers: bearer(tok),
			body:    map[string]any{"libraryId": h.libraryID(), "name": "Tasting"},
		})
		return code
	}
	// signIn seeds a user, with the curators role (HEAD's permission set holds
	// collections.manage) when curator, and signs in through HEAD's session.
	signIn := func(t *testing.T, curator bool) (*harness, *abscolFakeStore, string) {
		t.Helper()
		store := &abscolFakeStore{}
		h := newHarness(t, "jwt", nil, withLibrary(seedOracleLibrary(t)), withUserData(fixtureUserData()), withCollections(store))
		h.seedUser(t, "u1", "bruce", "", "pw-pw-pw-pw")
		if curator {
			abscolGrantManage(t, h, "u1")
		}
		return h, store, str(t, userObj(t, h.login(t, "bruce", "pw-pw-pw-pw")), "accessToken")
	}

	t.Run("legacy: HEAD's permission set holds it", func(t *testing.T) {
		h, store, tok := signIn(t, true)
		h.serveWithCore(t, nil)
		if code := create(t, h, tok); code != http.StatusOK || store.creates != 1 {
			t.Fatalf("create = %d (creates %d), want 200 and one create", code, store.creates)
		}
	})
	t.Run("legacy: a core principal holding the scope changes nothing", func(t *testing.T) {
		h, store, tok := signIn(t, false)
		h.serveWithCore(t, nil, scope)
		if code := create(t, h, tok); code != http.StatusForbidden || store.creates != 0 {
			t.Fatalf("create = %d (creates %d), want 403 and none", code, store.creates)
		}
	})
	t.Run("core identity: the principal holds the scope", func(t *testing.T) {
		h, store, tok := signIn(t, false)
		h.serveWithCore(t, bridgeIdentityFor(t, h, string(auth.PermCollectionsManage)), scope)
		if code := create(t, h, tok); code != http.StatusOK || store.creates != 1 {
			t.Fatalf("create = %d (creates %d), want 200 and one create", code, store.creates)
		}
	})
	t.Run("core identity: HEAD's permission set is not read", func(t *testing.T) {
		h, store, tok := signIn(t, true)
		h.serveWithCore(t, bridgeIdentityFor(t, h, string(auth.PermCollectionsManage)))
		if code := create(t, h, tok); code != http.StatusForbidden || store.creates != 0 {
			t.Fatalf("create = %d (creates %d), want 403 and none", code, store.creates)
		}
	})
}
