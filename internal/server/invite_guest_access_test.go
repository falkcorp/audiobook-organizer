// file: internal/server/invite_guest_access_test.go
// version: 1.0.0
// guid: 7a4c2e91-5d38-4b06-8f1a-c9e3b6d2a457
// last-edited: 2026-10-19

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// fakeGuestPolicy records EnsureEmail calls.
type fakeGuestPolicy struct {
	mu     sync.Mutex
	emails []string
	res    cfaccesspolicy.Result
	err    error
}

func (f *fakeGuestPolicy) EnsureEmail(_ context.Context, email string) (cfaccesspolicy.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emails = append(f.emails, email)
	return f.res, f.err
}

func acceptInvite(t *testing.T, srv *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "password": "correct-horse-1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/accept-invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

func createEmailInvite(t *testing.T, store database.Store, token, username, email string) {
	t.Helper()
	if _, err := store.CreateInvite(&database.Invite{Token: token, Username: username, Email: email, RoleID: "viewer", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptInvite_SetsEmailGrantsSSOAndSyncsGuestPolicy(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("a", 64)
	createEmailInvite(t, store, token, "guest", "Guest@Example.com")

	w := acceptInvite(t, srv, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	srv.bgWG.Wait()

	u, _ := store.GetUserByUsername("guest")
	if u == nil || u.Email != "guest@example.com" {
		t.Fatalf("user email not set from the invite: %+v", u)
	}
	allowed := inviteGrantAllowed(store)
	if allowed == nil || !allowed("GUEST@example.com") {
		t.Fatal("accepted invite email must be allowed for SSO")
	}
	if allowed("someone-else@example.com") {
		t.Fatal("an email nobody accepted an invite with must not be allowed")
	}
	cfg := oauth.New(oauth.Config{ExtraAllowed: allowed})
	if !cfg.IsEmailAllowed("guest@example.com") {
		t.Fatal("oauth.Config must consult ExtraAllowed")
	}

	if len(fake.emails) != 1 || fake.emails[0] != "guest@example.com" {
		t.Fatalf("guest policy calls = %v", fake.emails)
	}
	inv, _ := store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyAdded {
		t.Fatalf("guest policy outcome = %q", inv.GuestPolicyStatus)
	}

	// Second accept of the same token: refused, no second sync.
	if w := acceptInvite(t, srv, token); w.Code == http.StatusCreated {
		t.Fatal("an invite must be single-use")
	}
	srv.bgWG.Wait()
	if len(fake.emails) != 1 {
		t.Fatal("a refused accept must not sync")
	}

	// Deactivation ends the SSO grant.
	if creds, ok := database.AsCapability[database.UserCredentialWriter](store); ok {
		if _, err := creds.SetUserStatus(u.ID, "locked"); err != nil {
			t.Fatal(err)
		}
		if allowed("guest@example.com") {
			t.Fatal("a locked user's grant must not admit SSO")
		}
	} else {
		t.Fatal("store lacks UserCredentialWriter")
	}
}

func TestAcceptInvite_GuestPolicyFailureNeverFailsAccept(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	srv.guestPolicyOverride = &fakeGuestPolicy{err: errors.New("cfaccesspolicy: save policy: HTTP 403: 10000 Authentication error")}
	token := strings.Repeat("b", 64)
	createEmailInvite(t, store, token, "guest2", "guest2@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept must succeed when Cloudflare fails: %d %s", w.Code, w.Body.String())
	}
	srv.bgWG.Wait()
	inv, _ := store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyFailed || !strings.Contains(inv.GuestPolicyDetail, "403") {
		t.Fatalf("failure not recorded for the admin: %+v", inv)
	}
}

func TestAcceptInvite_GuestPolicyUnconfiguredIsRecorded(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	saved := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
	config.Mutate(func(c *config.Config) { c.CFAccountID, c.CFGuestsPolicyID, c.CFPolicyAPIToken = "", "", "" })
	token := strings.Repeat("c", 64)
	createEmailInvite(t, store, token, "guest3", "guest3@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	srv.bgWG.Wait()
	inv, _ := store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyNotConfigured {
		t.Fatalf("status = %q, want not_configured", inv.GuestPolicyStatus)
	}
}

func TestAcceptInvite_NoEmailSkipsGuestPolicy(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("d", 64)
	createEmailInvite(t, store, token, "plain", "")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	srv.bgWG.Wait()
	if len(fake.emails) != 0 {
		t.Fatal("an invite with no email must not touch Cloudflare")
	}
}

func TestPublicInviteRoute_IsRateLimited(t *testing.T) {
	srv, _ := setupUserHandlerServer(t)
	limited := false
	for range inviteAuthRateBurst + 5 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/invite/"+strings.Repeat("f", 64), nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		if w.Code != http.StatusNotFound {
			t.Fatalf("unknown token: %d %s", w.Code, w.Body.String())
		}
	}
	if !limited {
		t.Fatal("public invite lookup is not rate-limited")
	}
}

// TestConfigGET_NeverReturnsInviteSecrets drives the real GET /api/v1/config.
func TestConfigGET_NeverReturnsInviteSecrets(t *testing.T) {
	srv, _ := setupUserHandlerServer(t)
	saved := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
	secrets := []string{"smtp-pw-SENTINEL-41", "guest-secret-SENTINEL-42", "guest-id-SENTINEL-43.access", "cf-token-SENTINEL-44", "mailer-SENTINEL@example.com"}
	config.Mutate(func(c *config.Config) {
		c.SMTPHost, c.SMTPFrom = "127.0.0.1", "books@example.com"
		c.SMTPPassword, c.InviteCFAccessClientSecret, c.InviteCFAccessClientID, c.CFPolicyAPIToken, c.SMTPUsername =
			secrets[0], secrets[1], secrets[2], secrets[3], secrets[4]
	})
	for _, path := range []string{"/api/v1/config", "/api/v1/users/invites"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		for _, s := range secrets {
			if strings.Contains(w.Body.String(), s) {
				t.Fatalf("%s leaked an invite secret", path)
			}
		}
	}
}
