// file: internal/server/guest_key_reissue_test.go
// version: 1.0.3
// guid: 7adb11d4-24ec-4f03-a1b2-9e8a576a5ab1
// last-edited: 2026-11-13

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfservicetoken"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
	"github.com/falkcorp/mediacore/coretest"
	imail "github.com/falkcorp/mediacore/integrations/notify/mail"
	"github.com/gin-gonic/gin"
)

const fakeClientID = "abcdef0123456789.access"

// gatedSender is a mail.Sender that can block in Send (a slow relay), run a
// hook, and fail, so a test can hold or break the email step of an issue.
type gatedSender struct {
	mu      sync.Mutex
	sent    []imail.Message
	err     error
	onSend  func()
	entered chan struct{}
	release chan struct{}
}

func (g *gatedSender) Send(_ context.Context, msg imail.Message) error {
	if g.entered != nil {
		g.entered <- struct{}{}
		<-g.release
	}
	if g.onSend != nil {
		g.onSend()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.sent = append(g.sent, msg)
	return nil
}

func (g *gatedSender) Sent() []imail.Message {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]imail.Message(nil), g.sent...)
}

// reissueFixture is the token fixture plus working mail, a public URL and an
// admin who clicks the button.
type reissueFixture struct {
	*tokenFixture
	mail  *coretest.Recorder
	admin *database.User
}

func newReissueFixture(t *testing.T) *reissueFixture {
	t.Helper()
	f := newTokenFixture(t)
	r := &reissueFixture{tokenFixture: f, mail: &coretest.Recorder{}}
	f.srv.mailSenderOverride = r.mail
	f.srv.externalURL = ""
	config.Mutate(func(c *config.Config) { c.PublicBaseURL = "https://books.example.com" })
	admin, err := f.store.CreateUser("batman-admin", "batman@wayne.example", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	r.admin = admin
	return r
}

// post is the admin's click: the handler, with the admin signed in.
func (r *reissueFixture) post(userID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c := ginTestContext(w, httptest.NewRequest(http.MethodPost, "/x", nil))
	c.Params = gin.Params{{Key: "userId", Value: userID}}
	c.Set("auth_user", r.admin)
	r.srv.handleReissueGuestServiceToken(c)
	return w
}

// lock deactivates a user the way the Users page does (status "locked").
func (r *reissueFixture) lock(t *testing.T, id string) {
	t.Helper()
	locker, ok := r.store.(interface {
		SetUserStatus(id, status string) (*database.User, error)
	})
	if !ok {
		t.Fatal("store cannot lock users")
	}
	if _, err := locker.SetUserStatus(id, "locked"); err != nil {
		t.Fatal(err)
	}
}

// done waits for the background worker (and its guard release).
func (r *reissueFixture) done() { r.srv.bgWG.Wait() }

func (r *reissueFixture) newUser(t *testing.T, name string) *database.User {
	t.Helper()
	u, err := r.store.CreateUser(name, name+"@wayne.example", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func respJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, w.Body.String())
	}
	return m
}

func respData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	d, _ := respJSON(t, w)["data"].(map[string]any)
	return d
}

func (f *fakeTokenAPI) snapshot() (created, revoked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...), append([]string(nil), f.revoked...)
}

