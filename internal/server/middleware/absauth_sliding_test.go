// file: internal/server/middleware/absauth_sliding_test.go
// version: 1.0.0
// guid: 23a1f237-1dc7-4ba6-b00b-fd8d85b71221
// last-edited: 2026-10-19

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
)

const slidingDay = 24 * time.Hour

var errTest = errors.New("pebble: injected extend failure")

// slidingHarness is an absHarness on a fake clock, with one user and one session
// created at t0 exactly as login creates it (ExpiresAt = SessionExpiry(t0, t0)).
type slidingHarness struct {
	*absHarness
	t0      time.Time
	now     time.Time
	session *database.ABSSession
	token   string
}

func newSlidingHarness(t *testing.T, mutate func(*absauth.Config)) *slidingHarness {
	t.Helper()
	h := newABSHarness(t, "cf,jwt", nil)
	if mutate != nil {
		mutate(h.cfg)
	}
	sh := &slidingHarness{absHarness: h, t0: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	sh.now = sh.t0
	h.resolver.SetClock(func() time.Time { return sh.now })
	h.store.addUser(activeUser("u1", "owner"))
	sh.session = &database.ABSSession{ID: "s1", UserID: "u1", CreatedAt: sh.t0, LastUsedAt: sh.t0,
		ExpiresAt: h.cfg.SessionExpiry(sh.t0, sh.t0)}
	h.store.addSession(sh.session)
	tok, _, err := h.cfg.MintAccessToken("u1", "s1", sh.t0)
	if err != nil {
		t.Fatal(err)
	}
	sh.token = tok
	return sh
}

func (sh *slidingHarness) get(t *testing.T) int {
	t.Helper()
	return sh.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + sh.token}).Code
}

func (sh *slidingHarness) expiresAt() time.Time {
	sh.store.mu.Lock()
	defer sh.store.mu.Unlock()
	return sh.session.ExpiresAt
}

// TestABSAuth_BearerUseExtendsSessionThrottled: authenticated use slides the
// session's expiry, but writes at most once per SessionExtendWindow (24h at the
// default 60d idle TTL), not once per request.
func TestABSAuth_BearerUseExtendsSessionThrottled(t *testing.T) {
	sh := newSlidingHarness(t, nil)
	loginExpiry := sh.expiresAt()

	sh.now = sh.t0.Add(23 * time.Hour)
	for range 5 {
		if code := sh.get(t); code != http.StatusOK {
			t.Fatalf("got %d", code)
		}
	}
	if sh.store.extendWrites != 0 || !sh.expiresAt().Equal(loginExpiry) {
		t.Fatalf("inside the throttle window: %d writes, ExpiresAt %v (want 0 writes, unchanged)", sh.store.extendWrites, sh.expiresAt())
	}

	sh.now = sh.t0.Add(25 * time.Hour)
	if code := sh.get(t); code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if sh.store.extendWrites != 1 {
		t.Fatalf("past the window: %d writes, want 1", sh.store.extendWrites)
	}
	if want := sh.now.Add(sh.cfg.RefreshTTL); !sh.expiresAt().Equal(want) {
		t.Fatalf("ExpiresAt = %v, want now+idle %v", sh.expiresAt(), want)
	}

	sh.now = sh.now.Add(time.Hour)
	sh.get(t)
	if sh.store.extendWrites != 1 {
		t.Fatalf("an hour after a write: %d writes, want still 1", sh.store.extendWrites)
	}
}

// TestABSAuth_LegacyFixedExpiryRowSlidesOnFirstUse: rows written before sliding
// expiry carry ExpiresAt = login+30d. The first authenticated use moves them onto
// the sliding schedule; no migration is needed.
func TestABSAuth_LegacyFixedExpiryRowSlidesOnFirstUse(t *testing.T) {
	sh := newSlidingHarness(t, nil)
	sh.store.mu.Lock()
	sh.session.ExpiresAt = sh.t0.Add(30 * slidingDay)
	sh.store.mu.Unlock()

	sh.now = sh.t0.Add(20 * slidingDay)
	if code := sh.get(t); code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if want := sh.now.Add(60 * slidingDay); !sh.expiresAt().Equal(want) {
		t.Fatalf("legacy row ExpiresAt = %v, want %v", sh.expiresAt(), want)
	}
}

