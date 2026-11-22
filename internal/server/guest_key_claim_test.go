// file: internal/server/guest_key_claim_test.go
// version: 0.1.0
// guid: e8d2a147-93bc-4f60-a5d1-6b04c7f3e982
// last-edited: 2026-11-22

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/cfservicetoken"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// Phase S1 of F4-08 B10s: the guest's one-time phone-app key under core
// identity is claimed with POST /api/v1/auth/phone-key/claim. These tests run a
// real Server built WithIdentity (the claim route, its credential guard and its
// manifest class are the ones a production boot has) with Cloudflare, the mail
// relay and the activity service replaced by fakes, and a principal bound the way
// core's stage 12 binds one.

const claimPath = "/api/v1/auth/phone-key/claim"

// fakeAdminUsers is guestAdminReader as core publishes it: every call
// re-checks the principal of ctx for core:users.manage, so a caller without the
// right reads nothing.
type fakeAdminUsers struct {
	// Admin is nil: the rest of core's handle is not what these tests call.
	coreauth.Admin
	mu    sync.Mutex
	users []*coreauth.User
	calls int
}

func (f *fakeAdminUsers) check(ctx context.Context) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if _, ok := coreauth.PrincipalFrom(ctx); !ok {
		return errors.New("fake core: no principal")
	}
	if err := rbac.Require(ctx, rbac.Permission("core:users.manage")); err != nil {
		return errors.New("fake core: users.manage required")
	}
	return nil
}

func (f *fakeAdminUsers) AdminUser(ctx context.Context, id coreauth.UserID) (*coreauth.User, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	for _, u := range f.users {
		if u.ID == id {
			c := *u
			return &c, nil
		}
	}
	return nil, coreauth.ErrNotFound
}

func (f *fakeAdminUsers) AdminUsers(ctx context.Context, _ coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(yield func(*coreauth.User, error) bool) {
		if err := f.check(ctx); err != nil {
			yield(nil, err)
			return
		}
		for _, u := range f.users {
			c := *u
			if !yield(&c, nil) {
				return
			}
		}
	}
}

func (f *fakeAdminUsers) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// claimFixture is a core-identity server with the feature on.
type claimFixture struct {
	srv    *Server
	store  database.Store
	api    *fakeTokenAPI
	policy *fakeTokenPolicy
	tokens database.GuestServiceTokenStore
	act    *activity.Service
	mail   *coretest.MailRecorder
	admins *fakeAdminUsers
	guest  *database.User // viewer, emailed invitee
	other  *database.User // a second account
	clock  *time.Time
}

func newClaimFixture(t *testing.T) *claimFixture { return newClaimFixtureWith(t, nil) }

// newClaimFixtureWith is newClaimFixture whose bridge reads a user through
// lookup(next, u) when it is set: next is the plain read, u the record it found.
// A test uses it to change what core says about the guest at a precise point.
func newClaimFixtureWith(t *testing.T, lookup func(u *database.User) (*database.User, error)) *claimFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/claim.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	saved := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
	config.Mutate(func(c *config.Config) {
		c.CFGuestServiceTokens = true
		c.CFAccountID, c.CFPolicyAPIToken, c.CFServiceAuthPolicyID = "acct", "tok", "pol"
		c.InviteEmailServiceToken = false
	})
	guest := &database.User{ID: "u-dick-grayson", Username: "nightwing", Email: "dick@wayne.example", Roles: []string{"viewer"}, Status: "active"}
	other := &database.User{ID: "u-jason-todd", Username: "redhood", Email: "jason@wayne.example", Roles: []string{"viewer"}, Status: "active"}
	f := &claimFixture{store: store, api: &fakeTokenAPI{}, policy: &fakeTokenPolicy{}, mail: &coretest.MailRecorder{},
		admins: &fakeAdminUsers{}, guest: guest, other: other}
	id := adoptIdentity(t, nil, nil, nil, guest, other)
	id.Admin = f.admins
	if lookup != nil {
		b, err := legacyauth.New(adoptTable(nil), func(_ context.Context, uid coreauth.UserID) (*database.User, error) {
			for _, u := range []*database.User{guest, other} {
				if u.ID == string(uid) {
					return lookup(u)
				}
			}
			return nil, errors.New("no such user")
		}, nil)
		require.NoError(t, err)
		id.Bridge = b
	}
	f.srv = newTestServerWith(t, store, WithIdentity(id))
	f.srv.guestTokens.apiOverride, f.srv.guestTokens.policyOverride = f.api, f.policy
	f.srv.guestPolicyOverride = &fakeGuestPolicy{res: cfaccesspolicy.Added}
	f.srv.mailSenderOverride = f.mail
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](store)
	require.True(t, ok)
	f.tokens = tokens
	db, err := pebble.Open("claim-act.pebble", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	f.act = activity.NewService(database.NewPebbleActivityStore(database.NewGatedDB(db)))
	f.srv.activityService = f.act
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	f.clock = &now
	f.srv.guestKeyClaims.now = func() time.Time { return *f.clock }
	return f
}