// assertNoCredentialAnywhere checks that neither the secret nor the client id
// is in the store, the logs, any audit entry or any of the given response
// bodies, and that no audit entry carries an email address.
func (r *reissueFixture) assertNoCredentialAnywhere(t *testing.T, logs interface{ String() string }, bodies ...string) {
	t.Helper()
	db := r.store.(*database.PebbleStore).DB()
	it, err := db.NewIter(nil)
	if err != nil {
		t.Fatal(err)
	}
	for it.First(); it.Valid(); it.Next() {
		for _, bad := range []string{fakeSecret, fakeClientID} {
			if strings.Contains(string(it.Key()), bad) || strings.Contains(string(it.Value()), bad) {
				t.Fatalf("a credential is stored under key %q", it.Key())
			}
		}
	}
	_ = it.Close()
	for _, bad := range []string{fakeSecret, fakeClientID} {
		if strings.Contains(logs.String(), bad) {
			t.Fatalf("a credential reached the logs:\n%s", logs.String())
		}
		for _, b := range bodies {
			if strings.Contains(b, bad) {
				t.Fatalf("a credential reached a response: %s", b)
			}
		}
	}
	for _, typ := range []string{
		auditGuestTokenMinted, auditGuestTokenMintFailed, auditGuestTokenRevoked, auditGuestTokenRevokeFailed,
		auditGuestTokenReissued, auditGuestTokenReissueFailed,
	} {
		for _, e := range r.auditTypes(t, typ) {
			b, _ := json.Marshal(e)
			for _, bad := range []string{fakeSecret, "abcdef0123456789", "@example.com", "@wayne.example"} {
				if strings.Contains(string(b), bad) {
					t.Fatalf("audit entry %s carries %q: %s", typ, bad, b)
				}
			}
		}
	}
}

func TestReissue_RevokesTheOldKeyBeforeMintingAndEmailsTheNewOne(t *testing.T) {
	r := newReissueFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	old := r.api.created[0]
	r.api.entered, r.api.release = make(chan struct{}), make(chan struct{})

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- r.post(u.ID) }()
	<-r.api.entered // Create is about to run
	_, revoked := r.api.snapshot()
	if !reflect.DeepEqual(revoked, []string{old}) {
		t.Fatalf("revoked at the moment of the new Create = %v, want exactly the old token %s", revoked, old)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenPending || rec.TokenID != "" || rec.ReissuedAt == nil {
		t.Fatalf("record during the mint = %+v, want a reopened pending record", rec)
	}
	close(r.api.release)
	w := <-done
	r.done()

	if w.Code != http.StatusOK {
		t.Fatalf("reissue: %d %s", w.Code, w.Body.String())
	}
	created, _ := r.api.snapshot()
	if len(created) != 2 || r.api.names[1] != "aorg-guest-"+u.ID {
		t.Fatalf("created = %v names = %v", created, r.api.names)
	}
	data := respData(t, w)
	if data["reissued"] != true || data["token_id"] != created[1] || data["emailed"] != true || data["first_key"] != false {
		t.Fatalf("response = %v", data)
	}
	rec, _ := r.tokens.GetGuestServiceToken(u.ID)
	if rec.Status != database.GuestTokenActive || rec.TokenID != created[1] || !rec.PolicyAdded || rec.ReissuedAt == nil ||
		rec.RevokedAt != nil || rec.SecretHash != hashSecret(fakeSecret) {
		t.Fatalf("record = %+v", rec)
	}
	if got := r.policy.ensured; !reflect.DeepEqual(got, []string{old, created[1]}) {
		t.Fatalf("policy ensured = %v", got)
	}
	if !reflect.DeepEqual(r.policy.removed, []string{old}) {
		t.Fatalf("policy removed = %v, want the old token only", r.policy.removed)
	}
	sent := r.mail.Sent()
	if len(sent) != 1 || sent[0].To != "robin@example.com" || sent[0].Subject != "Your new Audiobook Organizer phone-app key" {
		t.Fatalf("sent = %+v", sent)
	}
	for _, part := range []string{sent[0].Text, sent[0].HTML} {
		for _, want := range []string{
			"CF-Access-Client-Id: " + fakeClientID, "CF-Access-Client-Secret: " + fakeSecret,
			"https://books.example.com", "Your old key no longer works", "Lissen", "AudioBooth",
		} {
			if !strings.Contains(part, want) {
				t.Fatalf("email missing %q: %s", want, part)
			}
		}
	}
	if _, loaded := r.srv.guestTokens.reissueGuard.Load(u.ID); loaded {
		t.Fatal("the per-user guard was not released")
	}
	// Audit: the reissue entry carries both token ids and the actor, no credential.
	entries := r.auditTypes(t, auditGuestTokenReissued)
	if len(entries) != 1 {
		t.Fatalf("reissued audit entries = %d", len(entries))
	}
	d := entries[0].Details
	if d["token_id"] != created[1] || d["previous_token_id"] != old || d["first_key"] != false ||
		!strings.Contains(d["detail"].(string), r.admin.ID) {
		t.Fatalf("reissued audit details = %v", d)
	}
	if len(r.auditTypes(t, auditGuestTokenRevoked)) != 1 || len(r.auditTypes(t, auditGuestTokenMinted)) != 2 {
		t.Fatal("the revoke and the mint were not audited by the shared code")
	}
	r.assertNoCredentialAnywhere(t, logs, w.Body.String())
}