// TestABSAuth_ExtendFailureDoesNotFailRequest: the request is already
// authenticated; a failed bookkeeping write is audited, not surfaced.
func TestABSAuth_ExtendFailureDoesNotFailRequest(t *testing.T) {
	sh := newSlidingHarness(t, nil)
	sh.store.extendErr = errTest
	sh.now = sh.t0.Add(10 * slidingDay)
	if code := sh.get(t); code != http.StatusOK {
		t.Fatalf("an extension failure must not fail the request, got %d", code)
	}
}

// TestABSAuth_NoExtensionOnRejectedRequest: only a request that passed every
// check may slide the session.
func TestABSAuth_NoExtensionOnRejectedRequest(t *testing.T) {
	sh := newSlidingHarness(t, nil)
	sh.store.mu.Lock()
	sh.store.users["u1"].Status = "suspended"
	sh.store.mu.Unlock()
	sh.now = sh.t0.Add(10 * slidingDay)
	if code := sh.get(t); code != http.StatusForbidden {
		t.Fatalf("got %d want 403", code)
	}
	if sh.store.extendWrites != 0 {
		t.Fatalf("a rejected request extended the session (%d writes)", sh.store.extendWrites)
	}
}

// TestABSAuth_SessionMaxLifetimeEnforced: with the absolute cap on, use slides
// the session only up to createdAt+cap, and at the cap the bearer stops working
// even if the stored ExpiresAt is later (rows written before the cap was set).
func TestABSAuth_SessionMaxLifetimeEnforced(t *testing.T) {
	sh := newSlidingHarness(t, func(c *absauth.Config) { c.SessionMaxTTL = 90 * slidingDay })
	sh.now = sh.t0.Add(50 * slidingDay)
	tok, _, _ := sh.cfg.MintAccessToken("u1", "s1", sh.now)
	sh.token = tok
	if code := sh.get(t); code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if want := sh.t0.Add(90 * slidingDay); !sh.expiresAt().Equal(want) {
		t.Fatalf("capped extension = %v, want createdAt+cap %v", sh.expiresAt(), want)
	}

	// A legacy row whose stored expiry overshoots the cap is still cut off.
	sh.store.mu.Lock()
	sh.session.ExpiresAt = sh.t0.Add(500 * slidingDay)
	sh.store.mu.Unlock()
	sh.now = sh.t0.Add(90 * slidingDay)
	tok, _, _ = sh.cfg.MintAccessToken("u1", "s1", sh.now.Add(-time.Hour))
	sh.token = tok
	if code := sh.get(t); code != http.StatusUnauthorized {
		t.Fatalf("at the absolute cap the bearer must 401, got %d", code)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	c.Request.Header.Set("Authorization", "Bearer "+sh.token)
	if _, e := sh.resolver.ResolveBearer(c); e == nil || e.Reason != "session-max-lifetime" {
		t.Fatalf("reason = %v, want session-max-lifetime", e)
	}
}

// TestABSAuth_ExpiredTokenReasonIsTokenExpired: a routine access-token expiry is
// logged as token-expired; forged and malformed tokens stay token-invalid. Both
// are 401 so the client refreshes either way.
func TestABSAuth_ExpiredTokenReasonIsTokenExpired(t *testing.T) {
	sh := newSlidingHarness(t, nil)
	resolve := func(tok string) *ABSAuthError {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
		c.Request.Header.Set("Authorization", "Bearer "+tok)
		_, e := sh.resolver.ResolveBearer(c)
		return e
	}

	sh.now = sh.t0.Add(sh.cfg.AccessTTL + time.Minute)
	if e := resolve(sh.token); e == nil || e.Reason != "token-expired" || e.Status != http.StatusUnauthorized {
		t.Fatalf("expired token: %+v, want 401 token-expired", e)
	}
	tampered := sh.token[:len(sh.token)-4] + "AAAA"
	if e := resolve(tampered); e == nil || e.Reason != "token-invalid" {
		t.Fatalf("tampered (and expired) token: %+v, want token-invalid", e)
	}
	other, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: "ffffffffffffffffffffffffffffffff"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, _ := other.MintAccessToken("u1", "s1", sh.t0)
	if e := resolve(foreign); e == nil || e.Reason != "token-invalid" {
		t.Fatalf("expired token from another secret: %+v, want token-invalid", e)
	}
	sh.now = sh.t0.Add(time.Hour)
	if e := resolve("not-a-jwt"); e == nil || e.Reason != "token-invalid" {
		t.Fatalf("garbage: %+v, want token-invalid", e)
	}
}
