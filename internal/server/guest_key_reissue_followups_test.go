// file: internal/server/guest_key_reissue_followups_test.go
// version: 1.0.1
// guid: 5d0c9b3e-8a41-4f62-9c17-2b6e0f4a7d83
// last-edited: 2026-11-09

package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfservicetoken"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// tokenStoreSpy wraps the real store and lets a test change what the key-record
// reads and writes answer, at a precise point of an issue. It implements
// database.GuestServiceTokenStore itself, so AsCapability finds it first.
type tokenStoreSpy struct {
	database.Store
	inner database.GuestServiceTokenStore
	// getHook, when set, may replace the answer of GetGuestServiceToken.
	getHook func(rec *database.GuestServiceToken, err error) (*database.GuestServiceToken, error)
	// updateErr, when set, makes every UpdateGuestServiceToken fail.
	updateErr error
}

func (s *tokenStoreSpy) CreateGuestServiceToken(rec database.GuestServiceToken) error {
	return s.inner.CreateGuestServiceToken(rec)
}

func (s *tokenStoreSpy) GetGuestServiceToken(userID string) (*database.GuestServiceToken, error) {
	rec, err := s.inner.GetGuestServiceToken(userID)
	if s.getHook != nil {
		return s.getHook(rec, err)
	}
	return rec, err
}

func (s *tokenStoreSpy) ListGuestServiceTokens() ([]database.GuestServiceToken, error) {
	return s.inner.ListGuestServiceTokens()
}

func (s *tokenStoreSpy) UpdateGuestServiceToken(userID string, fn func(*database.GuestServiceToken) error) (*database.GuestServiceToken, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	return s.inner.UpdateGuestServiceToken(userID, fn)
}

func (r *reissueFixture) spy() *tokenStoreSpy {
	sp := &tokenStoreSpy{Store: r.srv.store, inner: r.tokens}
	r.srv.store = sp
	return sp
}

// afterNewMint reports whether the reissue's new key exists (the accept made
// the first, so the second is the reissue's).
func (r *reissueFixture) afterNewMint() bool {
	created, _ := r.api.snapshot()
	return len(created) >= 2
}

// assertNewKeyDiscarded checks that the key the reissue made (the second one)
// was revoked at Cloudflare, nothing was mailed and the record is closed.
func (r *reissueFixture) assertNewKeyDiscarded(t *testing.T, userID string) {
	t.Helper()
	created, revoked := r.api.snapshot()
	if len(created) != 2 {
		t.Fatalf("created = %v", created)
	}
	found := false
	for _, id := range revoked {
		if id == created[1] {
			found = true
		}
	}
	if !found {
		t.Fatalf("created %v revoked %v: the new key nobody holds was not revoked", created, revoked)
	}
	if n := len(r.mail.Sent()); n != 0 {
		t.Fatalf("%d emails sent", n)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(userID); rec.Status != database.GuestTokenRevoked {
		t.Fatalf("record = %+v, want revoked", rec)
	}
}

// ── 1. the post-mint record check fails closed ─────────────────────────────

func TestReissue_RecordReadErrorAfterTheMintRevokesTheNewKey(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	sp := r.spy()
	sp.getHook = func(rec *database.GuestServiceToken, err error) (*database.GuestServiceToken, error) {
		if r.afterNewMint() {
			return nil, errors.New("pebble: read failed")
		}
		return rec, err
	}
	w := r.post(u.ID)
	r.done()
	if w.Code == http.StatusOK {
		t.Fatalf("status 200 for a reissue whose key was discarded: %s", w.Body.String())
	}
	r.assertNewKeyDiscarded(t, u.ID)
	body := w.Body.String()
	if !strings.Contains(body, "new key was revoked") || strings.Contains(body, "pebble") {
		t.Fatalf("response must say the new key was revoked, without the store's text: %s", body)
	}
	if len(r.auditTypes(t, auditGuestTokenReissueFailed)) != 1 {
		t.Fatal("the discard was not audited as reissue_failed")
	}
}

func TestReissue_UnexpectedStatusAfterTheMintRevokesTheNewKey(t *testing.T) {
	for _, status := range []string{database.GuestTokenRevokeFailed, database.GuestTokenFailed, database.GuestTokenPending, database.GuestTokenMinted} {
		t.Run(status, func(t *testing.T) {
			r := newReissueFixture(t)
			_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
			sp := r.spy()
			sp.getHook = func(rec *database.GuestServiceToken, err error) (*database.GuestServiceToken, error) {
				if rec != nil && r.afterNewMint() {
					c := *rec
					c.Status = status
					return &c, err
				}
				return rec, err
			}
			w := r.post(u.ID)
			r.done()
			if w.Code == http.StatusOK {
				t.Fatalf("status 200: %s", w.Body.String())
			}
			r.assertNewKeyDiscarded(t, u.ID)
			if !strings.Contains(w.Body.String(), "new key was revoked") {
				t.Fatalf("response: %s", w.Body.String())
			}
		})
	}
}

// Someone else's revoke already took the key out: do not revoke again, and say
// that is what happened.
func TestReissue_RevokedByAnotherActorAfterTheMintIsNotRevokedAgain(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	sp := r.spy()
	sp.getHook = func(rec *database.GuestServiceToken, err error) (*database.GuestServiceToken, error) {
		if rec != nil && r.afterNewMint() {
			c := *rec
			c.Status = database.GuestTokenRevoked
			return &c, err
		}
		return rec, err
	}
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "was revoked") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	created, revoked := r.api.snapshot()
	for _, id := range revoked {
		if id == created[1] {
			t.Fatalf("revoked %v: the new key was revoked although a revoke had already settled it", revoked)
		}
	}
	if n := len(r.mail.Sent()); n != 0 {
		t.Fatalf("%d emails sent", n)
	}
}