func TestReissue_IssuesAFirstKeyToAVerifiedUserWithNoRecord(t *testing.T) {
	r := newReissueFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	u := r.newUser(t, "alfred")
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusOK {
		t.Fatalf("issue: %d %s", w.Code, w.Body.String())
	}
	data := respData(t, w)
	created, revoked := r.api.snapshot()
	if len(created) != 1 || len(revoked) != 0 || data["first_key"] != true || data["token_id"] != created[0] {
		t.Fatalf("created=%v revoked=%v data=%v", created, revoked, data)
	}
	if r.api.names[0] != cfservicetoken.Name(u.ID) {
		t.Fatalf("token name = %q", r.api.names[0])
	}
	rec, _ := r.tokens.GetGuestServiceToken(u.ID)
	if rec == nil || rec.Status != database.GuestTokenActive || rec.TokenID != created[0] || rec.ReissuedAt != nil || !rec.PolicyAdded {
		t.Fatalf("record = %+v", rec)
	}
	sent := r.mail.Sent()
	if len(sent) != 1 || sent[0].To != "alfred@wayne.example" || sent[0].Subject != "Your Audiobook Organizer phone-app key" ||
		!strings.Contains(sent[0].Text, "Here is your own phone-app key") || strings.Contains(sent[0].Text, "no longer works") ||
		!strings.Contains(sent[0].Text, "CF-Access-Client-Secret: "+fakeSecret) {
		t.Fatalf("sent = %+v", sent)
	}
	if e := r.auditTypes(t, auditGuestTokenReissued); len(e) != 1 || e[0].Details["first_key"] != true || e[0].Details["previous_token_id"] != nil {
		t.Fatalf("audit = %+v", e)
	}
	r.assertNoCredentialAnywhere(t, logs, w.Body.String())
}

// A guest whose accept hit the Cloudflare 403 has a "failed" record and no
// token: once the permission is fixed, Reissue is their first real path to a key.
func TestReissue_FromAFailedRecordNeedsNoRevokeAtCloudflare(t *testing.T) {
	r := newReissueFixture(t)
	r.api.createErr = &cfservicetoken.APIError{Status: 403, Codes: []int{10000}}
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "oracle")
	r.api.createErr = nil
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusOK {
		t.Fatalf("reissue: %d %s", w.Code, w.Body.String())
	}
	created, revoked := r.api.snapshot()
	if len(created) != 1 || len(revoked) != 0 {
		t.Fatalf("created=%v revoked=%v: a failed record has no token to delete", created, revoked)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenActive || rec.ReissuedAt == nil {
		t.Fatalf("record = %+v", rec)
	}
}

