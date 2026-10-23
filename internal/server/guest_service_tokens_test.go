// file: internal/server/guest_service_tokens_test.go
// version: 1.0.1
// guid: 4d8a2f63-7b19-4c50-a3e8-1f6c9b0d5e27
// last-edited: 2026-10-23

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/cfservicetoken"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
	"github.com/falkcorp/audiobook-organizer/internal/mail/mailtest"
)

const fakeSecret = "SENTINEL-client-secret-0123456789abcdef"

// fakeTokenAPI stands in for Cloudflare's service-token API.
type fakeTokenAPI struct {
	mu        sync.Mutex
	n         int
	created   []string
	names     []string
	revoked   []string
	createErr error
	revokeErr error
	list      []cfservicetoken.Token
	listErr   error
	// entered/release make Create block (a mint in flight).
	entered chan struct{}
	release chan struct{}
}

func (f *fakeTokenAPI) Create(_ context.Context, name string) (*cfservicetoken.Created, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.n++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.n)
	f.created = append(f.created, id)
	f.names = append(f.names, name)
	return &cfservicetoken.Created{ID: id, Name: name, ClientID: "abcdef0123456789.access", ClientSecret: fakeSecret}, nil
}

func (f *fakeTokenAPI) Revoke(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
	return f.revokeErr == nil, f.revokeErr
}

func (f *fakeTokenAPI) List(context.Context) ([]cfservicetoken.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list, f.listErr
}

type fakeTokenPolicy struct {
	mu        sync.Mutex
	ensured   []string
	removed   []string
	ensureErr error
	removeErr error
}

func (f *fakeTokenPolicy) EnsureServiceToken(_ context.Context, id string) (cfaccesspolicy.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensureErr != nil {
		return "", f.ensureErr
	}
	f.ensured = append(f.ensured, id)
	return cfaccesspolicy.Added, nil
}

func (f *fakeTokenPolicy) RemoveServiceToken(_ context.Context, id string) (cfaccesspolicy.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return cfaccesspolicy.Removed, f.removeErr
}

type tokenFixture struct {
	srv    *Server
	store  database.Store
	api    *fakeTokenAPI
	policy *fakeTokenPolicy
	tokens database.GuestServiceTokenStore
	act    *activity.Service
}

// newTokenFixture builds a server with the feature on and fakes in place of
// Cloudflare. Config is restored afterwards.
func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()
	srv, store := setupUserHandlerServer(t)
	saved := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
	config.Mutate(func(c *config.Config) {
		c.CFGuestServiceTokens = true
		c.CFAccountID, c.CFPolicyAPIToken, c.CFServiceAuthPolicyID = "acct", "tok", "pol"
		c.InviteEmailServiceToken = false
	})
	f := &tokenFixture{srv: srv, store: store, api: &fakeTokenAPI{}, policy: &fakeTokenPolicy{}}
	srv.guestTokens.apiOverride, srv.guestTokens.policyOverride = f.api, f.policy
	srv.guestPolicyOverride = &fakeGuestPolicy{res: cfaccesspolicy.Added}
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](store)
	if !ok {
		t.Fatal("store lacks GuestServiceTokenStore")
	}
	f.tokens = tokens
	db, err := pebble.Open("act.pebble", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.act = activity.NewService(database.NewPebbleActivityStore(database.NewGatedDB(db)))
	srv.activityService = f.act
	return f
}

type acceptResp struct {
	Data struct {
		PhoneApp map[string]any `json:"phone_app"`
		User     map[string]any `json:"user"`
	} `json:"data"`
	PhoneApp map[string]any `json:"phone_app"`
}

// phoneApp returns the response's phone_app object (nil when absent), however
// the success envelope wraps it.
func phoneApp(t *testing.T, body string) map[string]any {
	t.Helper()
	var r acceptResp
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, body)
	}
	if r.Data.PhoneApp != nil {
		return r.Data.PhoneApp
	}
	return r.PhoneApp
}