// offer is the hook's call for u: an emailed invitation by an admin.
func (f *claimFixture) offer(u *database.User) bool {
	return f.srv.OfferGuestKeyClaim(GuestKeyOffer{UserID: u.ID, InviteID: "inv-" + u.ID, BindEmailOnAccept: true, CreatorHeldAdmin: true})
}

// claimAs posts the claim with a principal of method m for u.
func (f *claimFixture) claimAs(m coreauth.Method, u *database.User) *httptest.ResponseRecorder {
	p, c := adoptPrincipal(m, u)
	return identityServe(f.srv.router, httptest.NewRequest(http.MethodPost, claimPath, nil), p, c)
}

func (f *claimFixture) claim(u *database.User) *httptest.ResponseRecorder {
	return f.claimAs(coreauth.MethodSessionDelegated, u)
}

// refusal asserts w is THE refusal: the same status, code and text whoever asked
// and whatever the reason, no secret, and no-store.
func refusal(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	assert.Contains(t, w.Body.String(), "PHONE_KEY_CLAIM_REFUSED")
	assert.Contains(t, w.Body.String(), guestKeyClaimRefusedMsg)
	assert.NotContains(t, w.Body.String(), fakeSecret)
}

// assertSecretNowhere fails when the sentinel is in the logs, any activity entry
// or any key or value of books' store.
func (f *claimFixture) assertSecretNowhere(t *testing.T, logs fmtStringer) {
	t.Helper()
	assert.NotContains(t, logs.String(), fakeSecret, "the client secret reached the logs")
	assert.NotContains(t, logs.String(), "abcdef0123456789.access", "the client id reached the logs")
	entries, _, err := f.act.Query(context.Background(), database.ActivityFilter{Limit: 200})
	require.NoError(t, err)
	for _, e := range entries {
		b, _ := json.Marshal(e)
		assert.NotContains(t, string(b), fakeSecret, "activity entry %s carries the secret", e.Type)
		assert.NotContains(t, string(b), "abcdef0123456789", "activity entry %s carries the client id", e.Type)
	}
	it, err := f.store.(*database.PebbleStore).DB().NewIter(nil)
	require.NoError(t, err)
	defer it.Close()
	for it.First(); it.Valid(); it.Next() {
		require.False(t, bytes.Contains(it.Key(), []byte(fakeSecret)) || bytes.Contains(it.Value(), []byte(fakeSecret)),
			"the client secret is stored under key %q", it.Key())
	}
}

type fmtStringer interface{ String() string }

func claimPhoneApp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	pa := phoneApp(t, w.Body.String())
	require.NotNil(t, pa, w.Body.String())
	return pa
}

