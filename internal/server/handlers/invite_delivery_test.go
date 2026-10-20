// file: internal/server/handlers/invite_delivery_test.go
// version: 1.3.0
// guid: e4b17c92-6a35-4d80-9f2e-1c8d5a7b3f06
// last-edited: 2026-10-19

package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	imail "github.com/falkcorp/audiobook-organizer/internal/mail"
	"github.com/falkcorp/audiobook-organizer/internal/mail/mailtest"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

const (
	testGuestID     = "guest-client-id-SENTINEL.access"
	testGuestSecret = "guest-client-secret-SENTINEL-0a1b2c"
	testBase        = "https://books.example.com"
)

type inviteFixture struct {
	store  *database.PebbleStore
	router *gin.Engine
	rec    *mailtest.Recorder
	h      *handlers.UserHandler
	mailOn bool
	hdrOn  bool
}

func newInviteFixture(t *testing.T) *inviteFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &inviteFixture{store: store, rec: &mailtest.Recorder{}, mailOn: true, hdrOn: true}
	f.h = handlers.NewUserHandler(store)
	f.h.SetInviteDelivery(&handlers.InviteDelivery{
		Sender: func() imail.Sender {
			if !f.mailOn {
				return nil
			}
			return f.rec
		},
		BaseURL: func() string { return testBase + "/" },
		GuestHeaders: func() (handlers.GuestHeaders, bool) {
			return handlers.GuestHeaders{ClientID: testGuestID, ClientSecret: testGuestSecret}, f.hdrOn
		},
		GuestPolicySyncConfigured: func() bool { return false },
	})
	r := gin.New()
	r.POST("/users/invite", f.h.CreateInvite)
	r.GET("/users/invites", f.h.ListInvites)
	r.POST("/users/invites/:token/resend", f.h.ResendInvite)
	r.GET("/auth/invite/:token", f.h.PublicInvite)
	f.router = r
	return f
}

func (f *inviteFixture) do(t *testing.T, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	var env map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	data, _ := env["data"].(map[string]any)
	return w, data
}

// assertNoGuestSecrets fails when an admin-facing response carries either
// half of the shared guest header pair.
func assertNoGuestSecrets(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, testGuestSecret) || strings.Contains(body, testGuestID) {
		t.Fatalf("admin response leaked the guest header pair: %s", body)
	}
}

func TestCreateInvite_SendsEmailWithLinkAndHeaders(t *testing.T) {
	f := newInviteFixture(t)
	w, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": "guest", "role_id": "viewer", "email": " Guest@Example.com ", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	assertNoGuestSecrets(t, w.Body.String())
	if data["email_sent"] != true {
		t.Fatalf("email_sent = %v, error = %v", data["email_sent"], data["email_error"])
	}
	token, _ := data["token"].(string)
	if data["invite_url"] != testBase+"/invite/"+token {
		t.Fatalf("invite_url = %v", data["invite_url"])
	}
	sent := f.rec.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(sent))
	}
	m := sent[0]
	if m.To != "guest@example.com" {
		t.Fatalf("To = %q", m.To)
	}
	for _, part := range []string{m.Text, m.HTML} {
		for _, want := range []string{testBase + "/invite/" + token, testGuestID, testGuestSecret, "CF-Access-Client-Id", "CF-Access-Client-Secret", "AudioBooth"} {
			if !strings.Contains(part, want) {
				t.Fatalf("email part missing %q", want)
			}
		}
	}
	inv, _ := f.store.GetInvite(token)
	if inv.Email != "guest@example.com" || inv.EmailSentAt == nil {
		t.Fatalf("invite not stored with email and send time: %+v", inv)
	}
}

func TestCreateInvite_HeadersOmittedWhenUnconfigured(t *testing.T) {
	f := newInviteFixture(t)
	f.hdrOn = false
	w, _ := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": "guest", "role_id": "viewer", "email": "guest@example.com", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	m := f.rec.Sent()[0]
	if strings.Contains(m.Text, "CF-Access-Client-Id") || strings.Contains(m.HTML, testGuestSecret) {
		t.Fatal("header instructions sent although the header pair is not configured")
	}
}

func TestCreateInvite_MailNotConfiguredStillReturnsLink(t *testing.T) {
	f := newInviteFixture(t)
	f.mailOn = false
	w, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": "guest", "role_id": "viewer", "email": "guest@example.com", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	if data["email_sent"] != false || !strings.Contains(fmt.Sprint(data["email_error"]), "not configured") {
		t.Fatalf("want email_sent=false with a not-configured error: %v", data)
	}
	if data["token"] == "" || data["invite_url"] == "" {
		t.Fatal("the link must still be returned")
	}
}

