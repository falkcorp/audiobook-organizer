// file: internal/books/cutover/bootstrap_redeem_test.go
// version: 0.1.0
// guid: 7e3b5a18-4c2d-4f90-a6b1-d85e0c9f2a47
// last-edited: 2026-11-22

package cutover_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/auth"
)

// The flipped build's bootstrap (F4-08 B10bs). The operator reads the token from
// <state dir>/.bootstrap-token and redeems it at core's route; the books route
// keeps its path and answers 401, and nothing of HEAD's stored token (a hash and an
// expiry in books settings, left by a HEAD boot) can make that route act.
//
// Expiry past core's ten minutes is not proved here: the boot's clock reaches core
// only through core's internal bootseam, and the token's lifetime is a constant.
// Core's own clock-driven tests pin the refusal (core/auth.TestBootstrap_
// ExpiredTokenIsSpentAndRefused); this file pins that ABO's boot arms the
// ten-minute expiry they exercise, from the boot's own log line.

const (
	bootstrapCoreRoute  = "/api/core/v1/auth/bootstrap"
	bootstrapBooksRoute = "/api/v1/auth/bootstrap"
	bootstrapTokenFile  = ".bootstrap-token"
)

// booksRouteRefusal is the body HEAD's handler gave an unmatched token and the
// flipped build gives every token.
const booksRouteRefusal = `{"error":"invalid bootstrap token","code":"UNAUTHORIZED","status":401}`

func bsPostJSON(t *testing.T, h *Registered, route string, body any) Result {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	// Raw gives each request its own address: a failed redeem is charged to its
	// source, so a cell that reuses one would measure the throttle.
	return h.Raw(req)
}

// readBootstrapToken is the operator's step: the token file in the state directory.
func readBootstrapToken(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(config.SecureStateDir(), bootstrapTokenFile))
	if err != nil {
		t.Fatalf("the flip boot wrote no %s: %v", bootstrapTokenFile, err)
	}
	return strings.TrimSpace(string(b))
}