func TestClaimGuestKey_MintsOnceForTheAcceptedUserAndShowsTheSecretOnce(t *testing.T) {
	f := newClaimFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	require.True(t, f.offer(f.guest))

	w := f.claim(f.guest)
	assert.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	pa := claimPhoneApp(t, w)
	assert.Equal(t, "issued", pa["status"])
	assert.Equal(t, fakeSecret, pa["client_secret"])
	assert.Equal(t, "abcdef0123456789.access", pa["client_id"])

	require.Len(t, f.api.created, 1)
	assert.Equal(t, "aorg-guest-"+f.guest.ID, f.api.names[0], "the token is the guest's own")
	assert.Equal(t, []string{f.api.created[0]}, f.policy.ensured)
	rec, _ := f.tokens.GetGuestServiceToken(f.guest.ID)
	require.NotNil(t, rec)
	assert.Equal(t, database.GuestTokenActive, rec.Status)
	assert.Equal(t, hashSecret(fakeSecret), rec.SecretHash, "only the hash is kept")

	assert.Len(t, f.auditTypes2(t, auditGuestTokenClaimed), 1, "the claim is audited")
	assert.Len(t, f.auditTypes2(t, auditGuestTokenMinted), 1, "the mint keeps HEAD's audit entry")
	f.assertSecretNowhere(t, logs)
}

func (f *claimFixture) auditTypes2(t *testing.T, typ string) []database.ActivityEntry {
	t.Helper()
	entries, _, err := f.act.Query(context.Background(), database.ActivityFilter{Type: typ, Limit: 20})
	require.NoError(t, err)
	return entries
}

func TestClaimGuestKey_SecondClaimGetsNothing(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	claimPhoneApp(t, f.claim(f.guest))
	refusal(t, f.claim(f.guest))
	assert.Len(t, f.api.created, 1, "a second claim must not mint again")
}

func TestClaimGuestKey_AnotherUserGetsNothingAndLeavesTheEntitlement(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	refusal(t, f.claim(f.other))
	assert.Empty(t, f.api.created, "another user's claim must not mint")
	// The rightful user still claims: the refused claim consumed nothing.
	claimPhoneApp(t, f.claim(f.guest))
	assert.Equal(t, "aorg-guest-"+f.guest.ID, f.api.names[0])
}

func TestClaimGuestKey_EveryRefusalIsTheSameAnswer(t *testing.T) {
	f := newClaimFixture(t)
	never := f.claim(f.guest) // nothing was ever offered
	refusal(t, never)

	require.True(t, f.offer(f.guest))
	other := f.claim(f.other) // someone else's claim
	refusal(t, other)
	claimPhoneApp(t, f.claim(f.guest))
	second := f.claim(f.guest) // a second claim
	refusal(t, second)

	assert.Equal(t, never.Body.String(), other.Body.String(), "nothing offered and another user's claim read alike")
	assert.Equal(t, never.Body.String(), second.Body.String(), "nothing offered and a second claim read alike")
}

func TestClaimGuestKey_ExpiresAfterTheWindow(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	*f.clock = f.clock.Add(guestKeyClaimWindow - time.Second)
	// Just inside the window it still works for a different, fresh offer.
	require.True(t, f.offer(f.other))
	*f.clock = f.clock.Add(time.Second) // guest is now exactly at its expiry
	refusal(t, f.claim(f.guest))
	assert.Empty(t, f.api.created, "an expired claim must not mint")
	assert.Len(t, f.auditTypes2(t, auditGuestTokenClaimExpired), 1, "the late claim is audited")
	// The expired entitlement is gone, not revived.
	*f.clock = f.clock.Add(-time.Hour)
	refusal(t, f.claim(f.guest))
}

func TestClaimGuestKey_JustInsideTheWindowMints(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	*f.clock = f.clock.Add(guestKeyClaimWindow - time.Nanosecond)
	claimPhoneApp(t, f.claim(f.guest))
}