func (f *tokenFixture) acceptGuest(t *testing.T, tok, username string) (*httptest.ResponseRecorder, *database.User) {
	t.Helper()
	createEmailInvite(t, f.store, "admin", true, tok, username, username+"@example.com")
	w := acceptInvite(t, f.srv, tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	f.srv.bgWG.Wait()
	u, _ := f.store.GetUserByUsername(username)
	if u == nil {
		t.Fatal("guest account missing")
	}
	return w, u
}

func (f *tokenFixture) auditTypes(t *testing.T, typ string) []database.ActivityEntry {
	t.Helper()
	entries, _, err := f.act.Query(context.Background(), database.ActivityFilter{Type: typ, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestGuestToken_AcceptMintsOwnTokenAndShowsSecretOnce(t *testing.T) {
	f := newTokenFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	w, u := f.acceptGuest(t, strings.Repeat("a", 64), "guesta")

	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("accept response Cache-Control = %q, want no-store", cc)
	}
	pa := phoneApp(t, w.Body.String())
	if pa["status"] != "issued" || pa["client_id"] != "abcdef0123456789.access" || pa["client_secret"] != fakeSecret {
		t.Fatalf("phone_app = %v", pa)
	}
	if pa["id_header"] != "CF-Access-Client-Id" || pa["secret_header"] != "CF-Access-Client-Secret" {
		t.Fatalf("header names = %v", pa)
	}
	if len(f.api.created) != 1 || f.api.names[0] != "aorg-guest-"+u.ID {
		t.Fatalf("created = %v names = %v", f.api.created, f.api.names)
	}
	id := f.api.created[0]
	if !reflect.DeepEqual(f.policy.ensured, []string{id}) {
		t.Fatalf("policy got %v, want exactly the new token id", f.policy.ensured)
	}
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec == nil || rec.Status != database.GuestTokenActive || rec.TokenID != id || !rec.PolicyAdded ||
		rec.SecretHash != hashSecret(fakeSecret) || rec.Name != "aorg-guest-"+u.ID {
		t.Fatalf("record = %+v", rec)
	}

	// The secret is in the response and nowhere else: not the store (every key
	// and value), not the logs, not the audit entries.
	db := f.store.(*database.PebbleStore).DB()
	it, err := db.NewIter(nil)
	if err != nil {
		t.Fatal(err)
	}
	for it.First(); it.Valid(); it.Next() {
		if strings.Contains(string(it.Key()), fakeSecret) || strings.Contains(string(it.Value()), fakeSecret) {
			t.Fatalf("the client secret is stored under key %q", it.Key())
		}
		if strings.Contains(string(it.Value()), "abcdef0123456789.access") {
			t.Fatalf("the client id is stored under key %q", it.Key())
		}
	}
	_ = it.Close()
	if strings.Contains(logs.String(), fakeSecret) {
		t.Fatalf("the client secret reached the logs:\n%s", logs.String())
	}
	minted := f.auditTypes(t, auditGuestTokenMinted)
	if len(minted) != 1 {
		t.Fatalf("mint audit entries = %d", len(minted))
	}
	if b, _ := json.Marshal(minted[0]); strings.Contains(string(b), fakeSecret) || strings.Contains(string(b), "abcdef0123456789") {
		t.Fatalf("audit entry carries a credential: %s", b)
	}
}

func TestGuestToken_OffByDefaultTouchesNothing(t *testing.T) {
	f := newTokenFixture(t)
	config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
	w, u := f.acceptGuest(t, strings.Repeat("b", 64), "guestb")
	if pa := phoneApp(t, w.Body.String()); pa != nil {
		t.Fatalf("phone_app present with the flag off: %v", pa)
	}
	if len(f.api.created)+len(f.policy.ensured) != 0 {
		t.Fatal("Cloudflare was called with the flag off")
	}
	if rec, _ := f.tokens.GetGuestServiceToken(u.ID); rec != nil {
		t.Fatalf("a record was written with the flag off: %+v", rec)
	}
}

func TestGuestToken_403FailsClosedWithAdminMessage(t *testing.T) {
	f := newTokenFixture(t)
	f.api.createErr = &cfservicetoken.APIError{Status: 403, Detail: "10000 Authentication error"}
	w, u := f.acceptGuest(t, strings.Repeat("c", 64), "guestc")
	pa := phoneApp(t, w.Body.String())
	if pa["status"] != "unavailable" || pa["client_secret"] != nil {
		t.Fatalf("phone_app = %v", pa)
	}
	msg, _ := pa["message"].(string)
	if msg == "" || strings.Contains(msg, "Cloudflare") || strings.Contains(msg, "403") || strings.Contains(msg, "Service Tokens") {
		t.Fatalf("guest-facing message leaks operator detail or is empty: %q", msg)
	}
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec == nil || rec.Status != database.GuestTokenFailed ||
		!strings.Contains(rec.Detail, "Service Tokens") || !strings.Contains(rec.Detail, "403") {
		t.Fatalf("admin-facing record = %+v", rec)
	}
	if len(f.policy.ensured) != 0 {
		t.Fatal("the policy was edited although no token was made")
	}
	if got := f.auditTypes(t, auditGuestTokenMintFailed); len(got) != 1 {
		t.Fatalf("mint-failed audit entries = %d", len(got))
	}
}

func TestGuestToken_PolicyFailureDeletesTheToken(t *testing.T) {
	f := newTokenFixture(t)
	f.policy.ensureErr = errors.New("cfaccesspolicy: save policy: HTTP 500: boom")
	w, u := f.acceptGuest(t, strings.Repeat("d", 64), "guestd")
	if pa := phoneApp(t, w.Body.String()); pa["status"] != "unavailable" {
		t.Fatalf("phone_app = %v", pa)
	}
	if !reflect.DeepEqual(f.api.revoked, f.api.created) || len(f.api.created) != 1 {
		t.Fatalf("created %v revoked %v: a token outside the policy must be deleted", f.api.created, f.api.revoked)
	}
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec == nil || rec.Status != database.GuestTokenFailed {
		t.Fatalf("record = %+v", rec)
	}
}

func TestGuestToken_PolicyFailureAndCleanupFailureIsVisibleToTheAdmin(t *testing.T) {
	f := newTokenFixture(t)
	f.policy.ensureErr = errors.New("cfaccesspolicy: save policy: HTTP 500: boom")
	f.api.revokeErr = &cfservicetoken.APIError{Status: 500, Detail: "x"}
	_, u := f.acceptGuest(t, strings.Repeat("e", 64), "gueste")
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec == nil || rec.Status != database.GuestTokenRevokeFailed || rec.TokenID != f.api.created[0] ||
		!strings.Contains(rec.Detail, "cleanup incomplete") {
		t.Fatalf("a token that may still exist must be a retryable revoke_failed: %+v", rec)
	}
}

func TestGuestToken_UnverifiedPolicyWriteCountsAsAdded(t *testing.T) {
	f := newTokenFixture(t)
	f.policy.ensureErr = fmt.Errorf("%w: verify: x", cfaccesspolicy.ErrWriteUnverified)
	w, u := f.acceptGuest(t, strings.Repeat("f", 64), "guestf")
	if pa := phoneApp(t, w.Body.String()); pa["status"] != "issued" {
		t.Fatalf("phone_app = %v", pa)
	}
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec == nil || rec.Status != database.GuestTokenActive || !strings.Contains(rec.Detail, "check the books policy") {
		t.Fatalf("record = %+v", rec)
	}
}

func TestGuestToken_OnlyGrantedInvitesGetAToken(t *testing.T) {
	f := newTokenFixture(t)
	// Creator is an editor: no SSO grant, so no Cloudflare admission either.
	createEmailInvite(t, f.store, "editor", true, strings.Repeat("1", 64), "guestg", "guestg@example.com")
	// Never emailed: the address is unproven.
	createEmailInvite(t, f.store, "admin", false, strings.Repeat("2", 64), "guesth", "guesth@example.com")
	for _, tok := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		w := acceptInvite(t, f.srv, tok)
		if w.Code != http.StatusCreated {
			t.Fatalf("accept: %d %s", w.Code, w.Body.String())
		}
		if pa := phoneApp(t, w.Body.String()); pa != nil {
			t.Fatalf("phone_app present for an ungranted invite: %v", pa)
		}
	}
	f.srv.bgWG.Wait()
	if len(f.api.created) != 0 {
		t.Fatal("a token was minted for an invite without an SSO grant")
	}
}

func TestGuestToken_DeactivationRevokesTheGuestsOwnTokenOnly(t *testing.T) {
	f := newTokenFixture(t)
	_, a := f.acceptGuest(t, strings.Repeat("a", 64), "guesta")
	_, b := f.acceptGuest(t, strings.Repeat("b", 64), "guestb")
	if len(f.api.created) != 2 {
		t.Fatalf("created = %v", f.api.created)
	}
	idA, idB := f.api.created[0], f.api.created[1]

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/"+a.ID+"/deactivate", nil)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body.String())
	}
	f.srv.bgWG.Wait()

	if !reflect.DeepEqual(f.api.revoked, []string{idA}) || !reflect.DeepEqual(f.policy.removed, []string{idA}) {
		t.Fatalf("revoked %v removed %v, want only guest A's token %s", f.api.revoked, f.policy.removed, idA)
	}
	ra, _ := f.tokens.GetGuestServiceToken(a.ID)
	rb, _ := f.tokens.GetGuestServiceToken(b.ID)
	if ra.Status != database.GuestTokenRevoked || ra.RevokedAt == nil || ra.RevokeReason != "user deactivated" || ra.PolicyAdded {
		t.Fatalf("A = %+v", ra)
	}
	if rb.Status != database.GuestTokenActive || rb.TokenID != idB {
		t.Fatalf("B must be untouched: %+v", rb)
	}
	if got := f.auditTypes(t, auditGuestTokenRevoked); len(got) != 1 {
		t.Fatalf("revoke audit entries = %d", len(got))
	}
	// Deactivating again changes nothing.
	f.srv.revokeGuestServiceTokenAsync(a.ID, "again")
	f.srv.bgWG.Wait()
	if len(f.api.revoked) != 1 {
		t.Fatalf("a second revoke called Cloudflare again: %v", f.api.revoked)
	}
}

func TestGuestToken_EmailChangeDoesNotRevoke(t *testing.T) {
	f := newTokenFixture(t)
	_, u := f.acceptGuest(t, strings.Repeat("a", 64), "guesta")
	// The email-change path is revokeGuestAccess; it must leave the token alone.
	f.srv.revokeGuestAccess(u.ID, "guesta@example.com", "email changed")
	f.srv.bgWG.Wait()
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec.Status != database.GuestTokenActive || len(f.api.revoked) != 0 || len(f.policy.removed) != 0 {
		t.Fatalf("an email change touched the token: %+v revoked=%v removed=%v", rec, f.api.revoked, f.policy.removed)
	}
}

func TestGuestToken_RevokeDuringMintDeletesTheNewToken(t *testing.T) {
	f := newTokenFixture(t)
	f.api.entered, f.api.release = make(chan struct{}), make(chan struct{})
	tok := strings.Repeat("a", 64)
	createEmailInvite(t, f.store, "admin", true, tok, "guesta", "guesta@example.com")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- acceptInvite(t, f.srv, tok) }()
	<-f.api.entered // Cloudflare is mid-answer; the pending record exists
	u, _ := f.store.GetUserByUsername("guesta")
	if err := f.srv.revokeGuestServiceToken(context.Background(), u.ID, "deactivated", ""); err != nil {
		t.Fatal(err)
	}
	close(f.api.release)
	w := <-done
	f.srv.bgWG.Wait()
	if w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	if pa := phoneApp(t, w.Body.String()); pa["status"] != "unavailable" || pa["client_secret"] != nil {
		t.Fatalf("a revoked mint must not hand out a secret: %v", pa)
	}
	if !reflect.DeepEqual(f.api.revoked, f.api.created) || len(f.api.created) != 1 {
		t.Fatalf("created %v revoked %v: the token made during the revoke must be deleted", f.api.created, f.api.revoked)
	}
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec.Status != database.GuestTokenRevoked {
		t.Fatalf("record = %+v", rec)
	}
	if len(f.policy.ensured) != 0 {
		t.Fatal("a revoked guest's token was added to the policy")
	}
}

