// file: internal/server/handlers/abs/authorize_apikey_test.go
// version: 1.0.0
// guid: 9a3d6e12-7c48-4b5f-b0e9-2d81f4c6a573
// last-edited: 2026-10-07

package abs_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// keyStore is the ABS fake store plus API-key lookup.
type keyStore struct {
	*fakeStore
	keys map[string]*database.APIKey
}

func (k *keyStore) GetAPIKeyByHash(hash string) (*database.APIKey, error) { return k.keys[hash], nil }

// TestAuthorize_APIKeyIsEchoedNotExchanged: /api/authorize called with an
// "abk_" key used to mint an ABS access token, turning the key into a second
// bearer credential with its own lifetime. It now echoes the key back.
func TestAuthorize_APIKeyIsEchoedNotExchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: testSecret, AuthModes: "cf,jwt"})
	if err != nil {
		t.Fatal(err)
	}
	store := &keyStore{fakeStore: newFakeStore(), keys: map[string]*database.APIKey{}}
	store.addUser(&database.User{ID: "u1", Username: "owner", Status: "active", Roles: []string{"admin"}})
	const raw = "abk_synthetic_key"
	exp := time.Now().Add(time.Hour)
	store.keys[database.HashAPIKeyToken(raw)] = &database.APIKey{ID: "k1", UserID: "u1", Status: "active", ExpiresAt: &exp}

	verifier := &fakeVerifier{byToken: map[string]*oauth.IdentityClaims{}}
	resolver := servermiddleware.NewABSIdentityResolver(cfg, verifier, oauth.New(oauth.Config{DefaultRole: "viewer"}), store)
	h, err := abshandler.New(abshandler.Options{Config: cfg, Store: store, Resolver: resolver,
		UserData: &fakeUserData{progress: []any{}, bookmarks: []any{}}})
	if err != nil {
		t.Fatal(err)
	}
	h.SetSleep(func(time.Duration) {})
	t.Cleanup(func() { abshandler.WaitCacheRefreshes(h) })
	r := gin.New()
	h.Register(r)
	hs := &harness{router: r}

	w, body := hs.do(t, request{method: http.MethodPost, path: "/api/authorize",
		headers: map[string]string{"Authorization": "Bearer " + raw}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	tok := str(t, userObj(t, body), "accessToken")
	if tok != raw {
		prefix := tok
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		t.Fatalf("accessToken = %q..., want the presented key echoed; an API key must not be exchanged for a new token", prefix)
	}
	if strings.Count(tok, ".") == 2 {
		t.Fatal("a JWT was minted for an API key")
	}
}
