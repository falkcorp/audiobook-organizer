// file: internal/server/handlers/invite_delivery_test.go
// version: 1.11.0
// guid: e4b17c92-6a35-4d80-9f2e-1c8d5a7b3f06
// last-edited: 2026-10-21

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

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	imail "github.com/falkcorp/audiobook-organizer/internal/mail"
	"github.com/falkcorp/audiobook-organizer/internal/mail/mailtest"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

const (
	testBase = "https://books.example.com"
)

type inviteFixture struct {
	store  *database.PebbleStore
	router *gin.Engine
	rec    *mailtest.Recorder
	h      *handlers.UserHandler
	mailOn bool
	syncOn bool           // GuestPolicySyncConfigured: accept adds the invitee to the Access guest policy
	base   string         // the link base BaseURL returns
	actor  *database.User // the signed-in caller (auth_user) for routes that read it
}

// asUser makes the next requests arrive from a stored user with roles.
func (f *inviteFixture) asUser(t *testing.T, name string, roles ...string) *database.User {
	t.Helper()
	u, err := f.store.CreateUser(name, name+"@example.org", "bcrypt", "h", roles, "active")
	if err != nil {
		t.Fatal(err)
	}
	f.actor = u
	return u
}

func newInviteFixture(t *testing.T) *inviteFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &inviteFixture{store: store, rec: &mailtest.Recorder{}, mailOn: true, base: testBase + "/"}
	f.h = handlers.NewUserHandler(store)
	f.h.SetInviteDelivery(&handlers.InviteDelivery{
		Sender: func() imail.Sender {
			if !f.mailOn {
				return nil
			}
			return f.rec
		},
		BaseURL:                   func() string { return f.base },
		GuestPolicySyncConfigured: func() bool { return f.syncOn },
	})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if f.actor != nil {
			c.Set("auth_user", f.actor)
		}
	})
	r.POST("/users/invite", f.h.CreateInvite)
	r.GET("/users/invites", f.h.ListInvites)
	r.POST("/users/invites/:id/resend", f.h.ResendInvite)
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

// The shared guest service token is gone (2026-10-21): no response and no email
// may carry the header names or tell a guest to install custom headers.
func assertNoGuestHeaderInstructions(t *testing.T, label, body string) {
	t.Helper()
	for _, banned := range []string{"CF-Access-Client", "Custom Headers", "guest_headers"} {
		if strings.Contains(body, banned) {
			t.Fatalf("%s mentions %q", label, banned)
		}
	}
}

func TestCreateInvite_SendsEmailWithLinkAndEmailLoginSteps(t *testing.T) {
	f := newInviteFixture(t)
	f.syncOn = true
	f.asUser(t, "boss", "admin")
	w, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": "guest", "role_id": "viewer", "email": " Guest@Example.com ", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	assertNoGuestHeaderInstructions(t, "admin response", w.Body.String())
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
		for _, want := range []string{testBase + "/invite/" + token, "one-time code", "guest@example.com", "Phone app access will be added to your account soon"} {
			if !strings.Contains(part, want) {
				t.Fatalf("email part missing %q", want)
			}
		}
		assertBrowserOnlyEmail(t, part)
		assertNoGuestHeaderInstructions(t, "invitation email", part)
	}
	inv, _ := f.store.GetInvite(token)
	if inv.Email != "guest@example.com" || inv.EmailSentAt == nil {
		t.Fatalf("invite not stored with email and send time: %+v", inv)
	}
}

// Without the policy sync the invitee is not on the Access policy, so the email
// must not send them to a Cloudflare login that does not know them.
func TestCreateInvite_EmailLoginStepOmittedWithoutPolicySync(t *testing.T) {
	f := newInviteFixture(t)
	f.syncOn = false
	w, _ := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": "guest", "role_id": "viewer", "email": "guest@example.com", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	m := f.rec.Sent()[0]
	for _, part := range []string{m.Text, m.HTML} {
		if strings.Contains(part, "one-time code") {
			t.Fatal("email-login step sent although the policy sync is not configured")
		}
		if !strings.Contains(part, "guest") || !strings.Contains(part, "Phone app access will be added to your account soon") {
			t.Fatalf("the sign-in step or the phone-app line is missing: %s", part)
		}
		assertBrowserOnlyEmail(t, part)
		assertNoGuestHeaderInstructions(t, "invitation email", part)
	}
}