func TestGuestToken_DeactivatedBeforeMintMintsNothing(t *testing.T) {
	f := newTokenFixture(t)
	tok := strings.Repeat("a", 64)
	createEmailInvite(t, f.store, "admin", true, tok, "guesta", "guesta@example.com")
	// The account is locked after the invite is consumed and before the mint
	// runs (the window a deactivation can land in).
	u, err := f.srv.Ops().ConsumeInvite(tok, "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	locker, ok := f.store.(interface {
		SetUserStatus(id, status string) (*database.User, error)
	})
	if !ok {
		t.Fatal("store cannot lock users")
	}
	if _, err := locker.SetUserStatus(u.ID, "locked"); err != nil {
		t.Fatal(err)
	}
	issue := f.srv.mintGuestServiceToken(context.Background(), tok, u)
	if issue == nil || issue.Issued {
		t.Fatalf("issue = %+v", issue)
	}
	if len(f.api.created) != 0 {
		t.Fatal("a token was minted for a locked account")
	}
}

func TestGuestToken_AdminRevokeAndRetry(t *testing.T) {
	f := newTokenFixture(t)
	_, u := f.acceptGuest(t, strings.Repeat("a", 64), "guesta")
	f.api.revokeErr = &cfservicetoken.APIError{Status: 403, Detail: "x"}
	err := f.srv.revokeGuestServiceToken(context.Background(), u.ID, "revoked by an admin", "ADMIN1")
	if err == nil || !strings.Contains(err.Error(), "Service Tokens") {
		t.Fatalf("err = %v, want the permission message", err)
	}
	rec, _ := f.tokens.GetGuestServiceToken(u.ID)
	if rec.Status != database.GuestTokenRevokeFailed || len(f.policy.removed) != 0 {
		t.Fatalf("a failed delete must stay retryable and leave the rule: %+v removed=%v", rec, f.policy.removed)
	}
	f.api.revokeErr = nil
	if err := f.srv.revokeGuestServiceToken(context.Background(), u.ID, "revoked by an admin", "ADMIN1"); err != nil {
		t.Fatal(err)
	}
	rec, _ = f.tokens.GetGuestServiceToken(u.ID)
	if rec.Status != database.GuestTokenRevoked {
		t.Fatalf("retry did not finish: %+v", rec)
	}
	if err := f.srv.revokeGuestServiceToken(context.Background(), "nobody", "x", ""); !errors.Is(err, errGuestTokenNone) {
		t.Fatalf("revoke of an unknown user = %v", err)
	}
	entries := f.auditTypes(t, auditGuestTokenRevoked)
	if len(entries) != 1 || !strings.Contains(fmt.Sprint(entries[0].Details["detail"]), "ADMIN1") {
		t.Fatalf("audit = %+v", entries)
	}
	if len(f.auditTypes(t, auditGuestTokenRevokeFailed)) != 1 {
		t.Fatal("the failed revoke was not audited")
	}
}