func TestOfferGuestKeyClaim_OnlyAnEmailedAdminInvitationEarnsOne(t *testing.T) {
	f := newClaimFixture(t)
	offer := GuestKeyOffer{UserID: f.guest.ID, InviteID: "inv-1", BindEmailOnAccept: true, CreatorHeldAdmin: true}
	for name, mut := range map[string]func(*GuestKeyOffer){
		"link-only invitation":  func(o *GuestKeyOffer) { o.BindEmailOnAccept = false },
		"creator was not admin": func(o *GuestKeyOffer) { o.CreatorHeldAdmin = false },
		"no user":               func(o *GuestKeyOffer) { o.UserID = "" },
		"no invitation":         func(o *GuestKeyOffer) { o.InviteID = "" },
	} {
		o := offer
		mut(&o)
		assert.False(t, f.srv.OfferGuestKeyClaim(o), name)
		refusal(t, f.claim(f.guest))
	}
	config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
	assert.False(t, f.srv.OfferGuestKeyClaim(offer), "feature off")
	config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = true })
	refusal(t, f.claim(f.guest))
	assert.Empty(t, f.api.created)
}

func TestClaimGuestKey_FeatureOffAtClaimTimeMintsNothing(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
	refusal(t, f.claim(f.guest))
	assert.Empty(t, f.api.created)
}

func TestClaimGuestKey_RefusesAKeyAnUnknownMethodAndAuthOffAndKeepsTheEntitlement(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	for _, m := range []coreauth.Method{coreauth.MethodAPIKey, coreauth.MethodNone, coreauth.MethodAuthDisabled} {
		w := f.claimAs(m, f.guest)
		assert.NotEqual(t, http.StatusOK, w.Code, "method %q must not claim", m)
		assert.NotContains(t, w.Body.String(), fakeSecret)
	}
	assert.Empty(t, f.api.created)
	// A refused credential method consumed nothing.
	claimPhoneApp(t, f.claimAs(coreauth.MethodSession, f.guest))
}

func TestClaimGuestKey_NoPrincipalIsRefusedBeforeTheHandler(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	w := identityServe(f.srv.router, httptest.NewRequest(http.MethodPost, claimPath, nil), nil, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, f.api.created)
}

func TestClaimGuestKey_DeactivationDropsTheEntitlement(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	f.srv.RevokeGuestKeyForAccount(f.guest.ID, "deactivated")
	f.srv.bgWG.Wait()
	refusal(t, f.claim(f.guest))
	assert.Empty(t, f.api.created)
}

func TestClaimGuestKey_FailedMintIsConsumedAndSaysAskTheInviter(t *testing.T) {
	f := newClaimFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	f.api.createErr = &cfservicetoken.APIError{Status: 403, Codes: []int{10000}}
	require.True(t, f.offer(f.guest))
	w := f.claim(f.guest)
	pa := claimPhoneApp(t, w)
	assert.NotEqual(t, "issued", pa["status"])
	assert.NotContains(t, w.Body.String(), fakeSecret)
	// Consumed: a retry cannot reach Cloudflare again.
	f.api.createErr = nil
	refusal(t, f.claim(f.guest))
	assert.Empty(t, f.api.created)
	f.assertSecretNowhere(t, logs)
}

func TestClaimGuestKey_ConcurrentClaimsMintOnce(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for range 12 {
		wg.Go(func() {
			w := f.claim(f.guest)
			mu.Lock()
			codes[w.Code]++
			mu.Unlock()
		})
	}
	wg.Wait()
	assert.Equal(t, 1, codes[http.StatusOK], "exactly one claim wins: %v", codes)
	assert.Equal(t, 11, codes[http.StatusNotFound], "every other claim is the refusal: %v", codes)
	assert.Len(t, f.api.created, 1)
}

func TestClaimGuestKey_EmailCopyKeptWhenOptedIn(t *testing.T) {
	f := newClaimFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	config.Mutate(func(c *config.Config) {
		c.InviteEmailServiceToken = true
		c.PublicBaseURL = "https://books.example.com"
	})
	require.True(t, f.offer(f.guest))
	claimPhoneApp(t, f.claim(f.guest))
	f.srv.bgWG.Wait() // the copy is sent in the background, as at HEAD
	sent := f.mail.Sent()
	require.Len(t, sent, 1)
	assert.Equal(t, f.guest.Email, sent[0].To)
	assert.Contains(t, sent[0].Text, "CF-Access-Client-Secret")
	assert.Contains(t, sent[0].Text, fakeSecret, "the e-mailed copy carries the key, as HEAD's did")
	assert.NotContains(t, logs.String(), fakeSecret)
}