// Guests are browser-only for now: no phone-app setup steps, in the email or
// anywhere else, until per-guest tokens ship.
func assertBrowserOnlyEmail(t *testing.T, part string) {
	t.Helper()
	for _, banned := range []string{"AudioBooth", "Servers", "Tap "} {
		if strings.Contains(part, banned) {
			t.Fatalf("invitation email still carries phone-app setup (%q): %s", banned, part)
		}
	}
}

// The Cloudflare email-login step needs accept to add the address to the
// guest policy, which it does only for an invite created by an active admin.
func TestCreateInvite_EmailLoginStepOmittedWhenCreatorIsNotAdmin(t *testing.T) {
	f := newInviteFixture(t)
	f.syncOn = true
	f.asUser(t, "helper", "viewer")
	w, _ := f.do(t, http.MethodPost, "/users/invite", map[string]any{
		"username": "guest", "role_id": "viewer", "email": "guest@example.com", "send_email": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	m := f.rec.Sent()[0]
	for _, part := range []string{m.Text, m.HTML} {
		if strings.Contains(part, "one-time code") {
			t.Fatal("email-login step sent although accept will not add the address to the guest policy")
		}
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
	assertNoGuestHeaderInstructions(t, "admin response", w.Body.String())
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
	w, rd := f.do(t, http.MethodPost, "/users/invites/"+database.InviteID(token)+"/resend", nil)
	if w.Code != http.StatusOK || rd["email_sent"] != true {
		t.Fatalf("first resend: %d %s", w.Code, w.Body.String())
	}
	assertNoGuestHeaderInstructions(t, "admin response", w.Body.String())
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+database.InviteID(token)+"/resend", nil); w.Code != http.StatusTooManyRequests {
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
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+database.InviteID(token)+"/resend", nil); w.Code != http.StatusTooManyRequests {
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
		w, _ := f.do(t, http.MethodPost, "/users/invites/"+database.InviteID(fmt.Sprint(data["token"]))+"/resend", nil)
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
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+database.InviteID(fmt.Sprint(data["token"]))+"/resend", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("no-email invite: %d", w.Code)
	}
	_, data = f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "m", "role_id": "viewer", "email": "m@example.com"})
	f.mailOn = false
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+database.InviteID(fmt.Sprint(data["token"]))+"/resend", nil); w.Code != http.StatusServiceUnavailable {
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
		if k != "username" && k != "expires_at" && k != "email_login" {
			t.Fatalf("public invite returned extra field %q", k)
		}
	}
	if pub["username"] != "guest" || pub["email_login"] != false {
		t.Fatalf("public invite = %v (email_login must be false without a policy sync)", pub)
	}
	assertNoGuestHeaderInstructions(t, "public invite lookup", w.Body.String())
	if strings.Contains(w.Body.String(), "guest@example.com") {
		t.Fatal("public invite must not reveal the email")
	}

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
		assertNoGuestHeaderInstructions(t, "admin response", r.Body.String())
	}
}