func TestGuestToken_RevokeWorksAfterFlagTurnedOff(t *testing.T) {
	f := newTokenFixture(t)
	_, u := f.acceptGuest(t, strings.Repeat("a", 64), "guesta")
	config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
	if err := f.srv.revokeGuestServiceToken(context.Background(), u.ID, "x", ""); err != nil {
		t.Fatalf("revoke with the flag off: %v", err)
	}
}

func TestGuestToken_RevokeWithoutSettingsSaysHowToCleanUp(t *testing.T) {
	f := newTokenFixture(t)
	_, u := f.acceptGuest(t, strings.Repeat("a", 64), "guesta")
	f.srv.guestTokens = guestTokenState{} // a fresh process: no override, no settings
	config.Mutate(func(c *config.Config) { c.CFServiceAuthPolicyID = "" })
	err := f.srv.revokeGuestServiceToken(context.Background(), u.ID, "x", "")
	if err == nil || !strings.Contains(err.Error(), "aorg-guest-"+u.ID) {
		t.Fatalf("err = %v, want the by-hand cleanup instruction naming the token", err)
	}
	if rec, _ := f.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevokeFailed {
		t.Fatalf("record = %+v", rec)
	}
}

func TestGuestToken_AdminListJoinsOnlyOurTokens(t *testing.T) {
	f := newTokenFixture(t)
	_, u := f.acceptGuest(t, strings.Repeat("a", 64), "guesta")
	seen := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	f.api.list = []cfservicetoken.Token{
		{ID: f.api.created[0], Name: "aorg-guest-" + u.ID, LastSeenAt: &seen},
		{ID: "90a520fb-0000-4000-8000-000000000000", Name: "owner-phone", LastSeenAt: &seen},
	}
	views, note, err := f.srv.guestTokenViews(context.Background())
	if err != nil || note != "" || len(views) != 1 {
		t.Fatalf("views = %+v note=%q err=%v", views, note, err)
	}
	v := views[0]
	if v.UserID != u.ID || v.Username != "guesta" || v.TokenID != f.api.created[0] || v.LastSeenAt == nil || !v.LastSeenAt.Equal(seen) {
		t.Fatalf("view = %+v", v)
	}
	b, _ := json.Marshal(views)
	for _, bad := range []string{"90a520fb", "owner-phone", fakeSecret, "secret_hash", "client_id"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("list leaks %q: %s", bad, b)
		}
	}
	// Cloudflare unavailable: the list still renders, with a reason.
	f.api.listErr = &cfservicetoken.APIError{Status: 403, Detail: "x"}
	views, note, err = f.srv.guestTokenViews(context.Background())
	if err != nil || len(views) != 1 || views[0].LastSeenAt != nil || !strings.Contains(note, "Service Tokens") {
		t.Fatalf("views=%+v note=%q err=%v", views, note, err)
	}
}

