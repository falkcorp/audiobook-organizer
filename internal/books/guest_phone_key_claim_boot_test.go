// file: internal/books/guest_phone_key_claim_boot_test.go
// version: 0.1.0
// guid: 5d2f7c84-1b96-4e30-a8d7-6c0e93b1f245
// last-edited: 2026-11-22

package books

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// The guest's phone-app key under core identity, through the real composition
// (F4-08 B10s). The accept goes through CORE's route (core consumes the invite,
// signs the guest in, and its response carries no books secret); the claim is
// then one call to books' POST /api/v1/auth/phone-key/claim with that session.
//
// Cloudflare is faked below the clients, at http.DefaultTransport (both clients
// build a nil-Transport http.Client), so the production mint code runs unchanged
// and no test seam exists in production code.

const (
	claimAccountID = "0123456789abcdef0123456789abcdef"
	claimPolicyID  = "aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb"
	claimTokenID   = "cccccccc-1111-2222-3333-dddddddddddd"
	claimClientID  = "robin-client-id.access"
	claimSecret    = "SENTINEL-boot-claim-secret-0123456789abcdef"
	claimGuestPass = "Gotham-Robin-Passw0rd-9931"
)

// cloudflareFake serves the two Cloudflare endpoints the mint uses: the service
// token create and the (stateful) Access policy, with the GET-PUT-GET verify.
type cloudflareFake struct {
	mu      sync.Mutex
	tokens  int
	include []any
	next    http.RoundTripper
}

func (f *cloudflareFake) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.cloudflare.com" {
		return f.next.RoundTrip(r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(code int, result any) (*http.Response, error) {
		raw, _ := json.Marshal(map[string]any{"success": code/100 == 2, "errors": []any{}, "result": result})
		return &http.Response{
			StatusCode: code, Status: http.StatusText(code), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r,
		}, nil
	}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access/service_tokens"):
		f.tokens++
		return reply(http.StatusOK, map[string]any{
			"id": claimTokenID, "client_id": claimClientID, "client_secret": claimSecret, "name": "aorg-guest-x",
		})
	case strings.Contains(r.URL.Path, "/access/policies/"+claimPolicyID):
		if r.Method == http.MethodPut {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return reply(http.StatusBadRequest, nil)
			}
			f.include, _ = body["include"].([]any)
		}
		inc := f.include
		if inc == nil {
			inc = []any{map[string]any{"email": map[string]any{"email": "bruce@wayne.example"}}}
		}
		return reply(http.StatusOK, map[string]any{"id": claimPolicyID, "name": "books", "decision": "non_identity", "include": inc})
	}
	return reply(http.StatusNotFound, nil)
}

func (f *cloudflareFake) minted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens
}

func installCloudflareFake(t *testing.T) *cloudflareFake {
	t.Helper()
	f := &cloudflareFake{next: http.DefaultTransport}
	orig := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = orig })
	return f
}