// ── 2. shutdown ────────────────────────────────────────────────────────────

func (r *reissueFixture) shutdownCtx(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.srv.bgCtx = ctx
	return cancel
}

func TestReissue_ShutdownBeforeTheRevokeChangesNothing(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	cancel := r.shutdownCtx(t)
	cancel()
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "shutting down") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	created, revoked := r.api.snapshot()
	if len(created) != 1 || len(revoked) != 0 {
		t.Fatalf("created %v revoked %v: nothing may be touched once the server is stopping", created, revoked)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenActive {
		t.Fatalf("record = %+v, want the old key left active", rec)
	}
}

func TestReissue_ShutdownAfterTheRevokeLeavesTheRecordRevokedAndMintsNothing(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	cancel := r.shutdownCtx(t)
	r.policy.onRemove = cancel // the old key's revoke is mid-flight when the server stops
	w := r.post(u.ID)
	r.done()
	if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "shutting down") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	created, _ := r.api.snapshot()
	if len(created) != 1 {
		t.Fatalf("created = %v: a new key was made after shutdown began", created)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevoked {
		t.Fatalf("record = %+v, want revoked (not pending: a pending record would look stuck)", rec)
	}
	failed := r.auditTypes(t, auditGuestTokenReissueFailed)
	if len(failed) != 1 || !strings.Contains(failed[0].Details["detail"].(string), "shutting down") {
		t.Fatalf("reissue_failed audit = %+v", failed)
	}
	if n := len(r.mail.Sent()); n != 0 {
		t.Fatalf("%d emails sent", n)
	}
	// After the restart the admin reissues from the revoked record.
	r.policy.onRemove = nil
	r.srv.bgCtx = context.Background()
	if w := r.post(u.ID); w.Code != http.StatusOK {
		t.Fatalf("reissue after the restart: %d %s", w.Code, w.Body.String())
	}
	r.done()
}

func TestReissue_ShutdownAfterTheMintDiscardsTheUnsentKey(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	cancel := r.shutdownCtx(t)
	r.policy.onEnsure = cancel // the mint is finishing when the server stops
	w := r.post(u.ID)
	r.done()
	if w.Code == http.StatusOK {
		t.Fatalf("status 200: %s", w.Body.String())
	}
	r.assertNewKeyDiscarded(t, u.ID)
	failed := r.auditTypes(t, auditGuestTokenReissueFailed)
	if len(failed) != 1 || !strings.Contains(failed[0].Details["detail"].(string), "shutting down") {
		t.Fatalf("reissue_failed audit = %+v", failed)
	}
}

// ── 3. a revoke of a pending record with no token id reaps by name ─────────