// NIT5: the lookup answers 404 whenever accept would refuse, not only for
// missing, used and expired tokens. A 200 here followed by a refused accept
// would tell the link holder that the invited username or address already has
// an account.
func TestPublicInvite_NotFoundWhenAcceptWouldRefuse(t *testing.T) {
	f := newInviteFixture(t)
	missing, _ := f.do(t, http.MethodGet, "/auth/invite/"+strings.Repeat("0", 64), nil)
	admin, err := f.store.CreateUser("boss", "boss@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	// An admin emailed an invite to an address another account holds verified.
	taken := strings.Repeat("a", 64)
	if _, err := f.store.CreateInvite(&database.Invite{Token: taken, Username: "newcomer", Email: "held@example.com",
		RoleID: "viewer", CreatedByUserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkInviteEmailSent(taken, "held@example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.do(t, http.MethodGet, "/auth/invite/"+taken, nil); w.Code != http.StatusOK {
		t.Fatalf("before the address is held: %d, want 200", w.Code)
	}
	if _, err := f.store.CreateUser("holder", "held@example.com", "bcrypt", "h", []string{"viewer"}, "active"); err != nil {
		t.Fatal(err)
	}
	// Someone took the invited username since the invite was made.
	dup := strings.Repeat("b", 64)
	if _, err := f.store.CreateInvite(&database.Invite{Token: dup, Username: "squat", RoleID: "viewer", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUser("squat", "", "bcrypt", "h", []string{"viewer"}, "active"); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"address held verified": taken, "username taken": dup} {
		w, _ := f.do(t, http.MethodGet, "/auth/invite/"+tok, nil)
		if w.Code != http.StatusNotFound || w.Body.String() != missing.Body.String() {
			t.Fatalf("%s: %d %s, want the same 404 as a missing token", name, w.Code, w.Body.String())
		}
		assertNoGuestHeaderInstructions(t, "admin response", w.Body.String())
		if _, err := f.store.ConsumeInvite(tok, "bcrypt", "h"); !database.IsInviteRefusal(err) {
			t.Fatalf("%s: accept = %v, want a refusal (the lookup and accept must agree)", name, err)
		}
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
	assertNoGuestHeaderInstructions(t, "admin response", w.Body.String())
	if list["mail_configured"] != true || list["guest_policy_sync_configured"] != false {
		t.Fatalf("flags = %v", list)
	}
	if _, present := list["guest_headers_configured"]; present {
		t.Fatal("the invite list still reports guest_headers_configured")
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

// Nit 2: with no absolute public address the server refuses to email an
// invitation (the link would be relative and open nothing); the admin still
// gets the link to copy, and is told why.
func TestInviteEmail_RefusedWithoutAnAbsoluteBaseURL(t *testing.T) {
	for _, base := range []string{"", "/", "audiobooks.example.com", "ftp://audiobooks.example.com"} {
		t.Run(fmt.Sprintf("base=%q", base), func(t *testing.T) {
			f := newInviteFixture(t)
			f.base = base
			w, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{
				"username": "guest", "role_id": "viewer", "email": "guest@example.com", "send_email": true,
			})
			if w.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			if data["email_sent"] != false || !strings.Contains(fmt.Sprint(data["email_error"]), "PUBLIC_BASE_URL") {
				t.Fatalf("create response = %v", data)
			}
			if len(f.rec.Sent()) != 0 {
				t.Fatal("no email may go out with a relative link")
			}
			token, _ := data["token"].(string)
			if w, _ := f.do(t, http.MethodPost, "/users/invites/"+database.InviteID(token)+"/resend", nil); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "PUBLIC_URL_NOT_CONFIGURED") {
				t.Fatalf("resend: %d %s", w.Code, w.Body.String())
			}
			_, list := f.do(t, http.MethodGet, "/users/invites", nil)
			if list["mail_configured"] != false || !strings.Contains(fmt.Sprint(list["mail_unavailable_reason"]), "PUBLIC_BASE_URL") {
				t.Fatalf("list settings = %v / %v", list["mail_configured"], list["mail_unavailable_reason"])
			}
		})
	}
}

// listAs serves GET /users/invites as a caller authenticated by method, acting
// as actor (nil: none), and returns the invites it lists by username.
func (f *inviteFixture) listAs(t *testing.T, method auth.Method, actor *database.User) map[string]map[string]any {
	t.Helper()
	r := gin.New()
	r.GET("/users/invites", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithMethod(c.Request.Context(), method))
		if actor != nil {
			c.Set("auth_user", actor)
		}
	}, f.h.ListInvites)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/invites", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Invites []map[string]any `json:"invites"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, inv := range env.Data.Invites {
		out[fmt.Sprint(inv["username"])] = inv
	}
	return out
}

// SF2: the invite list never hands a token to an API key, and an emailed
// invite's token only to an active admin's own sign-in. Every entry carries
// its non-secret ID, which the revoke and resend routes take.
func TestListInvites_TokensOnlyWhereTheCallerMayHaveThem(t *testing.T) {
	f := newInviteFixture(t)
	admin, err := f.store.CreateUser("boss", "boss@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := f.store.CreateUser("mgr", "mgr@example.com", "bcrypt", "h", []string{"editor"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	emailed := strings.Repeat("c", 64)
	plain := strings.Repeat("d", 64)
	for _, inv := range []*database.Invite{
		{Token: emailed, Username: "emailed", Email: "emailed@example.com", RoleID: "viewer", CreatedByUserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour)},
		{Token: plain, Username: "plain", RoleID: "viewer", CreatedByUserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if _, err := f.store.CreateInvite(inv); err != nil {
			t.Fatal(err)
		}
	}
	want := func(name string, list map[string]map[string]any, user, token string) {
		t.Helper()
		inv := list[user]
		if inv["id"] != database.InviteID(map[string]string{"emailed": emailed, "plain": plain}[user]) {
			t.Fatalf("%s: %s has id %v", name, user, inv["id"])
		}
		if got, _ := inv["token"].(string); got != token {
			t.Fatalf("%s: %s token = %q, want %q", name, user, got, token)
		}
	}
	// An admin's API key: no tokens at all.
	l := f.listAs(t, auth.MethodAPIKey, admin)
	want("admin API key", l, "emailed", "")
	want("admin API key", l, "plain", "")
	// An ABS token or no recorded method: none either (allowlist).
	l = f.listAs(t, auth.MethodABS, admin)
	want("ABS", l, "emailed", "")
	l = f.listAs(t, auth.MethodNone, admin)
	want("no method", l, "plain", "")
	// A non-admin session with users.manage: only the invite with no email.
	l = f.listAs(t, auth.MethodSession, manager)
	want("non-admin session", l, "emailed", "")
	want("non-admin session", l, "plain", plain)
	// An admin's session (or SSO sign-in): every token.
	for _, m := range []auth.Method{auth.MethodSession, auth.MethodCFAccess} {
		l = f.listAs(t, m, admin)
		want(string(m), l, "emailed", emailed)
		want(string(m), l, "plain", plain)
	}
	// A deactivated admin is no admin.
	locked := *admin
	locked.Status = "locked"
	l = f.listAs(t, auth.MethodSession, &locked)
	want("locked admin", l, "emailed", "")
	// With auth off there is nobody to tell apart: tokens are listed.
	f.h.SetAuthEnabled(false)
	l = f.listAs(t, auth.MethodNone, nil)
	want("auth off", l, "emailed", emailed)
}

// SF2: revoke and resend take the ID; a token in that place is not found.
func TestDeleteInvite_ByIDNotToken(t *testing.T) {
	f := newInviteFixture(t)
	f.router.DELETE("/users/invites/:id", f.h.DeleteInvite)
	_, data := f.do(t, http.MethodPost, "/users/invite", map[string]any{"username": "g", "role_id": "viewer", "email": "g@example.com"})
	token := fmt.Sprint(data["token"])
	inv, _ := data["invite"].(map[string]any)
	id := database.InviteID(token)
	if inv["id"] != id {
		t.Fatalf("create response invite id = %v, want %s", inv["id"], id)
	}
	if w, _ := f.do(t, http.MethodDelete, "/users/invites/"+token, nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete by token: %d, want 404", w.Code)
	}
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+token+"/resend", nil); w.Code != http.StatusNotFound {
		t.Fatalf("resend by token: %d, want 404", w.Code)
	}
	if w, d := f.do(t, http.MethodDelete, "/users/invites/"+id, nil); w.Code != http.StatusOK || d["deleted"] != id {
		t.Fatalf("delete by id: %d %v", w.Code, d)
	}
	if got, _ := f.store.GetInvite(token); got != nil {
		t.Fatal("the invite is still there")
	}
	if w, _ := f.do(t, http.MethodDelete, "/users/invites/"+id, nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d, want 404", w.Code)
	}
}

// email_login on the public lookup is exactly the condition under which accept
// will add the address to the Cloudflare guest policy, so the page and the
// email agree: policy sync configured, the invite emailed to its address, and
// an active admin as creator.
func TestPublicInvite_EmailLoginMatchesWhatAcceptWillGrant(t *testing.T) {
	f := newInviteFixture(t)
	admin, err := f.store.CreateUser("boss", "boss@example.org", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := f.store.CreateUser("viewer1", "viewer1@example.org", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(tok, username, email, creator string, emailed bool) string {
		token := strings.Repeat(tok, 64)
		if _, err := f.store.CreateInvite(&database.Invite{Token: token, Username: username, Email: email, RoleID: "viewer",
			CreatedByUserID: creator, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if emailed {
			if _, err := f.store.MarkInviteEmailSent(token, email, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		return token
	}
	tests := []struct {
		name  string
		token string
		sync  bool
		want  bool
	}{
		{"admin, emailed, sync on", mk("1", "g1", "g1@example.com", admin.ID, true), true, true},
		{"admin, emailed, sync off", mk("2", "g2", "g2@example.com", admin.ID, true), false, false},
		{"admin, copy-link (not emailed), sync on", mk("3", "g3", "g3@example.com", admin.ID, false), true, false},
		{"non-admin creator, emailed, sync on", mk("4", "g4", "g4@example.com", viewer.ID, true), true, false},
		{"no email on the invite, sync on", mk("5", "g5", "", admin.ID, false), true, false},
	}
	for _, tt := range tests {
		f.syncOn = tt.sync
		w, pub := f.do(t, http.MethodGet, "/auth/invite/"+tt.token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tt.name, w.Code, w.Body.String())
		}
		if pub["email_login"] != tt.want {
			t.Errorf("%s: email_login = %v, want %v", tt.name, pub["email_login"], tt.want)
		}
		if strings.Contains(w.Body.String(), "@example.com") {
			t.Errorf("%s: the lookup revealed the address", tt.name)
		}
	}
}