func bootGuestKeyApp(t *testing.T) (*runningApp, *cloudflareFake) {
	t.Helper()
	t.Cleanup(SetIdentityFlipForTest(true))
	// The Cloudflare settings are operator-only: their layer is the environment.
	t.Setenv("ABO_BOOKS_CFGUESTS_ACCOUNT_ID", claimAccountID)
	t.Setenv("ABO_BOOKS_CFGUESTS_POLICY_API_TOKEN", "Fox-policy-token-0902")
	t.Setenv("ABO_BOOKS_CFGUESTS_GUESTS_POLICY_ID", "eeeeeeee-1111-2222-3333-ffffffffffff")
	t.Setenv("ABO_BOOKS_CFGUESTS_SERVICE_AUTH_POLICY_ID", claimPolicyID)
	t.Setenv("ABO_BOOKS_CFGUESTS_GUEST_SERVICE_TOKENS", "true")
	r := runComposedWith(t, func(c *config.Config) {
		c.EnableAuth = true
		c.CFAccountID = claimAccountID
		c.CFPolicyAPIToken = "Fox-policy-token-0902"
		c.CFGuestsPolicyID = "eeeeeeee-1111-2222-3333-ffffffffffff"
		c.CFServiceAuthPolicyID = claimPolicyID
		c.CFGuestServiceTokens = true
	}, nil, nil, nil)
	// After the boot, not before: Register clones http.DefaultTransport as an
	// *http.Transport and panics on any other RoundTripper.
	return r, installCloudflareFake(t)
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any, ck *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	if ck != nil {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, ck := range rec.Result().Cookies() {
		if strings.HasSuffix(ck.Name, "session_id") && ck.Value != "" {
			return ck
		}
	}
	t.Fatalf("no session cookie: %v", rec.Result().Header["Set-Cookie"])
	return nil
}

// inviteAndAccept has the admin invite robin (an e-mailed admin invitation is
// what the hook would flag) and robin accept it through core's route. It returns
// robin's session cookie and the accepted user's id.
func inviteAndAccept(t *testing.T, r *runningApp, username, email string) (guest *http.Cookie, guestID string, admin *http.Cookie) {
	t.Helper()
	h := r.app.Handler()
	admin = bootstrapAdmin(t, r)
	rec := doJSON(t, h, http.MethodPost, "/api/core/v1/users/invites",
		map[string]string{"username": username, "email": email, "role": "viewer"}, admin)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Link string `json:"link"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created), rec.Body.String())
	_, token, ok := strings.Cut(created.Link, "#token=")
	require.True(t, ok, "invite link has no token fragment: %q", created.Link)

	acc := doJSON(t, h, http.MethodPost, "/api/core/v1/auth/invites/accept",
		map[string]string{"token": token, "password": claimGuestPass}, nil)
	require.Equal(t, http.StatusOK, acc.Code, acc.Body.String())
	// Core's accept answers with a session and the account; it carries no books
	// secret (it cannot: it is core's response).
	assert.NotContains(t, acc.Body.String(), claimSecret)
	assert.NotContains(t, acc.Body.String(), "client_secret")
	var opened struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(acc.Body.Bytes(), &opened), acc.Body.String())
	require.NotEmpty(t, opened.User.ID, acc.Body.String())
	return sessionCookie(t, acc), opened.User.ID, admin
}

func claimKey(t *testing.T, r *runningApp, ck *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, r.app.Handler(), http.MethodPost, "/api/v1/auth/phone-key/claim", nil, ck)
}

func TestIdentityBoot_GuestClaimsThePhoneKeyOnceThroughCoresAccept(t *testing.T) {
	r, cf := bootGuestKeyApp(t)
	guest, guestID, _ := inviteAndAccept(t, r, "robin", "robin@wayne.example")
	srv := r.mod.srv.Load()
	require.NotNil(t, srv)

	// No entitlement was offered (the hook is S2): production refuses every claim.
	refused := claimKey(t, r, guest)
	require.Equal(t, http.StatusNotFound, refused.Code, refused.Body.String())
	assert.Contains(t, refused.Body.String(), "PHONE_KEY_CLAIM_REFUSED")
	assert.Zero(t, cf.minted())

	// The hook's trigger, as it will call it once core has committed the accept.
	require.True(t, srv.OfferGuestKeyClaim(server.GuestKeyOffer{UserID: guestID, InviteID: "inv-batcave-1", BindEmailOnAccept: true, CreatorHeldAdmin: true}))

	w := claimKey(t, r, guest)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	assert.Contains(t, w.Body.String(), claimSecret, "the guest is shown the key once")
	assert.Contains(t, w.Body.String(), claimClientID)
	assert.Equal(t, 1, cf.minted())

	// A second claim gets nothing and mints nothing.
	again := claimKey(t, r, guest)
	require.Equal(t, http.StatusNotFound, again.Code, again.Body.String())
	assert.NotContains(t, again.Body.String(), claimSecret)
	assert.Contains(t, again.Header().Get("Cache-Control"), "no-store")
	assert.Equal(t, 1, cf.minted())

	// The secret is in no log and not in the store (only its hash).
	assert.NotContains(t, r.newLog.String(), claimSecret)
	assert.NotContains(t, r.newLog.String(), "Fox-policy-token-0902")
	assertSecretNotStored(t, r, claimSecret)
	rec, ok := database.AsCapability[database.GuestServiceTokenStore](r.store)
	require.True(t, ok)
	row, gerr := rec.GetGuestServiceToken(guestID)
	require.NoError(t, gerr)
	require.NotNil(t, row)
	assert.Equal(t, database.GuestTokenActive, row.Status)
	assert.NotEmpty(t, row.SecretHash)
}

// assertSecretNotStored scans every key and value of books' store for the secret
// in clear: only its hash may be kept.
func assertSecretNotStored(t *testing.T, r *runningApp, secret string) {
	t.Helper()
	kvs, err := r.store.ScanPrefix("")
	require.NoError(t, err)
	require.NotEmpty(t, kvs)
	for _, kv := range kvs {
		assert.NotContains(t, string(kv.Key), secret)
		assert.NotContains(t, string(kv.Value), secret, "the secret is stored in clear under %s", kv.Key)
	}
}

func TestIdentityBoot_AnotherSignedInUserCannotTakeTheGuestsKey(t *testing.T) {
	r, cf := bootGuestKeyApp(t)
	_, guestID, admin := inviteAndAccept(t, r, "robin", "robin@wayne.example")
	require.True(t, r.mod.srv.Load().OfferGuestKeyClaim(server.GuestKeyOffer{UserID: guestID, InviteID: "inv-batcave-1", BindEmailOnAccept: true, CreatorHeldAdmin: true}))

	// The admin is signed in, as a different user, and asks.
	w := claimKey(t, r, admin)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), claimSecret)
	assert.Zero(t, cf.minted(), "another user's claim minted a key")

	// An anonymous caller never reaches the handler.
	anon := claimKey(t, r, nil)
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, anon.Code, anon.Body.String())
	assert.Zero(t, cf.minted())
}