func (r *reissueFixture) pendingRecordWithNoToken(t *testing.T, name string) *database.User {
	t.Helper()
	u := r.newUser(t, name)
	if err := r.tokens.CreateGuestServiceToken(database.GuestServiceToken{
		UserID: u.ID, Name: cfservicetoken.Name(u.ID), Status: database.GuestTokenPending,
	}); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRevoke_PendingRecordWithNoTokenIdReapsTheOrphanByName(t *testing.T) {
	r := newReissueFixture(t)
	u := r.pendingRecordWithNoToken(t, "alfred")
	// A crash after Cloudflare's Create succeeded left a token nobody recorded.
	r.api.list = []cfservicetoken.Token{
		{ID: "00000000-0000-4000-8000-0000000000aa", Name: cfservicetoken.Name(u.ID)},
		{ID: "00000000-0000-4000-8000-0000000000bb", Name: "owner-laptop"},
	}
	if err := r.srv.revokeGuestServiceToken(context.Background(), u.ID, "revoked by an admin", r.admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, revoked := r.api.snapshot(); len(revoked) != 1 || revoked[0] != "00000000-0000-4000-8000-0000000000aa" {
		t.Fatalf("revoked = %v, want exactly the token named for this guest", revoked)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevoked {
		t.Fatalf("record = %+v", rec)
	}
}

// If Cloudflare cannot be asked, the orphan may be live: the record must say so
// (revoke_failed, retryable), not claim a clean revoke.
func TestRevoke_PendingRecordWithNoTokenIdFailsClosedWhenTheReapFails(t *testing.T) {
	r := newReissueFixture(t)
	u := r.pendingRecordWithNoToken(t, "alfred")
	r.api.listErr = &cfservicetoken.APIError{Status: 502}
	if err := r.srv.revokeGuestServiceToken(context.Background(), u.ID, "revoked by an admin", r.admin.ID); err == nil {
		t.Fatal("revoke reported success although Cloudflare could not be asked about an orphan")
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevokeFailed {
		t.Fatalf("record = %+v, want revoke_failed", rec)
	}
	// Retry once Cloudflare answers.
	r.api.listErr = nil
	if err := r.srv.revokeGuestServiceToken(context.Background(), u.ID, "revoked by an admin", r.admin.ID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevoked {
		t.Fatalf("record after the retry = %+v", rec)
	}
}

// A failed record (the mint reaped its own orphan, or Cloudflare refused) has
// nothing to look for: no list call, just closed.
func TestRevoke_FailedRecordWithNoTokenIdMakesNoCloudflareCall(t *testing.T) {
	r := newReissueFixture(t)
	u := r.pendingRecordWithNoToken(t, "alfred")
	if _, err := r.tokens.UpdateGuestServiceToken(u.ID, func(g *database.GuestServiceToken) error {
		g.Status = database.GuestTokenFailed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.srv.revokeGuestServiceToken(context.Background(), u.ID, "revoked by an admin", r.admin.ID); err != nil {
		t.Fatal(err)
	}
	if r.api.listCalls != 0 {
		t.Fatalf("%d Cloudflare list calls for a record that never had a mint in flight", r.api.listCalls)
	}
	if rec, _ := r.tokens.GetGuestServiceToken(u.ID); rec.Status != database.GuestTokenRevoked {
		t.Fatalf("record = %+v", rec)
	}
}

// ── 4. nits ────────────────────────────────────────────────────────────────

func TestReissue_RevokeStoreErrorIsNotEchoedToTheAdmin(t *testing.T) {
	r := newReissueFixture(t)
	_, u := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	logs := logtest.Capture(t, slog.LevelDebug)
	r.spy().updateErr = errors.New("pebble: disk /var/lib/aorg/guest failure")
	w := r.post(u.ID)
	r.done()
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "could not be confirmed deleted") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "/var/lib") || strings.Contains(w.Body.String(), "pebble") {
		t.Fatalf("an internal error text reached the admin: %s", w.Body.String())
	}
	if !strings.Contains(logs.String(), "/var/lib/aorg/guest") {
		t.Fatalf("the detail was not logged:\n%s", logs.String())
	}
}

func TestReissue_RowIsBlockedWhileAWorkerIsIssuing(t *testing.T) {
	r := newReissueFixture(t)
	_, robin := r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	alfred := r.newUser(t, "alfred")
	blocked := func() map[string]string {
		views, _, err := r.srv.guestTokenViews(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, v := range views {
			m[v.Username] = v.ReissueBlocked
		}
		return m
	}
	if m := blocked(); m["robin"] != "" || m["alfred"] != "" {
		t.Fatalf("rows blocked with no worker running: %v", m)
	}
	r.srv.guestTokens.reissueGuard.Store(robin.ID, struct{}{})
	r.srv.guestTokens.reissueGuard.Store(alfred.ID, struct{}{})
	m := blocked()
	for _, name := range []string{"robin", "alfred"} {
		if !strings.Contains(m[name], "being issued") {
			t.Fatalf("%s row blocked = %q, want it to say a key is being issued now", name, m[name])
		}
	}
}

func TestAccept_EmailedKeyTrimsTheBaseURLSlashAndOmitsTheOldKeyLine(t *testing.T) {
	r := newReissueFixture(t)
	config.Mutate(func(c *config.Config) {
		c.InviteEmailServiceToken = true
		c.PublicBaseURL = "https://books.example.com/"
	})
	_, _ = r.acceptGuest(t, strings.Repeat("a", 64), "robin")
	sent := r.mail.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent = %d", len(sent))
	}
	if strings.Contains(sent[0].Text, "example.com/") || strings.Contains(sent[0].HTML, "example.com/") {
		t.Fatalf("the accept-time email keeps the base URL's trailing slash: %s", sent[0].Text)
	}
	if strings.Contains(sent[0].Text, "old key") {
		t.Fatalf("a first-key email mentions an old key: %s", sent[0].Text)
	}
}

func TestReissue_AdminWordingDoesNotCallEveryAddressVerified(t *testing.T) {
	r := newReissueFixture(t)
	u, err := r.store.CreateUser("selina", "", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	w := r.post(u.ID)
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "verified") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// ── owner decision: the first-key path is for active non-admin users ───────

func TestFirstKey_ListExcludesAdminsAndTheOwner(t *testing.T) {
	r := newReissueFixture(t)
	r.newUser(t, "alfred")
	owner, err := r.store.CreateUser("gordon", "gordon@wayne.example", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	config.Mutate(func(c *config.Config) { c.OwnerEmail = "Gordon@Wayne.example" })
	_ = owner
	views, _, err := r.srv.guestTokenViews(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, v := range views {
		names[v.Username] = true
	}
	if !names["alfred"] {
		t.Fatalf("a plain active user is not listed: %v", names)
	}
	for _, absent := range []string{"batman-admin", "gordon"} {
		if names[absent] {
			t.Errorf("%s is listed for a first key", absent)
		}
	}
}

func TestFirstKey_AdminOrOwnerTargetIsRefusedAndNothingIsMinted(t *testing.T) {
	r := newReissueFixture(t)
	owner, err := r.store.CreateUser("gordon", "gordon@wayne.example", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	config.Mutate(func(c *config.Config) { c.OwnerEmail = "gordon@wayne.example" })
	for name, id := range map[string]string{"admin": r.admin.ID, "owner": owner.ID} {
		w := r.post(id)
		r.done()
		if w.Code != http.StatusConflict || respJSON(t, w)["code"] != guestKeyBlockedCode ||
			!strings.Contains(w.Body.String(), "Cloudflare's own login") {
			t.Fatalf("%s: status %d: %s", name, w.Code, w.Body.String())
		}
		if rec, _ := r.tokens.GetGuestServiceToken(id); rec != nil {
			t.Fatalf("%s: a record was written: %+v", name, rec)
		}
	}
	if created, _ := r.api.snapshot(); len(created) != 0 {
		t.Fatalf("created = %v", created)
	}
	if n := len(r.mail.Sent()); n != 0 {
		t.Fatalf("%d emails sent", n)
	}
}

// Reissue for an EXISTING record is unchanged, an admin's included.
func TestFirstKey_AdminWithAnExistingRecordCanStillBeReissued(t *testing.T) {
	r := newReissueFixture(t)
	if err := r.tokens.CreateGuestServiceToken(database.GuestServiceToken{
		UserID: r.admin.ID, Name: cfservicetoken.Name(r.admin.ID), Status: database.GuestTokenRevoked,
	}); err != nil {
		t.Fatal(err)
	}
	w := r.post(r.admin.ID)
	r.done()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
