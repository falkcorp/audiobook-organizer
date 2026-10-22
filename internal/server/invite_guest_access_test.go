// file: internal/server/invite_guest_access_test.go
// version: 1.12.0
// guid: 7a4c2e91-5d38-4b06-8f1a-c9e3b6d2a457
// last-edited: 2026-10-21

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// fakeGuestPolicy records EnsureEmail and RemoveEmail calls. When entered
// and release are set, EnsureEmail signals entered and then blocks until
// release is closed (without holding mu, so RemoveEmail is not blocked).
type fakeGuestPolicy struct {
	mu      sync.Mutex
	emails  []string
	removed []string
	res     cfaccesspolicy.Result
	err     error
	// removeErr, when set, is returned by RemoveEmail alongside Removed.
	removeErr error
	entered   chan struct{}
	release   chan struct{}
}

func (f *fakeGuestPolicy) RemoveEmail(_ context.Context, email string) (cfaccesspolicy.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, email)
	return cfaccesspolicy.Removed, f.removeErr
}

func (f *fakeGuestPolicy) EnsureEmail(_ context.Context, email string) (cfaccesspolicy.Result, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
		<-f.release
	}
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

// createEmailInvite creates an invite by a fresh user with creatorRole and,
// when emailed, records it as delivered to its address.
func createEmailInvite(t *testing.T, store database.Store, creatorRole string, emailed bool, token, username, email string) {
	t.Helper()
	creator, err := store.CreateUser("creator-"+token[:6], "creator-"+token[:6]+"@admins.example.com", "bcrypt", "h", []string{creatorRole}, "active")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := store.CreateInvite(&database.Invite{Token: token, Username: username, Email: email, RoleID: "viewer",
		CreatedByUserID: creator.ID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if emailed {
		ds, ok := database.AsCapability[database.InviteDeliveryStore](store)
		if !ok {
			t.Fatal("store lacks InviteDeliveryStore")
		}
		if _, err := ds.MarkInviteEmailSent(token, inv.Email, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAcceptInvite_SetsEmailGrantsSSOAndSyncsGuestPolicy(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("a", 64)
	createEmailInvite(t, store, "admin", true, token, "guest", "Guest@Example.com")

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

	gs, _ := database.AsCapability[database.InviteEmailGrantStore](store)
	if g, _ := gs.GetInviteEmailGrant("guest@example.com"); g == nil || !g.CFAdded {
		t.Fatalf("grant must record that the sync added the email: %+v", g)
	}

	// Deactivation through the real route ends the SSO grant and removes the
	// email the sync added from the Cloudflare policy.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/"+u.ID+"/deactivate", nil)
	dw := httptest.NewRecorder()
	srv.router.ServeHTTP(dw, req)
	if dw.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", dw.Code, dw.Body.String())
	}
	srv.bgWG.Wait()
	if allowed("guest@example.com") {
		t.Fatal("a deactivated user's grant must not admit SSO")
	}
	if g, _ := gs.GetInviteEmailGrant("guest@example.com"); g != nil {
		t.Fatal("deactivation must delete the grant")
	}
	if len(fake.removed) != 1 || fake.removed[0] != "guest@example.com" {
		t.Fatalf("Cloudflare removals = %v", fake.removed)
	}
	// Idempotent: a second revoke finds nothing to do.
	srv.revokeGuestAccess(u.ID, "guest@example.com", "test")
	srv.bgWG.Wait()
	if len(fake.removed) != 1 {
		t.Fatal("a second revoke must not call Cloudflare again")
	}
}

func TestAcceptInvite_NonAdminCreatorGetsNoGrantOrCloudflare(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("1", 64)
	createEmailInvite(t, store, "editor", true, token, "guest", "guest@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	srv.bgWG.Wait()
	if inviteGrantAllowed(store)("guest@example.com") {
		t.Fatal("an editor's invite must not grant SSO")
	}
	if len(fake.emails) != 0 {
		t.Fatal("an editor's invite must not touch Cloudflare")
	}
	inv, _ := store.GetInvite(token)
	if inv.SSOGrant != database.InviteSSOCreatorNotAdmin || inv.GuestPolicyStatus != database.InviteGuestPolicySkipped || !strings.Contains(inv.GuestPolicyDetail, "admin") {
		t.Fatalf("admin must see why: %+v", inv)
	}
	if inv.EmailBinding != database.InviteEmailCreatorNotAdmin {
		t.Fatalf("EmailBinding = %q, want creator_not_admin", inv.EmailBinding)
	}
	if u, _ := store.GetUserByUsername("guest"); u == nil || u.Email != "" {
		t.Fatalf("an editor's invite must not bind the address: %+v", u)
	}
}

// S2: an accepted admin invitation emailed to an address takes it from an
// account that had only typed it in, and the admin's audit log says so.
func TestAcceptInvite_DisplacesAProfileTypedEmailAndAudits(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	srv.guestPolicyOverride = &fakeGuestPolicy{res: cfaccesspolicy.Added}
	db, err := pebble.Open("act.pebble", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := activity.NewService(database.NewPebbleActivityStore(database.NewGatedDB(db)))
	srv.activityService = svc
	token := strings.Repeat("d", 64)
	createEmailInvite(t, store, "admin", true, token, "guest", "guest@example.com")
	squatter, err := store.CreateUser("squatter", "squatter@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	squatter.Email = "guest@example.com"
	if err := store.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	srv.bgWG.Wait()
	if got, _ := store.GetUserByID(squatter.ID); got.Email != "" {
		t.Fatalf("the squatter must lose the address: %q", got.Email)
	}
	guest, _ := store.GetUserByUsername("guest")
	if guest == nil || guest.Email != "guest@example.com" {
		t.Fatalf("the guest holds the address: %+v", guest)
	}
	entries, _, err := svc.Query(context.Background(), database.ActivityFilter{Type: "auth_email_displaced", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Source != "invite" || !strings.Contains(entries[0].Summary, guest.ID) ||
		fmt.Sprint(entries[0].Details["displaced_user_ids"]) != "["+squatter.ID+"]" {
		t.Fatalf("audit entries = %+v", entries)
	}
	if strings.Contains(entries[0].Summary, "guest@example.com") {
		t.Fatal("the audit entry must not record the address")
	}
}

func TestAcceptInvite_ManualLinkBindsNoEmail(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("2", 64)
	createEmailInvite(t, store, "admin", false, token, "guest", "guest@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	srv.bgWG.Wait()
	u, _ := store.GetUserByUsername("guest")
	if u == nil || u.Email != "" {
		t.Fatalf("a never-emailed invite must not bind the email: %+v", u)
	}
	if inviteGrantAllowed(store)("guest@example.com") || len(fake.emails) != 0 {
		t.Fatal("a never-emailed invite must not grant SSO or touch Cloudflare")
	}
	inv, _ := store.GetInvite(token)
	if inv.EmailBinding != database.InviteEmailNotEmailed || inv.GuestPolicyStatus != database.InviteGuestPolicySkipped {
		t.Fatalf("admin must see why: %+v", inv)
	}
}

func TestRevokeGuestAccess_KeepsSharedOwnerAndAllowlistedEmails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, store database.Store)
	}{
		{"another active user holds it, verified by an identity provider", func(t *testing.T, store database.Store) {
			// What oauth.ResolveUser records when a linked identity's verified
			// email equals the account's.
			twin := claimGuestEmailAfterMove(t, store)
			m, ok := database.AsCapability[database.VerifiedEmailMarker](store)
			if !ok {
				t.Fatal("store lacks VerifiedEmailMarker")
			}
			if marked, err := m.MarkUserEmailVerified(twin.ID, "Guest@example.com"); err != nil || !marked {
				t.Fatalf("MarkUserEmailVerified = %v, %v", marked, err)
			}
		}},
		{"owner email", func(t *testing.T, _ database.Store) {
			config.Mutate(func(c *config.Config) { c.OwnerEmail = "Guest@example.com" })
		}},
		{"operator allowlist", func(t *testing.T, _ database.Store) {
			config.Mutate(func(c *config.Config) { c.OAuthAllowedEmails = "a@example.com, guest@example.com" })
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, store := setupUserHandlerServer(t)
			saved := config.Snapshot()
			t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
			fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
			srv.guestPolicyOverride = fake
			token := strings.Repeat(string(rune('3'+i)), 64)
			createEmailInvite(t, store, "admin", true, token, "guest", "guest@example.com")
			if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
				t.Fatalf("accept: %d", w.Code)
			}
			srv.bgWG.Wait()
			u, _ := store.GetUserByUsername("guest")
			tc.setup(t, store)
			srv.revokeGuestAccess(u.ID, "guest@example.com", "email changed")
			srv.bgWG.Wait()
			gs, _ := database.AsCapability[database.InviteEmailGrantStore](store)
			if g, _ := gs.GetInviteEmailGrant("guest@example.com"); g != nil {
				t.Fatal("the user's own grant must still be deleted")
			}
			if len(fake.removed) != 0 {
				t.Fatalf("Cloudflare entry removed although %s", tc.name)
			}
		})
	}
}

// claimGuestEmailAfterMove moves the accepted guest off guest@example.com
// and lets a second user set that address on their own profile, returning
// that second user.
func claimGuestEmailAfterMove(t *testing.T, store database.Store) *database.User {
	t.Helper()
	guest, _ := store.GetUserByUsername("guest")
	guest.Email = "moved@example.com"
	if err := store.UpdateUser(guest); err != nil {
		t.Fatal(err)
	}
	twin, err := store.CreateUser("twin", "twin-placeholder@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	twin.Email = "guest@example.com"
	if err := store.UpdateUser(twin); err != nil {
		t.Fatal(err)
	}
	return twin
}

// A self-set profile email is not verification: it must not pin the address
// in the Cloudflare policy after the invitee who was granted it gives it up.
func TestRevokeGuestAccess_SelfSetEmailDoesNotBlockRemoval(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("7", 64)
	createEmailInvite(t, store, "admin", true, token, "guest", "guest@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	srv.bgWG.Wait()
	guest, _ := store.GetUserByUsername("guest")
	claimGuestEmailAfterMove(t, store)
	srv.revokeGuestAccess(guest.ID, "guest@example.com", "email changed")
	srv.bgWG.Wait()
	if len(fake.removed) != 1 || fake.removed[0] != "guest@example.com" {
		t.Fatalf("Cloudflare removals = %v; a self-set email must not keep the entry", fake.removed)
	}
}

// Through the real route, the profile form answers every address the same
// way: an address another account holds verified is accepted, unverified,
// and the verified holder is unchanged, so the form cannot be used to learn
// which addresses have accounts.
func TestUpdateMeRoute_AnyAddressAcceptedUnverified(t *testing.T) {
	saved := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
	config.Mutate(func(c *config.Config) { c.EnableAuth = true })
	srv, store := setupUserHandlerServer(t)
	holder, err := store.CreateUser("holder", "held@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	me, err := store.CreateUser("me", "me@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.CreateSession(me.ID, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	patch := func(email string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"email": email})
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+sess.ID)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w
	}
	verified, fresh := patch("HELD@example.com"), patch("nobody@example.com")
	if verified.Code != http.StatusOK || fresh.Code != http.StatusOK {
		t.Fatalf("PATCH /auth/me: verified address %d, unused address %d; want 200 for both", verified.Code, fresh.Code)
	}
	if patch("held@example.com").Code != http.StatusOK {
		t.Fatal("re-typing the verified address must be accepted too")
	}
	if got, _ := store.GetUserByID(me.ID); got.EmailSource != database.EmailSourceSelf {
		t.Fatalf("the copy has source %q, want self", got.EmailSource)
	}
	if u, _ := store.GetUserByVerifiedEmail("held@example.com"); u == nil || u.ID != holder.ID {
		t.Fatalf("the verified holder must be unchanged: %+v", u)
	}
}

func TestRevokeGuestAccess_NeverRemovesAnEmailTheSyncDidNotAdd(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.AlreadyPresent}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("9", 64)
	createEmailInvite(t, store, "admin", true, token, "guest", "guest@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	srv.bgWG.Wait()
	u, _ := store.GetUserByUsername("guest")
	srv.revokeGuestAccess(u.ID, "guest@example.com", "user deactivated")
	srv.bgWG.Wait()
	if len(fake.removed) != 0 {
		t.Fatal("an email that was already in the policy was put there by hand and must stay")
	}
}

func TestAcceptInvite_GuestPolicyFailureNeverFailsAccept(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	srv.guestPolicyOverride = &fakeGuestPolicy{err: errors.New("cfaccesspolicy: save policy: HTTP 403: 10000 Authentication error")}
	token := strings.Repeat("b", 64)
	createEmailInvite(t, store, "admin", true, token, "guest2", "guest2@example.com")
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
	createEmailInvite(t, store, "admin", true, token, "guest3", "guest3@example.com")
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
	createEmailInvite(t, store, "admin", false, token, "plain", "")
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
	secrets := []string{"smtp-pw-SENTINEL-41", "cf-token-SENTINEL-44", "mailer-SENTINEL@example.com"}
	config.Mutate(func(c *config.Config) {
		c.SMTPHost, c.SMTPFrom = "127.0.0.1", "books@example.com"
		c.SMTPPassword, c.CFPolicyAPIToken, c.SMTPUsername = secrets[0], secrets[1], secrets[2]
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

// deactivateViaRoute deactivates userID through the real route.
func deactivateViaRoute(t *testing.T, srv *Server, userID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/"+userID+"/deactivate", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body.String())
	}
}

func (f *fakeGuestPolicy) snapshot() (added, removed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.emails...), append([]string(nil), f.removed...)
}

// A revoke that lands while EnsureEmail is in flight sees CFAdded=false; the
// sync, which then adds the email, must find the grant gone and remove it.
func TestGuestPolicySync_RevokeDuringAddRemovesTheEntry(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added, entered: make(chan struct{}), release: make(chan struct{})}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("b", 64)
	createEmailInvite(t, store, "admin", true, token, "guest", "guest@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	<-fake.entered // the sync is now inside EnsureEmail
	inv, _ := store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyPending {
		t.Fatalf("status before Cloudflare answers = %q, want pending", inv.GuestPolicyStatus)
	}
	guest, _ := store.GetUserByUsername("guest")
	deactivateViaRoute(t, srv, guest.ID)
	close(fake.release)
	srv.bgWG.Wait()

	added, removed := fake.snapshot()
	if len(added) != 1 || len(removed) != 1 || removed[0] != "guest@example.com" {
		t.Fatalf("added=%v removed=%v; the entry added after the revoke must be removed again", added, removed)
	}
	inv, _ = store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyRemoved {
		t.Fatalf("status = %q, want removed", inv.GuestPolicyStatus)
	}
	gs, _ := database.AsCapability[database.InviteEmailGrantStore](store)
	if g, _ := gs.GetInviteEmailGrant("guest@example.com"); g != nil {
		t.Fatalf("grant must stay deleted, got %+v", g)
	}
}

// Same interleaving, but the address was already in the policy (added by
// hand): nothing is removed.
func TestGuestPolicySync_RevokeDuringAddLeavesAHandAddedEntry(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.AlreadyPresent, entered: make(chan struct{}), release: make(chan struct{})}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("c", 64)
	createEmailInvite(t, store, "admin", true, token, "guest", "guest@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	<-fake.entered
	guest, _ := store.GetUserByUsername("guest")
	deactivateViaRoute(t, srv, guest.ID)
	close(fake.release)
	srv.bgWG.Wait()
	if _, removed := fake.snapshot(); len(removed) != 0 {
		t.Fatalf("removed %v; an entry that was already present must stay", removed)
	}
	inv, _ := store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyAlreadyPresent {
		t.Fatalf("status = %q, want already_present", inv.GuestPolicyStatus)
	}
}

// A revoke that lands before the sync reaches Cloudflare: Cloudflare is
// never called.
func TestGuestPolicySync_RevokedBeforeSyncNeverCallsCloudflare(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("e", 64)
	createEmailInvite(t, store, "admin", true, token, "guest", "guest@example.com")
	// Accept straight through the store so no sync starts yet.
	u, err := store.ConsumeInvite(token, "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	srv.revokeGuestAccess(u.ID, "guest@example.com", "user deactivated")
	srv.startGuestPolicySync(token, u.ID)
	srv.bgWG.Wait()
	if added, removed := fake.snapshot(); len(added) != 0 || len(removed) != 0 {
		t.Fatalf("added=%v removed=%v; Cloudflare must not be touched", added, removed)
	}
	inv, _ := store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyRevoked {
		t.Fatalf("status = %q, want revoked", inv.GuestPolicyStatus)
	}
}

// The displacement hook writes an audit entry an admin can find, naming the
// accounts and never the address.
func TestRecordEmailDisplaced_WritesAnAuditEntry(t *testing.T) {
	db, err := pebble.Open("act.pebble", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := activity.NewService(database.NewPebbleActivityStore(database.NewGatedDB(db)))
	s := &Server{activityService: svc}
	s.recordEmailDisplaced("new-user-1", []string{"squatter-1"}, "google")
	entries, _, err := svc.Query(context.Background(), database.ActivityFilter{Type: "auth_email_displaced", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Tier != "audit" || !strings.Contains(entries[0].Summary, "new-user-1") {
		t.Fatalf("entries = %+v", entries)
	}
	// Stored as JSON, so the list reads back as []any.
	if got := fmt.Sprint(entries[0].Details["displaced_user_ids"]); got != "[squatter-1]" {
		t.Fatalf("displaced_user_ids = %s", got)
	}
	(&Server{}).recordEmailDisplaced("x", nil, "google") // no activity service: no panic
}

// An add whose write could not be verified must still set CFAdded, so a later
// revoke removes the address; the admin sees a note on the invite.
func TestGuestPolicySync_UnverifiedAddStillRecordsCFAddedAndRevokeRemoves(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added,
		err: fmt.Errorf("%w: %w", cfaccesspolicy.ErrWriteUnverified, errors.New("cfaccesspolicy: policy not as intended after 3 attempts"))}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("e", 64)
	createEmailInvite(t, store, "admin", true, token, "guest5", "guest5@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	srv.bgWG.Wait()

	inv, _ := store.GetInvite(token)
	if inv.GuestPolicyStatus != database.InviteGuestPolicyAdded || !strings.Contains(inv.GuestPolicyDetail, "could not confirm") {
		t.Fatalf("unverified add not surfaced: %q %q", inv.GuestPolicyStatus, inv.GuestPolicyDetail)
	}
	gs, _ := database.AsCapability[database.InviteEmailGrantStore](store)
	if g, _ := gs.GetInviteEmailGrant("guest5@example.com"); g == nil || !g.CFAdded {
		t.Fatalf("CFAdded must be set after an unverified add, grant = %+v", g)
	}
	u, _ := store.GetUserByUsername("guest5")
	srv.revokeGuestAccess(u.ID, "guest5@example.com", "deactivated")
	srv.bgWG.Wait()
	if len(fake.removed) != 1 || fake.removed[0] != "guest5@example.com" {
		t.Fatalf("revoke must remove the possibly-added address, removed = %v", fake.removed)
	}
}

func TestRevokeGuestAccess_UnverifiedRemovalDoesNotPanicOrRetry(t *testing.T) {
	srv, store := setupUserHandlerServer(t)
	fake := &fakeGuestPolicy{res: cfaccesspolicy.Added,
		removeErr: fmt.Errorf("%w: %w", cfaccesspolicy.ErrWriteUnverified, errors.New("verify failed"))}
	srv.guestPolicyOverride = fake
	token := strings.Repeat("f", 64)
	createEmailInvite(t, store, "admin", true, token, "guest6", "guest6@example.com")
	if w := acceptInvite(t, srv, token); w.Code != http.StatusCreated {
		t.Fatalf("accept: %d", w.Code)
	}
	srv.bgWG.Wait()
	u, _ := store.GetUserByUsername("guest6")
	srv.revokeGuestAccess(u.ID, "guest6@example.com", "deactivated")
	srv.bgWG.Wait()
	if len(fake.removed) != 1 {
		t.Fatalf("removed = %v", fake.removed)
	}
}

// Invitation email on without the Cloudflare guest-policy sync is the dead end
// the startup warning names.
func TestGuestAccessDeadEnd(t *testing.T) {
	mail := config.Config{SMTPHost: "192.0.2.25", SMTPFrom: "invites@example.test"}
	sync := config.Config{CFAccountID: "acct", CFGuestsPolicyID: "pol", CFPolicyAPIToken: "tok"}
	both := mail
	both.CFAccountID, both.CFGuestsPolicyID, both.CFPolicyAPIToken = sync.CFAccountID, sync.CFGuestsPolicyID, sync.CFPolicyAPIToken
	for name, tc := range map[string]struct {
		c    config.Config
		want bool
	}{
		"mail without sync":   {mail, true},
		"mail with sync":      {both, false},
		"neither":             {config.Config{}, false},
		"sync without mail":   {sync, false},
		"partial sync (mail)": {config.Config{SMTPHost: mail.SMTPHost, SMTPFrom: mail.SMTPFrom, CFAccountID: "acct"}, true},
	} {
		if got := guestAccessDeadEnd(tc.c); got != tc.want {
			t.Errorf("%s: guestAccessDeadEnd = %v, want %v", name, got, tc.want)
		}
	}
}
