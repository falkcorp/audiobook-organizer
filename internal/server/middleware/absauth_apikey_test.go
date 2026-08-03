// file: internal/server/middleware/absauth_apikey_test.go
// version: 1.0.0
// guid: 5d3b8a71-9e64-4c02-b1f7-8a06d2e93c45
// last-edited: 2026-08-03

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/gin-gonic/gin"
)

// apiKeyStore decorates the shared fakeABSStore with the OPTIONAL API-key slice
// that ResolveAPIKey type-asserts for. Keeping it separate is the whole point of
// the optional-interface design: every other ABS test keeps using a store that
// does NOT implement this, and therefore never resolves an API key.
type apiKeyStore struct {
	*fakeABSStore
	keys map[string]*database.APIKey // token hash -> key
}

func newAPIKeyStore(base *fakeABSStore) *apiKeyStore {
	return &apiKeyStore{fakeABSStore: base, keys: map[string]*database.APIKey{}}
}

func (s *apiKeyStore) GetAPIKeyByHash(hash string) (*database.APIKey, error) {
	return s.keys[hash], nil
}

func (s *apiKeyStore) addKey(raw string, k *database.APIKey) {
	k.TokenHash = database.HashAPIKeyToken(raw)
	s.keys[k.TokenHash] = k
}

// apiKeyHarness mirrors absHarness but wires an API-key-capable store.
type apiKeyHarness struct {
	router   *gin.Engine
	store    *apiKeyStore
	resolver *ABSIdentityResolver
}

func newAPIKeyHarness(t *testing.T) *apiKeyHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: absTestSecret, AuthModes: "cf,jwt"})
	if err != nil {
		t.Fatalf("absauth.Load: %v", err)
	}
	store := newAPIKeyStore(newFakeABSStore())
	verifier := &fakeCFVerifier{byToken: map[string]*oauth.IdentityClaims{}}

	resolver := NewABSIdentityResolver(cfg, verifier, nil, store)
	r := gin.New()
	r.GET("/api/me", ABSRequireAuth(resolver), func(c *gin.Context) {
		u, _ := CurrentUser(c)
		c.JSON(http.StatusOK, gin.H{"user": u.ID, "mode": ABSAuthMode(c)})
	})
	return &apiKeyHarness{router: r, store: store, resolver: resolver}
}

func (h *apiKeyHarness) get(raw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	if raw != "" {
		req.Header.Set("Authorization", "Bearer "+raw)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

// 🔴 TestABSAPIKey_ReachesABSSurface is the regression this change exists for.
//
// Before it, absBearerFromRequest discarded every `abk_` token, so the API key
// minted at startup could reach /api/v1 but got a flat 401 from every ABS route.
// That is what made the ABS surface untestable with the one credential we have,
// and why the author count and cold-cache latency could not be measured after
// PR #2122 deployed.
// It deliberately asserts only BEHAVIOUR — no reference to any symbol added by
// this change — so that reverting absauth.go leaves this test COMPILING and
// failing on the real 401-vs-200 regression, rather than failing to build. A test
// that only fails to compile proves the API is new; it does not prove the
// behaviour changed.
func TestABSAPIKey_ReachesABSSurface(t *testing.T) {
	h := newAPIKeyHarness(t)
	h.store.addUser(&database.User{ID: "u-admin", Username: "admin", Status: "active"})
	h.store.addKey("abk_valid_token", &database.APIKey{ID: "k1", UserID: "u-admin", Status: "active"})

	w := h.get("abk_valid_token")
	if w.Code != http.StatusOK {
		t.Fatalf("valid admin API key rejected by the ABS surface: got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "u-admin") {
		t.Fatalf("expected the key owner to be bound as the current user, got %s", w.Body.String())
	}
}


// A revoked key must not become a back door just because it is pointed at ABS.
func TestABSAPIKey_RevokedRejected(t *testing.T) {
	h := newAPIKeyHarness(t)
	h.store.addUser(&database.User{ID: "u1", Username: "admin", Status: "active"})
	h.store.addKey("abk_revoked", &database.APIKey{ID: "k1", UserID: "u1", Status: "revoked"})

	if w := h.get("abk_revoked"); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key should be 401, got %d", w.Code)
	}
}

// Expiry is what makes this credential short-lived; it must be enforced here too.
func TestABSAPIKey_ExpiredRejected(t *testing.T) {
	h := newAPIKeyHarness(t)
	h.store.addUser(&database.User{ID: "u1", Username: "admin", Status: "active"})
	past := time.Now().Add(-time.Hour)
	h.store.addKey("abk_expired", &database.APIKey{ID: "k1", UserID: "u1", Status: "active", ExpiresAt: &past})

	if w := h.get("abk_expired"); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired key should be 401, got %d", w.Code)
	}
}

// A key whose owner was deactivated must stop working.
//
// 403, not 401, and deliberately so: the CREDENTIAL is valid and was accepted —
// it is the USER who is denied. This reuses absCheckUserActive, so the API-key
// path reports a disabled account exactly the way the CF and JWT paths already
// do, rather than inventing a third answer for the same condition.
func TestABSAPIKey_InactiveOwnerRejected(t *testing.T) {
	h := newAPIKeyHarness(t)
	h.store.addUser(&database.User{ID: "u1", Username: "admin", Status: "disabled"})
	h.store.addKey("abk_ok", &database.APIKey{ID: "k1", UserID: "u1", Status: "active"})

	w := h.get("abk_ok")
	if w.Code != http.StatusForbidden {
		t.Fatalf("key owned by an inactive user should be 403 (credential good, user denied), got %d", w.Code)
	}
	if w.Code == http.StatusOK {
		t.Fatal("an inactive user must never be authenticated")
	}
}

// An unknown key is a plain 401 — never a 500, never a pass.
func TestABSAPIKey_UnknownRejected(t *testing.T) {
	h := newAPIKeyHarness(t)
	if w := h.get("abk_nosuchkey"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key should be 401, got %d", w.Code)
	}
}

// A non-abk_ bearer must NOT be routed down the API-key path. The two credential
// schemes stay strictly separate, so a malformed ABS access token can never be
// reinterpreted as an API-key lookup.
func TestABSAPIKey_NonPrefixedTokenNotTreatedAsKey(t *testing.T) {
	h := newAPIKeyHarness(t)
	h.store.addUser(&database.User{ID: "u1", Username: "admin", Status: "active"})
	// Registered under the hash of a NON-prefixed token: a resolver sloppy about
	// the prefix would find this and wrongly authenticate.
	h.store.addKey("not_an_abk_token", &database.APIKey{ID: "k1", UserID: "u1", Status: "active"})

	if w := h.get("not_an_abk_token"); w.Code != http.StatusUnauthorized {
		t.Fatalf("a non-abk_ bearer must not authenticate via the API-key path, got %d", w.Code)
	}
}