func TestGuestToken_AdminRoutesNeedAnAdmin(t *testing.T) {
	f := newTokenFixture(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/guest-service-tokens"},
		{http.MethodPost, "/api/v1/admin/guest-service-tokens/U1/revoke"},
	} {
		w := httptest.NewRecorder()
		f.srv.router.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Errorf("%s %s without an admin = %d, want 401/403", c.method, c.path, w.Code)
		}
	}
}

func TestGuestToken_PublicLookupAnnouncesTheKeyOnlyWhenItWillBeMinted(t *testing.T) {
	f := newTokenFixture(t)
	createEmailInvite(t, f.store, "admin", true, strings.Repeat("a", 64), "guesta", "guesta@example.com")
	lookup := func(tok string) map[string]any {
		w := httptest.NewRecorder()
		f.srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/invite/"+tok, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("lookup: %d %s", w.Code, w.Body.String())
		}
		var r struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		if r.Data != nil {
			return r.Data
		}
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	if got := lookup(strings.Repeat("a", 64)); got["phone_app_token"] != true {
		t.Fatalf("lookup = %v, want phone_app_token true", got)
	}
	config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
	if got := lookup(strings.Repeat("a", 64)); got["phone_app_token"] == true {
		t.Fatalf("lookup = %v, want phone_app_token false with the flag off", got)
	}
}