type bsUserPage struct {
	Items []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"items"`
}

func bsListCoreUsers(t *testing.T, h *Registered) bsUserPage {
	t.Helper()
	var page bsUserPage
	h.coreJSON(h.Principal("session:admin"), http.MethodGet, "/api/core/v1/users?limit=100", nil, &page)
	return page
}

// TestBootstrapToken_RedeemsOnceAtCoreAndNotAtBooks: the token the flip boot wrote
// redeems at core's route to a key that authenticates, and only once; the books
// route is refused whatever it is sent.
func TestBootstrapToken_RedeemsOnceAtCoreAndNotAtBooks(t *testing.T) {
	before := time.Now()
	h := bootRegistered(t, registeredConfig{})
	after := time.Now()

	// ABO's boot arms core's ten-minute expiry: the line core logs when it writes
	// the file carries it. The log's second resolution is why the window is wide.
	m := regexp.MustCompile(`bootstrap token written.*?expires_at=(\S+)`).FindStringSubmatch(h.Logs())
	if m == nil {
		t.Fatalf("the boot logged no bootstrap-token line; logs:\n%s", h.Logs())
	}
	expiry, err := time.Parse(time.RFC3339, strings.Trim(m[1], `"`))
	if err != nil {
		t.Fatal(err)
	}
	if expiry.Before(before.Add(auth.DefaultBootstrapTokenTTL).Add(-time.Second)) || expiry.After(after.Add(auth.DefaultBootstrapTokenTTL).Add(time.Second)) {
		t.Errorf("token expiry %s, want boot (%s .. %s) + %s", expiry, before, after, auth.DefaultBootstrapTokenTTL)
	}

	token := readBootstrapToken(t)
	usersBefore := bsListCoreUsers(t, h)

	// The books route refuses the very token core is about to accept.
	res := bsPostJSON(t, h, bootstrapBooksRoute, map[string]string{"token": token, "key_name": "workspace-key"})
	if res.Status != http.StatusUnauthorized || strings.TrimSpace(string(res.Body)) != booksRouteRefusal {
		t.Errorf("books route: %d %s, want 401 %s", res.Status, res.excerpt(), booksRouteRefusal)
	}
	t.Logf("books route: %d %s", res.Status, strings.TrimSpace(string(res.Body)))

	res = bsPostJSON(t, h, bootstrapCoreRoute, map[string]string{"token": token})
	if res.Status != http.StatusOK {
		t.Fatalf("core redeem: %d %s", res.Status, res.excerpt())
	}
	var got struct {
		Key       string    `json:"key"`
		KeyID     string    `json:"key_id"`
		ExpiresAt time.Time `json:"expires_at"`
		User      struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
		GeneratedPassword string `json:"generated_password"`
	}
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Key, "mck_") || got.KeyID == "" || got.User.ID == "" {
		t.Fatalf("core redeem body: %s", res.excerpt())
	}
	if got.GeneratedPassword != "" {
		t.Errorf("the corpus has an admin, yet core generated a password")
	}
	if want := time.Now().Add(auth.DefaultBootstrapKeyTTL); got.ExpiresAt.Before(want.Add(-time.Minute)) || got.ExpiresAt.After(want.Add(time.Second)) {
		t.Errorf("key expires %s, want about %s (now + core.auth.bootstrap.key_ttl, %s)", got.ExpiresAt, want, auth.DefaultBootstrapTokenTTL)
	}
	if _, err := os.Stat(filepath.Join(config.SecureStateDir(), bootstrapTokenFile)); !os.IsNotExist(err) {
		t.Errorf("a redeemed token's file is still there: %v", err)
	}

	// The key authenticates, as a key.
	req := httptest.NewRequest(http.MethodGet, "/api/core/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+got.Key)
	me := h.Raw(req)
	var who struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(me.Body, &who); err != nil || me.Status != http.StatusOK || who.Method != "api_key" {
		t.Fatalf("GET /api/core/v1/me with the bootstrap key: %d %s", me.Status, me.excerpt())
	}

	// The key's own record: name, scopes and lifetime as core mints them.
	var keys struct {
		Items []struct {
			ID        string     `json:"id"`
			Name      string     `json:"name"`
			Scopes    []string   `json:"scopes"`
			CreatedAt time.Time  `json:"created_at"`
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"items"`
	}
	h.coreJSON(h.Principal("session:admin"), http.MethodGet, "/api/core/v1/users/"+got.User.ID+"/api-keys?limit=100", nil, &keys)
	found := false
	for _, k := range keys.Items {
		if k.ID != got.KeyID {
			continue
		}
		found = true
		if k.Name != "bootstrap" {
			t.Errorf("key name %q, want bootstrap", k.Name)
		}
		if len(k.Scopes) != 1 || k.Scopes[0] != "core:admin" {
			t.Errorf("key scopes %v, want [core:admin]", k.Scopes)
		}
		if k.ExpiresAt == nil || k.ExpiresAt.Sub(k.CreatedAt) != auth.DefaultBootstrapKeyTTL || !k.ExpiresAt.Equal(got.ExpiresAt) {
			t.Errorf("key lifetime %v .. %v, want %s and the redeem's expires_at %s", k.CreatedAt, k.ExpiresAt, auth.DefaultBootstrapKeyTTL, got.ExpiresAt)
		}
	}
	if !found {
		t.Fatalf("core lists no key %s for %s: %+v", got.KeyID, got.User.Username, keys.Items)
	}
	if usersAfter := bsListCoreUsers(t, h); len(usersAfter.Items) != len(usersBefore.Items) {
		t.Errorf("a redeem against a corpus with an admin created a user: %d -> %d", len(usersBefore.Items), len(usersAfter.Items))
	}

	// Single use: the same token is refused, with core's own problem document.
	again := bsPostJSON(t, h, bootstrapCoreRoute, map[string]string{"token": token})
	h.Refused(again, http.StatusUnauthorized, "bootstrap_invalid")
	t.Logf("second redeem: %d %s", again.Status, again.excerpt())
}

// TestBootstrapBooksRoute_AFlippedStoreHoldsNoHeadTokenAndTheRouteStaysRefused: HEAD
// kept its bootstrap token as a hash and an expiry in books settings. The flip
// boot's M3 deletes both rows (m3 head.go bootstrapHashKey, step.go) and the claim
// is retired, so the pair cannot be put back (the store refuses the write): the
// route has nothing to honour. The cell pins that, and that a HEAD-shaped token and
// an empty body both get the exact 401 and leave core's token file, core's users and
// books' users alone. The handler-level refusal that does not rely on M3's deletion
// is internal/server TestHandleBootstrap_UnderIdentityRefusesBeforeReadingAnything.
func TestBootstrapBooksRoute_AFlippedStoreHoldsNoHeadTokenAndTheRouteStaysRefused(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	bs := store.GetGlobalStore()
	if bs == nil {
		t.Fatal("the boot left no books store")
	}
	for _, k := range []string{"bootstrap_token_hash", "bootstrap_token_expires_at"} {
		if s, err := bs.GetSetting(k); err == nil && s != nil {
			t.Fatalf("the flip boot left HEAD's %s in books settings", k)
		}
	}
	if err := bs.SetSetting("bootstrap_token_hash", "x", "string", false); err == nil {
		t.Error("books settings accept HEAD's bootstrap hash after the flip: the claim is not retired")
	}
	usersBefore := bsListCoreUsers(t, h)
	booksUsersBefore, err := bs.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	tokenBefore := readBootstrapToken(t)

	const headToken = "abbs_Alfred-Pennyworth-batcave-0123456789abcdef0123456789"
	res := bsPostJSON(t, h, bootstrapBooksRoute, map[string]string{"token": headToken})
	if res.Status != http.StatusUnauthorized || strings.TrimSpace(string(res.Body)) != booksRouteRefusal {
		t.Errorf("books route, a HEAD-shaped token: %d %s, want 401 %s", res.Status, res.excerpt(), booksRouteRefusal)
	}
	empty := h.Raw(httptest.NewRequest(http.MethodPost, bootstrapBooksRoute, nil))
	if empty.Status != http.StatusUnauthorized || strings.TrimSpace(string(empty.Body)) != booksRouteRefusal {
		t.Errorf("books route, no body: %d %s, want 401 %s", empty.Status, empty.excerpt(), booksRouteRefusal)
	}
	if got := readBootstrapToken(t); got != tokenBefore {
		t.Errorf("core's token file changed")
	}
	if usersAfter := bsListCoreUsers(t, h); len(usersAfter.Items) != len(usersBefore.Items) {
		t.Errorf("core users %d -> %d", len(usersBefore.Items), len(usersAfter.Items))
	}
	if after, err := bs.ListUsers(); err != nil || len(after) != len(booksUsersBefore) {
		t.Errorf("books users %d -> %d (%v)", len(booksUsersBefore), len(after), err)
	}
	if ok := bsPostJSON(t, h, bootstrapCoreRoute, map[string]string{"token": tokenBefore}); ok.Status != http.StatusOK {
		t.Errorf("core's own token after the books-route attempts: %d %s", ok.Status, ok.excerpt())
	}
}