func TestReissue_AlsoWorksFromARevokedRecord(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	if err := r.srv.revokeGuestServiceToken(context.Background(), u.ID, "revoked by an admin", r.admin.ID); err != nil {
		t.Fatal(err)
	}
	_, revokedBefore := r.api.snapshot()
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusOK {
		t.Fatalf("reissue: %d %s", w.Code, w.Body.String())
	}
	if _, revokedAfter := r.api.snapshot(); len(revokedAfter) != len(revokedBefore) {
		t.Fatalf("an already revoked key was revoked again: %v -> %v", revokedBefore, revokedAfter)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenActive || rec.RevokedAt != nil || rec.RevokeReason != "" {
		t.Fatalf("record = %+v", rec)
	}
}

// Every refusal happens before anything is revoked, minted or mailed.
func TestReissue_PreflightRefusalsTouchNothing(t *testing.T) {
	type tc struct {
		name   string
		status int
		code   string
		want   string
		setup  func(t *testing.T, r *reissueFixture, u *database.User)
	}
	cases := []tc{
		{"the feature flag is off", 409, guestKeyBlockedCode, "switched off", func(t *testing.T, r *reissueFixture, u *database.User) {
			config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
		}},
		{"the Cloudflare settings are incomplete", 409, guestKeyBlockedCode, "incomplete", func(t *testing.T, r *reissueFixture, u *database.User) {
			config.Mutate(func(c *config.Config) { c.CFPolicyAPIToken = "" })
		}},
		{"mail is not configured", 409, guestKeyBlockedCode, "SMTP_HOST", func(t *testing.T, r *reissueFixture, u *database.User) {
			r.srv.mailSenderOverride = nil
		}},
		{"the public URL is relative", 409, guestKeyBlockedCode, "PUBLIC_BASE_URL", func(t *testing.T, r *reissueFixture, u *database.User) {
			config.Mutate(func(c *config.Config) { c.PublicBaseURL = "/books" })
		}},
		{"the account is not active", 409, guestKeyBlockedCode, "not active", func(t *testing.T, r *reissueFixture, u *database.User) {
			r.lock(t, u.ID)
		}},
		{"the email is self-set, not verified", 409, guestKeyBlockedCode, "no trusted email", func(t *testing.T, r *reissueFixture, u *database.User) {
			changed := *u
			changed.Email = "robin.grayson@wayne.example"
			if err := r.store.UpdateUser(&changed); err != nil {
				t.Fatal(err)
			}
		}},
		{"the record is pending", 409, guestKeyBusyCode, "Revoke", func(t *testing.T, r *reissueFixture, u *database.User) {
			if _, err := r.tokens.UpdateGuestServiceToken(u.ID, func(g *database.GuestServiceToken) error {
				g.Status, g.TokenID = database.GuestTokenPending, ""
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"the record is minted", 409, guestKeyBusyCode, "Revoke", func(t *testing.T, r *reissueFixture, u *database.User) {
			if _, err := r.tokens.UpdateGuestServiceToken(u.ID, func(g *database.GuestServiceToken) error {
				g.Status = database.GuestTokenMinted
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"the record is revoking", 409, guestKeyBusyCode, "Revoke", func(t *testing.T, r *reissueFixture, u *database.User) {
			if _, err := r.tokens.UpdateGuestServiceToken(u.ID, func(g *database.GuestServiceToken) error {
				g.Status = database.GuestTokenRevoking
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReissueFixture(t)
			_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
			createdBefore, revokedBefore := r.api.snapshot()
			ensuredBefore := len(r.policy.ensured)
			recBefore, _ := r.tokens.GetGuestServiceToken(u.ID)
			c.setup(t, r, u)
			recSetup, _ := r.tokens.GetGuestServiceToken(u.ID)

			w := r.post(u.ID)
			r.done()
			if w.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, c.status, w.Body.String())
			}
			body := respJSON(t, w)
			if body["code"] != c.code || !strings.Contains(body["error"].(string), c.want) {
				t.Fatalf("body = %v, want code %s containing %q", body, c.code, c.want)
			}
			created, revoked := r.api.snapshot()
			if len(created) != len(createdBefore) || len(revoked) != len(revokedBefore) ||
				len(r.policy.ensured) != ensuredBefore || len(r.policy.removed) != 0 {
				t.Fatalf("Cloudflare was called: created %v revoked %v ensured %v removed %v", created, revoked, r.policy.ensured, r.policy.removed)
			}
			if n := len(r.mail.Sent()); n != 0 {
				t.Fatalf("%d emails sent", n)
			}
			rec, _ := r.tokens.GetGuestServiceToken(u.ID)
			if !reflect.DeepEqual(rec, recSetup) {
				t.Fatalf("the record changed: before %+v after %+v (originally %+v)", recSetup, rec, recBefore)
			}
		})
	}
}

func TestReissue_UnknownOrMalformedUser(t *testing.T) {
	r := newReissueFixture(t)
	if w := r.post("NO-SUCH-USER"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d %s", w.Code, w.Body.String())
	}
	if w := r.post(strings.Repeat("x", 65)); w.Code != http.StatusBadRequest {
		t.Fatalf("overlong id: %d %s", w.Code, w.Body.String())
	}
	// A first key for a user with no verified address is refused as well.
	u, err := r.store.CreateUser("selina", "", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if w := r.post(u.ID); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no trusted email") {
		t.Fatalf("no address: %d %s", w.Code, w.Body.String())
	}
	if created, _ := r.api.snapshot(); len(created) != 0 {
		t.Fatal("Cloudflare was called")
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec != nil {
		t.Fatalf("a refused first key left a record: %+v", rec)
	}
}

func TestReissue_RevokeFailureMintsNothingAndKeepsTheRecordRetryable(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	r.api.revokeErr = &cfservicetoken.APIError{Status: 403}
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusBadGateway || respJSON(t, w)["code"] != guestKeyFailedCode ||
		!strings.Contains(w.Body.String(), "could not be confirmed deleted") || !strings.Contains(w.Body.String(), "may still work") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if created, _ := r.api.snapshot(); len(created) != 1 {
		t.Fatalf("created = %v: a new key was minted although the old one was not confirmed deleted", created)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevokeFailed {
		t.Fatalf("record = %+v", rec)
	}
	if n := len(r.mail.Sent()); n != 0 {
		t.Fatalf("%d emails sent", n)
	}
	// Retry once Cloudflare answers: allowed from revoke_failed.
	r.api.revokeErr = nil
	if w := r.post(u.ID); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	r.done()
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenActive {
		t.Fatalf("record after the retry = %+v", rec)
	}
}

func TestReissue_CreateFailureSaysTheOldKeyIsGone(t *testing.T) {
	t.Run("403", func(t *testing.T) {
		r := newReissueFixture(t)
		_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
		old := r.api.created[0]
		r.api.createErr = &cfservicetoken.APIError{Status: 403, Codes: []int{10000}}
		w := r.post(u.ID)
		r.done()
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "The old key is revoked; no new key exists") ||
			!strings.Contains(w.Body.String(), "Service Tokens") {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if _, revoked := r.api.snapshot(); !reflect.DeepEqual(revoked, []string{old}) {
			t.Fatalf("revoked = %v", revoked)
		}
		rec, _ := r.tokens.GetGuestServiceToken(u.ID)
		if rec.Status != database.GuestTokenFailed || rec.TokenID != "" {
			t.Fatalf("record = %+v", rec)
		}
		if n := len(r.mail.Sent()); n != 0 {
			t.Fatalf("%d emails sent", n)
		}
	})
	t.Run("ambiguous 5xx reaps the orphan", func(t *testing.T) {
		r := newReissueFixture(t)
		_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
		orphan := "00000000-0000-4000-8000-0000000000ff"
		r.api.createErr = &cfservicetoken.APIError{Status: 502}
		r.api.onAmbiguous = func(name string) {
			r.api.list = append(r.api.list, cfservicetoken.Token{ID: orphan, Name: name})
		}
		w := r.post(u.ID)
		r.done()
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "no new key exists") {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if _, revoked := r.api.snapshot(); !reflect.DeepEqual(revoked[len(revoked)-1:], []string{orphan}) {
			t.Fatalf("revoked = %v, want the orphaned mint deleted", revoked)
		}
		if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenFailed {
			t.Fatalf("record = %+v", rec)
		}
	})
	t.Run("a first key that cannot be made", func(t *testing.T) {
		r := newReissueFixture(t)
		u := r.newUser(t, "alfred")
		r.api.createErr = &cfservicetoken.APIError{Status: 403}
		w := r.post(u.ID)
		r.done()
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "No phone-app key could be made") {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestReissue_MailFailureRevokesTheNewKey(t *testing.T) {
	r := newReissueFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	r.mail.SetErr(errors.New("smtp: relay refused"))
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "No phone-app key is active for robin") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	created, revoked := r.api.snapshot()
	if len(created) != 2 || len(revoked) != 2 || revoked[1] != created[1] {
		t.Fatalf("created %v revoked %v: the key nobody received must be revoked", created, revoked)
	}
	if last := r.policy.removed[len(r.policy.removed)-1]; last != created[1] {
		t.Fatalf("policy removed = %v", r.policy.removed)
	}
	rec, _ := r.tokens.GetGuestServiceToken(u.ID)
	if rec.Status != database.GuestTokenRevoked || rec.RevokeReason != "reissue email could not be sent" {
		t.Fatalf("record = %+v", rec)
	}
	failed := r.auditTypes(t, auditGuestTokenReissueFailed)
	if len(failed) != 1 || failed[0].Details["token_id"] != created[1] || len(r.auditTypes(t, auditGuestTokenReissued)) != 0 {
		t.Fatalf("audit = %+v", failed)
	}
	r.assertNoCredentialAnywhere(t, logs, w.Body.String())
	// Mail works again: Reissue from the revoked record.
	r.mail.SetErr(nil)
	if w := r.post(u.ID); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	r.done()
	if n := len(r.mail.Sent()); n != 1 {
		t.Fatalf("emails after the retry = %d", n)
	}
}

func TestReissue_MailFailureAndRevokeFailureSaysTheKeyMayBeLive(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	gs := &gatedSender{err: errors.New("smtp: relay refused"), onSend: func() {
		r.api.mu.Lock()
		r.api.revokeErr = &cfservicetoken.APIError{Status: 502}
		r.api.mu.Unlock()
	}}
	r.srv.mailSenderOverride = gs
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "could not be revoked either") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevokeFailed {
		t.Fatalf("record = %+v", rec)
	}
}

// A deactivation that lands while Cloudflare is making the key: the token is
// deleted again and nothing is emailed.
func TestReissue_DeactivationDuringTheMintEmailsNothing(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	r.api.entered, r.api.release = make(chan struct{}), make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- r.post(u.ID) }()
	<-r.api.entered
	r.lock(t, u.ID)
	if err := r.srv.revokeGuestServiceToken(context.Background(), u.ID, "deactivated", ""); err != nil {
		t.Fatal(err)
	}
	close(r.api.release)
	w := <-done
	r.done()
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	created, revoked := r.api.snapshot()
	if len(created) != 2 || revoked[len(revoked)-1] != created[1] {
		t.Fatalf("created %v revoked %v: the key made during the deactivation must be deleted", created, revoked)
	}
	if n := len(r.mail.Sent()); n != 0 {
		t.Fatalf("%d emails sent to a deactivated account", n)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevoked {
		t.Fatalf("record = %+v", rec)
	}
	if len(r.policy.ensured) != 1 { // only the accept-time rule
		t.Fatalf("a deactivated guest's new token was added to the policy: %v", r.policy.ensured)
	}
}

// A second click while the first is making the key is refused and starts no
// second mint.
func TestReissue_SecondClickWhileTheFirstMintsIsRefused(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	r.api.entered, r.api.release = make(chan struct{}), make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- r.post(u.ID) }()
	<-r.api.entered
	w2 := r.post(u.ID)
	if w2.Code != http.StatusConflict || respJSON(t, w2)["code"] != guestKeyBusyCode {
		t.Fatalf("second click: %d %s", w2.Code, w2.Body.String())
	}
	close(r.api.release)
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("first click: %d %s", w.Code, w.Body.String())
	}
	r.done()
	if created, _ := r.api.snapshot(); len(created) != 2 {
		t.Fatalf("created = %v, want the accept's key and exactly one reissue", created)
	}
}

// Past the handler's wait the answer is 202; the per-user guard stays held by
// the worker, so a click during the slow mail step cannot revoke the key being
// mailed.
func TestReissue_SlowMailAnswers202AndTheWorkerKeepsTheGuest(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	gs := &gatedSender{entered: make(chan struct{}), release: make(chan struct{})}
	r.srv.mailSenderOverride = gs
	r.srv.guestTokens.reissueWait = 50 * time.Millisecond

	w := r.post(u.ID)
	if w.Code != http.StatusAccepted || respData(t, w)["status"] != "running" {
		t.Fatalf("slow reissue: %d %s", w.Code, w.Body.String())
	}
	<-gs.entered // the worker is inside Send; the record already reads active
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenActive {
		t.Fatalf("record = %+v", rec)
	}
	w2 := r.post(u.ID)
	if w2.Code != http.StatusConflict || respJSON(t, w2)["code"] != guestKeyBusyCode {
		t.Fatalf("click during the mail step: %d %s", w2.Code, w2.Body.String())
	}
	created, revoked := r.api.snapshot()
	if len(created) != 2 || len(revoked) != 1 {
		t.Fatalf("created %v revoked %v: the second click must not revoke the key being mailed", created, revoked)
	}
	close(gs.release)
	r.done()
	if n := len(gs.Sent()); n != 1 {
		t.Fatalf("emails = %d", n)
	}
	// The guard is gone with the worker: the next reissue runs.
	r.srv.guestTokens.reissueWait = 0
	r.srv.mailSenderOverride = r.mail
	if w := r.post(u.ID); w.Code != http.StatusOK {
		t.Fatalf("reissue after the slow one: %d %s", w.Code, w.Body.String())
	}
	r.done()
}

func TestReissue_ListShowsFirstKeyRowsAndBlockedReasons(t *testing.T) {
	r := newReissueFixture(t)
	_, robin := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	r.newUser(t, "alfred")
	// Not listed: a locked account, a self-set (unverified) address, no address.
	locked := r.newUser(t, "penguin")
	r.lock(t, locked.ID)
	joker := r.newUser(t, "joker")
	changed := *joker
	changed.Email = "joker.jr@wayne.example"
	if err := r.store.UpdateUser(&changed); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.CreateUser("selina", "", "bcrypt", "h", []string{"viewer"}, "active"); err != nil {
		t.Fatal(err)
	}

	views, _, err := r.srv.guestTokenViews(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]guestTokenView{}
	for _, v := range views {
		byName[v.Username] = v
	}
	for _, absent := range []string{"penguin", "joker", "selina"} {
		if _, ok := byName[absent]; ok {
			t.Errorf("%s is listed although a key cannot be issued to them", absent)
		}
	}
	if v := byName["robin"]; v.UserID != robin.ID || v.Status != database.GuestTokenActive || v.ReissueBlocked != "" || v.CreatedAt == nil {
		t.Fatalf("robin = %+v", v)
	}
	alfred := byName["alfred"]
	if alfred.Status != guestTokenStatusNone || alfred.ReissueBlocked != "" || alfred.CreatedAt != nil || alfred.TokenID != "" {
		t.Fatalf("alfred = %+v", alfred)
	}
	if b, _ := json.Marshal(alfred); strings.Contains(string(b), "created_at") || strings.Contains(string(b), "token_id") {
		t.Fatalf("a row with no record carries record fields: %s", b)
	}
	// Records come first, then the users with no key, by name.
	if views[0].Username != "robin" {
		t.Fatalf("first row = %+v, want the record rows first", views[0])
	}

	// A pending record cannot be reissued: the row says why.
	if _, err := r.tokens.UpdateGuestServiceToken(robin.ID, func(g *database.GuestServiceToken) error {
		g.Status = database.GuestTokenPending
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	views, _, _ = r.srv.guestTokenViews(context.Background())
	for _, v := range views {
		if v.Username == "robin" && !strings.Contains(v.ReissueBlocked, "Revoke") {
			t.Fatalf("pending row = %+v", v)
		}
	}

	// No mail: every row says so, and the button is disabled with the reason.
	r.srv.mailSenderOverride = nil
	views, _, _ = r.srv.guestTokenViews(context.Background())
	for _, v := range views {
		if v.Username == "alfred" && !strings.Contains(v.ReissueBlocked, "SMTP_HOST") {
			t.Fatalf("alfred without mail = %+v", v)
		}
	}
	// Flag off: no first-key rows at all, and the rows with records say why.
	config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
	views, _, _ = r.srv.guestTokenViews(context.Background())
	for _, v := range views {
		if v.Status == guestTokenStatusNone {
			t.Fatalf("a first-key row is listed with the feature off: %+v", v)
		}
	}
}
