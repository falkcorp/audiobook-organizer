// file: internal/server/handlers/abs/sliding_session_test.go
// version: 1.1.0
// guid: e1047d7e-5e2b-4df6-aa64-51878f896f74
// last-edited: 2026-10-19

package abs_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

const sDay = 24 * time.Hour

var errInjected = errors.New("pebble: injected extend failure")

// clockedHarness is a harness whose handler AND resolver run on a fake clock
// (Handler.SetClock moves both), with one password user.
type clockedHarness struct {
	*harness
	t0, now time.Time
}

func newClockedHarness(t *testing.T, allowed []string) *clockedHarness {
	t.Helper()
	ch := &clockedHarness{harness: newHarness(t, "cf,jwt", allowed), t0: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	ch.now = ch.t0
	ch.handler.SetClock(func() time.Time { return ch.now })
	ch.seedPasswordUser(t, "u1", "owner", "pw-pw-pw-pw")
	return ch
}

// onlySession returns a copy of the single stored session.
func (ch *clockedHarness) onlySession(t *testing.T) database.ABSSession {
	t.Helper()
	ch.store.mu.Lock()
	defer ch.store.mu.Unlock()
	if len(ch.store.sessions) != 1 {
		t.Fatalf("want exactly one session, have %d", len(ch.store.sessions))
	}
	for _, s := range ch.store.sessions {
		return *s
	}
	panic("unreachable")
}

func (ch *clockedHarness) refresh(t *testing.T, token string, extra map[string]string) (int, string, string) {
	t.Helper()
	hdrs := map[string]string{"x-refresh-token": token}
	for k, v := range extra {
		hdrs[k] = v
	}
	w, body := ch.do(t, request{method: http.MethodPost, path: "/auth/refresh", headers: hdrs})
	if w.Code != http.StatusOK {
		return w.Code, "", ""
	}
	u := userObj(t, body)
	return w.Code, str(t, u, "accessToken"), str(t, u, "refreshToken")
}

func TestRefresh_RotateSlidesSessionExpiry(t *testing.T) {
	ch := newClockedHarness(t, nil)
	refresh := str(t, userObj(t, ch.login(t, "owner", "pw-pw-pw-pw")), "refreshToken")
	if got, want := ch.onlySession(t).ExpiresAt, ch.t0.Add(60*sDay); !got.Equal(want) {
		t.Fatalf("login ExpiresAt = %v, want login+60d %v", got, want)
	}

	ch.now = ch.t0.Add(20 * sDay)
	if code, _, _ := ch.refresh(t, refresh, nil); code != http.StatusOK {
		t.Fatalf("refresh got %d", code)
	}
	if got, want := ch.onlySession(t).ExpiresAt, ch.now.Add(60*sDay); !got.Equal(want) {
		t.Fatalf("after rotate ExpiresAt = %v, want refresh time + 60d %v", got, want)
	}
}

func TestRefresh_GraceReplaySlidesSessionExpiry(t *testing.T) {
	ch := newClockedHarness(t, nil)
	old := str(t, userObj(t, ch.login(t, "owner", "pw-pw-pw-pw")), "refreshToken")
	ch.now = ch.t0.Add(10 * sDay)
	if code, _, _ := ch.refresh(t, old, nil); code != http.StatusOK {
		t.Fatalf("rotate got %d", code)
	}
	ch.now = ch.now.Add(5 * time.Minute) // inside the 10m grace window
	if code, _, _ := ch.refresh(t, old, nil); code != http.StatusOK {
		t.Fatalf("grace replay got %d", code)
	}
	if got, want := ch.onlySession(t).ExpiresAt, ch.now.Add(60*sDay); !got.Equal(want) {
		t.Fatalf("after grace replay ExpiresAt = %v, want replay time + 60d %v", got, want)
	}
}

func TestRefresh_CFReuseSlidesSessionExpiry(t *testing.T) {
	ch := newClockedHarness(t, []string{"owner@example.com"})
	ch.verifier.byToken["good"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|owner", Email: "owner@example.com", EmailVerified: true,
	}
	refresh := str(t, userObj(t, ch.login(t, "owner", "pw-pw-pw-pw")), "refreshToken")
	sessionID := ch.onlySession(t).ID

	ch.now = ch.t0.Add(40 * sDay)
	if code, _, _ := ch.refresh(t, refresh, map[string]string{oauth.CFAccessHeader: "good"}); code != http.StatusOK {
		t.Fatalf("CF refresh got %d", code)
	}
	s := ch.onlySession(t)
	if s.ID != sessionID {
		t.Fatalf("CF refresh minted a new session %s instead of reusing %s", s.ID, sessionID)
	}
	if want := ch.now.Add(60 * sDay); !s.ExpiresAt.Equal(want) {
		t.Fatalf("after CF reuse ExpiresAt = %v, want %v", s.ExpiresAt, want)
	}
}

// TestRefresh_SessionMaxLifetime: with the absolute cap on, refresh slides only up
// to createdAt+cap, and at the cap refresh is a 401 (a real logout, by design).
func TestRefresh_SessionMaxLifetime(t *testing.T) {
	ch := newClockedHarness(t, nil)
	ch.cfg.SessionMaxTTL = 90 * sDay
	refresh := str(t, userObj(t, ch.login(t, "owner", "pw-pw-pw-pw")), "refreshToken")

	ch.now = ch.t0.Add(50 * sDay)
	code, _, next := ch.refresh(t, refresh, nil)
	if code != http.StatusOK {
		t.Fatalf("refresh before the cap got %d", code)
	}
	if got, want := ch.onlySession(t).ExpiresAt, ch.t0.Add(90*sDay); !got.Equal(want) {
		t.Fatalf("capped ExpiresAt = %v, want createdAt+90d %v", got, want)
	}

	// Even a row whose stored ExpiresAt overshoots (written before the cap was on)
	// stops refreshing at the cap.
	ch.store.mu.Lock()
	for _, s := range ch.store.sessions {
		s.ExpiresAt = ch.t0.Add(400 * sDay)
	}
	ch.store.mu.Unlock()
	ch.now = ch.t0.Add(90 * sDay)
	if code, _, _ := ch.refresh(t, next, nil); code != http.StatusUnauthorized {
		t.Fatalf("refresh at the absolute cap got %d, want 401", code)
	}
}

// TestRefresh_RevokedDuringRefreshIs401Not500: a logout landing between the
// refresh's read and its write makes the store refuse the write; that is a dead
// session (401), not a transient error (5xx makes the client retry forever).
func TestRefresh_RevokedDuringRefreshIs401Not500(t *testing.T) {
	ch := newClockedHarness(t, nil)
	refresh := str(t, userObj(t, ch.login(t, "owner", "pw-pw-pw-pw")), "refreshToken")
	ch.store.updateErr = database.ErrABSSessionRevoked
	if code, _, _ := ch.refresh(t, refresh, nil); code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

// simulateAudioBooth drives the client the way AudioBooth behaves: it uses its
// access token every useEvery, refreshes ONLY after a 401, and is logged out
// (test failure) if that refresh is refused.
func simulateAudioBooth(t *testing.T, ch *clockedHarness, days int, useEvery time.Duration) {
	t.Helper()
	tokens := userObj(t, ch.login(t, "owner", "pw-pw-pw-pw"))
	access, refresh := str(t, tokens, "accessToken"), str(t, tokens, "refreshToken")
	refreshes := 0
	for ch.now = ch.t0; ch.now.Before(ch.t0.Add(time.Duration(days) * sDay)); ch.now = ch.now.Add(useEvery) {
		w, _ := ch.do(t, request{method: http.MethodGet, path: "/api/me",
			headers: map[string]string{"Authorization": "Bearer " + access}})
		if w.Code == http.StatusOK {
			continue
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("day %.1f: /api/me got %d", ch.now.Sub(ch.t0).Hours()/24, w.Code)
		}
		code, a, r := ch.refresh(t, refresh, nil)
		if code != http.StatusOK {
			t.Fatalf("day %.1f: access token rejected and refresh answered %d — the app would be logged out",
				ch.now.Sub(ch.t0).Hours()/24, code)
		}
		access, refresh = a, r
		refreshes++
		if w, _ := ch.do(t, request{method: http.MethodGet, path: "/api/me",
			headers: map[string]string{"Authorization": "Bearer " + access}}); w.Code != http.StatusOK {
			t.Fatalf("day %.1f: freshly refreshed token got %d", ch.now.Sub(ch.t0).Hours()/24, w.Code)
		}
	}
	if refreshes < 2 {
		t.Fatalf("only %d refreshes in %d days: the simulation did not cross access-token expiry", refreshes, days)
	}
}

// TestSession_RegularUseNeverExpiresOver90Days is the regression test for the
// 2026-10-19 forced logout: before sliding expiry every session died 30 days after
// login, at the same instant as its access token, so this failed on day 30.
func TestSession_RegularUseNeverExpiresOver90Days(t *testing.T) {
	for name, every := range map[string]time.Duration{"daily": sDay, "every 9 days": 9 * sDay} {
		t.Run(name, func(t *testing.T) {
			simulateAudioBooth(t, newClockedHarness(t, nil), 90, every)
		})
	}
}

// TestSession_RefreshAloneKeepsSessionAlive: with the bearer-path extension
// failing, refresh-on-401 must still carry the session indefinitely.
func TestSession_RefreshAloneKeepsSessionAlive(t *testing.T) {
	ch := newClockedHarness(t, nil)
	ch.store.extendErr = errInjected
	simulateAudioBooth(t, ch, 90, 3*sDay)
}

// TestSessions_HidesExpiredRows: expired rows now linger for a retention window
// before the sweep deletes them (so a returning client's refresh is logged as
// session-not-live), and the device list must not show them meanwhile.
func TestSessions_HidesExpiredRows(t *testing.T) {
	ch := newClockedHarness(t, nil)
	ch.login(t, "owner", "pw-pw-pw-pw")
	ch.store.mu.Lock()
	for _, s := range ch.store.sessions {
		s.ExpiresAt = ch.now.Add(-time.Minute)
	}
	ch.store.mu.Unlock()
	access := str(t, userObj(t, ch.login(t, "owner", "pw-pw-pw-pw")), "accessToken")

	w, body := ch.do(t, request{method: http.MethodGet, path: "/api/me/sessions",
		headers: map[string]string{"Authorization": "Bearer " + access}})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	rows, _ := body["sessions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("device list shows %d sessions, want only the live one: %v", len(rows), body)
	}
}

// TestRefresh_InactiveUserIsRefusedBeforeAnyWrite: the user is checked before the
// session is rotated or slid, so a locked user's refresh attempts cannot keep the
// session alive (or rotate its tokens) while being refused.
func TestRefresh_InactiveUserIsRefusedBeforeAnyWrite(t *testing.T) {
	ch := newClockedHarness(t, nil)
	refresh := str(t, userObj(t, ch.login(t, "owner", "pw-pw-pw-pw")), "refreshToken")
	before := ch.onlySession(t)

	ch.store.mu.Lock()
	ch.store.users["u1"].Status = "locked"
	ch.store.mu.Unlock()
	ch.now = ch.t0.Add(20 * sDay)
	if code, _, _ := ch.refresh(t, refresh, nil); code != http.StatusForbidden {
		t.Fatalf("locked user's refresh got %d, want 403", code)
	}
	after := ch.onlySession(t)
	if !after.ExpiresAt.Equal(before.ExpiresAt) || after.Generation != before.Generation ||
		after.RefreshTokenHash != before.RefreshTokenHash {
		t.Fatalf("a refused refresh wrote the session: before exp=%v gen=%d, after exp=%v gen=%d",
			before.ExpiresAt, before.Generation, after.ExpiresAt, after.Generation)
	}
}