func TestGuestToken_EmailOptInSendsPostAcceptMail(t *testing.T) {
	f := newTokenFixture(t)
	rec := &mailtest.Recorder{}
	f.srv.mailSenderOverride = rec
	// Default off: no mail.
	_, _ = f.acceptGuest(t, strings.Repeat("a", 64), "guesta")
	if n := len(rec.Sent()); n != 0 {
		t.Fatalf("%d emails sent with INVITE_EMAIL_SERVICE_TOKEN off", n)
	}
	config.Mutate(func(c *config.Config) { c.InviteEmailServiceToken = true })
	_, _ = f.acceptGuest(t, strings.Repeat("b", 64), "guestb")
	sent := rec.Sent()
	if len(sent) != 1 || sent[0].To != "guestb@example.com" ||
		!strings.Contains(sent[0].Text, fakeSecret) || !strings.Contains(sent[0].Text, "CF-Access-Client-Secret") {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestGuestTokenIssue_NeverPrintsTheSecret(t *testing.T) {
	g := guestTokenIssue{Issued: true, ClientID: "id.access", ClientSecret: fakeSecret}
	for _, s := range []string{fmt.Sprint(g), fmt.Sprintf("%+v", g), fmt.Sprintf("%#v", g), fmt.Sprintf("%v", &g)} {
		if strings.Contains(s, fakeSecret) {
			t.Fatalf("secret printed: %s", s)
		}
	}
}