func TestCreateInvite_SendFailureIsReportedNotFatal(t *testing.T) {
	f := newInviteFixture(t)
	f.rec.SetErr(errors.New("mail: RCPT TO: 550 no such user"))
	w, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": "guest", "role_id": "viewer", "email": "guest@example.com", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	assertNoGuestSecrets(t, w.Body.String())
	if data["email_sent"] != false || !strings.Contains(fmt.Sprint(data["email_error"]), "550") {
		t.Fatalf("send failure not reported: %v", data)
	}
	inv, _ := f.store.GetInvite(fmt.Sprint(data["token"]))
	if inv == nil || inv.EmailSentAt != nil {
		t.Fatalf("invite must exist and not be marked sent: %+v", inv)
	}
}

func TestCreateInvite_NoEmailNoSend(t *testing.T) {
	f := newInviteFixture(t)
	w, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "guest", "role_id": "viewer"})
	if w.Code != http.StatusCreated || data["email_sent"] != false || data["email_error"] != nil {
		t.Fatalf("status %d data %v", w.Code, data)
	}
	if len(f.rec.Sent()) != 0 {
		t.Fatal("no email requested, none must be sent")
	}
}

func TestCreateInvite_Validation(t *testing.T) {
	f := newInviteFixture(t)
	cases := map[string]map[string]any{
		"send without email": {"username": "a", "role_id": "viewer", "send_email": true},
		"display-name form":  {"username": "b", "role_id": "viewer", "email": "Guest <g@example.com>"},
		"two addresses":      {"username": "c", "role_id": "viewer", "email": "a@example.com, b@example.com"},
		"header injection":   {"username": "d", "role_id": "viewer", "email": "a@example.com\r\nBcc: x@example.com"},
	}
	for name, body := range cases {
		if w, _ := f.do(t, http.MethodPost, "/users/invite", body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", name, w.Code)
		}
	}
	if _, err := f.store.CreateUser("owner", "taken@example.com", "bcrypt", "h", []string{"admin"}, "active"); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "e", "role_id": "viewer", "email": "taken@example.com"}); w.Code != http.StatusConflict {
		t.Fatalf("taken email: status %d, want 409", w.Code)
	}
}

func TestResendInvite_RateLimitedPerInvite(t *testing.T) {
	f := newInviteFixture(t)
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "g", "role_id": "viewer", "email": "g@example.com"})
	token := fmt.Sprint(data["token"])
	w, rd := f.do(t, http.MethodPost, "/users/invites/"+token+"/resend", nil)
	if w.Code != http.StatusOK || rd["email_sent"] != true {
		t.Fatalf("first resend: %d %s", w.Code, w.Body.String())
	}
	assertNoGuestSecrets(t, w.Body.String())
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+token+"/resend", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second resend within a minute: %d, want 429", w.Code)
	}
	if n := len(f.rec.Sent()); n != 1 {
		t.Fatalf("sent %d, want 1", n)
	}
}

func TestCreateInviteSend_CountsTowardResendLimit(t *testing.T) {
	f := newInviteFixture(t)
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "g", "role_id": "viewer", "email": "g@example.com", "send_email": true})
	token := fmt.Sprint(data["token"])
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+token+"/resend", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("resend right after the create-time send: %d, want 429", w.Code)
	}
}

func TestResendInvite_PerAdminCap(t *testing.T) {
	f := newInviteFixture(t)
	var lastCode int
	for i := range 11 {
		_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{
			"username": fmt.Sprintf("g%d", i), "role_id": "viewer", "email": fmt.Sprintf("g%d@example.com", i),
		})
		w, _ := f.do(t, http.MethodPost, "/users/invites/"+fmt.Sprint(data["token"])+"/resend", nil)
		lastCode = w.Code
		if i < 10 && w.Code != http.StatusOK {
			t.Fatalf("resend %d: %d", i, w.Code)
		}
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("11th resend by one admin: %d, want 429", lastCode)
	}
}

func TestResendInvite_Refusals(t *testing.T) {
	f := newInviteFixture(t)
	if w, _ := f.do(t, http.MethodPost, "/users/invites/nope/resend", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing invite: %d", w.Code)
	}
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "noemail", "role_id": "viewer"})
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+fmt.Sprint(data["token"])+"/resend", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("no-email invite: %d", w.Code)
	}
	_, data = f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "m", "role_id": "viewer", "email": "m@example.com"})
	f.mailOn = false
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+fmt.Sprint(data["token"])+"/resend", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("mail off: %d", w.Code)
	}
}