func TestClaimGuestKey_NoMailWhenNotOptedIn(t *testing.T) {
	f := newClaimFixture(t)
	require.True(t, f.offer(f.guest))
	claimPhoneApp(t, f.claim(f.guest))
	f.srv.bgWG.Wait()
	assert.Empty(t, f.mail.Sent())
}

// The route's throttle is the one the other credential routes use: the /api
// limiter, which core's stage 10 applies as RateDefault.
func TestClaimGuestKey_ThrottledLikeTheOtherCredentialRoutes(t *testing.T) {
	classes := map[string]httpapi.RateClass{}
	for _, lr := range legacyManifest(true) {
		classes[lr.Method+" "+lr.GinPath] = lr.RateClass
	}
	claim, ok := classes["POST "+claimPath]
	require.True(t, ok, "the claim route has a manifest row")
	for _, sibling := range []string{"POST /api/v1/auth/temp-tokens", "POST /api/v1/auth/api-keys", "PUT /api/v1/auth/me/password"} {
		assert.Equal(t, classes[sibling], claim, "same rate class as %s", sibling)
	}
	assert.Equal(t, httpapi.RateDefault, claim, "the /api limiter, never exempt")
}

func TestGuestKeyClaims_TableBounds(t *testing.T) {
	var g guestKeyClaims
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	for i := range guestKeyClaimMax {
		require.True(t, g.offer("u"+itoa(i), "inv"))
	}
	assert.False(t, g.offer("one-too-many", "inv"), "a full table refuses a new offer")
	_, why := g.take("u0")
	assert.Equal(t, claimRefusal(""), why, "an old entitlement is not evicted by the flood")
	now = now.Add(guestKeyClaimWindow + time.Second)
	assert.True(t, g.offer("fresh", "inv"), "expired rows are swept on the next offer")
	assert.Len(t, g.byUser, 1)
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// --- the core-backed account reads ---

func coreUser(id, name, email string, verified bool, status coreauth.UserStatus) *coreauth.User {
	return &coreauth.User{ID: coreauth.UserID(id), Username: name, Email: email, EmailVerified: verified, Status: status}
}

func adminCtx(t *testing.T, scopes ...rbac.Permission) context.Context {
	t.Helper()
	p, c := adoptPrincipal(coreauth.MethodSession, nil, scopes...)
	return rbac.WithChecker(coreauth.WithPrincipal(context.Background(), p), c)
}

func TestCoreGuestUsers_OtherAccountsNeedTheActorCheckedReader(t *testing.T) {
	admins := &fakeAdminUsers{users: []*coreauth.User{
		coreUser("u1", "nightwing", "dick@wayne.example", true, coreauth.StatusActive),
		coreUser("u2", "redhood", "jason@wayne.example", false, coreauth.StatusActive),
	}}
	u := coreGuestUsers{admin: admins}

	// An admin that holds users.manage reads another account.
	got, err := u.GetUserByID(adminCtx(t, "core:users.manage"), "u1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "nightwing", got.Username)

	// A caller who does not hold it reads nothing: an error, never "no such user".
	got, err = u.GetUserByID(adminCtx(t), "u1")
	require.Error(t, err)
	assert.Nil(t, got)
	_, err = u.ListUsers(adminCtx(t))
	require.Error(t, err)
	_, err = u.GetUserByVerifiedEmail(adminCtx(t), "dick@wayne.example")
	require.Error(t, err)

	// No principal at all: refused before core is asked.
	before := admins.callCount()
	_, err = u.GetUserByID(context.Background(), "u1")
	require.ErrorIs(t, err, errGuestNoPrincipal)
	assert.Equal(t, before, admins.callCount())

	// Absent account is (nil, nil), as the store answered.
	got, err = u.GetUserByID(adminCtx(t, "core:users.manage"), "missing")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestCoreGuestUsers_NoReaderIsAnErrorNotAFallbackToBooksStore(t *testing.T) {
	u := coreGuestUsers{}
	ctx := adminCtx(t, "core:users.manage")
	_, err := u.GetUserByID(ctx, "u1")
	require.ErrorIs(t, err, errGuestNoAdminReader)
	_, err = u.ListUsers(ctx)
	require.ErrorIs(t, err, errGuestNoAdminReader)
	_, err = u.GetUserByVerifiedEmail(ctx, "dick@wayne.example")
	require.ErrorIs(t, err, errGuestNoAdminReader)
}

func TestCoreGuestUsers_VerifiedHolderIsTheOnlyOne(t *testing.T) {
	ctx := adminCtx(t, "core:users.manage")
	read := func(users ...*coreauth.User) (*database.User, error) {
		return coreGuestUsers{admin: &fakeAdminUsers{users: users}}.GetUserByVerifiedEmail(ctx, "  Dick@Wayne.example ")
	}
	got, err := read(coreUser("u1", "nightwing", "dick@wayne.example", true, coreauth.StatusActive),
		coreUser("u2", "robin", "dick@wayne.example", false, coreauth.StatusActive))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "u1", got.ID, "an unverified claimant does not count as a holder")

	got, err = read(coreUser("u2", "robin", "dick@wayne.example", false, coreauth.StatusActive))
	require.NoError(t, err)
	assert.Nil(t, got, "no verified holder")

	// Two verified holders (core allows it after M3): refuse, never pick one.
	got, err = read(coreUser("u1", "nightwing", "dick@wayne.example", true, coreauth.StatusActive),
		coreUser("u3", "batman", "DICK@wayne.example", true, coreauth.StatusActive))
	require.ErrorIs(t, err, errGuestEmailAmbiguous)
	assert.Nil(t, got)
	// ...and guestKeyUserBlocked turns that error into a refusal of the issue.
	users := coreGuestUsers{admin: &fakeAdminUsers{users: []*coreauth.User{
		coreUser("u1", "nightwing", "dick@wayne.example", true, coreauth.StatusActive),
		coreUser("u3", "batman", "dick@wayne.example", true, coreauth.StatusActive),
	}}}
	why := guestKeyUserBlocked(ctx, users, &database.User{ID: "u1", Email: "dick@wayne.example", Status: "active"})
	assert.Contains(t, why, "could not be checked")
}

// A caller reads its OWN record through the principal it carries: no admin right,
// no reader call, and an account that is no longer active is refused.
func TestCoreGuestUsers_OwnRecordNeedsNoAdminRight(t *testing.T) {
	f := newClaimFixture(t)
	p, c := adoptPrincipal(coreauth.MethodSessionDelegated, f.guest)
	ctx := rbac.WithChecker(coreauth.WithPrincipal(context.Background(), p), c)
	u := coreGuestUsers{admin: f.admins, bridge: f.srv.identity.Bridge}
	got, err := u.GetUserByID(ctx, f.guest.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, f.guest.Username, got.Username)
	assert.Zero(t, f.admins.callCount(), "the own-record read never asks the admin reader")

	// Someone else's id goes to the reader, which refuses a caller without users.manage.
	_, err = u.GetUserByID(ctx, f.other.ID)
	require.Error(t, err)
	assert.Equal(t, 1, f.admins.callCount())
}

// The reissue worker runs on the server's context, not the request's: it must
// still carry the admin who clicked so core can judge its reads.
func TestWithRequestValues_CarriesPrincipalNotCancellation(t *testing.T) {
	p, c := adoptPrincipal(coreauth.MethodSession, nil, "core:users.manage")
	req, cancel := context.WithCancel(rbac.WithChecker(coreauth.WithPrincipal(context.Background(), p), c))
	ctx := withRequestValues(context.Background(), req)
	cancel()
	require.NoError(t, ctx.Err(), "the request's cancellation does not end the worker")
	got, ok := coreauth.PrincipalFrom(ctx)
	require.True(t, ok)
	assert.Same(t, p, got)
	require.NoError(t, rbac.Require(ctx, rbac.Permission("core:users.manage")))

	bg, stop := context.WithCancel(context.Background())
	ctx = withRequestValues(bg, req)
	stop()
	require.ErrorIs(t, ctx.Err(), context.Canceled, "the server's shutdown does")
}

// --- the mint's race rules, on the claim path ---

// The deactivation lands after the pending record exists and before the mint's
// own re-read of the account: either the re-read sees it (fails closed) or core
// cannot be asked (fails closed). Nothing is minted.
func TestClaimGuestKey_DeactivatedBeforeMintMintsNothing(t *testing.T) {
	for name, second := range map[string]func(*database.User) (*database.User, error){
		"deactivated": func(u *database.User) (*database.User, error) {
			c := *u
			c.Status = "locked"
			return &c, nil
		},
		"read fails": func(*database.User) (*database.User, error) { return nil, errors.New("core down") },
	} {
		t.Run(name, func(t *testing.T) {
			var f *claimFixture
			f = newClaimFixtureWith(t, func(u *database.User) (*database.User, error) {
				if rec, _ := f.tokens.GetGuestServiceToken(u.ID); rec != nil {
					return second(u) // the pending record exists: this is the mint's re-read
				}
				return u, nil // the request's own sign-in resolve
			})
			require.True(t, f.offer(f.guest))
			w := f.claim(f.guest)
			pa := claimPhoneApp(t, w)
			assert.Equal(t, "unavailable", pa["status"])
			assert.Nil(t, pa["client_secret"])
			assert.Empty(t, f.api.created, "no token may be made for an account that cannot be confirmed active")
			assert.Empty(t, f.policy.ensured)
			rec, _ := f.tokens.GetGuestServiceToken(f.guest.ID)
			require.NotNil(t, rec)
			assert.NotEqual(t, database.GuestTokenActive, rec.Status)
			// The account now reads as locked or unreadable, so a second request is
			// refused at sign-in (401); either way no key is minted and none is owed.
			again := f.claim(f.guest)
			assert.NotEqual(t, http.StatusOK, again.Code, again.Body.String())
			assert.Empty(t, f.api.created)
		})
	}
}

func TestClaimGuestKey_RevokeDuringMintDeletesTheNewToken(t *testing.T) {
	f := newClaimFixture(t)
	f.api.entered, f.api.release = make(chan struct{}), make(chan struct{})
	require.True(t, f.offer(f.guest))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.claim(f.guest) }()
	<-f.api.entered // Cloudflare is mid-answer; the pending record exists
	require.NoError(t, f.srv.revokeGuestServiceToken(context.Background(), f.guest.ID, "deactivated", ""))
	close(f.api.release)
	w := <-done
	f.srv.bgWG.Wait()
	pa := claimPhoneApp(t, w)
	assert.Equal(t, "unavailable", pa["status"], "a revoked mint must not hand out a secret")
	assert.Nil(t, pa["client_secret"])
	created, revoked := f.api.snapshot()
	assert.Equal(t, created, revoked, "the token made during the revoke must be deleted")
	assert.Len(t, created, 1)
	rec, _ := f.tokens.GetGuestServiceToken(f.guest.ID)
	require.NotNil(t, rec)
	assert.Equal(t, database.GuestTokenRevoked, rec.Status)
	assert.Empty(t, f.policy.ensured, "a revoked guest's token was added to the policy")
}

// --- the admin Reissue route and the panel, on core's user record ---

func (f *claimFixture) seedCoreUsers() {
	f.admins.users = []*coreauth.User{
		coreUser(f.guest.ID, f.guest.Username, f.guest.Email, true, coreauth.StatusActive),
		coreUser(f.other.ID, f.other.Username, f.other.Email, true, coreauth.StatusActive),
	}
	config.Mutate(func(c *config.Config) { c.PublicBaseURL = "https://books.example.com" })
	f.srv.externalURL = ""
}

// reissue is an admin's click on the guest's row, signed in with scopes.
func (f *claimFixture) reissue(admin *database.User, scopes ...rbac.Permission) *httptest.ResponseRecorder {
	p, c := adoptPrincipal(coreauth.MethodSession, admin, scopes...)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req = req.WithContext(rbac.WithChecker(coreauth.WithPrincipal(req.Context(), p), c))
	w := httptest.NewRecorder()
	g := ginTestContext(w, req)
	g.Params = gin.Params{{Key: "userId", Value: f.guest.ID}}
	g.Set("auth_user", admin)
	f.srv.handleReissueGuestServiceToken(g)
	f.srv.bgWG.Wait()
	return w
}

var batman = &database.User{ID: "u-bruce-wayne", Username: "batman", Email: "bruce@wayne.example", Roles: []string{"admin"}, Status: "active"}

func TestReissueUnderIdentity_AdminWithUsersManageIssuesAndEmailsTheKey(t *testing.T) {
	f := newClaimFixture(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	f.seedCoreUsers()
	w := f.reissue(batman, "core:admin", "core:users.manage")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), fakeSecret)
	require.Len(t, f.api.created, 1)
	assert.Equal(t, "aorg-guest-"+f.guest.ID, f.api.names[0], "the key is the target account's, read from core's record")
	sent := f.mail.Sent()
	require.Len(t, sent, 1)
	assert.Equal(t, f.guest.Email, sent[0].To)
	assert.Contains(t, sent[0].Text, fakeSecret)
	f.assertSecretNowhere(t, logs)
}

func TestReissueUnderIdentity_AdminWithoutUsersManageReadsNothingAndIssuesNothing(t *testing.T) {
	f := newClaimFixture(t)
	f.seedCoreUsers()
	w := f.reissue(batman, "core:admin")
	assert.NotEqual(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, f.api.created)
	assert.Empty(t, f.mail.Sent())
}

func TestReissueUnderIdentity_NoReaderRefusesRatherThanReadBooksStore(t *testing.T) {
	f := newClaimFixture(t)
	f.seedCoreUsers()
	f.srv.identity.Admin = nil
	// Books' own store even holds the account, as before the cut-over: it must
	// not be consulted.
	_, err := f.store.CreateUser(f.guest.Username, f.guest.Email, "bcrypt", "h", []string{"viewer"}, "active")
	require.NoError(t, err)
	w := f.reissue(batman, "core:admin", "core:users.manage")
	assert.NotEqual(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, f.api.created)
}

// The panel reads core's list once for the whole request, not once per row.
func TestPanelUnderIdentity_ReadsCoreOnceNotPerRow(t *testing.T) {
	f := newClaimFixture(t)
	f.seedCoreUsers()
	for _, n := range []string{"alfred", "barbara", "damian", "tim"} {
		f.admins.users = append(f.admins.users, coreUser("u-"+n, n, n+"@wayne.example", true, coreauth.StatusActive))
	}
	p, c := adoptPrincipal(coreauth.MethodSession, batman, "core:admin", "core:users.manage")
	ctx := rbac.WithChecker(coreauth.WithPrincipal(context.Background(), p), c)
	views, _, err := f.srv.guestTokenViews(ctx)
	require.NoError(t, err)
	assert.Len(t, withoutFirstKeyRows(views), 0)
	rows := 0
	for _, v := range views {
		if v.Status == guestTokenStatusNone {
			rows++
		}
	}
	assert.Equal(t, 6, rows, "every eligible account has a first-key row")
	assert.Equal(t, 1, f.admins.callCount(), "one judged read of core's list serves the whole panel")
}