func TestPublicInvite_OnlyLiveTokensAndOnlyAllowedFields(t *testing.T) {
	f := newInviteFixture(t)
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "guest", "role_id": "viewer", "email": "guest@example.com"})
	token := fmt.Sprint(data["token"])

	w, pub := f.do(t, http.MethodGet, "/auth/invite/"+token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("live token: %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("public invite response must not be cached")
	}
	for k := range pub {
		if k != "username" && k != "expires_at" && k != "guest_headers" {
			t.Fatalf("public invite returned extra field %q", k)
		}
	}
	gh, _ := pub["guest_headers"].(map[string]any)
	if pub["username"] != "guest" || gh["client_id"] != testGuestID || gh["client_secret"] != testGuestSecret {
		t.Fatalf("public invite = %v", pub)
	}
	if strings.Contains(w.Body.String(), "guest@example.com") {
		t.Fatal("public invite must not reveal the email")
	}

	f.hdrOn = false
	if _, pub := f.do(t, http.MethodGet, "/auth/invite/"+token, nil); pub["guest_headers"] != nil {
		t.Fatal("header pair returned while unconfigured")
	}
	f.hdrOn = true

	// Missing, used and expired answer identically.
	missing, _ := f.do(t, http.MethodGet, "/auth/invite/"+strings.Repeat("0", 64), nil)
	if _, err := f.store.ConsumeInvite(token, "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	used, _ := f.do(t, http.MethodGet, "/auth/invite/"+token, nil)
	if _, err := f.store.CreateInvite(&database.Invite{Token: strings.Repeat("e", 64), Username: "late", RoleID: "viewer",
		CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	expired, _ := f.do(t, http.MethodGet, "/auth/invite/"+strings.Repeat("e", 64), nil)
	for name, r := range map[string]*httptest.ResponseRecorder{"missing": missing, "used": used, "expired": expired} {
		if r.Code != http.StatusNotFound || r.Body.String() != missing.Body.String() {
			t.Fatalf("%s: %d %s, want the same 404 as missing", name, r.Code, r.Body.String())
		}
		assertNoGuestSecrets(t, r.Body.String())
	}
}

func TestListInvites_StatusFlagsAndAcceptedWithoutTokens(t *testing.T) {
	f := newInviteFixture(t)
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "acc", "role_id": "viewer", "email": "acc@example.com"})
	token := fmt.Sprint(data["token"])
	if _, err := f.store.ConsumeInvite(token, "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordInviteGuestPolicy(token, database.InviteGuestPolicyFailed, "HTTP 403: 10000 Authentication error", time.Now()); err != nil {
		t.Fatal(err)
	}
	w, list := f.do(t, http.MethodGet, "/users/invites", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	assertNoGuestSecrets(t, w.Body.String())
	if list["mail_configured"] != true || list["guest_headers_configured"] != true || list["guest_policy_sync_configured"] != false {
		t.Fatalf("flags = %v", list)
	}
	acc, _ := list["accepted"].([]any)
	if len(acc) != 1 {
		t.Fatalf("accepted = %v", list["accepted"])
	}
	a := acc[0].(map[string]any)
	if a["guest_policy_status"] != "failed" || a["email"] != "acc@example.com" || a["email_binding"] != "not_emailed" {
		t.Fatalf("accepted row = %v", a)
	}
	if strings.Contains(w.Body.String(), `"accepted":[{"token"`) || a["token"] != nil {
		t.Fatal("accepted rows must not carry tokens")
	}
}

func TestInviteEmail_HTMLEscapesValues(t *testing.T) {
	f := newInviteFixture(t)
	w, _ := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": `<script>alert(1)</script>`, "role_id": "viewer", "email": "x@example.com", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	html := f.rec.Sent()[0].HTML
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("username was not HTML-escaped in the email")
	}
}

func TestUpdateMe_EmailChangeRevokesGuestAccessForOldEmail(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	mockStore.EXPECT().UpdateUser(mock.Anything).Return(nil)
	h := handlers.NewAuthHandler(mockStore, true)
	var calls []string
	h.SetGuestAccessRevoker(func(userID, email, reason string) { calls = append(calls, userID+"|"+email+"|"+reason) })

	c, w := newAuthCtx(http.MethodPatch, "/api/v1/auth/me", map[string]any{"email": "new@example.com"})
	setAuthUser(c, &database.User{ID: "u1", Status: "active", Email: "old@example.com"})
	h.UpdateMe(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if len(calls) != 1 || calls[0] != "u1|old@example.com|email changed" {
		t.Fatalf("revoker calls = %v", calls)
	}

	// Same address, different case: not a change, nothing revoked.
	calls = nil
	c, _ = newAuthCtx(http.MethodPatch, "/api/v1/auth/me", map[string]any{"email": "NEW@example.com"})
	setAuthUser(c, &database.User{ID: "u1", Status: "active", Email: "new@example.com"})
	h.UpdateMe(c)
	if len(calls) != 0 {
		t.Fatalf("revoked on a case-only change: %v", calls)
	}
}

// S2: a refused change answers the same generic 400 as any unusable
// address, so the profile form cannot be used to test which addresses have
// accounts.
func TestUpdateMe_EmailTakenIsGenericAndRevokesNothing(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	mockStore.EXPECT().UpdateUser(mock.Anything).Return(database.ErrUserEmailTaken)
	h := handlers.NewAuthHandler(mockStore, true)
	var calls []string
	h.SetGuestAccessRevoker(func(userID, email, reason string) { calls = append(calls, userID) })

	c, w := newAuthCtx(http.MethodPatch, "/api/v1/auth/me", map[string]any{"email": "Taken@example.com"})
	setAuthUser(c, &database.User{ID: "u1", Status: "active", Email: "old@example.com"})
	h.UpdateMe(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
	for _, leak := range []string{"another account", "already", "exists", "taken", "Taken@example.com"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("body reveals the address is registered (%q): %s", leak, w.Body.String())
		}
	}
	if len(calls) != 0 {
		t.Fatalf("a refused change must not revoke the old email's access: %v", calls)
	}
}

func TestDeactivateUser_RevokesGuestAccess(t *testing.T) {
	f := newInviteFixture(t)
	u, err := f.store.CreateUser("guest", "guest@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	f.h.SetGuestAccessRevoker(func(userID, email, reason string) { calls = append(calls, userID+"|"+email+"|"+reason) })
	f.router.POST("/users/:id/deactivate", f.h.DeactivateUser)
	if w, _ := f.do(t, http.MethodPost, "/users/"+u.ID+"/deactivate", nil); w.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body.String())
	}
	if len(calls) != 1 || calls[0] != u.ID+"|guest@example.com|user deactivated" {
		t.Fatalf("revoker calls = %v", calls)
	}
}

// S2: an admin sets or clears another user's email as verified; a user with
// users.manage but without the admin role cannot; the old address's guest
// access is revoked.
func TestSetUserEmail(t *testing.T) {
	f := newInviteFixture(t)
	admin, err := f.store.CreateUser("boss", "boss@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	editor, err := f.store.CreateUser("ed", "ed@example.com", "bcrypt", "h", []string{"editor"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.store.CreateUser("member", "member.old@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	f.h.SetGuestAccessRevoker(func(userID, email, reason string) { calls = append(calls, userID+"|"+email+"|"+reason) })
	actor := admin
	f.router.PUT("/users/:id/email", func(c *gin.Context) { c.Set("auth_user", actor) }, f.h.SetUserEmail)
	path := "/users/" + member.ID + "/email"

	w, data := f.do(t, http.MethodPut, path, map[string]any{"email": " Member.New@Example.com "})
	if w.Code != http.StatusOK {
		t.Fatalf("admin set: %d %s", w.Code, w.Body.String())
	}
	if u, _ := data["user"].(map[string]any); u["email"] != "member.new@example.com" {
		t.Fatalf("response user = %v", data["user"])
	}
	got, _ := f.store.GetUserByID(member.ID)
	if got.EmailSource != database.EmailSourceAdmin {
		t.Fatalf("source = %q, want admin", got.EmailSource)
	}
	if v, _ := f.store.GetUserByVerifiedEmail("member.new@example.com"); v == nil || v.ID != member.ID {
		t.Fatal("an admin-set address is verified")
	}
	if len(calls) != 1 || calls[0] != member.ID+"|member.old@example.com|email changed by an admin" {
		t.Fatalf("revoker calls = %v", calls)
	}

	if w, _ := f.do(t, http.MethodPut, path, map[string]any{"email": "ed@example.com"}); w.Code != http.StatusConflict {
		t.Fatalf("another account's address: %d, want 409", w.Code)
	}
	if w, _ := f.do(t, http.MethodPut, path, map[string]any{"email": "not an address"}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad address: %d, want 400", w.Code)
	}
	if w, _ := f.do(t, http.MethodPut, path, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing email field: %d, want 400", w.Code)
	}
	if w, _ := f.do(t, http.MethodPut, "/users/01NOSUCHUSER/email", map[string]any{"email": "x@example.com"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d, want 404", w.Code)
	}
	if w, _ := f.do(t, http.MethodPut, path, map[string]any{"email": ""}); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if got, _ := f.store.GetUserByID(member.ID); got.Email != "" || got.EmailSource != "" {
		t.Fatalf("cleared row = %+v", got)
	}

	actor = editor // has users.manage at the route, not the admin role
	if w, _ := f.do(t, http.MethodPut, path, map[string]any{"email": "sneaky@example.com"}); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin actor: %d, want 403", w.Code)
	}
	locked := *admin
	locked.Status = "locked"
	actor = &locked
	if w, _ := f.do(t, http.MethodPut, path, map[string]any{"email": "sneaky@example.com"}); w.Code != http.StatusForbidden {
		t.Fatalf("locked admin actor: %d, want 403", w.Code)
	}
	if got, _ := f.store.GetUserByID(member.ID); got.Email != "" {
		t.Fatal("a refused set must write nothing")
	}
}
